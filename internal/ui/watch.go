package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
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
	Notify(ctx context.Context, title, body string)
}

// armed reports whether a pull request is being watched.
func (a *App) armed(key model.Key) bool {
	return a.state.Armed(key.String())
}

// clearWatch drops what a watch leaves behind on a pull request and records why
// it stopped. Both ways of disarming end here, so neither can grow a step the
// other lacks.
//
// It leaves the armed flag itself alone, because the two callers reach it
// differently: toggleWatch has already flipped the flag and lets syncWatch
// reconcile the engine, while disarmFinished clears the flag and forgets the
// engine's copy directly. handing and reviewing are left alone too — a handoff
// or a read still in flight reports its own end, and clearing the flag here
// would strand the spinner it answers to.
func (a *App) clearWatch(key model.Key, why string) {
	entry := a.mutate(key)
	entry.feedback, entry.hasFeedback = 0, false
	a.setWatchOperation(key, "")
	a.recordWatchActivity(key, why)
}

// forgetHandoffs drops what a pull request remembers about feedback already
// sent to an agent, and reports whether there was anything to drop.
//
// Compact keeps any entry still holding notified threads, so without this a
// pull request that ever reached a handoff — the ordinary life of a watched
// one — would keep a record in the state file for good. The threads are only
// worth keeping to recognise feedback already sent, and GitHub does not reuse a
// pull request number, so a finished one will never be asked about again.
//
// Only retirement may call it, which is why it is not part of clearWatch. A
// second press of w is the reader changing their mind about an open pull
// request, and forgetting there would hand every thread over again the next
// time they watched it.
func (a *App) forgetHandoffs(key model.Key) bool {
	got := a.state.Get(key.String())
	if len(got.NotifiedThreads) == 0 && got.LastHandoff.IsZero() {
		return false
	}
	entry := a.state.Mutate(key.String())
	entry.NotifiedThreads, entry.LastHandoff = nil, time.Time{}
	return true
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
	// Arming addresses a pull request by the node id the open list came with,
	// and the failed-check handoff below answers only for an open one, so a row
	// from another view could be marked and then never polled: a watch the
	// tally counts, that costs nothing and answers for nothing. Disarming stays
	// available from every view, so a mark already made can always be taken off.
	if !a.armed(key) && a.active != viewOpen {
		return status("watching is available only for open pull requests")
	}
	armed := a.state.ToggleArmed(key.String())
	if err := a.saveState(); err != nil {
		return status(err.Error())
	}

	tick := a.syncWatch()
	if !armed {
		a.clearWatch(key, "stopped watching")
		return tea.Batch(tick, status("stopped watching "+key.String()))
	}
	a.recordWatchActivity(key, "started watching")
	return tea.Batch(tick, a.checkHandoff(false, false, pr.HeadOID), status(fmt.Sprintf(
		"watching %s · its review feedback goes to an agent when it appears", key)))
}

func (a *App) selectedFailedCheck() bool {
	if a.focus != paneDetail || a.section != detailChecks {
		return false
	}
	checks := a.selectedChecks().checks
	return a.detailCursor >= 0 && a.detailCursor < len(checks) && checks[a.detailCursor].Status == model.StatusFailure
}

