package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/handoff"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
	"github.com/relloyd/prutil/internal/watch"
)

// watchGlyph marks a pull request that is armed for watching, and dormantGlyph
// one that is still armed but has gone long enough without changing that
// prutil has stopped asking about it.
const (
	watchGlyph   = "◉"
	dormantGlyph = "◎"
)

// handoffGrace is how much longer than the configured wait for an idle agent
// one handoff is allowed to take, covering the GitHub round trip at the start
// and the herdr calls at the end.
const handoffGrace = 2 * time.Minute

// dispatcher is the handoff surface the app depends on. Keeping it an
// interface lets the TUI tests run without herdr, a checkout or an agent.
type dispatcher interface {
	Dispatch(ctx context.Context, req handoff.Request) (handoff.Result, error)
	DryRun() bool
}

// armed reports whether a pull request is being watched.
func (a *App) armed(key model.Key) bool {
	return a.state.Armed(key.String())
}

// toggleWatch arms or disarms the selected pull request. Arming is per pull
// request on purpose: a review whose remaining comments are never going to be
// resolved should cost nothing to leave on screen.
func (a *App) toggleWatch() tea.Cmd {
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}
	if a.store == nil {
		return status("cannot remember what is watched: " + a.storeErr.Error())
	}

	key := pr.Key()
	armed := a.state.ToggleArmed(key.String())
	if err := a.saveState(); err != nil {
		return status(err.Error())
	}

	tick := a.syncWatch()
	if !armed {
		delete(a.feedback, key)
		a.setWatchOperation(key, "")
		a.recordWatchActivity(key, "stopped watching")
		return tea.Batch(tick, status("stopped watching "+key.String()))
	}
	a.recordWatchActivity(key, "started watching")
	return tea.Batch(tick, status(fmt.Sprintf(
		"watching %s · its review feedback goes to an agent when it appears", key)))
}

// watchRetry is how long a refused request waits before the pull requests it
// covered are asked about again. It is the base polling interval, which the
// configuration has already clamped to something GitHub will not mind, rather
// than a number of its own: retrying a refusal faster than prutil polls in the
// ordinary course of things would be the wrong way round.
func (a *App) watchRetry() time.Duration {
	return max(a.homeCfg.Watch.BaseInterval.Duration(), time.Second)
}

// syncWatch reconciles the engine with the armed set and returns the command
// that wakes prutil when the next poll falls due.
//
// Only a pull request the current list holds can be watched, because polling
// addresses them by the node id the list came with. One that has since been
// merged stays armed in the file and simply stops being asked about.
func (a *App) syncWatch() tea.Cmd {
	keys := make([]model.Key, 0, a.state.ArmedCount())
	for _, pr := range a.views[viewOpen].prs {
		if pr.NodeID != "" && a.state.Armed(pr.Key().String()) {
			keys = append(keys, pr.Key())
		}
	}
	a.engine.Sync(keys, a.now())
	return a.scheduleWatch()
}

// scheduleWatch asks to be woken when the engine next wants to poll. The
// sequence number is bumped on every schedule, so a tick left over from a
// schedule that has been replaced is dropped rather than polling twice.
func (a *App) scheduleWatch() tea.Cmd {
	next, ok := a.engine.NextDue()
	if !ok {
		a.watchSeq++
		return nil
	}

	a.watchSeq++
	seq := a.watchSeq
	delay := max(next.Sub(a.now()), 0)
	return tea.Tick(delay, func(time.Time) tea.Msg { return watchTickMsg{seq: seq} })
}

