# Prompt injection and the feedback loop

Status: Tier 1 is built. Tier 2 has profiles for Claude Code, Copilot CLI and
agy; only Claude's isolation has been verified with a live agent. Copilot and
agy check settings and argv, but still need live probes from
[`tier2-handoff.md`](tier2-handoff.md). Tier 3 is design only. This document
records the threat, what was checked, and the order the defences are built in.

## Summary

prutil's watcher turns review feedback into work for a coding agent running on
this workstation. The feedback is written by whoever can comment on the pull
request. On a public repository that means any GitHub account. The agent that
reads it runs with the reader's full authority:

- a shell;
- the SSH key (this machine's git configuration rewrites every github.com
  remote to SSH);
- the gh token in the keychain, which reaches every repository and
  organisation the reader can;
- unrestricted network access.

Untrusted input, private data and a way to send data out, all in one process,
is the combination Simon Willison calls the lethal trifecta. Once all three are
present, a prompt injection is an incident rather than a curiosity.

No single fix removes the risk, so the plan is three layers, cheapest first:

1. **prutil decides what reaches an agent.** Feedback from anyone outside a
   trusted set is held for the reader instead of being sent. Anything prutil
   itself puts into a prompt is sanitised. This is plain Go with no new
   dependencies, and it works the same for every agent.
2. **The agent runs inside its vendor's own sandbox.** Claude Code, GitHub
   Copilot CLI and Google Antigravity CLI all ship an OS-level sandbox that
   keeps the agent a normal host process, which is what herdr needs in order to
   see it. prutil only has to pass the right flags when it starts an agent.
3. **Optionally, the reader and the actor are separate.** A model with no tools
   reads the untrusted text and returns a constrained work list. The agent that
   edits code sees only that list. Later, the agent loses its GitHub
   credentials altogether and prutil does the pushing.

Layer 1 lowers the chance that hostile text reaches an agent. Layer 2 limits
what an agent can do once it has. Layer 3 narrows the channel between the two.
None of them makes a malicious change that is worded as a plausible one safe.
That still needs a person reviewing the commit, as every change does.

## What is exposed today

### Nobody's identity is checked

`model.ReviewThread.NeedsAttention` (`internal/model/review.go:99`) asks one
question: is the newest comment the viewer's own? Everything else is feedback.
`App.applyReview` (`internal/ui/watch.go:403`) then hands that feedback to an
agent with no human in between. Under the default `herdr.fallback: new`, it
creates a worktree and starts a new agent when none is working on the pull
request.

So a stranger's comment on a watched pull request is enough to put an agent to
work, and even to start one. `N` follows the same path.

### The agent reads every thread itself

The prompt prutil sends carries no comment text. That sounds safe, but the
prompt then tells the agent to go and read the threads, either through the
configured skill or through the default prompt's "Read each one". The agent
reads all of them, with its tools, from the raw API.

The raw body includes things github.com does not show:

- HTML comments;
- Unicode tag characters (U+E0000–E007F), which carry ASCII invisibly;
- zero-width characters;
- bidi controls.

A comment that looks empty or harmless to the reader can be a paragraph of
instructions to the agent.

### Unsanitised data goes into the prompt

`handoff.promptData` (`internal/handoff/handoff.go:562`) copies these fields
into the template verbatim:

- `Title`, `HeadRef` and `BaseRef`;
- each failed check's `Name`, `Workflow`, `Description` and `URL`.

`Description` is free text. For a legacy status context, anything with
commit-status write access sets it. Check names come from workflow files, which
live in the pull request's own head.

### Keystroke-level injection

`herdr agent prompt` types the prompt into the agent's terminal. prutil does not
know, and should not need to know, exactly how herdr delivers the text.

A control character or ESC sequence inside it could end a bracketed paste or
submit a line early. Claude Code runs a prompt line that begins with `!` as a
shell command without involving the model at all. Treat Copilot CLI and `agy`
as having the same shell escape until shown otherwise. This is not a weakness
of the model. It is ordinary terminal injection, and it is deterministic.

### Lateral movement through herdr

Every herdr pane exports `HERDR_SOCKET_PATH`, and the herdr CLI can prompt,
type into and run commands in any pane. An agent that has been talked into it
can:

- run `herdr agent prompt` against an unsandboxed agent;
- run `herdr pane run` in an ordinary shell;
- type into the pane prutil itself is running in.

Whatever containment an agent gets, the herdr socket has to be outside it.

### Provisioning over other people's code

`W` and `F`, and the automatic path under `fallback: new`, run
`Dispatcher.provision` (`internal/handoff/handoff.go:437`). That fetches
`pull/<number>/head` for whichever pull request is selected and starts an agent
in the resulting worktree.

The default search lists only the reader's own pull requests, but `-query` can
list anyone's. An agent started in someone else's checkout loads that
repository's agent configuration: project settings, hooks, MCP servers and
instruction files. Hooks run outside any sandbox.

## How herdr sees an agent

Any containment has to leave prutil's use of herdr working: `agent list`,
`agent get`, `agent start`, `agent prompt` and the lifecycle states that
`Dispatcher.settle` and `Dispatcher.accepted` depend on. The facts below were
checked against herdr 0.8.2 on this machine and against herdr's own
documentation.

- **Identity comes from the pane's foreground process.**
  `herdr pane process-info` reports `argv0: claude` for a Claude pane.
