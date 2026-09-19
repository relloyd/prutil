# Watch branch follow-ups: the disarm review

What a review of `fix/watch-honest-tally-and-disarm` turned up that is still
open. The review ran against `46dc18b`; line numbers below are current as of
`14c870f` unless a finding says otherwise.

## Provenance, and how much to trust each item

These came from an automated review agent, which was asked for PR 9 and
reviewed this branch instead. That makes the list useful but unearned: it was
never a considered review of this work. Each finding below carries its own
status.

- **Verified** means somebody read the code and confirmed the mechanism, and
  the finding says who or how.
- **Agent claim** means it has not been checked. The agent said it ran some of
  these; that claim itself is unverified. Treat the reasoning as a lead, not as
  fact, and confirm before acting.

Four findings from the original fourteen are already done and are not repeated
here: the three missing guards (`d29cc92`) and the duplicated disarm branch,
which `clearWatch` now holds (`14c870f`).

## Substantial

### 1. The state file still grows, and the design doc says it does not

**Status: mechanism verified.** `State.Compact` (`internal/home/state.go:122`)
deletes an entry only when `!entry.Armed && len(entry.NotifiedThreads) == 0`.
`disarmFinished` clears the armed flag but leaves `NotifiedThreads` and
`LastHandoff` in place, so every pull request that was watched to the point of
a handoff — the normal life of a watched pull request — keeps a permanent
record in the file.

`docs/watch/disarming-finished-pull-requests.md:28-30` states: "The save runs
the entry through `Compact`, so the record leaves the file rather than
lingering as `"armed": false`." That is true only for an entry that never had a
handoff. The stated problem, "a state file that only ever grows", is therefore
not fixed for the case that matters.

The agent argues that clearing `NotifiedThreads`/`LastHandoff` in the disarm
loop is safe because GitHub never reuses pull request numbers, which the design
doc itself argues elsewhere. That reasoning looks sound but has not been
checked against what else reads `NotifiedThreads`.

**Not yet decided:** whether to clear the two fields on disarm, or to teach
`Compact` that an unarmed entry whose pull request is known finished can go.

### 2. The detail pane contradicts the glyph it was meant to agree with

**Status: mechanism verified.** `watchFacts` takes its `armed` flag from the
engine, not from the state: `internal/ui/watch_detail.go:40` reads
`status, armed := a.engine.Status(key)`. So for a pull request that is armed
but that the engine does not hold, the row draws the hollow `◎`, the header
counts it, and the detail pane says "not watching" (`f.armed` gates
`watch_detail.go:62`, `:136`, `:193`).

That is exactly the state the new hollow glyph exists to show, and `46dc18b`'s
stated goal was that "a row and the header agree about the same pull request".
The third surface was not brought along. `TestAWatchedRowIsHollowWhenNothingIsScheduledToPollIt`
constructs precisely this state and asserts only the row.

Ways to reach it: arm from the closed view (no longer possible after
`d29cc92`), a pull request dropped from the open list by `-query`/`-limit`, or
one returned with an empty `NodeID` so `syncWatch` skipped it.

**Likely fix:** `armed` should come from `a.armed(key)`; the engine's `ok` flag
is the separate question of whether anything is scheduled, which the pane can
report as its own fact.

### 3. `watchAfterLoad` opts every future view into disarming

**Status: verified by reading.** `internal/ui/app.go:1049` routes on
`v != viewOpen`, not `v == viewClosed`:

```go
func (a *App) watchAfterLoad(v view) tea.Cmd {
	if v != viewOpen {
		return a.disarmFinished(a.views[v].prs)
	}
	return a.syncWatch()
}
```