// pollWatched asks GitHub the cheap question about every pull request due now,
// in one request whatever repositories they are spread across.
func (a *App) pollWatched() tea.Cmd {
	now := a.now()
	due := a.engine.Due(now)
	if len(due) == 0 {
		return a.scheduleWatch()
	}

	byID := make(map[string]model.Key, len(due))
	ids := make([]string, 0, len(due))
	for _, pr := range a.views[viewOpen].prs {
		if !slices.Contains(due, pr.Key()) || pr.NodeID == "" {
			continue
		}
		byID[pr.NodeID] = pr.Key()
		ids = append(ids, pr.NodeID)
	}
	if len(ids) == 0 {
		a.engine.Defer(due, a.watchRetry(), now)
		return a.scheduleWatch()
	}
	for _, key := range due {
		a.setWatchOperation(key, "checking for changes")
		a.recordWatchActivity(key, "started a change check")
	}

	client := a.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		snaps, err := client.WatchSnapshot(ctx, ids)
		if err != nil {
			return watchErrMsg{keys: due, err: err}
		}
		for i := range snaps {
			snaps[i].Key = byID[snaps[i].NodeID]
		}
		return watchSnapshotMsg{keys: due, snaps: snaps}
	}
}

// applyWatch takes a batch of readings and asks the expensive question of
// whichever pull requests moved.
func (a *App) applyWatch(msg watchSnapshotMsg) tea.Cmd {
	now := a.now()
	precise := a.engine.Observe(msg.snaps, now)

	// A pull request GitHub answers with a null node, or one the reply simply
	// did not cover, produces no reading. Observe only reschedules what it has
	// a reading for, so such a pull request stays due at a time already past,
	// the next schedule computes a zero delay, and the poll repeats as fast as
	// the round trip allows. Pushing it out is what a refused poll already
	// does, and it is the right answer here too.
	answered := make(map[model.Key]bool, len(msg.snaps))
	for _, snap := range msg.snaps {
		answered[snap.Key] = true
	}
	unanswered := make([]model.Key, 0, len(msg.keys))
	for _, key := range msg.keys {
		if !answered[key] {
			unanswered = append(unanswered, key)
		}
	}
	a.engine.Defer(unanswered, a.watchRetry(), now)

	for _, key := range msg.keys {
		a.setWatchOperation(key, "")
		switch {
		case !answered[key]:
			a.recordWatchActivity(key, "GitHub said nothing about it; asking again later")
		case slices.Contains(precise, key):
			a.setWatchOperation(key, "reading review feedback")
			a.recordWatchActivity(key, "changes found; reading review feedback")
		default:
			a.recordWatchActivity(key, "checked for changes")
		}
	}

	cmds := []tea.Cmd{a.scheduleWatch()}
	for _, key := range precise {
		cmds = append(cmds, a.loadReview(key, false))
	}
	return tea.Batch(cmds...)
}

// loadReview reads the review conversations for a watcher or manual discovery.
func (a *App) loadReview(key model.Key, manual bool) tea.Cmd {
	if a.reviewing[key] {
		return nil
	}
	a.reviewing[key] = true
	client := a.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		review, err := client.ReviewThreads(ctx, key)
		if err != nil {
			return watchReviewMsg{key: key, manual: manual, err: err}
		}
		return watchReviewMsg{key: key, manual: manual, review: review}
	}
}

// applyReview decides what a pull request's review conversations mean: nothing
// at all, or work for an agent.
func (a *App) applyReview(msg watchReviewMsg) tea.Cmd {
	now := a.now()
	delete(a.reviewing, msg.key)
	a.setWatchOperation(msg.key, "")
	if msg.err != nil {
		a.engine.Defer([]model.Key{msg.key}, a.watchRetry(), now)
		a.recordWatchActivity(msg.key, "could not read review feedback: "+msg.err.Error())
		return status("could not read the review threads on " + msg.key.String() + ": " + msg.err.Error())
	}

	feedback := msg.review.Feedback()
	a.feedback[msg.key] = len(feedback)
	a.engine.Precise(msg.key, len(feedback), false, now)
	activity := fmt.Sprintf("review feedback: %d %s awaiting",
		len(feedback), plural(len(feedback), "thread"))
	if msg.manual {
		activity = "manual discovery: " + activity
	}
	a.recordWatchActivity(msg.key, activity)

	fresh := model.Unhandled(feedback, a.state.Get(msg.key.String()).NotifiedThreads)
	if len(fresh) == 0 || a.handing[msg.key] {
		if msg.manual && len(fresh) == 0 {
			return status("no new review feedback on " + msg.key.String())
		}
		return nil
	}

	pr, ok := a.prByKey(msg.key)
	if !ok {
		return nil
	}
	if a.hand == nil {
		return status(fmt.Sprintf("%s has %d new review %s, but prutil cannot reach herdr",
			msg.key, len(fresh), plural(len(fresh), "comment")))
	}

	a.handing[msg.key] = true
	a.setWatchOperation(msg.key, "handing feedback to an agent")
	activity = "started an automatic handoff"
	if msg.manual {
		activity += " after manual discovery"
	}
	a.recordWatchActivity(msg.key, activity)
	return tea.Batch(
		a.handoffOf(handoffMsg{
			pr:      pr,
			open:    len(feedback),
			fresh:   len(fresh),
			threads: model.Digest(feedback),
		}),
		status(fmt.Sprintf("%s has %d new review %s · handing it to an agent…",
			msg.key, len(fresh), plural(len(fresh), "comment"))),
		a.spin.Tick,
	)
}