- **State comes from screen manifests.** These are rules over the terminal
  title and the bottom of the screen buffer (`herdr agent explain` lists the
  rules that matched). herdr's docs name the screen manifest as the state
  authority for Copilot CLI and Antigravity CLI. Their integrations, like the
  Claude hook installed here (`~/.claude/hooks/herdr-agent-state.sh`), report
  only the session id over the socket.
- **`herdr agent start NAME --kind K --pane P -- [AGENT_ARG]...`** passes
  arguments through to the agent. prutil can launch an agent with extra flags
  without any change to herdr.
- **Wrapped agents are supported, with a condition.** herdr's docs say that a
  host-visible wrapper hides the real agent, and that setting
  `HERDR_AGENT=<agent>` on the wrapper command tells herdr which screen
  manifest to use. The hint "cannot work if set inside a VM or container".
  `herdr pane run` followed by `herdr agent wait` is how such a wrapper would be
  launched, since `agent start` always runs the canonical executable.

The consequence decides most of what follows. A sandbox that keeps the agent as
an ordinary host process is invisible to herdr. All three vendors' built-in
sandboxes work that way: they confine the child processes the agent spawns, not
the agent itself.

## Vendor compatibility

Claude Code was checked against version 2.1.273, and again against 2.1.282
with a live sandboxed agent when Tier 2 was built. Copilot CLI 1.0.88 and agy
1.2.11 were inspected locally for their flags and settings keys; their OS
isolation has **not** been tested with a live agent. Rows not specifically
confirmed by CLI help or binary inspection remain documentation-based.

