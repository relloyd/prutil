package ui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/model"
)

// The checks passed comment is what P arms: a comment, configured as
// checks_passed.comment, that prutil posts itself on a watched pull request
// once every check on its head commit has passed, such as a command a
// deployment bot answers. It is posted once per head commit, so a push whose
// checks pass is posted for again and a poll that finds them still green is
// not.
//
// It reads the rollup the watcher already reads, so it costs no requests until
// it posts. And like every other thing prutil does on its own, it waits until
// the review threads have been read and posts nothing while the pull request
// is held: a deployment runs the pull request's code.

// passGlyph marks a list row whose pull request has the checks passed comment
// armed: something goes out once it is green. It is a standing instruction
// that posts to GitHub, so it is worth seeing without opening each one.
const passGlyph = "↗"

// passSeg is the mark shown against a pull request with P armed.
func (a *App) passSeg(pr model.PullRequest) seg {
	if !a.state.PostOnPass(pr.Key().String()) {
		return seg{}
	}
	return seg{text: passGlyph, style: a.styles.Watch}
}

// passNote is what the header says about P, apart from the watch tally: the
// two watch glyphs add up to everything watched, and this is not a third kind
// of watch but something some of them also do.
func (a *App) passNote() string {
	n := a.state.PostOnPassCount()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" · %s %d on pass", passGlyph, n)
}

// togglePostOnPass arms or disarms the checks passed comment on the selected
// pull request.
func (a *App) togglePostOnPass() tea.Cmd {
	if a.active != viewOpen {
		return status("posting when checks pass is available only for open pull requests")
	}
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}
	key := pr.Key()
	name := key.String()
	by := a.keys.PostOnPass.Help().Key

	if a.state.PostOnPass(name) {
		a.state.TogglePostOnPass(name)
		if err := a.saveState(); err != nil {
			return status(err.Error())
		}
		a.mutate(key).passNote = ""
		a.recordWatchActivity(key, "stopped posting when checks pass")
		return status(fmt.Sprintf("no longer posting on %s when its checks pass", key))
	}

	if !a.armed(key) {
		return status(fmt.Sprintf("%s is not watched · press %s to watch it, then %s",
			key, a.keys.Watch.Help().Key, by))
	}
	comment := a.homeCfg.ChecksPassed.CommentFor(key.Repo)
	if comment == "" {
		return status(fmt.Sprintf("no checks passed comment for %s · set one in settings (%s)",
			key.Repo, a.keys.Settings.Help().Key))
	}
	// Arming on a head whose checks have already passed posts at the next poll,
	// so that is said, and asked, before it happens rather than after.
	if a.passedNow(pr) && !a.confirms(key, confirmPostOnPass) {
		return status(fmt.Sprintf("checks have already passed on %s · press %s again to post %s now",
			key, by, comment))
	}

	a.state.TogglePostOnPass(name)
	if err := a.saveState(); err != nil {
		return status(err.Error())
	}
	a.mutate(key).passNote = ""
	a.recordWatchActivity(key, "posting "+comment+" when checks pass")
	a.engine.WakeKey(key, a.now())
	return tea.Batch(a.scheduleWatch(), status(fmt.Sprintf(
		"posting %s on %s each time its checks pass", comment, key)))
}

// passedNow reports whether every check on the pull request's head has
// passed, by the newest reading prutil holds.
func (a *App) passedNow(pr model.PullRequest) bool {
	if got := a.runtimeOf(pr.Key()); got.headOID != "" && got.rollup != model.StatusUnknown {
		return got.rollup == model.StatusSuccess
	}
	return pr.Rollup == model.StatusSuccess
}