// notifyNewFeedback manually enters the watcher path after its change-detection
// step. It still uses the automatic handoff semantics: only fresh feedback is
// sent, and it cannot provision a workspace or agent.
func (a *App) notifyNewFeedback() tea.Cmd {
	if a.active != viewOpen {
		return status("new-feedback notification is available only for open pull requests")
	}
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}
	key := pr.Key()
	if a.handing[key] {
		return status("already handing " + key.String() + " over")
	}
	if a.reviewing[key] {
		return status("already reading review feedback on " + key.String())
	}

	a.setWatchOperation(key, "reading review feedback for manual discovery")
	a.recordWatchActivity(key, "manually triggered review discovery")
	return tea.Batch(
		a.loadReview(key, true),
		a.spin.Tick,
	)
}

// prByKey finds a pull request in the open list.
func (a *App) prByKey(key model.Key) (model.PullRequest, bool) {
	for _, pr := range a.views[viewOpen].prs {
		if pr.Key() == key {
			return pr, true
		}
	}
	return model.PullRequest{}, false
}

// handOff sends the selected pull request's open review feedback to a coding
// agent. It is the manual trigger: it ignores whatever prutil has handed over
// before, because a reader pressing the key has decided the work is worth
// doing again.
func (a *App) handOff() tea.Cmd {
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}
	if a.hand == nil {
		return status("cannot reach herdr: " + a.handErr.Error())
	}
	if a.handing[pr.Key()] {
		return status("already handing " + pr.Key().String() + " over")
	}
	if a.reviewing[pr.Key()] {
		return status("already reading review feedback on " + pr.Key().String())
	}

	a.handing[pr.Key()] = true
	a.setWatchOperation(pr.Key(), "reading review feedback for a manual handoff")
	a.recordWatchActivity(pr.Key(), "started a manual handoff")
	note := "reading the review threads on " + pr.Key().String() + "…"
	if a.hand.DryRun() {
		note = "dry run: " + note
	}
	return tea.Batch(a.dispatch(pr), status(note), a.spin.Tick)
}

// dispatch reads a pull request's review threads and gives whatever is still
// open to an agent. It is deliberately not tied to the refresh generation: a
// handoff can take minutes, and a refresh in the meantime is no reason to
// throw away the record of what was sent.
func (a *App) dispatch(pr model.PullRequest) tea.Cmd {
	client, send := a.client, a.sender(true)
	notified := a.state.Get(pr.Key().String()).NotifiedThreads
	budget := a.handoffBudget()

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()

		review, err := client.ReviewThreads(ctx, pr.Key())
		if err != nil {
			return handoffMsg{pr: pr, err: err, result: handoff.Result{
				Outcome: home.OutcomeFailed,
				Detail:  err.Error(),
			}}
		}

		feedback := review.Feedback()
		msg := handoffMsg{
			pr:      pr,
			open:    len(feedback),
			fresh:   len(model.Unhandled(feedback, notified)),
			threads: model.Digest(feedback),
		}
		if len(feedback) == 0 {
			msg.nothing = true
			return msg
		}
		return send(ctx, msg)
	}
}

