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

- **Tests never touch the network.** `gh.Runner` and `browser.Opener` are the
  seams; fake them. GraphQL fixtures live in `internal/gh/testdata`.
- Use testify's `assert` and `require`, table-driven where the cases are
  uniform, and give each case a sentence-long name.
- All colour lives in `internal/ui/styles.go`. All key bindings live in
  `internal/ui/keys.go` and are surfaced through `keyMap.ShortHelp`, so a new
  binding shows up in the footer automatically.
- List rows are a fixed `rowHeight` lines. Scrolling arithmetic depends on that,
  so pad rather than shrink a row.
- Layout code measures plain text with `ansi.StringWidth` and applies styles
  last, via the `seg` helpers in `internal/ui/format.go`. Never measure a string
  that already carries escape codes with `len`.
- Rendering must fit the terminal at any width. `TestRenderFitsEveryTerminalSize`
  asserts it; keep it passing.
- Below 80 columns (`narrowWidth`) the layout collapses to a single pane.

## Adding the review-requested view

`tab` is already bound and currently reports that the view does not exist. The
intended shape is a second list backed by the same `gh.Client` with the search
query `is:open is:pr review-requested:@me`, a `view` field on `App` selecting
which slice the list pane renders, and the detail pane left untouched.

## Things to avoid

- Reading `~/.config/gh/hosts.yml` or otherwise handling tokens. gh owns auth.
- Blocking the Bubble Tea update loop. Network work belongs in a `tea.Cmd`.
- Widening the gh process pool beyond the `concurrency` constant in
  `cmd/prutil/main.go` without a reason; each unit is a forked process.
