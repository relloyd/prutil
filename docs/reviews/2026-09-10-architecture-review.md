# Architecture review: the watch and handoff feature

Scope: the thirteen commits from `c7ab72f` to `f7d4672`, roughly 8,000 added
lines across ten packages, seven of them new. Reviewed against `main` at
`dc96b32`. `go build`, `go vet` and `go test ./...` are all clean;
`golangci-lint` could not run here because the installed build targets Go 1.25
and the module targets 1.26, which `AGENTS.md` already predicts.

## Verdict

The shape is right and the boundaries are, with one exception, in the right
places. `watch` as a clock-free state machine, `run` as the single place that
forks a process, and the `Controller`/`Identifier`/`dispatcher` interfaces at
each consumer are all decisions that will keep paying. The package count is
justified: every one of the seven new packages has a distinct collaborator it
owns.

Two defects need fixing before this is trusted unattended. Neither is a design
flaw; both are a missing case at an edge. Beyond those, the main structural
debt is in `internal/ui`, which has absorbed the orchestration for a feature it
should only be rendering.

## Defects

### 1. A pull request GitHub answers with a null node spins the poller

`watch.Engine.Observe` only reschedules the entries it finds a `model.Snapshot`
for. A watched pull request whose node comes back null (access revoked, node
deleted, a transient partial reply) is skipped, so its `dueAt` is never moved.
It stays permanently due, `NextDue` keeps returning a time in the past,
`scheduleWatch` computes a zero delay, and `pollWatched` fires again the moment
the previous reply lands. Because the pull request is still in the open list
with a node id, `ids` is non-empty every time, so each iteration is a real
`gh api graphql` call. The result is an unthrottled request loop against
GitHub for as long as the app is open.

Confirmed directly against the engine: after three rounds of `Observe` with no
matching snapshot, `NextDue` was still zero seconds away each time.

The fix belongs in `applyWatch`: any key in `msg.keys` that produced no
snapshot should be handed to `Engine.Defer` with `watchRetry()`, exactly the
way `watchErrMsg` already treats a refused request. Two lines, and the
`WatchSnapshot` doc comment that explains dropping null nodes is already the
justification for it.

### 2. Pressing `W` with no application directory panics the whole TUI

`openHome` returns a nil `*home.Store` when it fails. `main` then hands that
nil pointer to `git.NewResolver`, whose parameter is the `CacheStore`
interface. A nil pointer in a non-nil interface passes the `r.store != nil`
guard in `Resolver.Resolve`, and `(*home.Store).Path` dereferences the nil
receiver. Reproduced: `nil pointer dereference` at `home.go:78` via
`repos.go:87` from `resolver.go:62`.

Reaching it takes an unwritable config directory, then `W` on a pull request
with no agent already checked out. Narrow, but the failure is a crash of the
dashboard rather than a message.

The irony is that `main.go` carries a comment warning about precisely this
trap, two statements above the call that falls into it:

```go
// Assigned inside the branch rather than from a two-value call, because a
// nil *Dispatcher stored in an interface field is not a nil interface...
```

Guard the store the same way the dispatcher is guarded, or drop the interior
`r.store != nil` check and give `Resolver` a no-op cache store when there is no
directory. The second is better: it removes the nil check from three methods.

## The structural problem: `internal/ui` owns too much

`App` now carries seven maps keyed by `model.Key`:

| Map | Holds |
| --- | --- |
| `checks` | fetched check runs |
| `handing` | handoff in flight |
| `reviewing` | review-thread query in flight |
| `feedback` | open thread count |
| `activity` | recent watcher events |
| `watching` | current operation label |
| `handoffHistory` | durable log tail |

Every one is a column of the same table. They are written from six different
places and cleaned up from four, and the cleanup is already inconsistent:
`applyWatch` sets `watching[key]` to "reading review feedback" and then calls
`loadReview`, which returns nil when a query is already in flight, leaving that
label stuck on the row until some unrelated event clears it.

Collapse them into one `map[model.Key]*prRuntime`. A single struct makes the
lifetime obvious, makes "clear everything for this key" one statement, and
removes the class of bug where five maps are updated and the sixth is
forgotten. `checks` can stay separate if you want to keep the refresh
semantics that wipe it wholesale, but the other six belong together.