// handoffOf gives an agent feedback prutil has already read, which is what the
// watcher does: it has the threads in hand and has no reason to ask for them
// again.
func (a *App) handoffOf(msg handoffMsg) tea.Cmd {
	send, budget := a.sender(false), a.handoffBudget()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		return send(ctx, msg)
	}
}

// sender closes over the dispatcher so that a command can run off the update
// loop without reaching back into the app.
func (a *App) sender(allowProvision bool) func(context.Context, handoffMsg) handoffMsg {
	hand := a.hand
	return func(ctx context.Context, msg handoffMsg) handoffMsg {
		msg.result, msg.err = hand.Dispatch(ctx, handoff.Request{
			PR:              msg.pr,
			UnresolvedCount: msg.open,
			NewCount:        msg.fresh,
			Threads:         msg.threads,
			AllowProvision:  allowProvision,
		})
		return msg
	}
}

// handoffBudget bounds one handoff: the wait for a busy agent, plus room for
// the GitHub round trip in front of it and the herdr calls behind.
func (a *App) handoffBudget() time.Duration {
	return a.homeCfg.Herdr.WaitForIdle.Duration() + handoffGrace
}

// applyHandoff records what became of a handoff: a line in the log whatever
// happened, and the threads marked as handed over only when they really were.
func (a *App) applyHandoff(msg handoffMsg) error {
	delete(a.handing, msg.pr.Key())
	a.setWatchOperation(msg.pr.Key(), "")
	if msg.nothing {
		a.recordWatchActivity(msg.pr.Key(), "no open review feedback")
		return nil
	}

	var recordErr error
	if msg.result.Outcome == home.OutcomeSent {
		a.state.RecordHandoff(msg.pr.Key().String(), msg.threads, a.now())
		// An agent will be minutes over this, so the pull request moves onto
		// the slower of the two backoffs until the work comes back.
		a.engine.Precise(msg.pr.Key(), msg.open, true, a.now())
		if err := a.saveState(); err != nil {
			recordErr = errors.Join(recordErr, err)
		}
	}

	record := home.Handoff{
		At:          a.now(),
		PR:          msg.pr.Key().String(),
		URL:         msg.pr.URL,
		Outcome:     msg.result.Outcome,
		Target:      msg.result.Target,
		Kind:        msg.result.Kind,
		Dir:         msg.result.Dir,
		Provisioned: msg.result.Provisioned,
		Workspace:   msg.result.Workspace,
		Tab:         msg.result.Tab,
		Detail:      msg.result.Detail,
		Prompt:      msg.result.Prompt,
	}
	if a.store != nil {
		if err := a.store.AppendHandoff(record); err != nil {
			recordErr = errors.Join(recordErr, err)
		}
	}
	outcome := msg.result.Outcome
	if outcome == "" {
		outcome = home.OutcomeFailed
	}
	event := "handoff " + outcome
	if msg.result.Detail != "" {
		event += ": " + msg.result.Detail
	} else if msg.err != nil {
		event += ": " + msg.err.Error()
	}
	a.recordWatchActivity(msg.pr.Key(), event)
	return recordErr
}

// handoffNote is what the status line says about a finished handoff.
func (a *App) handoffNote(msg handoffMsg) string {
	key := msg.pr.Key().String()
	if msg.nothing {
		return "no open review feedback on " + key
	}

	counts := fmt.Sprintf("%d of %d open %s new", msg.fresh, msg.open, plural(msg.open, "thread"))
	target := msg.result.Kind
	if target == "" {
		target = msg.result.Target
	} else if msg.result.Target != "" {
		target += " " + msg.result.Target
	}

	switch {
	case errors.Is(msg.err, handoff.ErrNoAgent):
		return fmt.Sprintf("%s: %s, but no agent is checked out in %s", key, counts, msg.pr.Repo)
	case errors.Is(msg.err, handoff.ErrBlocked):
		return fmt.Sprintf("%s: %s is waiting on a dialog of its own, so %s was not sent", key, target, key)
	case errors.Is(msg.err, handoff.ErrStillWorking):
		return fmt.Sprintf("%s: %s is still working after %s, so nothing was sent", key, target, model.HumanDuration(msg.result.Waited))
	case msg.err != nil:
		return "could not hand " + key + " over: " + msg.err.Error()
	case msg.result.Outcome == home.OutcomeDryRun:
		return fmt.Sprintf("dry run: %s would go to %s · %s%s", key, target, counts, a.logNote())
	default:
		action := "handed"
		if msg.result.Provisioned {
			action = "created a workspace and handed"
		}
		note := fmt.Sprintf("%s %s to %s · %s", action, key, target, counts)
		if msg.result.Waited > 0 {
			note += " · waited " + model.HumanDuration(msg.result.Waited)
		}
		return note
	}
}

