# Tier 2 handoff: sandboxing Copilot CLI and Antigravity CLI

This was the brief for continuing Tier 2 of
[`prompt-injection.md`](prompt-injection.md) on a machine where GitHub Copilot
CLI (`copilot`) and Antigravity CLI (`agy`) are installed. It was written by the
session that built Tier 2 for Claude Code. The Copilot and agy profiles have
since been implemented using settings checks, but their live sandbox probes
remain outstanding; see [`prompt-injection.md`](prompt-injection.md) for their
current status and limitations.

## The job

prutil now starts Claude Code agents inside Claude's own sandbox. For automatic
handoffs, it gives work only to Claude agents that Claude confirms are
contained. Make it do the same for `copilot` and `agy`.

The design keeps this inside one package. Each kind of agent is a `Profile` in
`internal/sandbox`. The entries for `copilot` and `agy` in `copilot.go` and `agy.go` now have
`Launch` and `Inspect` hooks. The remaining work is live validation of their
isolation and, if the probes expose gaps, a corresponding correction to the
profiles and policies. The original implementation checklist below is kept as
a reference for those probes.

With `security.require_sandbox` on, missing vendor policies now prevent
automatic handoffs for both kinds until the reader configures them.

## Read first

1. `AGENTS.md`: the repository's conventions, and the sections *The trust
   boundary* and *Sandboxes*.
2. `docs/security/prompt-injection.md`, the Tier 2 section: what was built and
   what it found, then the vendor plans for Copilot and agy. Those plans came
   from documentation and have not been verified.
3. `internal/sandbox/claude.go`: the complete profile, as a worked example.
4. `~/prutil-tier2-discovery.md`, if Part A of
   [`tier2-discovery.md`](tier2-discovery.md) has been run on this machine. It
   records the installed versions, their help text, which flags and settings
   keys each build actually contains, and the shape of each CLI's settings.
   Start from it rather than from the vendor documentation.

## How Tier 2 works now

### Two paths through the dispatcher

**Starting an agent.** This is `Dispatcher.provision`, reached by `W` or by
`herdr.fallback: new`:

1. `openWorktree` gives a herdr pane in a new worktree.
2. `contain` checks `sandbox.Supports(kind)`. If the kind is supported, it reads
   the pane's working directory with `herdr.Controller.Process`, then calls
   `sandbox.Launch(kind, Target{Dir, Host, Owner})`.
3. If the returned posture is not `Contained` and `security.require_sandbox` is
   on, it refuses with `ErrUncontained`. No agent starts.
4. Otherwise `StartAgent(…, launch.Args, …)` runs. herdr puts those arguments
   after `--`.
5. `Result.Sandbox` records the posture. It reaches `handoffs.jsonl` and the
   WATCH history, for example `claude w3:p1 · sandboxed, strict`.

**Choosing an agent.** This is `Dispatcher.pick`:

1. An agent is checked only if it would otherwise be chosen, and only when the
   handoff is automatic (`!Request.Manual`), the requirement is on, and
   `sandbox.Supports(agent.Kind)`.
2. The check reads the agent's command line with `Process(pane).Argv`, then
   calls `sandbox.Inspect(kind, agent.Dir(), argv)`. Answers are cached for a
   minute.
3. An agent that is not contained goes into an `uncontained` list. `Dispatch`
   then reports it, naming `W` as the override. It deliberately does not
   provision a new agent beside it, because that would put two agents on one
   branch.
4. Manual handoffs (`W`, `F`) skip the check but still inspect the chosen
   agent, so the log says what the work went to.

### Files

| Path | What it holds |
| --- | --- |
| `internal/sandbox/sandbox.go` | `Profile`, `Posture`, `Target`, `Launch`, `Env`, `Runner`; the registry `Sandbox` with `Supports`, `Launch` and a cached `Inspect` |
| `internal/sandbox/claude.go` | Claude's profile: default policy, per-launch rendering, content-addressed launch files, `claude sandbox status` parsing, the git-over-HTTPS rules |
| `internal/sandbox/copilot.go`, `agy.go` | the entries to fill in |
| `internal/sandbox/sandbox_test.go` | fakes and tests, including one that runs real git and one opt-in live test |
| `internal/herdr/herdr.go` | `StartAgent(…, args, …)`, which sends the args after `--`; `Process(pane)`, which reads argv and cwd from `pane process-info`; `Prompt`, which refuses control characters and lines beginning with `!` |
| `internal/handoff/handoff.go` | the `Container` interface, `contain`, `posture`, the `pick` filter, `Request.Manual`, `ErrUncontained`, `Result.Sandbox` |
| `internal/home/files.go` | `CreateOnce`, for a file the reader then owns, and `WriteFile`, an atomic write with mode 0600 |
| `internal/home/config.go` | `security.require_sandbox`, default true |
| `cmd/prutil/main.go` | `newSandbox`, which builds the registry over `<prutil home>/sandbox` |