The larger version of the same point: `internal/ui/watch.go` is 600 lines and
almost none of it is rendering. Poll scheduling, review interpretation, freshness
comparison against notified threads, and handoff bookkeeping all live there
because that is where the Bubble Tea messages arrive. A `watchController` type
holding the engine, the runtime map and the state, exposing methods that return
`tea.Cmd`, would let `App` keep only what it draws. That is a refactor, not a
fix, and it can wait until after the defects.

## Simplifications worth making

**Collapse the three closed-view client methods into one.** `Client` now
declares `ListClosedPullRequests`, `SweepClosedPullRequests` and
`FinishClosedPullRequests`. The first has no caller outside its own doc
comments. It is 8 lines of the interface every test double must implement, for
a code path nothing exercises. Delete it, or keep it and delete the split; the
UI only uses the split.

The tell that the split is leaking is `NewClosedSweepState`, which exists,
by its own comment, so a fake can produce a value the real fields cannot
express. When a type needs a constructor for tests alone, the type is being
asked to be both an opaque continuation token and a value a caller inspects.
Export `Exhausted` as a field, or return a separate `bool` alongside the state
and keep the state genuinely opaque.

**Give `handoff` a failure helper.** `Dispatcher.provision` repeats this shape
six times:

```go
res := Result{Outcome: home.OutcomeFailed, Detail: err.Error()}
d.toast(ctx, req, res.Detail)
return res, err
```

A `func (d *Dispatcher) fail(ctx, req, err) (Result, error)` removes about
thirty lines and makes the two places that deliberately do *not* toast visible
as the exceptions they are.

**Read the origin remote instead of forking git for it.** `Resolver.discover`
walks a configured root with `filepath.WalkDir` and calls `matchCheckout` on
every `.git` it finds; `matchCheckout` calls `Identify`, which forks git three
times. A root holding two hundred checkouts is six hundred process spawns to
answer one question, inside a handoff the reader is waiting on. Parsing
`.git/config` for `[remote "origin"]` answers the repository question with one
file read, and git only needs forking for the checkout that matches. Bounding
the walk depth (checkouts are rarely nested more than three deep under a code
root) would help too.

**`git.Client`'s identify cache never evicts.** Entries expire logically after
`cacheLifetime` but stay in the map forever. Combined with a discovery walk
that touches hundreds of directories, that is an unbounded map in a
long-running TUI. Drop stale entries on read, or cap it.

**Duplicate `Identifier` interfaces.** `git.Identifier` and `handoff.Identifier`
are declared identically. Consumer-side interfaces are the right instinct, but
these two consumers want the same thing from the same package; one of them can
import the other.

**The handoff log has no bound.** `handoffs.jsonl` is appended to forever and
`RecentHandoffs` scans it from the start for every pull request whose detail
pane is opened. It also aborts the whole read on a single malformed line, so
one truncated write makes the history permanently unreadable. Skip bad lines
and either read backwards or rotate at a size cap.

## GitHub API and performance

The API work is the strongest part of this change. The two-stage closed sweep,
the alias-batch sizing, the refusal to raise `repoBatchSize`, and the reasoning
about the ten-second document budget are all correct and, unusually, all
written down where the next person will find them. `watchQuery` as a tripwire
with `reviewThreadQuery` as the follow-up is exactly the right split, and
`updatedAt` is the right field to hang it on.

Three gaps:

**`nodes(ids:)` caps at 100.** `WatchSnapshot` passes every armed pull request
in one call. Arm 101 and GitHub rejects the document, which surfaces as
`watchErrMsg` and defers every watched pull request — the feature quietly stops
working with no explanation. Chunk at 100.

**The rate-limit claim is optimistic.** `AGENTS.md` says the watch poll costs
"one request and one rate limit point however many pull requests are watched".
The point cost is derived from nodes requested, and `commits(last: 1)` is one
node per pull request. At the current scale that still rounds to one point, but
the invariant is "under 100 connection nodes", not "however many". Worth
correcting where it is written down, because the next person to add a field to
`watchQuery` will read that sentence as permission.