// postOnPass posts the checks passed comment if a reading of head and rollup
// calls for it. Anything that stops it is recorded once, in passNote, rather
// than on every poll that finds the same thing.
func (a *App) postOnPass(key model.Key, head string, rollup model.Status) tea.Cmd {
	name := key.String()
	if rollup != model.StatusSuccess || head == "" || !a.state.PostOnPass(name) {
		return nil
	}
	if a.state.Get(name).LastPassCommentHead == head {
		return nil
	}
	entry := a.mutate(key)
	if entry.postingPass {
		return nil
	}
	comment := a.homeCfg.ChecksPassed.CommentFor(key.Repo)
	switch {
	case comment == "":
		a.passBlocked(key, "checks passed, but no checks passed comment is configured for "+key.Repo)
		return nil
	case !entry.holdKnown:
		// As with failed checks: not having read the threads is not having
		// read them and found nothing. The read's reply comes back here.
		a.passBlocked(key, "checks passed; reading the review threads before posting")
		return a.loadReview(key, false)
	case entry.hold.Held():
		a.passBlocked(key, "checks passed, but not posting "+comment+": "+holdReason(entry.hold))
		return nil
	}
	pr, ok := a.prByKey(key)
	if !ok {
		return nil
	}

	entry.postingPass, entry.passNote = true, ""
	a.recordWatchActivity(key, "checks passed; posting "+comment)
	return tea.Batch(a.sendPassComment(pr, comment, head), a.spin.Tick)
}

// passBlocked records why the checks passed comment was not posted, once for
// each reason in a row.
func (a *App) passBlocked(key model.Key, why string) {
	entry := a.mutate(key)
	if entry.passNote == why {
		return
	}
	entry.passNote = why
	a.recordWatchActivity(key, why)
}

// postOnPassAfterReview gives a review read's reply to postOnPass, which may
// have been waiting on it to learn whether the pull request is held.
func (a *App) postOnPassAfterReview(msg watchReviewMsg) tea.Cmd {
	if msg.err != nil {
		return nil
	}
	got := a.runtimeOf(msg.key)
	if !got.holdKnown {
		return nil
	}
	return a.postOnPass(msg.key, got.headOID, got.rollup)
}

// sendPassComment posts the comment off the update loop. A dry run records
// what it would have posted instead, since a deployment is exactly the sort of
// thing a dry run is for not doing.
func (a *App) sendPassComment(pr model.PullRequest, comment, head string) tea.Cmd {
	client, dry := a.client, a.homeCfg.Herdr.DryRun
	return func() tea.Msg {
		msg := passCommentMsg{key: pr.Key(), comment: comment, head: head, dryRun: dry}
		if dry {
			return msg
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		msg.err = client.AddComment(ctx, pr.NodeID, comment)
		return msg
	}
}

// passCommentMsg reports the outcome of posting the checks passed comment.
type passCommentMsg struct {
	key     model.Key
	comment string
	head    string
	dryRun  bool
	err     error
}

// applyPassComment records a posted comment against its head, so that it is
// not posted again for that commit. A comment that failed is not recorded,
// and the next poll that finds the checks green tries again.
func (a *App) applyPassComment(msg passCommentMsg) tea.Cmd {
	a.mutate(msg.key).postingPass = false
	if msg.err != nil {
		a.recordWatchActivity(msg.key, "could not post "+msg.comment+": "+msg.err.Error())
		return status(fmt.Sprintf("could not post %s on %s: %s", msg.comment, msg.key, msg.err.Error()))
	}
	// The reader may have pressed P again while it was on its way, and a head
	// recorded for a pull request no longer armed would outlive the arming.
	if a.state.PostOnPass(msg.key.String()) {
		a.state.RecordPassComment(msg.key.String(), msg.head)
		if err := a.saveState(); err != nil {
			return status(err.Error())
		}
	}
	done := "posted " + msg.comment
	if msg.dryRun {
		done = "dry run: would have posted " + msg.comment
	}
	a.recordWatchActivity(msg.key, fmt.Sprintf("%s for %s", done, shortHead(msg.head)))
	return status(fmt.Sprintf("checks passed on %s · %s", msg.key, done))
}

// shortHead is a commit id as people write it.
func shortHead(head string) string {
	return head[:min(len(head), 7)]
}
