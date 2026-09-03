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
| `-skip-auth-check` | false | skip the `gh auth status` check at startup |
| `-version` | | print the version and exit |

## Keys

| Key | Action |
| --- | --- |
| `j` / `k` or `↓` / `↑` | move within the focused pane |
| `g` / `G` | jump to the first or last item |
| `l` or `→` | focus the checks pane |
| `h`, `←` or `esc` | go back to the list |
| `enter` | open the selected pull request, or the selected check, in your browser |
| `r` | refresh from GitHub |
| `tab` | reserved for the review-requested view |
| `?` | toggle the full key list |
| `q` or `ctrl+c` | quit |

Below 80 columns the two panes collapse into one: the list fills the terminal,
`l` swaps to the checks, and `h` swaps back.

## How it stays quick

The headline list is one GraphQL request, which includes the check rollup that
colours each dot, so the first screen appears after a single round trip. The
individual checks are fetched afterwards, in the background for the top of the
list and on demand as you move, with at most four gh processes at a time. Both
are cached until you press `r`.