**The spinner runs for the length of a handoff.** `busy()` is true while
`handing` is non-empty, and `WaitForIdle` defaults to fifteen minutes. That is
a full-frame redraw every hundred milliseconds for a quarter of an hour while
`settle` waits. Telling the reader it is not hung is right; doing it at
animation frame rate is not. Either slow the tick while only a handoff is
outstanding, or show a static line with the elapsed time.

## Smaller notes

- **`model.SelfTestMarker` is a test backdoor in the domain model.** An HTML
  comment in a review body changes which threads count as feedback, in shipped
  code, for every user. Two of the thirteen commits are about it. It reads like
  scaffolding for verifying the watcher against a real pull request. Make it a
  config key (empty by default) rather than a constant, so the behaviour is
  something a user opts into rather than something a string in a comment can
  trigger.

- **`w`, `W` and `N` overlap in ways the key names hide.** `N` is described as
  "notify new feedback", but `applyReview` gives it the full automatic path: it
  can dispatch a handoff to an agent. A reader who expects a notification gets a
  prompt submitted on their behalf. Either rename it or stop it short of
  dispatch.

- **`docs/tmp/watch-and-notify-handoff.md` is committed.** It is a session
  handoff note that names a branch and describes unbuilt phases. It will be
  wrong within a week and it is 333 lines of the diff. Move what is durable into
  `AGENTS.md`, which already carries this feature's real design notes, and
  delete the rest.

- **Watch messages skip the generation counter.** Every other reply carries
  `gen` and is dropped when stale. `watchSnapshotMsg`, `watchReviewMsg` and
  `handoffMsg` do not. For the handoff that is deliberate and documented. For
  the snapshot and review replies it is undocumented, and a refresh calls
  `Engine.Wake` underneath them. Say why in a comment, or tag them.

- **`repoCacheMu` is a package-level mutex.** It guards read-modify-write across
  `Store` instances in one process, which works, but it also serialises
  unrelated directories. Since `Store` is already the thing that owns a
  directory, the lock belongs on it.

## What not to change

- The `run.Runner` seam and the decision to shell out to `gh`, `git` and
  `herdr` rather than link libraries. It keeps credentials, terminal sessions
  and repository state owned by the tool that owns them, and it is what makes
  the tests hermetic.
- `watch.Engine` holding no clock, no connection and no goroutine. It is the
  reason a backoff measured in tens of minutes is testable in microseconds.
- The tier ladder and the `Wake`-on-refresh rule. The polling behaviour is
  well judged and the defaults are appropriately slack.
- The comment density. It is high, but it is nearly all *why*, and several
  comments record measurements that would otherwise have to be rediscovered.

---

# Addendum: `06bf62a`, mouse selection and the expanded WATCH page

512 added lines, all in `internal/ui`. Tests are green. The feature works, and
the section-navigation model (WATCH and CHECKS as two selectable sections, one
of which drills into a page) is a reasonable answer to a detail pane that had
outgrown a single scrolling list. Three defects and one design cost.

## Defects

### 3. The WATCH section appears and then vanishes on every selection

`hasWatchSection` counts `history.loading` as content. Selecting a pull request
starts a durable-history read, so the section renders immediately with
"not watching · manual handoff activity" and "loading handoff history…". When
the read returns nothing, which is the normal case for a pull request nothing
has been handed off for, the section disappears and the CHECKS list below it
jumps up two lines.

Reproduced: `hasWatchSection` is true straight after the list load and false
after an empty `handoffHistoryMsg`, with "WATCH" present in the rendered frame
before and absent after.

A section that has nothing to say should not claim to be loading. Drop
`history.loading` from `hasWatchSection`, or hold the section open once it has
been shown for the current selection.

### 4. The WATCH section can be selected, then drilled into when it is empty

The same transition leaves the navigation in a state nothing can render.
`detailSection` stays `detailWatch` after `hasWatchSection` goes false, so
`detailIndex` reports 0 while `setDetailIndex` would map 0 to a check. Nothing
is highlighted in either section. `Into` does not check `hasWatchSection`
either, so `l` from there opens the expanded page and gets
"nothing to show for WATCH" with no lines to scroll.

