# Watch and notify: handing pull request feedback to a coding agent

A handoff note for whoever picks this up next. It assumes you know Go and have
not seen this repository before.

Everything described here is on `feat/watch-and-notify-herdr` and open as
[pull request #1](https://github.com/relloyd/prutil/pull/1). Two of the three
planned phases are built, tested, and verified against the live GitHub and
herdr APIs. The third is not started, and the last section says exactly what it
is and what needs deciding before it can be written.

## What prutil is

A terminal dashboard for the GitHub pull requests you have open, across every
repository your account can see. Bubble Tea for the UI, and it shells out to
the `gh` CLI rather than holding a token of its own. `README.md` is accurate and
worth reading first.

## What this change adds

Press `w` on a pull request and prutil watches it. When review feedback appears
prutil finds the coding agent already sitting in that repository's checkout,
prefers the one on the pull request's head branch, waits for it to stop
working, and gives it the feedback through [herdr](https://herdr.dev), a
terminal multiplexer that knows which coding agent occupies which pane and
whether it is idle, working, blocked or done. `W` does the same on demand.

The user's own words for why: the tool surfaces the number of open comments on
each pull request and can do nothing about any of them.

## Decisions the user made, which are not yours to revisit

These were chosen explicitly. Treat them as settled unless the user says
otherwise.

1. **The watcher lives in the TUI for now.** The engine is written as a pure
   state machine so a headless mode can drive the same code later, but that
   headless mode was deliberately deferred.
2. **Change detection is two-stage.** A cheap tripwire poll over all watched
   pull requests, then a precise review-thread query only for the ones that
   moved.
3. **A busy agent is waited for, not interrupted.** Up to a configured budget,
   then prutil gives up and falls back to a notification.
4. **The automatic watcher never creates herdr workspaces or git worktrees.**
   With no agent to talk to it shows a toast. Pressing `W` is the consent to
   create. This is the part that is not built yet.

## Architecture

Eight commits, one per layer. Each builds and vets on its own, and the first
seven are self-contained packages with their own tests. Reading them in order is
the fastest way in.

| Package | Lines | What it owns |
| --- | --- | --- |
| `internal/run` | 95 | Finding a binary on PATH and running it. A `Runner` interface so clients can be driven from fixtures. |
| `internal/herdr` | 269 | The herdr control surface, over the `herdr` CLI. |
| `internal/git` | 154 | Which repository and branch a directory holds, cached 15s. |
| `internal/home` | 535 | The application directory: config, watch state, handoff log. |
| `internal/model` | +137 | `ReviewThread` and `Snapshot`, plus the selection helpers. |
| `internal/handoff` | 366 | Choosing the agent and sending it the work. |
| `internal/watch` | 277 | Deciding when a watched pull request is worth a request. |
| `internal/gh` | +452 | Two new queries and the node id that batches the first. |
| `internal/ui` | +650 | The keys, the row marker, the header, the tick loop. |

`internal/gh` keeps its own `Runner` rather than adopting `internal/run`.
Rewriting a working package and its fixtures to save a file would be churn.

### The two questions

This is the load-bearing design decision, and the reason for it is not obvious.

`comments.totalCount`, which the list query already selected, is GitHub's
**issue-comment** count. A review comment left on a line of the diff does not
move it. Anything built on that field alone silently misses the exact thing
this feature exists for.

So there are two queries, both in `internal/gh/query.go`:

- **`watchQuery`** is the tripwire. One `nodes(ids: [...])` document covering
  every watched pull request at once, addressed by GitHub node id rather than
  by repository, so it costs one request and one rate limit point however many
  are watched and whatever repositories they are spread across. It selects the
  head commit oid, the check rollup state, `updatedAt`, and both comment
  totals. Nothing that has to be paged.
- **`reviewThreadQuery`** is the precise one, run only for the pull requests
  the tripwire flagged. It reads the first and last comment of every thread
  under separate aliases, because they answer different questions: the first is
  the feedback, the last is what says whether the thread has been replied to.
  The viewer's login rides along in the same document for nothing, and without
  it prutil cannot tell a reviewer's comment from the pull request author
  answering their own thread.

Addressing by node id is why `listQuery` and the `closedPRFields` fragment now
select `id`, and why `model.PullRequest` has `NodeID`.

`model.Snapshot.Moved` compares all five tripwire fields on purpose. A push, a
check finishing, a comment on either side of the diff, and the timestamp each
catch something the others miss. Even so, a reply inside an existing thread
moves neither total and can leave `updatedAt` behind, which is why the engine
forces the precise query every fifth poll regardless.

### The engine

`internal/watch` holds no clock, opens no connection and starts no goroutine.
It is a state machine over readings somebody else took, which is what makes a
backoff measured in tens of minutes something a test walks through in
microseconds. Do not put IO in it.

The API is small: `Sync`, `Due`, `NextDue`, `Observe`, `Precise`, `Wake`,
`Defer`, `Forget`, `Tier`, `Watching`.

`Observe` applies a batch of readings and returns the keys worth the precise
query. `Precise` reports what that query found: `handedOff` moves a pull
request onto the slower backoff, and `open == 0` moves it back off again.

The ladder, all configurable:

| The pull request | Asked about |
| --- | --- |
| has checks running | every 30 seconds |
| has nothing in progress | after 2 minutes, then 4, 8, 16, and 30 |
| has been given to an agent | after 10 minutes, then 20, 40, and 60 |
| has not changed for about two hours | not at all |

Anything changing resets it. A dormant pull request stays armed, its row marker
turns hollow, and `r` wakes everything.

One subtlety worth preserving: `Precise` with `open == 0` and `handedOff` false
must **not** reset the run of quiet polls. If it did, the forced fifth-poll
query would keep a pull request awake forever and nothing would ever go
dormant. There is a test named for this.

### Deduplication

`home.PRState.NotifiedThreads` maps each thread prutil has handed off to the id
of the newest comment it held at the time. `model.Unhandled` drops a thread
whose newest comment is unchanged, so:

- A review whose remaining comments are never going to be resolved is asked
  about once and then costs nobody anything. This is what makes it safe to
  leave marked, and it is the user's stated reason for wanting per-pull-request
  arming in the first place.
- A reply since the handoff counts as new again.
- A thread the viewer had the last word in was never feedback, so
  `model.Feedback` excludes it before any of this.

`W` bypasses the check entirely. That is the point of the manual key: it is how
you say "look again anyway".

### Picking the agent

`internal/handoff` scores candidates. An agent is a candidate only when its
working directory is a checkout of the pull request's repository, which neither
GitHub nor herdr can answer and only the directory can. The right branch scores
higher than the right repository, and a settled agent higher than a busy one.
An agent one branch away is still used, with the mismatch spelled out in the
prompt, because that is far more use than a notification.

Candidates are sorted rather than scanned, so two equally good agents are
picked between the same way twice.

Two guards that matter:

- The pane prutil is itself running in is never given work. herdr injects
  `HERDR_PANE_ID` into every process it starts, which is how prutil knows.
- An agent at an approval or question dialog is never prompted. herdr rejects
  the submission anyway, and answering somebody else's dialog is not prutil's
  business.

## What herdr actually looks like

This was learned by running it. It saves you re-deriving it.

The CLI talks to a local socket at `~/.config/herdr/herdr.sock`. Success is
JSON on **stdout** with exit status 0, wrapped in `{"id":..., "result":{...}}`.
An error is JSON on **stderr** with exit status 1, shaped
`{"error":{"code":"...","message":"..."},"id":"..."}`. `internal/herdr` unwraps
both, so callers see typed values and typed errors.

Commands in use, and the shapes they return:

```
herdr agent list          -> {"result":{"agents":[...],"type":"agent_list"}}
herdr agent get <target>  -> {"result":{"agent":{...},"type":"agent_info"}}
herdr agent prompt <target> <text>
herdr agent start <name> --kind <kind> --pane <id> --timeout <ms>
herdr notification show <title> --body <text> --sound request
```

An agent entry carries `agent` (the kind, such as `claude`), `agent_status`,
`cwd`, `foreground_cwd`, `pane_id`, `tab_id`, `workspace_id`, and `name` only
when herdr was asked to start it. Address an agent by `pane_id`: it is always
present and always unique, where a name is not.

Lifecycle states are `idle`, `working`, `blocked`, `done`, `unknown`. `done` is
the same underlying idle state after unseen background work finished, so both
count as ready for work. `unknown` means herdr will not classify the agent and
is **not** proof it finished.

Error codes worth telling apart: `agent_blocked`, `agent_not_ready`,
`agent_prompt_stalled`, `agent_not_found`.

Agent names must match `[a-z][a-z0-9_-]{0,31}` and be unique among live agents.

`herdr --skill` prints the project's own instructions for driving it, and the
installed binary is the authority for command syntax. Run a command group
without a subcommand to see it. Do not run bare `herdr`; it launches the TUI.

## Configuration and state

Resolved from `PRUTIL_HOME`, then `XDG_CONFIG_HOME/prutil`, then
`~/.config/prutil`. Nothing requires the directory to exist, and a reader who
never arms a pull request never causes a file to be written.

- `config.yaml` is decoded **over** the defaults rather than into a zero value,
  so a file setting one key leaves every other key alone instead of quietly
  turning notifications off. Poll intervals are clamped to fifteen seconds at
  the shortest, and a cap below the interval it caps is raised to it.
- `watch.json` holds what is armed and which threads were handed off. Written
  by temp file and rename.
- `handoffs.jsonl` is one JSON object per attempt, sent or not.

`-dry-run` writes the log and shows the status line without saying anything to
an agent. Tell the user to try it that way first.

The prompt is a `text/template` given `Repo`, `Number`, `URL`, `Title`,
`HeadRef`, `BaseRef`, `Skill`, `UnresolvedCount`, `NewCount` and `Note`. With
`herdr.skill` set the default renders `/<skill> <url>`; without it, it spells
the job out.

## Testing

`task check` runs fmt, vet, golangci-lint and `go test -race ./...`. It must be
clean. `golangci-lint` here has `errcheck`, `nilerr`, `errorlint`, `godot`,
`revive` and others on, so:

- Comments end in a full stop, and exported things have doc comments starting
  with their name.
- `nilerr` fires on `if err != nil { return nil }`. Restructure rather than
  suppress; `internal/git` uses a `(value, bool)` read for exactly this.
- Ignore an error explicitly with `_ =`, including in `defer`.

Traps already hit, so you do not hit them again:

- **The footer fits 120 columns and there is a test pinning it.** Adding a
  binding to `ShortHelp` can push `q quit` off the screen. `←/h back` was
  dropped to make room for `w`; it is still under `?`.
- **A nil `*Dispatcher` in an interface field is not a nil interface.**
  `cmd/prutil/main.go` assigns it inside a branch for this reason, and the app
  decides whether the feature exists by comparing that field to nil.
- **UI tests must not run a real timer.** `internal/ui/support_test.go` has
  `fastWatch()`, which builds the app on millisecond intervals, and `advance()`
  to move its frozen clock. `internal/ui/watch_test.go` has `pump`, which feeds
  a command's messages back but never follows the watcher's own schedule, and
  `handOver`, which settles a handoff the way the runtime would rather than in
  batch order. A single hard-coded two-minute retry once made the suite take
  120 seconds; retries are derived from the configured base interval now.

Live verification is worth repeating after changes. Write a throwaway `main`
under the module, run it, delete it. That is how two real defects were caught
that fixtures had not: a cancelled context being logged as a blocked agent, and
the fallback toast sharing the expired context so it never fired exactly when
it was most needed. The toast now runs on `context.WithoutCancel`.

## What is left: phase 3

Two pieces, in this order.

### 1. Create a worktree and workspace on `W` when no agent exists

Only on the manual key. The automatic watcher must keep falling back to a
toast.

The intended sequence, once a repository root is known:

```
git -C <repo root> fetch origin pull/<n>/head:<branch>
herdr worktree create --cwd <repo root> --branch <branch> --label "<repo>#<n>" --no-focus
herdr agent start pr-<repo>-<n> --kind <kind> --pane <root_pane> --timeout 60000
herdr agent prompt pr-<repo>-<n> "<rendered prompt>"
```

`pull/<n>/head` rather than the head ref because it resolves pull requests
opened from forks as well as same-repository ones. Read the workspace, tab and
root pane ids from `.result` of the create response; never predict them.
`workspace create` returns `.result.workspace`, `.result.tab` and
`.result.root_pane`.

**Ask the user before doing this.** The one thing that needs checking is how
`herdr worktree create --branch` behaves when the branch already exists
locally. Finding out means creating a real worktree and workspace in the user's
live herdr session, which is a mutating, outward-facing action. If it refuses,
the fallback is `herdr worktree open --path ... --branch ...`. Prefer an
existing worktree over creating one either way, via
`herdr worktree list --cwd <root>`.

You will also need to map a repository to a local checkout, which prutil does
not do today. It only inspects the directories herdr already reports. The plan
was a `repos:` map in `config.yaml` for explicit overrides, plus a `discovery:`
section naming roots to scan for a `.git` and read the origin remote, cached in
`repos.json`. Neither config section exists yet, deliberately: dead
configuration is worse than configuration added when it is used.

### 2. A headless `prutil watch`

Drives the same `internal/watch` engine outside the TUI, so watching survives
closing the dashboard. It needs a lock file in the application directory so
that a TUI and a daemon do not both poll. The armed set is small and
last-writer-wins is fine for it; the runtime backoff belongs to whichever
process holds the lock.

### Smaller things worth doing

- The closed view selects `id` now but nothing watches closed pull requests.
  Harmless, and it keeps the fragment honest.
- `internal/herdr` has `StartAgent` written and tested but not yet called. It
  is there for phase 3.
- `gh.Review.Truncated` is set when a pull request has more than one page of
  review threads. Nothing surfaces it yet.

## Repository conventions

Match them; they are consistent and deliberate.

- Comments explain **why**, not what. Look at `internal/gh/query.go` for the
  house style: several paragraphs on why a page size is what it is, including
  the measurements behind it.
- Test names are sentences describing the behaviour, not the method. For
  example `TestAPullRequestWithNothingOpenIsNotHandedToAnybody`.
- Commit messages are a lowercase imperative subject and a body explaining the
  problem before the solution. Read `git log` before writing one.
- User-facing strings are plain sentences. No jargon, no shouting.
