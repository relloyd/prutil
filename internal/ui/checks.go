package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/model"
)

// applyEffectiveChecks shares the complete detail verdict with every
// consumer of checks. A raw failing rollup alone does not establish failure.
func (a *App) applyEffectiveChecks(key model.Key, head string, checks []model.Check, at time.Time) tea.Cmd {
	if at.IsZero() {
		at = a.now()
	}
	status := model.CountChecks(checks).Rollup()
	if status == model.StatusUnknown {
		return nil
	}

	var notices tea.Cmd
	if _, ok := a.prByKey(key); ok {
		notices = a.notice([]reading{{
			key: key, at: at,
			facts: prFacts{head: head, passed: status == model.StatusSuccess, checksKnown: true},
		}})
	}
	if !a.armed(key) || head == "" {
		return notices
	}
	entry := a.mutate(key)
	entry.headOID, entry.headAt, entry.rollup = head, at, status
	a.engine.ReconcileChecks(key, head, status, a.now())
	return tea.Batch(notices, a.scheduleWatch(), a.postOnPass(key, head, status))
}

// sameHead reports whether a check read and a reading can be about the same
// commit. An empty head is one nobody read, not a different commit: the closed
// list never selects the head, so its rows would otherwise disagree with every
// read of their checks for good.
func sameHead(a, b string) bool {
	return a == "" || b == "" || a == b
}

// detailFor is the cached detail that can stand for head: loaded, for that
// commit, and saying something. A read that found no checks says nothing — it
// is what a read taken before Actions queued anything finds — so the list's
// own rollup speaks until the checks are read again.
func (a *App) detailFor(key model.Key, head string) (checkState, bool) {
	state, ok := a.checks[key]
	if !ok || !state.loaded || !sameHead(state.headOID, head) {
		return checkState{}, false
	}
	return state, state.err != nil || len(state.checks) > 0
}

// settled reports whether complete detail has reached a verdict that another
// read of the same head would not be expected to change.
func settled(state checkState) bool {
	return state.err == nil && len(state.checks) > 0 &&
		model.CountChecks(state.checks).Rollup() != model.StatusPending
}

func (a *App) clearStaleCheckRead(key model.Key, head string, at time.Time) {
	state, ok := a.checks[key]
	if ok && (state.headOID == "" || state.headOID == head) &&
		(state.askedAt.IsZero() || !at.Before(state.askedAt)) {
		delete(a.checks, key)
	}
}

// resolveFailureChecks fetches detail for an ambiguous rollup. It is not
// gated on the checks-passed notification: a raw failure shows as unknown
// until it is resolved, so a failing row the prefetch does not reach would
// otherwise lose its red dot. needsCheckResolution keeps it to one read per
// head once the verdict has settled. A watched pull request already receives
// this read on its poll.
func (a *App) resolveFailureChecks(key model.Key) tea.Cmd {
	if a.armed(key) || a.checks[key].loading {
		return nil
	}
	state := a.checks[key]
	state.loading = true
	state.askedAt = a.now()
	a.checks[key] = state
	return a.loadChecks(a.gen, key)
}

// needsCheckResolution reports whether the cheap rollup for head has to be
// checked against the detail.
//
// A raw failure is provisional until the detail says otherwise, but once the
// detail for that head has settled, reading it again on every load would cost
// a paginated read per failing pull request per interval and learn nothing:
// GitHub's raw rollup for a head with old failures stays failed whatever the
// newest attempts did. A new push changes the head, which asks again.
func (a *App) needsCheckResolution(key model.Key, head string, raw model.Status) bool {
	state, ok := a.checks[key]
	if ok && state.loading {
		return false
	}
	current := ok && state.loaded && sameHead(state.headOID, head)
	if raw == model.StatusFailure {
		return !current || !settled(state)
	}
	if !current || raw == model.StatusUnknown {
		return false
	}
	// An error, or an empty read the checks have registered since.
	if state.err != nil || len(state.checks) == 0 {
		return true
	}
	return model.CountChecks(state.checks).Rollup() != raw
}

// provisionalCheckStatus withholds actions when a cheap rollup conflicts with
// detail we have already read, or when the detail is incomplete.
func (a *App) provisionalCheckStatus(key model.Key, head string, raw model.Status) model.Status {
	if raw == model.StatusFailure {
		return model.StatusUnknown
	}
	state, ok := a.detailFor(key, head)
	if !ok {
		return raw
	}
	if state.err != nil || model.CountChecks(state.checks).Rollup() != raw {
		return model.StatusUnknown
	}
	return raw
}

func (a *App) resolveFailedPRs(prs []model.PullRequest) tea.Cmd {
	var cmds []tea.Cmd
	for _, pr := range prs {
		if a.needsCheckResolution(pr.Key(), pr.HeadOID, pr.Rollup) {
			cmds = append(cmds, a.resolveFailureChecks(pr.Key()))
		}
	}
	return tea.Batch(cmds...)
}

func (a *App) resolveFailedSnapshots(snaps []model.Snapshot) tea.Cmd {
	var cmds []tea.Cmd
	for _, snap := range snaps {
		if snap.Key.Repo != "" && a.needsCheckResolution(snap.Key, snap.HeadOID, snap.Rollup) {
			cmds = append(cmds, a.resolveFailureChecks(snap.Key))
		}
	}
	return tea.Batch(cmds...)
}