// investigateChecks is the reader asking for a failed-check investigation now,
// by whichever key reached it: W does when the cursor is on a failed check, and
// F always does.
//
// It exists so that a held pull request asks for a second press here as it does
// for a handoff. force answers whether the investigation is due — the armed
// check, the wait on checks still running, the brake on a head already looked
// at — and none of those is the question of whether the reader has judged the
// trust boundary. An explicit key press is a good reason to skip a schedule; it
// is not on its own evidence that anybody has read the hostile thread.
//
// The question is asked here rather than in applyFailedChecks because the
// checks may still have to be fetched. A confirmation that appears a round trip
// after the key press is worse than none: the reader has moved on, and a second
// press in the meantime would answer a question that had not been asked yet.
//
// A pull request whose threads have not been read has no hold to ask about, so
// nothing is asked. Both explicit keys act on what is known rather than waiting
// on GitHub, which is the same trade handOff makes.
func (a *App) investigateChecks(by key.Binding, allowProvision bool) tea.Cmd {
	if pr, ok := a.selectedPR(); ok && a.active == viewOpen {
		if hold := a.runtimeOf(pr.Key()).hold; hold.Held() && !a.confirms(pr.Key(), confirmHeldChecks) {
			return status(fmt.Sprintf("%s is held, %s · press %s again to investigate anyway",
				pr.Key(), holdReason(hold), by.Help().Key))
		}
	}
	// Everything else, including saying why this is not an open pull request,
	// belongs to checkHandoff, where that wording already lives.
	return a.checkHandoff(true, allowProvision)
}

func (a *App) checkHandoff(force, allowProvision bool, headOID ...string) tea.Cmd {
	pr, ok := a.selectedPR()
	if !ok || a.active != viewOpen {
		return status("failed-check investigation is available only for open pull requests")
	}
	if a.hand == nil {
		return status("cannot reach herdr: " + a.handErr.Error())
	}
	key := pr.Key()
	entry := a.mutate(key)
	if entry.handing {
		return status("already handing " + key.String() + " over")
	}
	entry.handing = true
	if len(headOID) > 0 {
		entry.headOID = headOID[0]
	} else if pr.HeadOID != "" {
		entry.headOID = pr.HeadOID
	} else if entry.headOID == "" {
		entry.headOID = pr.HeadOID
	}
	state := a.checks[key]
	if state.loaded && !force {
		return a.applyFailedChecks(key, entry.headOID, state.checks, false, allowProvision)
	}
	return tea.Batch(a.loadChecksForHandoff(a.gen, key, entry.headOID, force, allowProvision), a.spin.Tick)
}

func (a *App) applyFailedChecks(key model.Key, headOID string, checks []model.Check, force, allowProvision bool) tea.Cmd {
	entry := a.mutate(key)
	entry.handing = false
	// The read this answers was dispatched while the pull request was watched,
	// and disarmFinished can retire it before the reply lands. Handing it over
	// now would give an agent work on a pull request prutil has just said it
	// stopped watching, and SetArmed has cleared LastCheckHandoffHead, so the
	// repeat brake below is gone exactly when it would be needed. force is the
	// reader's own key press, which answers whatever is armed.
	if !force && !a.armed(key) {
		a.setWatchOperation(key, "")
		return nil
	}
	failed := make([]model.Check, 0, len(checks))
	pending := false
	for _, check := range checks {
		if check.Status == model.StatusPending || check.Status == model.StatusUnknown {
			pending = true
		}
		if check.Status == model.StatusFailure {
			failed = append(failed, check)
		}
	}
	if !force && pending {
		return nil
	}
	if len(failed) == 0 || (!force && a.state.Get(key.String()).LastCheckHandoffHead == headOID) {
		a.setWatchOperation(key, "")
		return nil
	}
	pr, ok := a.prByKey(key)
	if !ok {
		return nil
	}
	// A held pull request holds its failed checks too. The check prompt carries
	// no comment text, but the agent it starts reads the same pull request, and
	// under fallback: new this path can create a worktree and start one, so it
	// must not be the way round a hold.
	//
	// Not having read the review threads is not the same as having read them
	// and found nothing, and the difference is the ordinary case rather than a
	// corner of one. prRuntime is session-only, so every restart begins not
	// knowing; applyWatch dispatches the review read and the check read in one
	// batch, which run concurrently and can land in either order; the check
	// read is dispatched whenever the rollup is failure, whether or not the
	// tripwire flagged the pull request for a precise read at all; and a review
	// read that GitHub refused leaves nothing behind. Treating any of those as
	// "nothing is wrong" would be a way past the gate.
	//
	// So an unread pull request asks for its threads instead of handing work
	// over. Nothing marks the head as investigated, so the next poll tries
	// again, by which time the read has landed.
	if !force {
		switch {
		case !entry.holdKnown:
			a.setWatchOperation(key, "")
			cmd := a.loadReview(key, false)
			if cmd == nil {
				// A read is already in flight; its reply settles this.
				return nil
			}
			a.recordWatchActivity(key, "failed checks wait on the review threads being read")
			return cmd
		case entry.hold.Held():
			a.setWatchOperation(key, "")
			return a.holdFeedback(pr, entry.hold, len(failed), "failed check")
		}
	}
	entry.handing = true
	a.setWatchOperation(key, "handing failed checks to an agent")
	a.recordWatchActivity(key, fmt.Sprintf("found %d failed %s", len(failed), plural(len(failed), "check")))
	return tea.Batch(a.failedCheckHandoff(handoffMsg{pr: pr, check: true, headOID: headOID, checks: failed, allowProvision: allowProvision, force: force, viewer: a.viewer}), a.spin.Tick)
}

