# AGENTS.md

Guidance for Claude and other coding agents working in this repository.

## What prutil is

A terminal dashboard for the pull requests the current user has open across
every GitHub repository their account can see. The list sits on the left, the
GitHub Actions checks for the selected pull request on the right.

## Prerequisites

- Go 1.26 (`go.mod` targets `go 1.26.0`; `GOTOOLCHAIN=auto` fetches it).
- The [gh CLI](https://cli.github.com), installed and authenticated. prutil
  never handles a token itself; `gh` resolves credentials, including `GH_TOKEN`
  and `GITHUB_TOKEN` when they are set, and `GH_HOST` for GitHub Enterprise.
- [go-task](https://taskfile.dev) for the `task` targets below.
- golangci-lint built against Go 1.26 or newer. An older build refuses the
  module with "the Go language version used to build golangci-lint is lower
  than the targeted Go version".

## Commands

Prefer the task targets over raw go commands so that everyone runs the same
checks.

| Command | What it does |
| --- | --- |
| `task` | fmt, vet, lint and test |
| `task test` | `go test -race ./...` |
| `task cover` | tests plus a per-function coverage report |
| `task build` | build into `./bin/prutil` |
| `task install` | tidy, check, build, then `go install` |
| `task run -- -limit 20` | run against your own gh credentials |

## Package map

| Path | Responsibility |
| --- | --- |
| `cmd/prutil` | flag parsing, preflight checks, program start |
| `internal/model` | domain types: `PullRequest`, `Check`, status enums, formatting of ages and durations |
| `internal/gh` | the `Client` interface and its gh-CLI implementation, the GraphQL documents, and wire decoding |
| `internal/browser` | the `Opener` interface and the platform handler |
| `internal/clipboard` | the `Writer` interface and the platform clipboard program |
| `internal/desktop` | the `Notifier` interface and the platform notification program |
| `internal/git` | reading the repository and branch behind a directory, and finding a repository's local checkout |
| `internal/herdr` | the `Controller` interface and the herdr CLI behind it |
| `internal/handoff` | choosing the agent a pull request's feedback goes to, and sending it |
| `internal/sandbox` | starting each kind of coding agent inside its vendor's sandbox, and asking whether one already running is; one `Profile` per kind |
| `internal/home` | the application directory: configuration, watch state, handoff log, repository cache |
| `internal/watch` | the polling schedule, as a state machine over readings somebody else took |
| `internal/run` | starting a helper process and reporting one that failed |
| `internal/ui` | the Bubble Tea model, both panes, key bindings and the palette |

## How the data flows

1. `ListPullRequests` runs one `gh api graphql` search that returns headline
   fields plus the head commit's `statusCheckRollup.state`. That single round
   trip is what the first paint needs.
2. The individual checks come from a second query, per pull request, issued
   lazily when a row is selected (debounced) and eagerly for the top of the list
   after it loads. Results are cached in the UI by `model.Key`.
3. Every reply carries the generation counter it was issued under. `r` bumps the
   counter, so replies from before a refresh are dropped rather than applied.

Keep that shape: headline first, detail afterwards. Do not add fields to the
list query that force it to walk `contexts`.

## Conventions

- **Tests never touch the network, never the real clipboard, and never raise a
  real notification.** `gh.Runner`, `browser.Opener`, `clipboard.Writer` and
  `desktop.Notifier` are the seams; fake them. GraphQL fixtures live in
  `internal/gh/testdata`. `ui.New` falls back to the real clipboard when
  `Config.Clipboard` is empty, so a test that presses `y` must build its app
  through `newTestApp`, or pass a `fakeClipboard` of its own. It does the
  opposite with `Config.Notifier`: empty means no notifications at all.
- Use testify's `assert` and `require`, table-driven where the cases are
  uniform, and give each case a sentence-long name.
- All colour lives in `internal/ui/styles.go`. All key bindings live in
  `internal/ui/keys.go` and are surfaced through `keyMap.ShortHelp`, so a new
  binding shows up in the footer automatically. The footer is one line and the
  help component drops the tail that does not fit, so `ShortHelp` is a curated
  subset rather than everything; `TestTheFooterFitsEveryShortcutAt120Columns`
  fails when a new binding pushes `q quit` off a 120-column terminal.
- The `?` overlay (`internal/ui/helpoverlay.go`) lists `keyMap.helpSections`,
  which also backs `FullHelp`. A new binding needs an entry there with a title
  and a detail sentence: `TestEveryBindingIsListedInTheShortcutOverlay` fails
  without one, and `TestTheReadmeKeysTableNamesEveryBindingsKeys` fails until
  README's Keys table names every key it answers to. `enter` in the overlay
  runs a shortcut by replaying the binding's first key through `handleKey`, so
  that key must be one `handleKey` matches. While the overlay is open, `Update`
  hands it key, paste and mouse messages before anything else; its own keys
  live in `overlayKeyMap`, apart from `keyMap`, because `q` must reach the
  filter. The `s` settings pane (`internal/ui/settings.go`) takes input the
  same way while open, with its own `settingsKeyMap`, and both panes float
  over the screen through `floatOver` in `internal/ui/frame.go`.
- The footer has no room for `s settings`; it is reachable through `?`.
- Tests must not run a `tea.Tick` command. `drain` calls the command, so
  draining one blocks for the whole interval. Send the message the tick would
  have produced instead, the way the `selectionMsg` and `autoRefreshMsg` tests
  do.
- List rows are a fixed `rowHeight` lines. Scrolling arithmetic depends on that,
  so pad rather than shrink a row.
- Layout code measures plain text with `ansi.StringWidth` and applies styles
  last, via the `seg` helpers in `internal/ui/format.go`. Never measure a string
  that already carries escape codes with `len`.
- Rendering must fit the terminal at any width. `TestRenderFitsEveryTerminalSize`
  asserts it; keep it passing.
- Below 80 columns (`narrowWidth`) the layout collapses to a single pane.

## Opening and copying

`enter` and `y` both act on `App.selectedTarget`: the pull request under the
list cursor, or the check under the detail cursor when that pane has focus and
the check carries a URL of its own. Keep them sharing it. A copy that ignored
the focus while an open respected it would be the sort of difference nobody
can remember.

## Auto-refresh

`a` buys `autoRefreshBurst` reloads spaced `autoRefreshInterval` apart, and
pressing it again adds another burst to `App.autoLeft` rather than restarting
the timer. `App.autoSeq` names the run of ticks in flight: `extendAutoRefresh`
bumps it only when starting from zero, and `Update` drops an `autoRefreshMsg`
whose `seq` is stale, so a tick from a spent run cannot revive it. Each tick
goes through the same `refresh` the `r` key uses, which is what makes the
checks refetch and the dot go green.

## Views

`tab` cycles the `view` enum in `internal/ui/app.go`. Each view owns a
`viewState` (its own list, cursor, scroll offset, load state and error), and
`a.cur()` is the one on screen. `a.checks` is deliberately *not* per view: it is
keyed by `model.Key`, so a pull request appearing in two views is fetched once.

Adding a third view (review-requested was the original plan) means a constant
before `viewCount`, a case in `load`, and nothing else. Do not reintroduce a
flat `a.prs`; the five places that used to assume it are `applyPRs`,
`itemCount`, `clampScroll`, `renderHeader` and `renderList`.

The closed view loads in two stages so a large organisation's fetch, which can
run to 20+ seconds end to end, does not block the first paint. `CLI.load`
calls `Client.SweepClosedPullRequests`, which fetches exactly one page sized to
`closedFirstPageSize` (smaller than the `closedPageSize` the rest of the sweep
uses) and applies it to the view immediately; unless that page already
exhausted the search, `Update` dispatches `Client.FinishClosedPullRequests` in
the background to resume the sweep, discover repositories, and run the
per-repo fill, replacing the partial list when it lands. `viewState.enriching`
tracks the background stage so the header can say "loading more…" without the
rows already on screen being any less interactive. Measured against a real
account, this cut first paint from 19-23s to about 2s; see
`internal/gh/client.go`'s doc comments for the underlying `SweepClosedPullRequests`
and `FinishClosedPullRequests` methods.

Both stages share the same exhaustion check inside `sweepClosedPages`: when the
sweep reaches the end of the search, the grouping is already exact and the
per-repo fill is skipped. `TestClosedSweepThatReachesTheEndCostsOneRequest` and
`TestFinishClosedPullRequestsSkipsWorkWhenTheSweepIsAlreadyExhausted` pin it.
`CLI.ListClosedPullRequests` still does the whole fetch in one blocking call,
built on the same helpers, for any caller that does not need the two-stage
split.

Repository discovery (`discoverRepos`) also stops reading pages as soon as it
has found enough not-yet-filled repositories to satisfy `RepoLimit`, rather
than always reading `discoverPages` of them; `TestClosedDiscoveryStopsEarlyOnceEnoughRepositoriesAreFound`
pins that.

Enumerating every repository in a large organisation is not feasible, so
discovery does not try. It reads on from the sweep's own cursor with
`repoNamesQuery`, which selects nothing but `repository { nameWithOwner }`, and
so learns exactly the repositories that hold matching pull requests. Two
tempting alternatives are both wrong and were tried: walking each organisation's
repositories returns hundreds the user has never opened a pull request against,
each costing an alias to learn nothing, and `repositoriesContributedTo` is
recency-biased, on a real account naming six repositories while omitting one
holding 125 of that user's closed pull requests.

**GitHub gives one GraphQL document roughly ten seconds before the edge returns
`HTTP 502`, and cost grows with the number of aliased searches in it.** Measured
against public repositories: 8 aliases 4.1s, 16 aliases 7.4s, 30 aliases 10.6s
and intermittently 502. A large organisation spends the same budget faster on
private-repo permission checks. That is why `repoBatchSize` is 3, why
`closedPageSize` is 25 rather than the open list's 50, and why `closedPRFields`
omits the check rollup that `listQuery` selects. Do not raise any of them to
save round trips: the rate limit charge is one point per document either way,
and the failure they prevent is a 502 that empties the view.

A failed batch is counted, never returned as an error. `searchRepoBatches`
reports how many repositories went unanswered and the sweep results are shown
regardless, because losing three repositories' rows beats losing the screen. A
failed *discovery* is still fatal; it is one small request and its failure means
something else is wrong.

Repository names reach `buildRepoBatchQuery` from the API, so its search strings
travel as GraphQL variables and any name failing `repoNamePattern` is dropped.
Keep it that way: never splice a repository name into the document text.

## Watching and the handoff

`internal/watch` is a state machine over readings somebody else took: no clock,
no connection, no goroutine, which is what makes a backoff measured in tens of
minutes testable in microseconds. Keep it that way.

`WatchSnapshot` is the cheap tripwire, one document per hundred armed pull
requests because GitHub caps `nodes(ids:)` there. Anything added to
`watchQuery` is resolved once per pull request in the document, so keep it to
fields that cost nothing to resolve; the precise question is
`reviewThreadQuery`'s, asked only of what the tripwire flagged.

A poll the reply did not cover must be pushed out by `Engine.Defer`. `Observe`
only reschedules what it has a reading for, so anything else stays due at a
time already past and the schedule computes a zero delay, which is a request
loop.

`model.ReviewFilter` decides what counts as feedback, and both its markers
answer only for a comment the viewer wrote. `DefaultSelfTestMarker` opts a
thread in, `AgentCommentMarker` opts one out, and neither is honoured in
somebody else's comment: a reviewer who could write either would be choosing
what a reader's watcher does. Both read the thread's newest comment alone, so a
thread that has been replied to since falls off the list rather than being
handed over for as long as it stays open.

Do not recognise an agent's reply by its prose. `AgentCommentMarker` is the
only thing that answers, because "automated response" is a phrase a reviewer
can type and a thread that matched it would take itself off the list by
accident. The marker gets onto a reply through the prompt, so `RenderPrompt` appends
`home.MarkerInstruction` to any prompt that renders without it. That is the
whole guarantee: `config.yaml` is written once and never rewritten, so an
installation predating the marker keeps a prompt that never asks for it, and a
prompt naming a `herdr.skill` is a slash command that says nothing about
replies. Leave that in the mechanism rather than in the text a reader owns;
`TestEveryRenderedPromptAsksForTheMarker` pins it.

`handoff.pick` matches an agent to a pull request on what git says about its
checkout, never on how its branch name looks: prutil's own workspace branch,
a branch that tracks or is named after the pull request's head, or a checkout
holding the head commit (`git.Client.Contains`), which a branch stacked on the
pull request only counts for when it tracks no remote branch of its own. Do not
add prefix stripping or any other name normalisation; `fix/foo` and `feat/foo`,
and `main` and `chore/sync-main`, are different work.

With no match, `herdr.fallback` decides: `new` (the default) sets a workspace up
and starts an agent, for the watcher, `F` and `N` as well as for `W`
(`Request.AllowProvision`, which always allows it); `none` sends nothing and the
`ErrNoAgent` detail names the agents passed over; `repo` hands the work to any
agent in the repository. Keep the repo fallback in its own list inside `pick`:
scored alongside the rest, being ready for input would put an agent on other
work above one that is on the pull request.

Only an agent prutil has just started is prompted twice. herdr's status lags a
prompt that did arrive, so re-sending to an agent that was already running
delivers the same feedback again; and `done` is a resting state like `idle`, so
acceptance means working, blocked, or any move to another state, never simply
"not idle". Once a submission has succeeded, nothing afterwards turns the
handoff into a failure.

`internal/ui` holds one `prRuntime` per pull request rather than a map per
field. Six maps written from six places and cleaned up from four is how a
cleanup comes to reach five of them. `a.checks` stays separate: it is fetched
data that a refresh wipes wholesale.

The two watch renderers share `watchFacts` and differ only in phrasing, because
the compact section packs onto one line what the expanded page gives a line
each. Rendering goes through `watchRow`, plain text and a style, so counting
the page is counting a slice; the scroll arithmetic asks on every key press.

## The trust boundary

Review feedback is written by whoever can comment on the pull request, and on a
public repository that is any GitHub account. `model.HoldFor` decides whether
an agent may be given it without asking the reader, from the participants
`reviewThreadQuery` reads and the `security` block in `config.yaml`. The design
and the threat it answers are in `docs/security/prompt-injection.md`.

Nine rules hold it together. Each is a thing that looks like a tidy-up and
is not.

- **Unknown is not trusted, and unread is not clear.** The automatic paths ask
  two questions: have the threads been read (`prRuntime.holdKnown`), and did
  what was read hold anything (`model.Hold.Held`). A pull request nobody has
  read is not one with nothing wrong. `prRuntime` is session-only, `applyWatch`
  batches the review read and the check read concurrently, the check read is
  dispatched on the rollup alone, and a refused review read leaves nothing
  behind — so "nobody has looked yet" is ordinary, not rare. Collapsing the
  two questions into one reopens the route.
- **Trust holds the handoff; it never filters threads.** `Feedback` answers
  whose turn it is, `HoldFor` answers whether anyone outside the boundary has
  spoken. Folding trust into `NeedsAttention` would take held feedback out of
  the counts on screen and tell the reader a pull request was quiet when it was
  not.
- **Hidden text holds a trusted author's comment too.** `hiddenRunes` flags tag
  characters, zero-width and format characters and bidi controls in anybody's
  comment, the viewer's own included, because a compromised account is still
  the account it was. Only the HTML-comment rule is exempted, for the viewer
  and for bots, whose metadata comments are how they work. The zero-width
  joiner is exempt between two emoji and nowhere else: two joiners in a row are
  not an emoji. prutil holds rather than strips, because the agent re-reads the
  threads from the API and would find whatever was hidden still there.
- **The gate reads every unresolved thread, not the feedback subset.** A thread
  whose last word is the viewer's own is not feedback, but an agent reads the
  whole pull request. It is also what makes a hold releasable: resolving the
  thread on GitHub is the reader's way out, with prutil doing nothing.
- **An empty login is nobody.** GitHub returns a null author for a deleted
  account, which decodes to the zero value. It must never match an empty
  `Viewer`, and `TrustPolicy.trusts` guards both directions.
- **A `[bot]` entry matches only a GitHub App.** GraphQL reports a bot's login
  without the suffix REST uses, so without `__typename` a person who registered
  that login would inherit the bot's trust.

- **Nothing prutil interpolates reaches a template as it came.** `promptData`
  puts every free-text value through `model.SafeLine`, which drops control and
  format characters and folds the value onto one line. A title is the pull
  request author's text, a check's name comes from a workflow file in the
  branch under review, and a legacy status context's description is set by
  anything with commit-status write access. A check URL is kept only when
  `model.SameHostURL` finds it on the same host as the pull request's own URL,
  which is how the host is known without configuration on an enterprise
  install, and the description is capped so one check cannot be the whole
  prompt.
- **`herdr.Client.Prompt` refuses control characters, and duplicates that
  knowledge on purpose.** It is the backstop for a prompt template somebody
  wrote by hand or a note built from data nobody has considered, so it must
  keep answering even if `promptData` stops. Newline and tab are allowed; a
  prompt is prose and the check list is indented. It does not import `model`:
  this is about what may cross the wire to herdr.

- **Provisioning asks a different question from the trust gate.** The gate asks
  who has commented; `Dispatcher.mayProvision` asks whose code prutil is about
  to check out, because an agent started in another author's worktree loads
  that repository's settings, hooks, MCP servers and instruction files, and
  hooks run outside any sandbox. Only the viewer's own pull requests and
  `trusted_authors` pass, and an author GitHub has lost fails like any other
  stranger. A reader may still hand work to an agent they checked out there
  themselves; what goes away is prutil doing it unasked.

Every explicit path asks for a second press before acting on a held pull
request, naming the key the reader pressed, via the shared `pendingConfirm`.
That gate is deliberately not `force`'s to answer: `force` says the work is due
— it answers the armed check, the wait on running checks and the brake on a
head already investigated — and a key press is not on its own evidence that
anybody has read the hostile thread. Attached to `force`, the gate was skipped
by `F` and by `W` in the detail pane, where `handleKey` sends `W` to the check
path and that branch provisions as well.

`investigateChecks` asks at the key press rather than in `applyFailedChecks`,
because the checks may still have to be fetched and a confirmation arriving a
round trip later would be answered by a press meant for something else. The
confirmation is one press per send, not a standing permission, and no override
puts a held pull request back on the automatic loop.

A held handoff is an outcome like any other: `home.OutcomeHeld` in
`handoffs.jsonl`, and a herdr notification through `dispatcher.Notify` under
the reader's own `herdr.toast`, once per set of newest comment ids.

## Sandboxes

`internal/sandbox` holds one `Profile` per kind of agent. `Launch` writes the
policy the agent's arguments refer to and returns them, with what the vendor
says they give; `Inspect` asks about an agent already running. Claude Code's is
complete. Copilot CLI and agy have entries with nothing filled in, in files of
their own; `docs/security/tier2-handoff.md` is the brief for them, and
`docs/security/prompt-injection.md` records what the Claude build found.

The rules, again each one a thing that looks like a tidy-up and is not:

- **A kind with no profile is not held to `security.require_sandbox`.** It is
  started and handed work exactly as before. Requiring a sandbox prutil cannot
  start would stop the loop dead for that kind.
- **The evidence is the agent's command line, never prutil's memory.**
  `Controller.Process` reads it from herdr. A list of the panes prutil started
  would forget its own contained agents on every restart, and pass them over.
- **Ask the vendor; do not read the policy.** Claude's `Launch` and `Inspect`
  both run `claude … sandbox status` from the agent's directory, because the
  answer includes the project's own settings and whatever the reader edited
  into their policy. prutil does not interpret that file itself.
- **The reader's policy is written once; a launch file is never rewritten.**
  `home.CreateOnce` writes `sandbox/claude-settings.json` and prutil never
  touches it again. Anything that moves or belongs to one pull request, the SSH
  socket and the owner's git rules, goes into a launch file,
  `sandbox/launch/<kind>-<hash>.json`, named by its contents so that a running
  agent's command line names exactly what it read.
- **An agent on the pull request that is not contained gets no neighbour.**
  `pick` returns it apart from the passed, and `Dispatch` reports it rather than
  answering it with `fallback: new`, which would put two agents on one branch.
- **`Request.Manual` is the reader, not the watcher.** W and F set it, and may
  use an agent that is not sandboxed; nothing automatic may.
- **Git leaves Claude's sandbox over HTTPS, by owner-length rules.** SSH cannot
  get out on macOS. The rules are owner-length for `insteadOf` and
  `pushInsteadOf` both, because a reader's host-level rewrite to SSH is as long
  as any host-level rule prutil could write.
  `TestTheGitRulesWinAgainstAReadersSSHRewrite` runs real git to pin it.
- **A prompt line may not begin with `!`.** Claude Code runs it as a shell
  command without the model, and outside the sandbox. `herdr.Client.Prompt`
  refuses one, whichever vendor the prompt is for.

## Desktop notifications

`notifications` in `internal/ui/notify.go` is the one list of what prutil can
notify about: the settings pane's row and explanation, the notification's
wording, and the rule that recognises the change. Each rule compares two
`prFacts`, which `prRuntime` holds per pull request. Adding a notification
means a `home.NotificationEvent` with its default, an entry in that list, and
whatever fact the rule needs; a test fails when the two lists disagree. A fact
that needs a new field must come from `listQuery` and `watchQuery` alike, and
`watchQuery` still has to stay cheap.

The `s` pane draws `allSettings` in `internal/ui/settings.go`, whose
notification rows are built from `notifications` rather than written out beside
it, so a new notification reaches the pane by itself. Each entry carries its
own `enabled`, `set` and `after` funcs; a setting that is neither a
notification nor a watch option costs an entry and nothing else. Do not give
`settingItem` a field naming its kind: the branch that field buys reappears in
every place the pane touches a setting.

Readings come from three places: the open list loading, the watcher's
snapshots, and a poll of every open pull request that runs only while a
notification is on (`scheduleNotifications`; `notifyPending` keeps it to one
wait or request at a time). Every reading is stamped with when it was asked for,
and `App.notice` ignores one older than what it holds, or a slow list load
could undo an approval a poll had already seen and have it announced twice. The
first reading of a pull request is a baseline and never announced. Readings are
recorded even while every notification is off, so turning one on does not
announce old news.

## The application directory

`home.Load` degrades and cannot fail. A configuration that will not parse
leaves the defaults standing and is never moved or rewritten, because the
reader wrote it deliberately; an unreadable watch state is moved aside before
the empty state replacing it can overwrite it. Only `home.Open` failing leaves
no store, which is the one case with nowhere to write. What could not be read
reaches the reader on the footer's notice line and stays there.

`git.NewResolver` absorbs a nil store in both its shapes, the plain nil
interface and a nil `*home.Store` inside one. The second is the trap: it passes
an ordinary nil check and then dereferences a nil receiver.

Apart from the first-run template, the settings pane is the only thing that
writes `config.yaml`. `Store` methods change values by editing the text in place
(`setScalar`, `setBlockScalar`, `setMapEntry`, `setSequence`, and `deleteKey` in
`internal/home/yamledit.go`), then read the result back and verify that nothing
else in the configuration changed. Do not replace it with a decode and re-encode:
yaml.v3 drops blank lines and moves comments, in a file the reader wrote by hand.
The file is resolved through symbolic links before it is replaced, so a
dotfiles-managed link stays a link.

## Settings and the settings pane

The `s` settings pane (`internal/ui/settings.go`) exposes all configuration options
defined in `config.yaml`. All settings are registered in `internal/ui/settings_registry.go`
as `settingDescriptor` values, almost all of them built by a constructor for their
kind rather than written out.

A setting gives up two things. A `settingMeta` says what it is called, which
section it sits in, where it lives in the file and what to say when it changes.
A `field[T]` says how to read the value from a `home.Config` and how to write it
back. Everything else — the display, the comparison against the default, the
step or toggle, the typed entry, the save and the reset — follows from those, so
there is one copy of each rather than one per setting.

To add a new setting:
1. Add the field and YAML tag to the appropriate struct in `internal/home/config.go`,
   along with any defaults in `DefaultConfig()`.
2. Call the constructor for its kind from `allSettings()`: `boolSetting`,
   `durationSetting`, `intSetting`, `stringSetting`, `templateSetting` or
   `collectionSetting`. Give it a `settingMeta` and a `field[T]`, plus whatever
   that kind needs — a minimum and a step for numbers, the placeholder for blank
   text, the default for a template.
3. If the setting uses a new composite structure or custom storage type, add the corresponding helper in
   `internal/home/settings_store.go` and `internal/home/yamledit.go`.

Write a descriptor out in full only when its behaviour really is its own, and say
why in a comment. Two are: `notifications.approved`, which applies for the session
and says so when a save fails rather than refusing, and `herdr.fallback`, the only
enum, which has nothing to share a constructor with.

Save through the `App` helpers (`saveSetting`, `resetSetting` and the rest), never
`a.store` directly. They write the file first and move `a.homeCfg` only once that
has succeeded, because `a.homeCfg` is what the watcher polls and hands off by: a
setting that is not in the file must not be one prutil is acting on.

`TestEverySettingAnswersItsOwnControls` runs one case per registered setting, so a
new one is covered by adding it.

## Things to avoid

- Reading `~/.config/gh/hosts.yml` or otherwise handling tokens. gh owns auth.
- Blocking the Bubble Tea update loop. Network work belongs in a `tea.Cmd`.
- Widening the gh process pool beyond the `concurrency` constant in
  `cmd/prutil/main.go` without a reason; each unit is a forked process.