`app.go:79` plans a third view ("for review-requested pull requests would slot
in before `viewCount`"), and AGENTS.md says adding one means "a constant before
`viewCount`, a case in `load`, and nothing else." A new view would be handed to
`disarmFinished` silently and never to `syncWatch`. Inert today only because
those rows happen to be open; a view that mixes states would disarm from a list
nobody intended as evidence.

The comment directly above says "The closed list is the one place…", so the
condition and its own comment already disagree.

### 4. The disarm only fires if the reader visits the closed view

**Status: agent claim, but consistent with the design doc.** `Init` dispatches
`loadOpen` only, so a reader who never presses tab never retires anything,
while the README now presents the hollow count as real watching state.

`docs/watch/disarming-finished-pull-requests.md` recommends option 1 — select
`state` on `PullRequest` in `watchQuery` and disarm in `Observe` on
`MERGED`/`CLOSED` — and notes it is scalar, pages nothing, and fits the query's
own cost rule. What shipped is the fallback for the overnight-merge case. The
common case, a pull request that merges while the watcher is polling it every
couple of minutes, still waits for a manual view switch.

This is a known, deliberately parked gap rather than a defect; the doc records
it. Listed here so the decision is not lost.

## Smaller

### 5. The retirement status names pull requests the footer then cuts off

**Status: verified by reading.** `internal/ui/watch.go:821-823` joins every
retired key into one line; `internal/ui/view.go:162` truncates the status to
the terminal width. Retire twelve at 120 columns and the reader gets
"stopped watching 12 finished pull requests: relloyd/a#1, relloyd/b#2, rell…".
The count survives, the names the message exists to give do not.

**Suggested:** cap the list (first two plus "and N more"), or put the names only
in each pull request's own watch activity.

### 6. A raw GraphQL enum in reader-facing prose

**Status: verified by reading.** `watch.go` records
`"stopped watching: "+pr.State.String()`, so the detail pane shows
"stopped watching: MERGED" beside lowercase sentences like "started watching"
and "checked for changes". `internal/model/status.go:173` returns
`"MERGED"`/`"CLOSED"` and deliberately returns `""` for open, so if the
`PRStateOpen` skip is ever relaxed the line reads "stopped watching: " with a
dangling colon.

### 7. `ArmedCount` allocates and sorts on every frame

**Status: verified by reading.** `internal/home/state.go:104` defines
`ArmedCount` as `len(s.ArmedKeys())`, and `ArmedKeys` (`:89`) builds a slice and
calls `sort.Strings` purely to discard it. `watchNote` (`internal/ui/watch.go:849`)
calls `ArmedCount` and is reached from `renderHeader` on every `Update` — every
keystroke, and every spinner tick while busy. The sort exists so the *keys* read
the same way twice; the count needs neither the slice nor the ordering.
`watchNote` also adds an O(n) `Engine.Polling()` scan per frame.

**Suggested:** give `State` a count-only loop and have `ArmedCount` use it.

### 8. `Engine.Watching()` has no production caller

**Status: verified.** `grep` across `internal/` and `cmd/` finds only its
definition at `internal/watch/watch.go:126`; every other reference is a test.
It sits immediately above the `Polling()` the header actually reads, so two
near-identical exported counters invite the next caller to pick the wrong one —
which is the mistake `watchNote` was just fixed for.

**Suggested:** fold it into `Status`/`Polling`, or say in its doc comment that
it exists for the tests.

### 9. `watchAfterLoad`'s doc comment describes only one of its two branches

**Status: verified by reading.** `internal/ui/app.go:1045-1047` still reads
"reconciles the watcher with a freshly loaded open list. It is the only place
the engine learns about node ids". The function's first statement is now the
closed-view disarm. In a codebase where comments are the design record, a lead
sentence naming one of two branches is the one a future reader trusts.

## Test gaps

### 10. The disarm tests miss the cases that differ

**Status: agent claim, partly checked.** `TestWatchingStopsOnceThePullRequestTurnsUpMergedInTheClosedList`
(`internal/ui/watch_test.go`) asserts `saved.ArmedCount() == 0`, which passes
whether or not the record leaves the file — so it cannot catch finding 1.
Asserting `len(saved.PRs) == 0` with a `RecordHandoff` in the setup would.

Also uncovered:

- `prsMsg{partial: true}` for `viewClosed`, where `disarmFinished` runs twice
  (once on the sweep's first page, once on the finish reply) and saves twice.
- `a.store == nil`, the early return deciding whether a store-less session
  silently keeps stale marks.

## Suggested order

1, 2 and 3 are the ones worth doing together: 1 is a correctness-and-honesty
problem with a doc that currently misstates behaviour, 2 is a user-visible
contradiction between three surfaces, 3 is a one-line change that stops a
future view inheriting a behaviour nobody chose for it. 10 belongs with 1,
since the test that would have caught it is the same test.