// logNote points at the handoff log, when there is one to point at.
func (a *App) logNote() string {
	if a.store == nil {
		return ""
	}
	return " · see " + a.store.Path(home.HandoffFile)
}

// saveState writes the armed set back to the application directory, turning a
// failure into the sentence the reader should see rather than an error nobody
// is going to check.
func (a *App) saveState() error {
	if a.store == nil {
		return nil
	}
	a.state.Compact()
	if err := a.store.SaveState(a.state); err != nil {
		return fmt.Errorf("could not save what is watched: %w", err)
	}
	return nil
}

// watchSeg is the marker shown against a watched pull request on a list row.
// It is hollow for a pull request prutil has stopped asking about, so that a
// glance says which of the marks are still costing anything.
func (a *App) watchSeg(pr model.PullRequest) seg {
	if !a.armed(pr.Key()) {
		return seg{}
	}
	glyph := watchGlyph
	if tier, ok := a.engine.Tier(pr.Key()); ok && tier == watch.TierDormant {
		glyph = dormantGlyph
	}
	return seg{text: glyph, style: a.styles.Watch}
}

// feedbackSeg reports how much review feedback is still waiting on a watched
// pull request, which is the number the reader actually wants from a row.
func (a *App) feedbackSeg(pr model.PullRequest) seg {
	open, ok := a.feedback[pr.Key()]
	if !ok || open == 0 || !a.armed(pr.Key()) {
		return seg{}
	}
	return seg{
		text:  fmt.Sprintf("%d open %s", open, plural(open, "thread")),
		style: a.styles.Watch,
	}
}

// watchNote is what the header says about watching, or the empty string when
// nothing is armed.
func (a *App) watchNote() string {
	count := a.state.ArmedCount()
	if count == 0 {
		return ""
	}

	note := fmt.Sprintf(" · %s %d", watchGlyph, count)
	if next, ok := a.engine.NextDue(); ok {
		note += " · next " + model.HumanDuration(max(next.Sub(a.now()), time.Second))
	}
	return note
}

// plural adds an s to word unless n is one.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// watchTickMsg is one beat of the watcher. seq names the schedule that asked
// for it, so a tick from a schedule since replaced is dropped rather than
// polling a second time.
type watchTickMsg struct {
	seq int
}

// watchSnapshotMsg carries the cheap readings of every pull request polled in
// one request.
type watchSnapshotMsg struct {
	keys  []model.Key
	snaps []model.Snapshot
}

// watchReviewMsg carries review conversations requested by the watcher or the
// manual discovery binding.
type watchReviewMsg struct {
	key    model.Key
	manual bool
	review gh.Review
	err    error
}

// watchErrMsg reports a poll that GitHub refused, naming the pull requests it
// covered so their next attempt can be pushed out.
type watchErrMsg struct {
	keys []model.Key
	err  error
}

// handoffMsg reports one finished handoff attempt.
type handoffMsg struct {
	pr model.PullRequest
	// open is how many review threads are still waiting on the viewer, and
	// fresh how many of those prutil had not already handed over.
	open  int
	fresh int
	// threads maps each open thread to its newest comment, which is what is
	// remembered once the handoff lands.
	threads map[string]string
	// nothing means there was no open feedback to send, so no agent was asked.
	nothing bool
	result  handoff.Result
	err     error
}
