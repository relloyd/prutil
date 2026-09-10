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