### The contract

```go
type Profile struct {
	Kind    string // herdr agent kind: "copilot", "agy"
	Bin     string // CLI looked up on PATH when a hook needs it
	Launch  func(ctx context.Context, env Env, cli Runner, t Target) (Launch, error)
	Inspect func(ctx context.Context, env Env, cli Runner, dir string, argv []string) (Posture, error)
}

type Launch struct {
	Args    []string // the agent's own command-line arguments
	Posture Posture  // what those arguments give it, established before it starts
}

type Posture struct {
	Known     bool   // something was established at all
	Contained bool   // the sandbox is on
	Strict    bool   // the agent cannot run anything outside it, even with approval
	Detail    string // one phrase, shown to the reader and in the log
}

type Target struct{ Dir, Host, Owner string } // where it will run; the pull request's GitHub host and owner
type Env struct{ Dir, Home, SSHAuthSock string; RealPath func(string) (string, error) }
```

What each piece must do:

- **`Launch` returns the arguments that start the agent contained.** Any
  policy file those arguments point at is written beneath `env.Dir`, which is
  prutil's own `sandbox` directory.
  - Never rewrite the reader's own vendor configuration. If the vendor has no
    launch-time policy, read the reader's settings to establish the posture,
    and leave them alone.
  - Use `home.CreateOnce` for a default the reader then owns.
  - For anything that changes between launches, use a content-addressed file,
    like `writeLaunchFile` does.
  - Return an error only when nothing could be established. The dispatcher
    reports an error as a failed handoff.
- **`Inspect` decides from the running agent's `argv`, and from its `dir` where
  that matters.** It runs on every automatic handoff to that kind; the registry
  caches it for a minute. If the vendor cannot report its state, decide from
  the argv flags prutil knows, and say so in `Detail`. Leaving `Inspect` nil
  means such an agent is never counted as contained, so with the requirement on
  the watcher would never hand it work.
- **`Posture` is the answer; an error means no answer.** A posture that is not
  `Known` is treated as not contained.

## The Claude profile, as a worked example

It keeps two files beneath `env.Dir`:

- `claude-settings.json` is the reader's policy, written once by
  `claudeDefaultPolicy` and then theirs to edit.
- `launch/claude-<hash>.json` is written for each launch, by `renderClaude`:
  - the reader's policy;
  - `SSH_AUTH_SOCK` under both of its names, since the socket moves every boot
    and `/var` is a link to `/private/var`;
  - the pull request's host, added to the network allowlist;
  - owner-length git rules that send that owner's repositories over HTTPS, with
    `gh auth git-credential` as the helper (`addGitOverHTTPS`).

  It is named by its contents and never rewritten, so an agent's command line
  names exactly what it read.

Verification uses the vendor's own answer. `Launch` runs
`claude --settings <launch file> sandbox status` from `t.Dir`. `Inspect` runs
the same command with whatever `--settings` the agent's argv names, from the
agent's directory. Both parse the one JSON line it prints (`statusVersion` 3:
`enabled`, `strictMode`, `enabledSource`, `supported`). prutil never
interprets the policy itself.

## What the Claude build found, and what to check for each vendor

Every finding came from a real agent started with
`herdr agent start … -- --settings <policy>` in a scratch repository and
worktree, running probes from inside its sandbox. The findings are Claude Code
2.1.282 on macOS. Check each one for each vendor before you rely on it.