Reproduced end to end: after an empty history read with WATCH selected,
`hasWatchSection` is false, `detailIndex` is 0, `itemCount` is 3, and `l` still
enters the page with `watchLineCount` of zero.

`setDetailIndex` already knows how to fall back to checks. Call the same path
when the section goes away, and gate `Into` on `hasWatchSection`.

### 5. The list cursor scrolls out of sight at some terminal heights

Pre-existing, but this commit makes it concrete. Three places compute how many
list rows exist and two of them disagree:

| Caller | Rows |
| --- | --- |
| `renderList` | `(height-1)/rowHeight` |
| `listIndexAt` (new) | `(bodyHeight-1)/rowHeight` |
| `clampScroll` | `bodyHeight/rowHeight` |

`renderList` holds a line back for the position indicator. `clampScroll` does
not, so whenever `bodyHeight` is an exact multiple of `rowHeight` it believes
one more row fits than is drawn, and never scrolls to bring the cursor back.

Reproduced at 100x28 with ten pull requests: `bodyHeight` is 24, `renderList`
draws 3 rows, `clampScroll` assumes 4, and from the fourth row down the
selected row is off screen and stays off screen for every subsequent `j`.

The new mouse code got the arithmetic right, which is what makes the
disagreement visible: click and keyboard now hold different beliefs about which
rows exist. Extract one `listRows()` method and have all three call it.

## Design cost: mouse mode is on with no way off and no wheel

`View` sets `MouseModeCellMotion` unconditionally. Two consequences, neither
mentioned in the commit message or the README:

- **Terminal text selection is taken over.** Once an application requests mouse
  reporting, dragging to select stops working normally; most terminals require
  a modifier to override. For a dashboard whose screen is full of URLs, error
  text and check names, that is a real loss, and `y` only copies the one URL
  under the cursor.
- **The wheel does nothing.** `MouseModeCellMotion` delivers wheel events, and
  `Update` handles only `MouseClickMsg`. So the mode captures the wheel and then
  ignores it. A reader who scrolls gets nothing, where before they at least got
  their terminal's own behaviour.

`MouseMode` is set per-`View`, so making it a field on `App` driven by a flag
or a toggle key is a few lines. Handling `MouseWheelMsg` by moving the list
cursor is a few more. Do both, or neither; the current halfway point is the
worst of the three.

## Smaller notes on this commit

- **`detailSection` and `detailPage` are embedded, not named.** They read like
  fields with an inferred type, but they are anonymous embedded fields of two
  `int` types. It works only because neither type has methods; the day
  `detailPage` gets a `String()`, `App` silently starts implementing
  `fmt.Stringer` and something prints a page number where a screen was wanted.
  Name them: `section detailSection` and `page detailPage`.

- **`watchDetail` and `watchPageLines` are the same content twice.** Both read
  status, armed, events and history, and format them in two different
  vocabularies: "checks running · next in 5m · every 2m" against "state: …",
  "cadence: …", "next check: …". Roughly 120 near-duplicate lines that will
  drift the first time one is edited. Produce one ordered slice of labelled
  rows and let the compact renderer take a prefix of it.

- **Counting is done by rendering.** `watchLineCount` builds the whole expanded
  page, styled and truncated, to return its length; `itemCount` and
  `clampScroll` call it on every key press while the page is open, and
  `checksHeight` renders `watchDetail` a second time purely to measure it. Have
  the shared row model return a count without producing strings.

- **`renderWatchPage` wastes a line when it is not scrolled.** The window is
  `height-1` to reserve room for the position indicator, but the indicator is
  only emitted when there is something above or below.

- **The added tests cover the happy paths only.** Click selection, narrow mode,
  drill-in, scrolling and terminal fitting are all covered. None of the three
  defects above is, and each is a short test: an empty `handoffHistoryMsg` for
  the first two, and a height that makes `bodyHeight` a multiple of `rowHeight`
  for the third.

---

# Fix plan