func (a *App) failedCheckHandoff(msg handoffMsg) tea.Cmd {
	hand, budget := a.hand, a.handoffBudget()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		msg.result, msg.err = hand.Dispatch(ctx, handoff.Request{PR: msg.pr, CheckHandoff: true, HeadOID: msg.headOID, Checks: msg.checks, AllowProvision: msg.allowProvision, Viewer: msg.viewer})
		return msg
	}
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
		return watchSnapshotMsg{keys: due, snaps: snaps, at: now}
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
		if snap, ok := snapshotFor(msg.snaps, key); ok {
			entry := a.mutate(key)
			entry.headOID, entry.rollup = snap.HeadOID, snap.Rollup
		}
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

	cmds := []tea.Cmd{a.scheduleWatch(), a.notice(readingsOfSnapshots(msg.snaps, msg.at))}
	for _, key := range precise {
		cmds = append(cmds, a.loadReview(key, false))
	}
	for _, snap := range msg.snaps {
		entry := a.runtimeOf(snap.Key)
		if snap.Rollup == model.StatusFailure && a.armed(snap.Key) && !entry.handing {
			cmds = append(cmds, a.loadChecksForHandoff(a.gen, snap.Key, snap.HeadOID, false, false))
		}
	}
	return tea.Batch(cmds...)
}

func snapshotFor(snaps []model.Snapshot, key model.Key) (model.Snapshot, bool) {
	for _, snap := range snaps {
		if snap.Key == key {
			return snap, true
		}
	}
	return model.Snapshot{}, false
}