| Finding for Claude | What to check for Copilot and agy |
| --- | --- |
| Reads are allowed almost everywhere by default, so secret paths must be denied explicitly | Can the sandboxed shell read `~/.aws`, `~/.gnupg` and a canary file in `~`? |
| A `Read(...)` deny rule reaches shell commands as well as the file tool | The documentation says Copilot's file tools are not sandboxed at all, so you will likely need `--deny-tool` rules *and* sandbox read denies. Probe both with the shell and with the agent's own file tool |
| herdr's socket is refused, because the sandbox blocks unix sockets unless they are listed | `herdr pane list` from the sandboxed shell must fail. This is the lateral-movement fix, and the most important check |
| `SSH_AUTH_SOCK` must be listed by its real `/private/var/…` path | Only if the vendor needs the SSH agent at all |
| gh needs Mach lookups for `com.apple.SecurityServer` (the keychain) and `com.apple.trustd` (Go's TLS) | Does `gh api user` work inside the sandbox? A report that the "token in keyring is invalid" means trustd, or the keychain, is blocked |
| SSH cannot leave the sandbox: its SOCKS proxy requires authentication, and the `nc -X 5` ProxyCommand offers none | Does `git ls-remote git@github.com:<owner>/<repo>.git` work inside it? If it does, you do not need the HTTPS rewrite. If it does not, reuse `addGitOverHTTPS`, and move it out of `claude.go` into a shared file |
| A `Bash(...)` deny rule matches only what is typed; a script walks past it | Do not rely on command-pattern rules as a boundary, for any vendor |
| **A line beginning `!` runs as a shell command outside the sandbox**, even in strict mode | Does the vendor's shell mode run inside its sandbox? `herdr.Client.Prompt` already refuses such lines for every vendor. Record what you find |
| `git commit` in a worktree and `go test` both work | Same checks |
| Workspace trust: Claude stops at "Is this a project you … trust?" in a new directory, and herdr reports it blocked | Copilot asks whether to trust a folder too. This happens with or without the sandbox, but it will block a probe agent, so be ready to answer it |
| herdr refuses an argument it cannot encode safely for the shell | Keep arguments plain |

## A plan for each CLI

These are starting points taken from the vendor documentation, and nothing more.
Correct them against the discovery output.

### GitHub Copilot CLI (`copilot`)

- **Launch arguments:** `--sandbox`, preceded by `--experimental` if the
  installed build still needs it (search its help text). Add a `--deny-tool`
  rule for each protected path, because Copilot's own file tools run outside
  its sandbox. Add `--secret-env-vars` for any token in the environment. Check
  every flag's exact syntax in `copilot --help`.
- **Policy:** there is no launch-time policy file. The sandbox policy lives
  under `sandbox` in `~/.copilot/settings.json`, or under `$COPILOT_HOME`.
  `Launch` should read that file and establish the posture from it (bypass off,
  network limited) without writing it. Pointing `COPILOT_HOME` at a directory
  prutil owns would move Copilot's whole state, sign-in included, so rule it
  out unless the discovery shows authentication lives somewhere else.
- **Posture:** nothing documented reports whether a session is sandboxed. So
  `Contained` means "started with `--sandbox`, and the settings don't disable
  it", and `Strict` means "sandbox bypass is off in the settings". Put that
  reasoning in `Detail`.
- **Inspect:** decide from argv: is `--sandbox` present? Add the settings read
  for `Strict`.

### Antigravity CLI (`agy`)

- **Launch arguments:** `--sandbox`. Check whether `agy` accepts a settings
  file or JSON at launch. If it does, write a launch policy the way the Claude
  profile does.
- **Policy:** otherwise, read `~/.gemini/antigravity-cli/settings.json`:
  `enableTerminalSandbox`, `toolPermission` (`"proceed-in-sandbox"`), the
  domains under `read_url`, and the absence of `unsandboxed` allow rules.
  Establish the posture from it, and don't write it.
- **Inspect:** as for Copilot, from argv plus the settings.

## Verifying on this machine

1. **Unit tests first.** Follow `internal/sandbox/sandbox_test.go`:
   - a fake `Runner` records the directory and arguments of each call;
   - `newSandbox` builds a registry over a temporary prutil home.

   For each vendor, test the arguments, the posture from each kind of
   settings, a refusal when the policy does not sandbox, and `Inspect` from
   argv. The dispatcher side needs no new tests unless you change it:
   `internal/handoff` already tests `contain` and the `pick` filter through a
   fake `Container`.
2. **A check that needs no agent, if the vendor has one.** Claude has
   `sandbox status`. If Copilot or agy has anything like it, add an opt-in live
   test beside `TestTheRealClaudeAcceptsThePolicyAsStrict`.
3. **A live probe through herdr.** Build a scratch layout, then drive a real
   agent from outside:

   ```sh
   T=/private/tmp/prutil-t2 && mkdir -p "$T/main" && cd "$T/main"
   git init -q -b main
   printf 'module probe\n\ngo 1.26\n' > go.mod
   printf 'package probe\n\nimport "testing"\n\nfunc TestProbe(t *testing.T) {}\n' > probe_test.go
   git add . && git -c user.email=probe@example.invalid -c user.name=probe commit -q -m init
   git worktree add -q ../wt -b probe
   printf 'canary\n' > ~/prutil-canary.txt   # deny this path in the policy under test
   ```

   Save this as `$T/wt/probe.sh`:

   ```bash
   #!/bin/bash
   # Run by the agent, inside its sandbox. One line per probe; never prints a secret.
   r() { id=$1; shift; out=$("$@" 2>&1); rc=$?; printf '%-4s exit=%-3s %s\n' "$id" "$rc" "$(printf '%s' "$out" | tr '\n' ' ' | cut -c1-170)"; }
   {
   r P1  cat "$HOME/prutil-canary.txt"                     # denied path: must fail
   r P2  sh -c 'echo x > "$HOME/prutil-outside.txt"'       # write outside the worktree: must fail
   r P3  sh -c 'echo x > ./inside.txt'                     # write inside: must work
   r P4  curl -sS -m 10 -o /dev/null -w '%{http_code}' https://example.com     # must be blocked
   r P5  curl -sS -m 10 -o /dev/null -w '%{http_code}' https://api.github.com  # 200
   r P6  sh -c 'herdr pane list >/dev/null'                # must fail
   r P7  sh -c 'ssh-add -l >/dev/null'                     # 0 or 1 reachable, 2 not
   r P8  gh api user --jq .type                            # User
   r G1  sh -c 'git ls-remote git@github.com:OWNER/REPO.git HEAD | cut -c1-12'
   r G2  git -c user.email=probe@example.invalid -c user.name=probe commit --allow-empty -q -m probe
   r G3  go test ./...
   r G4  git push --dry-run git@github.com:OWNER/REPO.git HEAD:refs/heads/prutil-tier2-dry-run-probe
   } > probe-results.txt 2>&1
   ```

   Then start the agent with the arguments your `Launch` returns, and have it
   run the probe:

   ```sh
   NEW=$(herdr tab create --cwd "$T/wt" --label probe --no-focus)
   P=$(echo "$NEW" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["root_pane"]["pane_id"])')
   herdr agent start probe --kind copilot --pane "$P" --timeout 60000 -- --sandbox
   herdr agent list; herdr pane process-info --pane "$P"; herdr agent explain probe   # kind, state, argv
   herdr agent prompt probe 'Please run `bash probe.sh` once with your Bash tool, then stop.' --wait --timeout 180000
   cat "$T/wt/probe-results.txt"
   herdr tab close <tab id from $NEW>
   ```

   Then check the agent's own file tool separately: ask it to read
   `~/prutil-canary.txt` with its file tool, not the shell.

   Pitfalls hit while doing this for Claude:
   - **Don't use the agent's `!` shell mode as sandbox evidence.** For Claude
     it runs outside the sandbox, so every probe "passes". Include P6 so that
     you would notice.
   - **Keep the probe prompt plain, and send it once.** A session that probes
     its own sandbox and pushes to GitHub reads like security testing. A
     model's safeguards stopped two of the four Claude probe sessions. Running
     one script keeps the prompt short.
   - **Pick files with a glob (`set -- dir/*.json`), not `ls`,** if your `ls`
     is a colourising alias. The colour codes end up in the argument, and herdr
     refuses it.
   - **Answer the trust prompt** in a new folder with
     `herdr pane send-keys <pane> down enter`.
   - **`G4` is `--dry-run`.** It authenticates and negotiates, and creates
     nothing. Check afterwards with
     `git ls-remote <repo> refs/heads/prutil-tier2-dry-run-probe`.
4. **End to end.** Press `W` on a test pull request in a repository you own.
   Confirm the agent pushed, and that the WATCH history names the posture.

## Done means

- `copilot` and `agy` have `Launch` and `Inspect`, and `Supports` is true for
  both. If one vendor turns out not to be containable in a way herdr can still
  see, leave its entry nil and record why in `prompt-injection.md`.
- Unit tests cover each profile, and `task` is green.
- `prompt-injection.md`:
  - its Tier 2 section gains a verified-probes table for each vendor;
  - the Copilot and agy columns of the vendor table are corrected from what was
    installed;
  - the vendor plans in 2b are replaced by what was built.
- `README.md`'s *Sandboxed agents* section, and the first rule under
  `AGENTS.md`'s *Sandboxes*, no longer say that Copilot and agy are
  unsandboxed.
- The package comment in `internal/sandbox/sandbox.go`, and the comments in
  `copilot.go` and `agy.go`, describe what now exists.

## Still open from the Claude build

- **An authenticated push through Claude's sandbox proxy is unverified.** The
  rewrite and the helper were shown to work, and so was a dry-run push, but
  only from bash mode, which runs outside the sandbox. The first real `W` on
  the machine this was built on settles it.
- **`herdr.agent_args` was deliberately not built.** A hand-written argument
  list could undo the sandbox. If one is ever needed, let it add to a profile's
  arguments, and verify the result, rather than replace them.
- **Hand-started Claude agents count as contained with the sandbox on, strict
  or not.** Escaping a non-strict sandbox needs a person to approve it, and
  prutil never answers dialogs. Requiring strict mode would pass over most
  hand-started agents. Revisit if that trade looks wrong.
- **Tier 3b** takes the gh token and GitHub access out of the sandbox
  altogether, by having prutil do the pushing and the replying. It is the
  real fix for *What tier 2 leaves open*.

## Conventions

- `task` runs fmt, vet, lint and the tests, and must be green before a commit.
- Tests use testify, are table-driven where the cases are uniform, and have
  sentence-long names. They never touch the network.
- Commits are conventional (`feat(sandbox): …`), with a prose body explaining
  why, in the voice of the existing history (`git log`).
- Comments and documents use British spelling and explain why rather than
  what.
