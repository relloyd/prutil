# Disarming pull requests that have finished

What prutil does today about a watched pull request that has merged or closed,
what it still misses, and what closing that gap would cost. Written 18 September
2026, alongside the change that added the closed-view disarm.

## The problem this came from

`w` arms a pull request, and until recently nothing ever disarmed one except a
second press of `w`. `State.Compact` deletes an entry only when it is *not*
armed and holds no notified threads, so an armed entry survives every save by
construction. Merging the pull request changed nothing: `syncWatch` schedules
only what the open list holds, so the entry stopped being polled, stopped being
drawn on any row, and sat in `watch.json` indefinitely — counted in the header,
answering for nothing.

The waste is small and worth being honest about. A stale entry costs no API
requests, consumes no rate limit, and is a few hundred bytes. Keys are
`owner/name#12` and GitHub never reuses pull request numbers, so a stale entry
cannot latch onto a future pull request either. What it costs is a header that
overstates, and a state file that only ever grows.

## What is fixed

`watchAfterLoad` now hands the closed view to `disarmFinished`. Any row whose
`State` is not `PRStateOpen` and that is still armed gets disarmed, forgotten by
the engine, cleared of its runtime feedback, and noted in its watch activity;
the reader is told which ones were retired.

Retirement also drops the entry's notified threads and last handoff time.
`Compact` keeps any entry still holding notified threads, so without that the
record would survive every save for each pull request that ever reached a
handoff — the ordinary life of a watched one, and precisely the growth this is
meant to stop. Clearing them is safe here and only here: those threads exist to
recognise feedback already sent, GitHub does not reuse a pull request number,
and nothing will ask about a finished one again. A second press of `w` is not
the same thing and must keep them, or every thread would be handed over again
the next time that pull request is watched.

With both, the save runs the entry through `Compact` and the record leaves the
file rather than lingering as `"armed": false`.

The trigger is deliberately positive evidence — a row GitHub returned saying
merged or closed. Absence from the open list is the obvious second signal and
is the wrong one: that list is narrowed by `-query` and `-limit`, so a pull
request can drop out of it while still being open and still worth watching.

## The gap that remains

The closed view loads on demand. `Init` dispatches `loadOpen` only, so a reader
who never presses the view key never loads it, and a finished pull request stays
armed until they do. It is inert while it waits — nothing polls it — but it sits
in the hollow half of the header tally, and on a long-lived install those
accumulate.

So the disarm is reliable in the sense that it never fires wrongly, and
unreliable in the sense that it may not fire for a long time.

## Option 1: tell the watcher what state a pull request is in

`watchQuery` selects `updatedAt`, `reviewDecision`, the approval and comment and
thread counts, and the head commit's rollup. It does not select `state`, so
`model.Snapshot` has no way to carry "this merged" and the watcher cannot notice
a merge even in principle.

Adding it is cheap in exactly the way that query asks for. `state` is a scalar
on `PullRequest` with nothing to page, so it costs no extra rate limit beyond
the resolution already happening per node, which is the rule the query's own
comment sets out. `Observe` would then disarm on a reading that came back
`MERGED` or `CLOSED`, and `toSnapshot` would need the field plumbed through.

The catch is the window. `syncWatch` only ever schedules pull requests the open
list holds, so the watcher can notice a merge only if it polls between the merge
landing and the next open-list refresh. After that refresh the pull request is
gone from the open list and is never scheduled again. For prutil's own workflow
— watch a pull request, an agent pushes fixes, it merges while the watcher is
polling every couple of minutes — that window is usually hit. For a pull request
merged overnight by somebody else it is usually missed, and the closed-view
disarm remains the only thing that retires it.

This is worth doing on its own terms: it makes the common case immediate and it
is a handful of lines. It does not make the disarm complete.

## Option 2: let the watcher address what the open list has dropped

The reason `syncWatch` is confined to the open list is addressing, not policy.
Polling is by GraphQL node id, and the node id arrives with the list; an armed
pull request that is no longer in the list leaves prutil with nothing to ask
about. `home.PRState` stores `Armed`, `LastHandoff`, `LastCheckHandoffHead` and
`NotifiedThreads` — no node id — so the id is lost as soon as the list turns
over.

Persisting the node id alongside `Armed` would let `syncWatch` schedule every
armed pull request whatever the list holds. Combined with option 1 the disarm
becomes reliable: a merged pull request is polled, the reading says `MERGED`,
and it retires itself without anybody visiting the closed view.

It is the bigger change, and it buys a behaviour worth thinking about before
committing to it. Today "not in the open list" is a free brake: the polling
budget is bounded by what is on screen. Remove it and prutil keeps asking about
pull requests the reader may have forgotten, which is a rate limit cost paid to
discover that something is finished. The brake would have to come back as
something explicit — a cap, or an age after which an armed entry is retired
unasked — and that needs an armed-at timestamp, which `PRState` also lacks.

## What was considered and rejected

**Sweeping on age alone.** Retire any armed entry older than some period. It
needs the same schema addition as option 2, and it disarms by guessing: a
long-running pull request that is genuinely being watched looks identical to a
forgotten one.

**Loading the closed view at startup.** It would make the existing disarm fire
without the reader doing anything, but it spends the expensive sweep — the one
`loadClosed` splits into a partial and a background finish precisely because it
can take ten seconds on a large organisation — on every launch, to clean up
bookkeeping. The cost lands on every start; the benefit is occasional.

## Recommendation

Take option 1 when the watcher is next being touched. It is small, it fits the
query's existing rule about what may be selected, and it covers the case prutil
is actually built around.

Leave option 2 until the header tally shows it is needed. The hollow count added
alongside this change is the instrument: if readers start seeing a hollow number
that stays high across sessions, stale entries are accumulating faster than the
closed view retires them, and the node id is worth persisting. Until then the
closed-view disarm plus an honest tally is the proportionate answer.