// rereadArmedReviews reads the review conversations on every armed pull
// request again, which is what a change to the feedback rules needs.
//
// Nothing on GitHub has to change for a rule change to mean something
// different, and the tripwire only notices what moves a counter, so without
// this the counts on screen stay as they were computed under the old rule
// until `force_precise_every` comes round. A pull request the watcher has let
// go dormant would wait longer still.
func (a *App) rereadArmedReviews() tea.Cmd {
	cmds := make([]tea.Cmd, 0, a.state.ArmedCount())
	for _, pr := range a.views[viewOpen].prs {
		key := pr.Key()
		if pr.NodeID == "" || !a.armed(key) || a.runtimeOf(key).handing {
			continue
		}
		cmd := a.loadReview(key, false)
		if cmd == nil {
			continue
		}
		a.setWatchOperation(key, "reading review feedback after a settings change")
		a.recordWatchActivity(key, "settings changed; reading review feedback")
		cmds = append(cmds, cmd)
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(append(cmds, a.spin.Tick)...)
}

// loadReview reads the review conversations for a watcher or manual discovery.
func (a *App) loadReview(key model.Key, manual bool) tea.Cmd {
	if a.runtimeOf(key).reviewing {
		return nil
	}
	a.mutate(key).reviewing = true
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
	a.mutate(msg.key).reviewing = false
	a.setWatchOperation(msg.key, "")
	// As in applyFailedChecks: the watcher's read can land after
	// disarmFinished retired the pull request, and acting on it would hand a
	// merged one to an agent moments after saying it was no longer watched.
	// Manual discovery is the reader's own request, so it still answers.
	if !msg.manual && !a.armed(msg.key) {
		return nil
	}
	if msg.err != nil {
		a.engine.Defer([]model.Key{msg.key}, a.watchRetry(), now)
		a.recordWatchActivity(msg.key, "could not read review feedback: "+msg.err.Error())
		return status("could not read the review threads on " + msg.key.String() + ": " + msg.err.Error())
	}

	feedback := msg.review.Feedback(a.homeCfg.Watch.ReviewFilter())
	entry := a.mutate(msg.key)
	entry.feedback, entry.hasFeedback = len(feedback), true
	entry.hold, entry.holdKnown = msg.review.Hold(a.homeCfg.TrustPolicy()), true
	a.viewer = msg.review.Viewer
	entry.holdMark = holdMarkOf(msg.review.Threads)
	a.engine.Precise(msg.key, len(feedback), false, now)
	activity := fmt.Sprintf("review feedback: %d %s awaiting",
		len(feedback), plural(len(feedback), "thread"))
	if msg.manual {
		activity = "manual discovery: " + activity
	}
	a.recordWatchActivity(msg.key, activity)

	fresh := model.Unhandled(feedback, a.state.Get(msg.key.String()).NotifiedThreads)
	if len(fresh) == 0 || entry.handing {
		if msg.manual && len(fresh) == 0 {
			return status("no new review feedback on " + msg.key.String())
		}
		return nil
	}

	pr, ok := a.prByKey(msg.key)
	if !ok {
		return nil
	}
	if entry.hold.Held() {
		return a.holdFeedback(pr, entry.hold, len(fresh), "new review comment")
	}
	if a.hand == nil {
		return status(fmt.Sprintf("%s has %d new review %s, but prutil cannot reach herdr",
			msg.key, len(fresh), plural(len(fresh), "comment")))
	}

	entry.handing = true
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

// holdFeedback records feedback prutil is declining to hand over on its own
// because somebody outside the reader's trust boundary has spoken on the pull
// request.
//
// Only the trusted threads could have been sent instead, and that would not
// help: the prompt gives an agent the pull request, not a list of thread ids,
// and it goes and reads all of them.
func (a *App) holdFeedback(pr model.PullRequest, hold model.Hold, count int, noun string) tea.Cmd {
	key, why := pr.Key(), holdReason(hold)
	held := fmt.Sprintf("%d %s", count, plural(count, noun))

	a.recordWatchActivity(key, "held "+held+": "+why)
	if a.store != nil {
		_ = a.store.AppendHandoff(home.Handoff{
			At:      a.now(),
			PR:      key.String(),
			URL:     pr.URL,
			Outcome: home.OutcomeHeld,
			Detail:  why,
		})
	}

	line := fmt.Sprintf("%s: %s held, %s · W to send it anyway", key, held, why)
	return tea.Batch(status(line), a.announceHold(pr, held, why))
}

// announceHold raises the herdr notification a held handoff would have raised
// had it reached the dispatcher, once per newest comment.
//
// held is the outcome the reader is least likely to be sitting in front of
// prutil for: nothing has been sent, so nothing will come back to tell them.
// Every other outcome toasts, under the reader's own herdr.toast switch, and
// this one only did not because the decision is made before Dispatch.
func (a *App) announceHold(pr model.PullRequest, held, why string) tea.Cmd {
	entry := a.mutate(pr.Key())
	if a.hand == nil || entry.heldAnnounced == entry.holdMark {
		return nil
	}
	entry.heldAnnounced = entry.holdMark

	hand, title := a.hand, pr.Key().String()+": "+held+" held"
	return func() tea.Msg {
		hand.Notify(context.Background(), title, why+" · W to send it anyway")
		return nil
	}
}

// holdMarkOf names the newest comment on each unresolved thread, which is what
// a hold is announced once per. A held pull request keeps being polled for as
// long as it stays held, and somebody adding a comment is the only thing that
// makes it news again.
//
// It is the set rather than the most recent of them, because a thread's
// LatestAt is only as good as what GitHub returned: keyed on the newest by
// time, a reply that arrived without a timestamp would announce nothing.
//
// A pull request with no comments to name still has to be announceable once,
// so it answers with something no comment id can be rather than with nothing.
func holdMarkOf(threads []model.ReviewThread) string {
	ids := make([]string, 0, len(threads))
	for _, thread := range threads {
		if !thread.Resolved && thread.LatestID != "" {
			ids = append(ids, thread.LatestID)
		}
	}
	if len(ids) == 0 {
		return "-"
	}
	slices.Sort(ids)
	return strings.Join(ids, ",")
}

// holdReason says in one phrase why a pull request is held, for the status
// line, the activity feed and the durable log alike.
func holdReason(hold model.Hold) string {
	var why []string
	if len(hold.Authors) > 0 {
		why = append(why, "feedback from "+english(hold.Authors))
	}
	if len(hold.Hidden) > 0 {
		why = append(why, "hidden text in a comment by "+english(hold.Hidden))
	}
	if hold.Unknown {
		why = append(why, "threads prutil could not read in full")
	}
	// Semicolons rather than "and": each clause can already name several people
	// with an "and" of its own, and two levels of them read as one list.
	return strings.Join(why, "; ")
}

// english joins names the way a sentence does, because this reaches the reader
// as prose rather than as a list.
func english(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// notifyNewFeedback manually enters the watcher path after its change-detection
// step. It still uses the automatic handoff semantics: only fresh feedback is
// sent, and whether a missing agent is set up follows herdr.fallback, exactly
// as it does for the watcher.
func (a *App) notifyNewFeedback() tea.Cmd {
	if a.active != viewOpen {
		return status("new-feedback notification is available only for open pull requests")
	}
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}
	key := pr.Key()
	if a.runtimeOf(key).handing {
		return status("already handing " + key.String() + " over")
	}
	if a.runtimeOf(key).reviewing {
		return status("already reading review feedback on " + key.String())
	}

	a.setWatchOperation(key, "reading review feedback for manual discovery")
	a.recordWatchActivity(key, "manually triggered review discovery")
	return tea.Batch(
		a.loadReview(key, true),
		a.spin.Tick,
	)
}

// triggerAIReview posts the configured review trigger comment to the selected
// open pull request after confirmation.
func (a *App) triggerAIReview() tea.Cmd {
	if a.active != viewOpen {
		return status("AI review is available only for open pull requests")
	}
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}
	key := pr.Key()
	comment := a.homeCfg.Review.CommentFor(key.Repo)
	if comment == "" {
		return status("AI review is not configured (set review.comment in config)")
	}
	if a.runtimeOf(key).requestingReview {
		return status("already triggering AI review for " + key.String())
	}
	if a.runtimeOf(key).handing {
		return status("already handing " + key.String() + " over")
	}
	if a.runtimeOf(key).reviewing {
		return status("already reading review feedback on " + key.String())
	}

	if !a.confirms(key, confirmReview) {
		return status(fmt.Sprintf("press R again to post %s on %s", comment, key))
	}

	a.mutate(key).requestingReview = true
	a.setWatchOperation(key, "triggering AI review")
	a.recordWatchActivity(key, "triggering AI review ("+comment+")")
	return tea.Batch(
		a.sendAIReviewComment(pr, comment),
		a.spin.Tick,
		status(fmt.Sprintf("triggering AI review on %s…", key)),
	)
}

// sendAIReviewComment issues the comment request asynchronously via the GitHub client.
func (a *App) sendAIReviewComment(pr model.PullRequest, comment string) tea.Cmd {
	client := a.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		err := client.AddComment(ctx, pr.NodeID, comment)
		return triggerReviewMsg{key: pr.Key(), comment: comment, err: err}
	}
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
	if a.runtimeOf(pr.Key()).handing {
		return status("already handing " + pr.Key().String() + " over")
	}
	if a.runtimeOf(pr.Key()).reviewing {
		return status("already reading review feedback on " + pr.Key().String())
	}
	// W is the override for a hold, and the second press is what makes it one.
	// It names what it is waving through, because a reader who has not looked
	// at the thread is not in a position to judge it, and it clears either kind
	// of hold: an untrusted author is a judgement call, and a false positive
	// must not be a dead end.
	if hold := a.runtimeOf(pr.Key()).hold; hold.Held() && !a.confirms(pr.Key(), confirmHeldHandoff) {
		return status(fmt.Sprintf("%s is held, %s · press W again to send it anyway",
			pr.Key(), holdReason(hold)))
	}

	a.mutate(pr.Key()).handing = true
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
	budget, filter := a.handoffBudget(), a.homeCfg.Watch.ReviewFilter()

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

		feedback := review.Feedback(filter)
		msg := handoffMsg{
			pr:      pr,
			open:    len(feedback),
			fresh:   len(model.Unhandled(feedback, notified)),
			threads: model.Digest(feedback),
			viewer:  review.Viewer,
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
			Viewer:          msg.viewer,
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
	a.mutate(msg.pr.Key()).handing = false
	a.setWatchOperation(msg.pr.Key(), "")
	if msg.nothing {
		a.recordWatchActivity(msg.pr.Key(), "no open review feedback")
		return nil
	}

	var recordErr error
	if msg.result.Outcome == home.OutcomeSent {
		if msg.check {
			// Check handoffs are marked below even when dispatch fails, so an
			// automatic attempt is not repeated forever without a new head.
		} else {
			a.state.RecordHandoff(msg.pr.Key().String(), msg.threads, a.now())
		}
		// An agent will be minutes over this, so the pull request moves onto
		// the slower of the two backoffs until the work comes back.
		a.engine.Precise(msg.pr.Key(), msg.open, true, a.now())
		if err := a.saveState(); err != nil {
			recordErr = errors.Join(recordErr, err)
		}
	}
	if msg.check && !msg.force {
		a.state.Mutate(msg.pr.Key().String()).LastCheckHandoffHead = msg.headOID
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
		return fmt.Sprintf("%s: %s, but no agent in %s is working on it", key, counts, msg.pr.Repo)
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
//
// Not being in the engine at all counts as hollow too, and not only as
// dormant: syncWatch can schedule a pull request only when the open list holds
// it with a node id to address it by, so one it cannot is just as unpolled as
// one that has gone quiet. Filled would claim otherwise, and would disagree
// with the header's tally, which counts the same two cases together.
func (a *App) watchSeg(pr model.PullRequest) seg {
	if !a.armed(pr.Key()) {
		return seg{}
	}
	glyph := watchGlyph
	if tier, ok := a.engine.Tier(pr.Key()); !ok || tier == watch.TierDormant {
		glyph = dormantGlyph
	}
	return seg{text: glyph, style: a.styles.Watch}
}

// disarmFinished stops watching every pull request in prs that has merged or
// closed, and reports what it turned off.
//
// It is the only thing that ever disarms without the reader pressing w, and it
// waits for positive evidence: a row GitHub has returned whose state is no
// longer open. Absence from the open list would be the wrong signal, because
// that list is narrowed by the configured query and limit, so a pull request
// can drop out of it while still being open and still worth watching.
//
// Without this an armed entry outlives its pull request. Nothing polls it —
// syncWatch only ever schedules what the open list holds — and Compact keeps
// anything armed, so it would sit in the state file for good, counted in the
// header and answering for nothing.
func (a *App) disarmFinished(prs []model.PullRequest) tea.Cmd {
	if a.store == nil {
		return nil
	}

	var done []string
	swept := false
	for _, pr := range prs {
		key := pr.Key()
		if pr.State == model.PRStateOpen {
			continue
		}
		if !a.armed(key) {
			// No watch to stop, so nothing to tell the reader about. It can
			// still hold threads from a handoff made before they unwatched it,
			// and those would keep the record in the file for good: this loop
			// is the only thing that collects them, and it used to skip
			// anything unarmed before it got this far.
			swept = a.forgetHandoffs(key) || swept
			continue
		}
		a.state.SetArmed(key.String(), false)
		a.forgetHandoffs(key)
		a.engine.Forget(key)
		a.clearWatch(key, "stopped watching: "+pr.State.String())
		done = append(done, key.String())
	}
	if len(done) == 0 {
		// A sweep on its own still has to reach the file, or Compact never
		// gets the chance to drop what it just cleared. It stays silent: the
		// reader ended these watches themselves and has nothing to be told.
		if swept {
			if err := a.saveState(); err != nil {
				return status(err.Error())
			}
		}
		return nil
	}

	if err := a.saveState(); err != nil {
		return status(err.Error())
	}
	slices.Sort(done)
	return tea.Batch(a.scheduleWatch(), status(fmt.Sprintf(
		"stopped watching %d finished %s: %s",
		len(done), plural(len(done), "pull request"), strings.Join(done, ", "))))
}

// feedbackSeg reports how much review feedback is still waiting on a watched
// pull request, which is the number the reader actually wants from a row.
func (a *App) feedbackSeg(pr model.PullRequest) seg {
	got := a.runtimeOf(pr.Key())
	if !got.hasFeedback || got.feedback == 0 || !a.armed(pr.Key()) {
		return seg{}
	}
	return seg{
		text:  fmt.Sprintf("%d open %s", got.feedback, plural(got.feedback, "thread")),
		style: a.styles.Watch,
	}
}

// watchNote is what the header says about watching, or the empty string when
// nothing is armed.
//
// It splits the armed set the way the list rows do, because the two glyphs
// mean different things and a single filled count claimed more polling than
// was happening. Filled is what the engine is still asking about; hollow is
// the rest, which is the dormant ones plus any armed pull request the current
// list does not hold, since syncWatch can only schedule what it can address.
// The unit is spelled out: a bare number in a header is a number without a
// question.
func (a *App) watchNote() string {
	armed := a.state.ArmedCount()
	if armed == 0 {
		return ""
	}

	polling := a.engine.Polling()
	note := fmt.Sprintf(" · %s %d", watchGlyph, polling)
	if idle := armed - polling; idle > 0 {
		note += fmt.Sprintf(" %s %d", dormantGlyph, idle)
	}
	note += " watched"
	if next, ok := a.engine.NextDue(); ok {
		note += " · next poll " + model.HumanDuration(max(next.Sub(a.now()), time.Second))
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
	// at is when the readings were asked for.
	at time.Time
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
	pr             model.PullRequest
	check          bool
	allowProvision bool
	force          bool
	headOID        string
	checks         []model.Check
	// open is how many review threads are still waiting on the viewer, and
	// fresh how many of those prutil had not already handed over.
	open  int
	fresh int
	// threads maps each open thread to its newest comment, which is what is
	// remembered once the handoff lands.
	threads map[string]string
	// viewer is the login the review read behind this handoff was made as,
	// which is how the dispatcher tells the reader's own pull request from
	// somebody else's before it checks a head out.
	viewer string
	// nothing means there was no open feedback to send, so no agent was asked.
	nothing bool
	result  handoff.Result
	err     error
}

// triggerReviewMsg reports the outcome of posting a review trigger comment.
type triggerReviewMsg struct {
	key     model.Key
	comment string
	err     error
}
