// Command prutil is a terminal dashboard for the pull requests you have open
// across every GitHub repository your account can see.
//
// It shells out to the gh CLI, so gh must be installed and authenticated:
//
//	gh auth login
//
// A personal access token also works without any extra configuration, because
// gh itself honours GH_TOKEN and GITHUB_TOKEN.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/browser"
	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/ui"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// concurrency caps how many gh processes run at once while check detail is
// fetched in the background.
const concurrency = 4

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "prutil:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		query       = flag.String("query", gh.DefaultSearchQuery, "GitHub search query used to find pull requests")
		limit       = flag.Int("limit", 100, "maximum number of pull requests to load")
		closedQuery = flag.String("closed-query", gh.DefaultClosedSearchQuery, "GitHub search query used by the recently closed view")
		perRepo     = flag.Int("closed-per-repo", gh.DefaultPerRepo, "maximum closed pull requests shown per repository")
		repoLimit   = flag.Int("closed-repo-limit", gh.DefaultRepoLimit, "maximum repositories the recently closed view queries individually")
		showVer     = flag.Bool("version", false, "print the version and exit")
		skipVerify  = flag.Bool("skip-auth-check", false, "do not verify gh authentication before starting")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("prutil", version)
		return nil
	}

	runner, err := gh.NewExecRunner()
	if err != nil {
		if errors.Is(err, gh.ErrNotInstalled) {
			return fmt.Errorf("%w\n\nprutil uses your gh CLI credentials, so gh must be installed and logged in", err)
		}
		return err
	}

	client := gh.New(runner, concurrency)
	if !*skipVerify {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := client.Ping(ctx); err != nil {
			return err
		}
	}

	app := ui.New(ui.Config{
		Client: client,
		Opener: browser.New(os.Getenv("BROWSER")),
		Query:  *query,
		Limit:  *limit,
		Closed: gh.ClosedOptions{
			Query:   *closedQuery,
			PerRepo: *perRepo,
			// The sweep and the open list are both "how much to load", so one
			// flag drives both.
			SweepLimit: *limit,
			RepoLimit:  *repoLimit,
		},
		Version: version,
	})

	if _, err := tea.NewProgram(app).Run(); err != nil {
		return err
	}
	return nil
}