| Capability | Claude Code | GitHub Copilot CLI | Antigravity CLI (`agy`) |
| --- | --- | --- | --- |
| OS sandbox on macOS | Seatbelt; shell commands | Seatbelt; shell commands, built-in searches, local MCP and LSP servers | `sandbox-exec`; shell commands |
| Covers the agent's own file tools | in effect, yes: a `Read`/`Edit` deny rule reaches the sandbox too (2.1.282) | no OS isolation; 1.0.88 help says built-in edits follow policy on a best-effort basis | not documented; assume no |
| Enable at launch | `--settings <file>` carrying `sandbox.enabled` | `--experimental --sandbox` (the switch is described in 1.0.88 `help sandbox` but omitted from `--help`) | `--sandbox` (in 1.2.11 `--help`) |
| Policy supplied at launch | yes, the whole `--settings` file | no; the `sandbox` key in `~/.copilot/settings.json`, or `COPILOT_HOME`, which moves the entire state directory | no; `~/.gemini/antigravity-cli/settings.json` |
| Stop the model escaping | `sandbox.allowUnsandboxedCommands: false` | `sandbox.allowBypass: false` | `toolPermission: "proceed-in-sandbox"`; 1.2.11 reports `unsandboxed` allow rules are invalid and ignored |
| Network allowlist | sandbox network settings | `userPolicy.network.allowOutbound` is a boolean for sandboxed commands; `--allow-url`/`--deny-url` govern URL tool permissions, not an OS-level domain allowlist | domains allowed under `read_url` (not live-probed) |
| Deny tools at launch | `--disallowedTools` | 1.0.88 `--deny-tool` supports `write(path)` but **not** `read(path)` or recursive path globs; `--secret-env-vars` excludes named tokens from shell/MCP environments | only through settings `permissions` |
| Report its sandbox state | `claude sandbox status`, one JSON line with `enabled` and `strictMode`; `--settings` before it asks about that file | nothing documented | nothing documented |
| Headless, no tools, schema-checked output | `-p --tools "" --json-schema <schema>` | `-p --available-tools … --output-format json`; no schema flag | `-p --json-schema <schema>`; no documented way to disable tools, and headless mode ignores `permissions.allow` and can hang (issue #548, open) |
| Hooks that can refuse a tool call | `PreToolUse` | `preToolUse`, `permissionRequest`; repository hooks load only in trusted folders | `hooks.json` |

For Copilot and agy, OS containment of their file tools cannot be assumed.
Copilot's file-tool policy is explicitly best-effort, and its `--deny-tool`
does not provide a protected-path read rule in this version. Do not treat
their profile's `Contained` posture as proof that a secret file cannot be
read: live probes with their own file tools are required.
  Claude Code turned out to be the exception: its deny rules feed the sandbox
  as well, so one rule does both. See 2b.
- **Only Claude Code can say whether it is sandboxed.** For the other two,
  prutil can only rely on how it launched the agent itself.

## Tier 1: prutil decides what reaches an agent

Everything in this tier runs inside prutil before any agent is involved, so it
is the same for every vendor. It adds no dependencies.

### 1a. An author trust gate

**What to fetch.** `reviewThreadQuery` (`internal/gh/query.go:214`) selects
each comment author's login only. Add, for both the `opener` and `latest`
aliases:

- `authorAssociation`;
- `author { __typename login }`.

Add a third alias that lists everyone who has spoken in the thread:

```graphql
participants: comments(first: 100) {
  totalCount
  nodes { authorAssociation author { __typename login } }
}
```

The first and last comment are not enough. An attacker can reply in the middle
of a thread, and a trusted reviewer can reply after them. By GitHub's published
cost formula the alias adds about one rate-limit point to the precise query. It
adds nothing to the tripwire (`watchQuery`), which is the query asked often.

**The policy.** It lives in `internal/model/review.go` beside `NeedsAttention`.
A thread is trusted when every participant is one of these:

- the viewer;
- an author whose association is in `security.trusted_associations`;
- an author listed in `security.trusted_authors`.

A thread whose participant list came back shorter than its `totalCount` is not
trusted. Not knowing who spoke is not the same as knowing.

GraphQL reports a bot's login without the `[bot]` suffix that REST uses. A
`trusted_authors` entry ending in `[bot]` therefore matches only an author whose
`__typename` is `Bot`, and any other entry matches only a `User`. Otherwise a
person who registered a bot's name as their login would inherit its trust.

**Configuration.** A new block in `internal/home/config.go`, the written
template and the README:

```yaml
security:
  # Authors whose review comments may be handed to an agent without asking.
  trusted_associations: [OWNER, COLLABORATOR]
  # Extra authors by login. A name ending in [bot] matches only a GitHub App.
  trusted_authors: ["gemini-code-assist[bot]"]
```

Two notes on the defaults:

- `MEMBER` is deliberately not among them. It means organisation member, which
  in a large organisation implies no write access at all. A reader whose
  organisation is small enough for membership to mean something adds it.
- `gemini-code-assist[bot]` is listed because prutil's own `review.comment`
  default summons it. A trusted bot can still quote somebody else, but an
  untrusted author in the same thread makes that thread untrusted anyway.

**Hold and notify.** A pull request is held when any unresolved thread on it
has an untrusted participant. That is every unresolved thread, not the subset
`Feedback` returns: a thread whose last word is the viewer's own is not
feedback, but an agent reads the whole pull request regardless, and resolving
the thread is what releases the hold. Then:

- **The automatic paths send nothing** — `applyReview`, and therefore `N`, and
  `applyFailedChecks` with it. A failed-check prompt carries no comment text,
  but the agent it starts reads the same pull request, and `fallback: new` can
  create a worktree and start one from that path too. Handing over only the
  trusted threads would not help either, because the agent reads the pull
  request, not a list of thread ids.
- **The attempt is recorded** as a new `home.OutcomeHeld` in `handoffs.jsonl`
  and in the watch activity feed, naming the untrusted authors. The `WATCH`
  section in `internal/ui/watch_detail.go` shows it.
- **The reader gets one toast** per new latest comment, so a held pull request
  does not notify on every poll.
- **`W` asks for a second press** that names what it is waving through: the
  untrusted authors, hidden content, or both. It clears either kind of hold,
  because a reader who has looked at the thread is the one qualified to judge
  it, and a false positive must not be a dead end. This is the confirmation
  `App.triggerAIReview` (`internal/ui/watch.go:500`) already does with
  `pendingReviewKey` and `pendingReviewAt`. Generalise that into a small
  pending-confirmation value rather than adding a second pair of fields.
- **Resolving a hostile thread on GitHub releases the hold.** The next poll no
  longer counts it as feedback.

Also add the trusted thread ids to `home.PromptData` as `TrustedThreads`, so a
triage skill can confine itself to them. `handoff.Request.Threads` already
means something else, each unresolved thread's newest comment id, which the
caller records once a handoff lands, so it becomes `HandedThreads` at the same
time rather than leaving two `Threads` with different meanings a field apart.
This is defence in depth. The gate above is the boundary.

### 1b. A hidden-content detector

prutil already fetches the opening and newest body of each thread. A function
in `internal/model` flags either body when it contains:

- Unicode tag characters (U+E0000–E007F), which have no legitimate use in a
  review comment;
- zero-width and other format characters (U+200B–U+200F, U+2060–U+2064,
  U+FEFF), except U+200D standing between two emoji: the joiner is how every
  family and profession emoji is built, and flagging it would hold a pull
  request over an ordinary comment. A joiner anywhere else still counts, since
  a run of them between words encodes data as readily as a tag character does;
- bidi controls (U+202A–U+202E, U+2066–U+2069);
- an HTML comment other than the configured self-test marker, in a comment by a
  person who is not the viewer. Review bots embed HTML comments as metadata
  routinely, so a bot's are not a signal.

A flagged thread is held exactly as an untrusted one is, even when its author is
trusted. Accounts do get compromised. `W` clears this hold as it clears the
other, and says which of the two it is clearing.

### 1c. Sanitise what prutil interpolates

**In `promptData`,** before anything reaches a template:

- strip C0 and C1 control characters, ESC and Unicode format characters from
  `Title`, `HeadRef`, `BaseRef` and each check's `Name`, `Workflow`,
  `Description` and `URL`;
- collapse newlines in those single-line fields;
- cap `Description` at 200 runes;
- keep a check URL only when its host is the GitHub host prutil is talking to.

**At the boundary,** `herdr.Client.Prompt` (`internal/herdr/herdr.go:288`)
refuses text containing ESC or any C0 control other than newline and tab. A
future template, or a note built from new data, cannot then reopen the terminal
injection route.

**In the prompt,** `DefaultCheckPrompt` introduces the check list as data
reported by CI, not instructions. This kind of labelling (sometimes called
spotlighting) is cheap and weak. It is kept because it costs nothing, and
nothing depends on it.

### 1d. No provisioning over untrusted code

- Add `author { login }` to `listQuery`'s pull request fields, and
  `viewer { login }` at the root of the document. Neither adds a connection, so
  neither changes the cost.
- `Dispatcher.provision` refuses with a new error unless the pull request's
  author is the viewer or listed in `trusted_authors`.
- `W` on someone else's pull request can still hand work to an agent the reader
  already has checked out there. That exposure is the reader's own choice. What
  goes away is prutil creating the exposure on its own.

### 1e. Unread is not the same as clear

Found while building 1a, and the reason the check path needed more than a
`hold.Held()` test.

The hold is what the last read of a pull request's review threads found. An
automatic path that acts whenever the hold is empty acts on two different
things: a pull request read and found clear, and a pull request nobody has
read. The second is not a judgement, and treating it as one is a way past the
gate that needs no injection at all — only timing.

It is also the ordinary case rather than a corner of one. Four ways in:

- **Every start.** `prRuntime` is session-only, so prutil begins each run
  knowing nothing about any pull request. The first poll after a restart, on a
  watched pull request whose checks are failing, is the whole exposure.
- **The poll race.** `applyWatch` dispatches the review read and the check read
  in one `tea.Batch`. Batched commands run concurrently and their replies land
  in whichever order the two GitHub requests finish in, so the check reply
  winning is neither rare nor detectable after the fact.
- **Checks without a precise read.** The check read is dispatched for any armed
  pull request whose rollup is failure. Whether the tripwire flagged it for a
  precise read is a separate question the engine answers on its own backoff, so
  there are polls that read the checks and never read the threads.
- **A refused review read.** `applyReview` returns early when GitHub errs, and
  a rate limit is the most likely reason. The pull request is then indefinitely
  in the state where nothing is known about it.

So the automatic paths ask for two things, not one: the threads have been read,
and what was read holds nothing. `prRuntime.holdKnown` is the first.

A pull request that fails the first asks for its threads rather than handing
work over, and records that it did. Nothing marks the head as investigated, so
the next poll tries again with the answer in hand. The cost of being right here
is one polling interval on the first failed check after a start, which is also
the only case a reader would notice.

Fail-closed is the point: a read prutil could not make leaves the check path
shut rather than open.

**Every explicit path asks, and the gate is not `force`'s to answer.** The
hold was first attached to `force`, which was the wrong axis. `force` means the
reader asked for this now, and answers the armed check, the wait on checks
still running, and the brake on a head already investigated — all questions
about whether the work is due. Whether the reader has judged the trust boundary
is a different question, and an explicit key press is not on its own evidence
that anybody has read the hostile thread.

Attached there, two of the three explicit paths skipped the gate, and one of
them was spelled `W`: `handleKey` sends `W` to the check path when the cursor
sits on a failed check, and that branch also provisions. So the same key asked
for a second press on the list and started an agent without asking in the
detail pane.

`investigateChecks` now asks for that second press, naming the key the reader
actually pressed, and does it at the key press rather than in
`applyFailedChecks`: the checks may still have to be fetched, and a
confirmation that appears a round trip later is worse than none, because the
reader has moved on and a second press in the gap would answer a question that
had not been asked yet.

The confirmation is one press per send rather than a standing permission.
Nothing remembers which hold was approved, so the next attempt asks again;
what keeps that from nagging is that a handoff which lands records its threads
as seen, so a held pull request stops re-asking until somebody comments again.
A held pull request never returns to the automatic loop on the strength of an
override.

**What this does not cover.** Neither explicit key waits for the review threads
to be read, so a pull request whose threads prutil has not read has no hold to
ask about and both keys act on what is known. Blocking a key press on a round
trip is the worse trade, but it is a difference from the automatic paths rather
than an accident.

## Tier 2: the agent runs inside its vendor's sandbox

**Profiles exist for all three vendors; only Claude has live isolation
evidence.** The mechanism is vendor-neutral, and each kind of agent is one
entry in `internal/sandbox`. [`tier2-handoff.md`](tier2-handoff.md) gives the
live-probe procedure still needed for Copilot and agy.

### What was verified, and how

Everything below was checked against Claude Code 2.1.282 and herdr 0.8.2 on
macOS. A real agent was started with
`herdr agent start … -- --settings <policy>` in a worktree laid out the way
prutil lays them out, and ran probes from inside its sandbox.

| Probe, from inside the sandbox | Result |
| --- | --- |
| Read a file outside the worktree | allowed: reads are broad by default |
| Read a path named in a `Read(...)` deny rule, with `cat` | refused |
| Write outside the worktree, then inside it | refused, then allowed |
| `curl` to example.com, then to api.github.com | blocked by the proxy, then 200 |
| `herdr pane list` | refused: the socket is out of reach |
| `ssh-add -l`, with the socket allowlisted by its real path | reached the agent |
| `gh api user`, with the keychain and trustd lookups allowed | worked |
| `git commit` in a worktree, and `go test` | both worked |
| `git ls-remote` over SSH | failed; see 2b |
| A real handoff: commit, push over HTTPS, reply with gh | all worked; see *End to end* below |

Copilot CLI 1.0.88 and agy 1.2.11 have **no live-agent probe results** yet:

| Probe | Copilot CLI | agy |
| --- | --- | --- |
| CLI accepts sandbox launch switch | `--sandbox` documented in `help sandbox`; empty-prompt invocation parsed it | `--sandbox` listed in help; empty-prompt invocation parsed it |
| Shell denied a protected secret read | not run | not run |
| Agent's own file tool denied a protected secret read | not run; policy documented as best-effort | not run |
| Write outside worktree denied, inside allowed | not run | not run |
| Network blocks unlisted hosts and allows GitHub | not run; sandbox policy exposes no domain allowlist | not run |
| herdr socket inaccessible from sandbox | not run | not run |
| gh API, git commit/test, authenticated dry-run push | not run | not run |

Checked from outside the sandbox:

- `herdr agent list` still showed kind `claude` with a live state.
- `pane process-info` gave argv0 `claude` and the full command line.
- `agent explain` matched the `live_prompt_box` rule.

So a vendor sandbox leaves the agent a host process that herdr can see, which
is the premise of this tier.

### 2a. Launch arguments through herdr (built)

`herdr.Controller.StartAgent` takes the agent's own arguments, and
`Client.StartAgent` sends them after `--`. `Controller.Process` reads a pane's
foreground command line and working directory from `pane process-info`.

The arguments come from the kind's profile. The planned `herdr.agent_args`
setting was not built. A hand-written argument list could undo the sandbox,
and prutil could then no longer say what it had started. A profile keeps the
arguments and the knowledge of what they do in one place.

herdr itself refuses an argument it cannot encode safely for the target shell.
It did so when a path carrying colour escape codes was passed by mistake. That
is a second backstop behind prutil's own.

### 2b. A profile for each vendor

A `sandbox.Profile` has two hooks:

- `Launch` writes whatever policy the arguments refer to, and returns the
  arguments with the posture the vendor says they give.
- `Inspect` asks about an agent that is already running.

A kind with no `Launch` is started as it always was.

**Claude Code (built).** The profile keeps two files beneath the application
directory:

- `sandbox/claude-settings.json` is the reader's policy. prutil writes it once,
  on first use, with mode 0600, and never again. The defaults are in
  `claudeDefaultPolicy`.
- `sandbox/launch/claude-<hash>.json` is written for each start. It is the
  reader's policy plus what only a launch can know. It is named by its contents
  and never rewritten, so a running agent's command line names exactly what it
  read.

Before any agent starts, prutil runs `claude --settings <launch file> sandbox
status` from the directory the agent will run in. That reports whether the file
sandboxes the agent, and whether strictly. prutil acts on Claude's answer
rather than on its own reading of the file.

Building it overturned several parts of the plan:

- **One deny rule, not two.** A `Read(...)` or `Edit(...)` deny rule is merged
  into the sandbox's own read and write lists. It confines shell commands as
  well as the file tools. The "deny every path twice" rule under *Vendor
  compatibility* holds for the other vendors until shown otherwise, but not for
  Claude.
- **No `Bash(herdr *)` rule.** It matches only the command as typed, and
  `bash probe.sh` walked straight past it. The herdr socket is kept out by
  leaving it off `allowUnixSockets`, which the sandbox enforces.
- **`~/.config/gh` stays readable.** gh cannot reply on a thread without it, and
  the token is in the keychain regardless.
- **The SSH socket is not in the reader's file.** On macOS, `SSH_AUTH_SOCK`
  changes every boot and lives under `/var`, which is a link to `/private/var`,
  and the sandbox matches real paths. The launch file lists the socket under
  both names.
- **gh needs two Mach lookups:** `com.apple.SecurityServer` for its token in the
  keychain, and `com.apple.trustd` for Go's TLS verification. Without trustd,
  gh reports its token as invalid.
- **Git goes over HTTPS.** SSH cannot leave Claude's sandbox on macOS. The
  sandbox's SOCKS proxy requires authentication, and the `nc -X 5`
  ProxyCommand the sandbox gives SSH offers none; having socat installed does
  not change which one it uses. So the launch file sends the pull request
  owner's git traffic over HTTPS, with `gh auth git-credential` as the
  credential helper. The rules travel as `GIT_CONFIG_COUNT` entries in the
  session's environment, and the reader's own git configuration is never
  touched.

  The rules are owner-length, for `insteadOf` and `pushInsteadOf` both. git
  takes the longest matching prefix, and a reader who rewrites
  `https://github.com/` to SSH already has a host-level rule as long as any
  that prutil could write.
- **Worktrees and the Go cache were never a problem.** A commit in a worktree
  writes to the main repository's `.git`, and `go test` writes under
  `~/Library/Caches`. The sandbox allowed both.
- **Strict mode protects the policy from the branch.** A `--settings` file with
  `allowUnsandboxedCommands: false` makes Claude ignore the sandbox values in
  the repository's own `.claude` settings, which belong to the branch under
  review.

**GitHub Copilot CLI (profile built, live probes pending).** Launch passes
`--experimental --sandbox` and `--secret-env-vars` for common AWS token
names. GitHub tokens must remain available for `gh` to push and reply;
Tier 3b is needed to take them out of the agent. Inspect checks
`--experimental --sandbox` in argv. Both read the reader's
`$COPILOT_HOME/settings.json` (or `~/.copilot/settings.json`) without writing
it. Containment is reported only when `allowBypass` and `allowLocalNetwork`
are explicitly false and `deniedPaths` names the secret and prutil/herdr
directories. Missing policy is *not* counted as contained. Neither the
vendor's sandbox status nor strictness can be measured directly: `Strict`
describes the no-bypass setting, not live verification. `--deny-tool` cannot
deny a file *read* by path in 1.0.88, so the earlier plan to add those flags
was not implementable. Domain-limited outbound network is likewise not an
OS sandbox setting in this release; the profile cannot claim that guarantee.

**Antigravity CLI (profile built, live probes pending).** Launch passes
`--sandbox`; Inspect checks argv, rejecting
`--dangerously-skip-permissions`. Both read
`~/.gemini/antigravity-cli/settings.json` without changing it. Containment
requires `enableTerminalSandbox: true`, `toolPermission:
"proceed-in-sandbox"`, and limited `read_url(domain)` rules, with no
`unsandboxed` or wildcard URL allow rules. Missing policy is not counted as
contained. `Strict` stays false because a person may approve a bypass.
There is no launch-time settings flag or vendor status command in 1.2.11.

### 2c. Automatic handoffs go only to contained agents (built)

`security.require_sandbox` is on by default. It applies to kinds that have a
profile. For those, `Dispatcher.pick` accepts an agent for an automatic handoff
only when its vendor confirms the agent is contained. prutil reads the agent's
command line from herdr and asks the profile about it, from the agent's own
directory. The answer is cached for a minute.

That replaced the plan to remember which panes prutil had started. A list of
panes would forget prutil's own contained agents on every restart, and then
pass them over; the command line outlives a restart. An agent whose command
line cannot be read is not contained.

An agent that is on the pull request but not contained is not answered with
`fallback: new`. Starting a contained agent beside it would put two agents on
one branch. The watcher reports it instead, naming the agent and `W` as the
override. `W` and `F` can use the agent, because the reader chose it
(`Request.Manual`).

When the requirement is on, provisioning also refuses a launch that Claude says
does not sandbox the agent.

### A shell escape the sandbox does not cover

Claude Code runs an input line beginning with `!` as a shell command without
involving the model. In 2.1.282 that command runs **outside the sandbox**, even
in strict mode. From a sandboxed agent's bash mode, a write outside the
worktree succeeded and herdr's socket answered.

So `herdr.Client.Prompt` refuses any prompt with a line beginning `!`, beside
its refusal of control characters. A sandboxed agent cannot type such a line
itself, because herdr's socket is out of its reach.

### What tier 2 leaves open

To push and to reply on threads, a sandboxed Claude agent still has:

- the gh token, through the keychain lookup;
- network access to GitHub;
- the SSH agent. git no longer uses it for the pull request's owner, and SSH
  cannot leave the sandbox on macOS, but other platforms route SSH differently.

An injected agent inside the sandbox can therefore still push to, or comment
on, anything that token reaches. That includes posting what it has read to a
public issue. Tier 3b removes this. Until then, the zero-code steps below
narrow it.

**End to end, verified.** On a scratch repository, with nothing configured but
`herdr.agent_kind: claude`, prutil found the clone from a herdr pane, created a
worktree, and started Claude with its launch file (`pane process-info` showed
`claude --settings …/sandbox/launch/claude-<hash>.json`). The handoff log
recorded it `sandboxed, strict`. Within a minute the agent had committed the
change the review asked for, pushed it to the pull request's branch over HTTPS
through the sandbox's proxy, and replied on the thread with the agent marker.
A second handoff ten minutes later went to the same agent, recognised as
contained from its command line alone.

## Tier 2b (optional): one wrapper for every agent

macOS ships `sandbox-exec`. It is deprecated, but all three vendors use it
underneath. prutil could launch any agent inside a Seatbelt profile of its own:

```sh
HERDR_AGENT=claude sandbox-exec -f <prutil profile> -D WORKTREE=<path> claude …
```

prutil would send that with `herdr pane run`, then run `herdr agent wait`, in
place of `herdr agent start`. `sandbox-exec` replaces itself with the agent, so
the foreground process keeps the agent's name. `HERDR_AGENT` is set anyway,
because that is what herdr documents for wrappers.

The attraction is that the whole agent process is confined, including the
built-in file tools that no vendor sandbox covers. The costs, each of which
needs checking before this is chosen:

- **No domain filtering.** Seatbelt cannot filter network traffic by domain;
  the vendors put a local proxy in front for that. A wrapper profile can confine
  the filesystem and unix sockets, not domains.
- **Tuning per agent.** Each agent's own state directory, caches and keychain
  access (for its own login) must be allowed.
- **Probably no nesting.** Nested Seatbelt sandboxes usually fail to apply, so
  the wrapper would replace the vendor's shell sandbox rather than sit around
  it.

## Tier 3: separate the reader from the actor

This is the idea behind:

- Simon Willison's Dual LLM pattern;
- Google DeepMind's CaMeL;
- the plan-then-execute and context-minimisation patterns in *Design Patterns
  for Securing LLM Agents against Prompt Injections* (2025).

A model that reads untrusted text is given nothing it can act with. The model
that acts never sees the untrusted text.

A pure form of the pattern does not fit a code-review agent, because the actor
has to understand what change was asked for. What does fit is a narrow,
checked channel between the two.

### 3a. A quarantined reader

1. **prutil fetches the thread bodies itself.** It already fetches two per
   thread. A reader pass needs them all.
2. **A reader process summarises them.** It has no tools, no hooks and no MCP
   servers, and runs in an empty temporary directory. It returns a
   schema-checked list with one entry per thread:
   ```json
   {"thread_id": "...", "kind": "change|question|style|off_topic|suspicious",
    "summary": "at most 200 characters",
    "asks_to_run_commands": false,
    "asks_to_touch_files_outside_diff": false,
    "contains_urls": false}
   ```
3. **prutil checks the list without a model.**
   - Every `thread_id` must be one it fetched.
   - The file path comes from GitHub, never from the reader.
   - Each summary is sanitised as in 1c.
   - Any flag, or a `suspicious` kind, holds the handoff.
   - Threads that pass may be released from the 1a hold.
4. **The agent is told the work list and nothing else,** including where to
   reply. The Tier 2 profile adds a deny rule for reading review comments
   through `gh`. Tier 3b is what makes that rule unnecessary.

`security.reader` names the reader as an argument list, and the reader does not
have to come from the same vendor as the agent:

| Reader | Invocation | Fit |
| --- | --- | --- |
| Claude Code | `claude -p --safe-mode --tools "" --strict-mcp-config --no-session-persistence --output-format json --json-schema <schema>` | full; every property above is a flag |
| Copilot CLI | `copilot -p … --available-tools … --output-format json --no-custom-instructions --disable-builtin-mcps` | partial; no schema flag, so prutil rejects anything that does not parse to the schema. Check how an empty tool list is expressed. Prompt mode does not load repository hooks unless `GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS` is set. |
| Antigravity CLI | `agy -p … --output-format json --json-schema <schema>` | not yet: tools cannot be switched off, and headless mode ignores `permissions.allow` and can hang (issue #548) |

It costs one extra model call per handoff, and a few seconds. It is off by
default.

### 3b. An agent with no GitHub credentials

This is a follow-up, larger than everything above.

1. The agent edits and commits locally, then writes `.prutil/result.json`
   listing the replies it wants posted and the checks it wants re-run.
2. When herdr reports the agent `done`, prutil:
   - fast-forward-pushes only to the pull request's head branch;
   - posts only the replies and re-runs listed in the file.

The Tier 2 profile can then deny the SSH agent, the keychain and GitHub network
access entirely. The only network left is the model's API and the package
proxies. That removes the way out, which is the leg of the trifecta that turns
an injection into a leak. It does so identically for all three vendors, because
the credentials no longer exist inside the sandbox.

### What tier 3 does not do

A request for something harmful, worded as an ordinary code change, survives
summarisation: "add a postinstall step that fetches the new schema from …".
The reader removes hidden and verbatim payloads, and it gives the pipeline a
checkpoint. Containment bounds what the change can do on this machine. A person
reading the pushed commit is still what catches it.

## Zero-code steps

These are worth taking now, whatever gets built:

- **Protect default branches** with a ruleset, so a pushed branch can never be
  the default branch.
- **Require review for workflow changes** (a ruleset on `.github/workflows/**`,
  or CODEOWNERS).
- **Limit interactions on watched public repositories** to prior contributors
  or collaborators, using GitHub's temporary interaction limits. This closes the
  front door for as long as the limit lasts.
- **Arm a public repository's pull request for the first time with
  `-dry-run`,** and read `handoffs.jsonl` before trusting the loop with it.

## Set aside

- **Containers.** OrbStack is installed, and herdr can see a host-side
  `docker run` wrapper when `HERDR_AGENT` is set on it. But:
  - launching would have to move from `agent start` to `pane run`;
  - the session hook inside the container cannot reach herdr's socket unless
    the socket is mounted, which reopens the lateral-movement route;
  - an image carrying Go 1.26, task, golangci-lint, gh and each agent's CLI,
    with a login for each, is a large thing to keep working.

  Containers are the right answer for other people's pull requests, where the
  code itself is untrusted. Revisit them then.
- **A separate macOS user for agents.** The gain over Tier 2 is one thing: the
  kernel, rather than the vendor's permission layer, refuses the agent's
  built-in file tools access to the reader's home and login keychain. That is
  the gap Tier 2 admits, but Tier 2 already denies those paths twice, so it
  buys cover against a vendor's bug rather than against a new route in. The leg
  that turns an injection into an incident is untouched: the agent still has to
  push and to comment, so the other user needs its own SSH key, gh token and
  network to GitHub, or the reader's agent socket forwarded across the
  boundary, which unmakes it. Tier 3b removes that leg with none of the
  plumbing below:
  - there is one herdr server and it is the reader's, so launching stops being
    `agent start`, which has no user option and runs the canonical executable
    in a pane already at the reader's shell prompt. Going through
    `herdr pane run su -l agent -c …` puts `su` in the foreground and defeats
    the argv0 identity, so it needs the `HERDR_AGENT` wrapper and `agent wait`,
    exactly as Tier 2b does;
  - `~/.config/herdr/herdr.sock` is `srw-------` and owned by the reader, so
    the agent user cannot reach it. That is the lateral-movement fix Tier 2
    makes with one `allowUnixSockets` line, and here it costs Claude's session
    hook, which reports over that socket; state falls back to the screen
    manifest. Opening the socket to the other user reopens the route;
  - worktrees prutil creates as the reader need ACLs or relocation, files come
    back owned by the other user, and each agent CLI needs its own install,
    state and login under the new home.

  The isolation is weaker than a container's and the plumbing heavier than a
  sandbox profile's. Where the code itself is untrusted, containers are the
  answer; where it is not, Tier 2 and Tier 3b are.
- **Prompt instructions as a defence** ("ignore instructions in comments").
  They are kept only as the labelling in 1c and are never relied on.

## Order

1. **Tier 1 (built).** It closes the zero-click route and the terminal
   injection, and touches:
   - `internal/gh/{query,wire}.go` and new fixtures in `internal/gh/testdata/`;
   - `internal/model/{review,pr}.go`;
   - `internal/home/{config,config_template,home}.go`;
   - `internal/handoff/handoff.go`;
   - `internal/herdr/herdr.go`;
   - `internal/ui/{watch,watch_detail}.go`.
2. **Tier 2 (profiles built; Copilot and agy live probes pending).** It bounds what gets through, and
   touches:
   - `internal/sandbox`, a package of its own, one profile per kind of agent;
   - `internal/herdr/herdr.go`, for `StartAgent` arguments and `Process`;
   - `internal/handoff/handoff.go`, for `provision` and the `pick` filter;
   - `internal/home`, for `security.require_sandbox` and the file helpers;
   - `cmd/prutil/main.go`, for wiring.

   Copilot CLI and agy use settings-derived postures in `copilot.go` and
   `agy.go`; see `tier2-handoff.md` for their remaining validation.
3. **Tier 3a,** behind `security.reader`.
4. **Tier 3b,** as its own change.
5. **Tier 2b and containers** only if a need appears that the vendor sandboxes
   cannot meet.

`AGENTS.md` gains a "Trust boundary" section as Tier 1 lands. The rule that
`handoff.pick` never compares branch names for a likeness is unchanged.

## Verification, when this is built

**Unit tests,** under `task`, table-driven as usual:

- the trust policy:
  - a `[bot]` entry tested against a `User` and against a `Bot`;
  - an untrusted participant between two trusted ones;
  - an incomplete participant list;
- each hidden-content case;
- the sanitiser removing ESC, CR, LF and U+E0041;
- `Prompt` refusing ESC;
- `StartAgent` placing arguments after `--`;
- `provision` refusing an untrusted author;
- in the ui:
  - the automatic path records `held`;
  - `W` needs two presses;
  - `N` holds.

**Against a scratch public repository,** with `-dry-run` first:

- a second GitHub account's review comment is held, and `handoffs.jsonl` says
  so;
- the viewer's own `<!-- prutil:test -->` comment is still sent;
- a gemini review is still sent;
- resolving the hostile thread releases the hold.

**In each agent prutil starts,** from the agent's own shell. For Claude Code
this was run against a live sandboxed agent; the results are in Tier 2 under
*What was verified, and how*.

- `herdr pane list` fails. Verified for Claude.
- A path the policy denies cannot be read. Verified for Claude, with `cat`.
  `~/.config/gh/hosts.yml` turned out to be one gh needs, so it is no longer
  denied; see 2b.
- `curl https://example.com` is refused. Verified for Claude.
- `go test ./...` and `git commit` succeed. Verified for Claude.
- A push to the pull request's branch succeeds. Verified for Claude, by a real
  handoff; see Tier 2, *End to end, verified*.
- Afterwards, `herdr agent list` and `herdr agent explain` still show the
  agent's kind and state. Verified for Claude.

For Claude, `claude --settings <launch file> sandbox status` reports
`"enabled":true` and `"strictMode":true`. The test
`TestTheRealClaudeAcceptsThePolicyAsStrict` checks that against the installed
binary when `PRUTIL_LIVE_CLAUDE=1` is set, without starting an agent.

Run model-driven probes sparingly. A session that asks an agent to probe its
own sandbox and push to GitHub reads like security testing, and a model's
safeguards flagged two of the four probe sessions here. Bash mode avoids the
model, but runs outside the sandbox, so it cannot stand in for a sandboxed
command.

## Sources

- herdr: [agents](https://raw.githubusercontent.com/herdrdev/herdr/v0.9.0/docs/next/website/src/content/docs/agents.mdx),
  [integrations](https://raw.githubusercontent.com/herdrdev/herdr/v0.9.0/docs/next/website/src/content/docs/integrations.mdx),
  and `herdr --help`, `herdr agent start --help`, `herdr pane process-info` and
  `herdr agent explain` from version 0.8.2.
- Claude Code: `claude --help` and `claude sandbox status` from version 2.1.273.
- GitHub Copilot CLI:
  - [command reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference)
  - [configuring the CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/configure-copilot-cli)
  - [local sandbox settings](https://docs.github.com/en/copilot/how-tos/cloud-and-local-sandboxes/configuring-local-sandbox-settings)
  - [understanding local sandboxing](https://docs.github.com/en/copilot/concepts/agents/copilot-cli/understanding-local-sandboxing)
  - [hooks reference](https://docs.github.com/en/copilot/reference/hooks-reference)
- Antigravity CLI:
  - [sandbox](https://antigravity.google/docs/cli/sandbox/)
  - [settings](https://antigravity.google/docs/cli/settings/)
  - [headless mode](https://antigravity.google/docs/cli/headless/)
  - [issue #548](https://github.com/google-antigravity/antigravity-cli/issues/548)
- Patterns: Simon Willison on the Dual LLM pattern (2023) and the lethal
  trifecta (2025); Google DeepMind, *Defeating Prompt Injections by Design*
  (CaMeL, 2025); Beurer-Kellner et al., *Design Patterns for Securing LLM
  Agents against Prompt Injections* (2025).
