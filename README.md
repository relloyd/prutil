# prutil

A terminal dashboard for the GitHub pull requests you have open, across every
repository your account can see.

The list on the left shows each pull request with a coloured dot for its CI
state, its age, repository, branches, number, review state and diff size. The
pane on the right shows the individual GitHub Actions checks for whichever pull
request is selected.

## Requirements

- Go 1.26 or newer, if you are building from source.
- The [gh CLI](https://cli.github.com), installed and logged in:

  ```sh
  gh auth login
  ```

prutil shells out to `gh`, so it uses your existing gh credentials and never
stores a token of its own. If you would rather use a personal access token,
export `GH_TOKEN` (or `GITHUB_TOKEN`) and gh will pick it up. `GH_HOST` selects
a GitHub Enterprise host.

## Install

```sh
task install     # tidy, vet, lint, test, build and go install
```

or, without [go-task](https://taskfile.dev):

```sh
go install github.com/relloyd/prutil/cmd/prutil@latest
```

## Usage

```sh
prutil
prutil -limit 20
prutil -query 'is:open is:pr author:@me org:acme sort:created-desc'
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-query` | `is:open is:pr author:@me archived:false sort:created-desc` | the GitHub search used to find pull requests |
| `-limit` | 100 | how many pull requests to load |
| `-closed-query` | `is:pr author:@me is:closed archived:false sort:updated-desc` | the search used by the recently closed view |
| `-closed-per-repo` | 3 | how many closed pull requests any one repository contributes |
| `-closed-repo-limit` | 30 | how many repositories the closed view may query individually |
| `-skip-auth-check` | false | skip the `gh auth status` check at startup |
| `-dry-run` | false | record what would be sent to a coding agent without sending it |
| `-version` | | print the version and exit |

## Keys

| Key | Action |
| --- | --- |
| `j` / `k` or `↓` / `↑` | move within the focused pane |
| `g` / `G` | jump to the first or last item |
| `l` or `→` | focus the checks pane |
| `h`, `←` or `esc` | go back to the list |
| `enter` | open the selected pull request, or the selected check, in your browser |
| `y` or `c` | copy the selected pull request's URL, or the selected check's, to the clipboard |
| `r` | refresh from GitHub |
| `a` | auto-refresh: reload every 30s, five times over. press again to add five more |
| `w` | watch the selected pull request, or stop watching it |
| `W` | hand the selected pull request's open review feedback to a coding agent now, creating one when needed |
| `N` | check the selected open pull request for new review feedback and notify an existing agent |
| `tab` | switch between your open and your recently closed pull requests |
| `?` | toggle the full key list |
| `q` or `ctrl+c` | quit |

Below 80 columns the two panes collapse into one: the list fills the terminal,
`l` swaps to the checks, and `h` swaps back.

The footer has one line, so it lists the actions and leaves moving about to the
arrow keys. `?` shows every binding, including `h`, `←` and `esc` for going back
and `W` for handing a pull request over and `N` for testing automatic
new-feedback notification.

Copying uses whichever clipboard program your platform provides: `pbcopy` on
macOS, `clip` on Windows, and `wl-copy`, `xclip` or `xsel` on Linux, whichever
is installed first. If none is, prutil says which ones it looked for.

## Auto-refresh

`a` reloads the view on screen every 30 seconds, five times, and then stops.
That is two and a half minutes of watching a pull request's checks turn green
without touching the keyboard. Press `a` again at any point during the run and
another five reloads are added, so a long CI run is a matter of topping the
counter up rather than holding a mode open. The header counts down what is
left, and once it runs out prutil is back to refreshing only when you press
`r`.

Each automatic reload is the same work `r` does, so it costs the same one
request for the list plus the checks it warms.

## Watching, and handing feedback to an agent

`w` marks a pull request as watched. The row grows a `◉`, the header counts how
many are marked, and the mark survives quitting: it is kept in prutil's own
directory, not in the terminal.

From then on prutil watches that pull request for review feedback, and when
some appears it gives it to a coding agent through
[herdr](https://herdr.dev). Open feedback means a review thread that is neither
resolved nor already answered by you, so a conversation you have had the last
word in is left alone. `W` does the same thing on demand, for a pull request
you have not armed or one you want looked at again now.

To test watcher delivery with a code-line comment of your own, put this exact
marker in the opening comment's Markdown source:

```html
<!-- prutil:test -->
```

GitHub hides the marker when it renders the comment. While that thread remains
unresolved, prutil treats it as feedback even though you wrote the latest
comment. The marker only works on a code-review thread that you opened; it
does not opt in an ordinary pull-request conversation comment or a marker
written by another reviewer. Normal duplicate suppression still applies, so
the thread is handed over again only when it gains a new latest comment.

`N` is a diagnostic trigger for the automatic path. It asks GitHub for the
selected open pull request's review threads and sends only feedback prutil has
not handed over before. It never provisions a checkout, worktree, workspace,
or agent, so it is useful for confirming the normal no-agent and herdr
notification behavior without waiting for the watcher to spot a change.

prutil picks the agent rather than asking you to. It lists the agents herdr
knows about, reads the repository and branch out of each one's working
directory, and prefers the one sitting on the pull request's head branch. An
agent one branch away is used too, and told so in the prompt. The terminal
prutil is itself running in is never given work. If the agent is busy prutil
waits for it to finish, up to fifteen minutes, and if it is stuck at a prompt of
its own nothing is sent at all.

`W` is also the explicit consent to set up a workspace when no suitable agent
exists. It resolves a local checkout, fetches the pull request's
`pull/<number>/head` ref, reopens an existing matching herdr worktree when it
can, or creates a no-focus worktree workspace and starts the configured agent
there. Set `herdr.agent_kind` to the herdr agent kind to start (for example
`claude` or `copilot`); it is required only for this creation path. The
automatic watcher never creates a Git worktree, herdr workspace, or agent: it
continues to report that no local agent was available.

Nothing is ever handed over twice. Each thread prutil sends is remembered
against the comment it ended on, so pressing `W` again on a review whose
comments you have decided not to act on costs one GitHub request and sends
nobody anything. That is what makes it safe to leave a pull request watched.

Every attempt is recorded, sent or not, one JSON object per line, in
`handoffs.jsonl` beside the configuration. Start with `-dry-run`, which writes
that log and shows the status line without mutating a repository, worktree,
workspace, or agent.

The selected pull request's detail pane includes a `WATCH` section when it has
watch or handoff activity. It shows the current state-machine tier, cadence and
next check; work currently in flight; a bounded activity feed from this TUI
session; and the three most recent durable handoff attempts for that pull
request. The activity feed resets when prutil exits; `handoffs.jsonl` is the
cross-session record.

### What the watching costs

Watching a pull request is two questions, asked at very different rates.

The first is cheap and batched. One GraphQL request covers every watched pull
request at once, whatever repositories they are spread across, and reads only
enough to notice that something moved: the head commit, the check state, the
last-updated time, the conversation-comment total, and the total review-thread
count. It is one request and one rate limit point however many pull requests
you have marked.

The second is the expensive one, and it is asked only of the pull requests the
first one flagged, or of one that has gone five polls without being asked. That
second part matters: a reply inside an existing review thread moves neither
count, so a counter on its own would miss it.

That precise read covers the first 100 code-review threads. For each one it
reads the opening comment's author and body plus the newest comment's author
and id. This is why the self-test marker belongs in the opening comment. The
top-level pull-request conversation-comment count is only a change signal; it
is not a code-review thread and is never handed to an agent.

How often the first question is asked depends on what the pull request is
doing:

| The pull request | Asked about |
| --- | --- |
| has checks running | every 30 seconds |
| has nothing in progress | after 2 minutes, then 4, 8, 16, and 30 |
| has been given to an agent | after 10 minutes, then 20, 40, and 60 |
| has not changed for about two hours | not at all |

Anything at all changing puts a pull request back to the top of that ladder. A
pull request prutil has stopped asking about is still armed, and its `◉` turns
hollow to say so; `r` wakes it, along with everything else.

### Configuration

prutil reads `config.yaml` from `$PRUTIL_HOME`, else `$XDG_CONFIG_HOME/prutil`,
else `~/.config/prutil`. On first startup it creates a complete editable
template there, with these defaults. Every key remains optional:

```yaml
herdr:
  agent_kind: claude        # required only when W starts a new agent
  skill: pr-comment-triage  # the skill the default prompt invokes
  wait_for_idle: 15m        # how long to wait for a busy agent
  dry_run: false
  toast: true               # show a herdr notification alongside each handoff
watch:
  active_interval: 30s      # while checks are still running
  base_interval: 2m         # once nothing is in progress
  max_interval: 30m         # where the backoff stops growing
  notified_interval: 10m    # after a handoff, when an agent is at work
  max_notified_interval: 60m
  idle_interval: 10s        # how often a busy agent is re-read
  dormant_after: 3          # polls at the cap before prutil stops asking
  force_precise_every: 5    # polls before the expensive question is asked anyway
repos:
  acme/widgets: ~/src/widgets  # optional explicit checkout for W
discovery:
  roots:
    - ~/src                    # optional roots scanned after repos misses
```

With `skill` set, the prompt is `/<skill> <pull request url>`. Without it,
prutil spells the job out instead. Either can be replaced with `herdr.prompt`,
a Go template given `Repo`, `Number`, `URL`, `Title`, `HeadRef`, `BaseRef`,
`Skill`, `UnresolvedCount`, `NewCount` and `Note`.

Explicit `repos` entries win. When none exists, prutil checks its private
`repos.json` cache and then scans `discovery.roots`, validating every candidate
against its origin remote before it can be used. Stale cache paths are ignored
and refreshed. GitHub poll intervals are clamped to fifteen seconds at the
shortest, so a typo cannot turn a dashboard into a load test.

## Views

`tab` switches the list between your open pull requests and your recently
closed ones. Each view keeps its own cursor and scroll position, and the closed
view is not fetched until the first time you show it.

The closed view is grouped: no repository contributes more than
`-closed-per-repo` rows, so a single busy repository cannot fill the screen and
hide everywhere else you have been working.

If GitHub refuses some of those per-repository queries, which a very large
organisation can provoke, the header says how many repositories were
unreachable and the rest of the list is shown anyway. Press `r` to try again.

Note that `-closed-query` inherits `archived:false` from the open view. If most
of your history is in repositories that have since been archived, drop that
qualifier to see it:

```sh
prutil -closed-query='is:pr author:@me is:closed sort:updated-desc'
```

## How it stays quick

The headline list is one GraphQL request, which includes the check rollup that
colours each dot, so the first screen appears after a single round trip. The
individual checks are fetched afterwards, in the background for the top of the
list and on demand as you move, with at most four gh processes at a time. Both
are cached until you press `r`.

The closed view usually costs one request too. It sweeps your closed pull
requests newest first, and when that sweep reaches the end of the search it
already holds everything, so the grouping is exact and nothing more is fetched.
Only when the sweep runs out of room first does it go further. It reads a little
further down the same search, collecting nothing but repository names, then
queries the ones that came up short directly. Nothing enumerates an
organisation's repositories, so the work does not grow with the size of the
organisations you belong to. Those per-repo searches are batched under GraphQL
aliases, so three repositories cost one request and one rate limit point.

Batches are small on purpose. GitHub gives a single GraphQL document about ten
seconds before returning a 502, and that budget is spent faster in a large
organisation, so the closed view trades round trips for headroom.