Five defects and a pile of structural work. Suggested order, with the
justification for the ordering rather than just the list.

**Status: tranches 1, 2, 2b, 3 and 4 are done.** Every defect below is closed, each with
a regression test confirmed to fail against the code as it stood. Tranches 3 to
5 are untouched.

## Tranche 1: correctness, before the watcher is left running (done)

These are the two that misbehave against something outside the process.

1. **Null node spins the poller.** In `applyWatch`, defer every key in
   `msg.keys` that produced no snapshot, using `watchRetry()`. Test: an
   `Observe` with a missing key leaves `NextDue` in the future.
2. **Nil store panics on `W`.** Give `git.Resolver` a no-op `CacheStore` when
   there is no application directory, and delete the three interior nil checks.
   Test: `Resolve` against a resolver built with no store returns
   `ErrCheckoutNotFound` rather than panicking.
3. **Chunk `WatchSnapshot` at 100 ids.** One loop in `CLI.WatchSnapshot`, and a
   test that 150 ids produce two calls.

## Tranche 2: the three UI defects (done)

Cheap, visible, and they undermine confidence in the newest feature.

4. **One `listRows()` helper** shared by `renderList`, `clampScroll` and
   `listIndexAt`.
5. **Stop `hasWatchSection` counting a loading history as content**, and snap
   `detailSection` back to checks when the section goes away.
6. **Gate `Into` on `hasWatchSection`** so the expanded page cannot be opened
   empty.

Do 5 and 6 together; they are the same transition seen from two angles.

### What the fixes turned out to be

Two of the six were not quite what the review predicted.

**The nil store needed catching in the constructor, not the caller.** The plan
said to hand `NewResolver` a no-op cache when there is no application
directory. Written that way the guard does not fire: `main` passes a nil
`*home.Store`, which is not a nil interface, so `store == nil` is false and the
nil receiver is dereferenced exactly as before. The first draft of the test
reproduced the panic against the supposed fix. `NewResolver` now tests for both
shapes, and `main` passes the store unconditionally, so one place knows.

**The empty WATCH section stopped being reachable by keystrokes.** Once a
loading history no longer opens the section, the only things that fill it are
arming and recorded activity, and neither is ever cleared within a session, so
no key sequence can empty it under the cursor. Disarming does not: what the
watcher did is still worth reading. The guard is still right, because the
section is built from state that can empty and nothing stops a future caller
emptying it, but it is tested against the state directly rather than through a
key sequence that no longer exists.

## Tranche 2b: a store that degrades rather than disappearing (done)

Added after checking why an application directory would be missing in the
first place. It is not: `LoadOrCreateConfig` runs at startup and creates the
directory at `0700` with a commented `config.yaml` at `0600`, using a temp file
and a hard link so two prutils starting at once cannot clobber each other.

The nil store had nothing to do with a missing directory. `openHome` was a
three-step chain returning nil on any failure, and the two realistic failures
were both data errors: a typo in `config.yaml` and a half-written `watch.json`.
Either one disabled watching, handoffs and the handoff log for the session, and
before tranche 1 armed the panic.

`Store.Load` now degrades per file and cannot fail. A configuration that will
not parse leaves the defaults standing and is never moved or rewritten, because
the reader wrote it on purpose. A watch state that cannot be understood is
moved to `watch.json.corrupt` before the empty state that replaces it gets a
chance to overwrite it; one that could not be opened at all is left alone,
since prutil most likely cannot move it either. Only failing to resolve the
directory leaves no store, which is the one case with nowhere to write.

The notes reach the reader on the footer's notice line, where they stay for the
session rather than passing by as a status message. A transient status takes
the line while it lasts and hands it back.

The package doc claimed nothing is written until a pull request is armed, which
the configuration template has contradicted since it was added. Corrected to
describe what the code does.

## Tranche 3: the duplication the last two commits introduced (done)

Worth doing before more is built on top, because both are already being copied.

7. **One row model behind `watchDetail` and `watchPageLines`**, returning
   labelled rows plus a count, so counting stops meaning rendering.
8. **`handoff.fail` helper** for the six-times-repeated failure shape in
   `provision`.
9. **Delete `ListClosedPullRequests` and `NewClosedSweepState`**, or delete the
   two-stage split. Keeping both means every test double implements a method
   with no caller.

### What the fixes turned out to be

**The dead closed-view method came out cleanly.** `FinishClosedPullRequests`
from the zero state is byte for byte what `ListClosedPullRequests` did: the
same helpers, the same page sizes, the same request counts. Eleven tests
translated with no change to a single assertion, which is what proves it. The
sweep state now carries an exported `Exhausted` field instead of a method and a
test-only constructor, since that is the one part of it a caller acts on.

**The two watch renderers now read one set of facts and phrase them
separately.** A shared prefix, as the plan suggested, was the wrong shape: the
compact section packs state, cadence and next check onto one line and the page
gives each a line of its own, so no prefix of one is the other. What was
genuinely duplicated was reading the state and deciding what there is to show,
and that is now `watchFacts`. Rendering goes through `watchRow`, plain text and
a style, so counting the page is counting a slice rather than styling and
truncating a screenful to throw away.

Two things were checked rather than assumed. The rendering was diffed against
the code it replaced across six scenarios, including narrow and short
terminals: identical except that `ACTIVITY` and `HANDOFFS` now carry the same
two-space indent as `WATCH` and `CHECKS`, which the original gave only to
`WATCH`. And the first draft quietly gave the compact section the page's
labels, which is a regression at that width; the compact wording is preserved
and now pinned by a test.

Also fixed in passing: the page heading was being truncated after its escape
codes had gone in, which `AGENTS.md` warns against. Headings are now a row kind
that renders through `sectionHeading` rather than being styled and then cut.

## Tranche 4: the structural change (done)

10. **Collapse the seven per-key maps in `App` into one
    `map[model.Key]*prRuntime`.** Do it after tranche 2, not before: two of the
    defects above are exactly the class of bug this removes, and fixing them
    first gives the refactor a regression test to land against.
11. **Rename the embedded `detailSection`/`detailPage` to named fields.** Fold
    into 10; it touches the same struct.

### What the fixes turned out to be

The six watcher maps are now one `map[model.Key]*prRuntime`. `checks` stays
separate, as planned: it is fetched data with a different lifetime, wiped
wholesale by a refresh, where the other six are watcher state. Rendering was
diffed against the code it replaced across seven scenarios and is byte for byte
identical.

**The plan's pruning was ceremony and came back out.** The first draft added a
prune step to drop entries holding nothing, which sounded tidy and almost never
fired: a history read that came back empty is worth remembering, since asking
again finds the same nothing, and that alone keeps every selected pull request's
entry alive. Rather than keep a guard that does not guard, `mutate` now says
plainly that an entry outlives the work that created it, and clearing an
operation that was never set creates nothing. The map is bounded by the pull
requests one session touched, which is what the seven maps did anyway.

Two of the tests written for this failed first time, and both times the test was
wrong rather than the code: one asserted no operation was left after a review
read when the state machine had legitimately moved on to a handoff, and the
other asserted an empty runtime map when the durable history cache is meant to
keep an entry. Both now assert what the code actually promises.

`detailSection` and `detailPage` are named fields, `section` and `page`.

## Tranche 5: cost and hygiene, in any order

12. Read `.git/config` in checkout discovery instead of forking git three times
    per candidate, and bound the walk depth.
13. Evict stale entries from the `git.Client` identify cache.
14. Rotate `handoffs.jsonl` and skip malformed lines rather than failing the
    whole read.
15. Decide the mouse question: handle `MouseWheelMsg` and add an off switch, or
    revert to `MouseModeNone`.
16. Move `model.SelfTestMarker` behind a config key.
17. Delete `docs/tmp/watch-and-notify-handoff.md`.
18. Correct the rate-limit sentence in `AGENTS.md` to name the 100-node limit.
19. Slow the spinner while only a handoff is outstanding.

## What this does not include

No change to the watch tier ladder, the closed-view sweep sizing, the
`run.Runner` seam or the shell-out design. Those are working and the reasoning
behind them is recorded.
