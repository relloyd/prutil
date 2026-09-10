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
	"github.com/relloyd/prutil/internal/clipboard"
	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/git"
	"github.com/relloyd/prutil/internal/handoff"
	"github.com/relloyd/prutil/internal/herdr"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/ui"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// concurrency caps how many gh processes run at once while check detail is
// fetched in the background.
const concurrency = 4

// herdrProbeTimeout bounds the one question prutil asks herdr at startup, to
// find out whether a server is there at all.
const herdrProbeTimeout = 5 * time.Second

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
		dryRun      = flag.Bool("dry-run", false, "record what would be sent to a coding agent without sending it")
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

	store, state, cfg, storeErr := openHome(*dryRun)

	uiCfg := ui.Config{
		Client:    client,
		Opener:    browser.New(os.Getenv("BROWSER")),
		Clipboard: clipboard.New(),
		Query:     *query,
		Limit:     *limit,
		Closed: gh.ClosedOptions{
			Query:   *closedQuery,
			PerRepo: *perRepo,
			// The sweep and the open list are both "how much to load", so one
			// flag drives both.
			SweepLimit: *limit,
			RepoLimit:  *repoLimit,
		},
		Version:  version,
		Store:    store,
		StoreErr: storeErr,
		State:    state,
		Home:     cfg,
	}

	// Assigned inside the branch rather than from a two-value call, because a
	// nil *Dispatcher stored in an interface field is not a nil interface, and
	// the app decides whether the feature exists by comparing that field.
	if dispatcher, err := newDispatcher(cfg, store); err != nil {
		uiCfg.HandoffErr = err
	} else {
		uiCfg.Handoff = dispatcher
	}

	if _, err := tea.NewProgram(ui.New(uiCfg)).Run(); err != nil {
		return err
	}
	return nil
}

// openHome loads the application directory: the configuration, and which pull
// requests were left armed for watching. None of it is required, so a failure
// disables the watch keys and is reported through them rather than stopping a
// dashboard that is perfectly useful without it.
func openHome(dryRun bool) (*home.Store, *home.State, home.Config, error) {
	cfg := home.DefaultConfig()
	cfg.Herdr.DryRun = cfg.Herdr.DryRun || dryRun

	store, err := home.Open()
	if err != nil {
		return nil, nil, cfg, err
	}

	loaded, err := store.LoadOrCreateConfig()
	if err != nil {
		return nil, nil, cfg, err
	}
	loaded.Herdr.DryRun = loaded.Herdr.DryRun || dryRun

	state, err := store.LoadState()
	if err != nil {
		return nil, nil, loaded, err
	}
	return store, state, loaded, nil
}

// newDispatcher wires the handoff to a running herdr server. herdr not being
// installed, or its server not running, is an ordinary answer: prutil is a
// dashboard first and a courier second.
func newDispatcher(cfg home.Config, store *home.Store) (*handoff.Dispatcher, error) {
	control, err := herdr.NewExec()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), herdrProbeTimeout)
	defer cancel()
	if err := control.Available(ctx); err != nil {
		return nil, fmt.Errorf("no herdr server is answering: %w", err)
	}

	checkouts, err := git.NewExec()
	if err != nil {
		return nil, err
	}

	return handoff.New(handoff.Options{
		Herdr:  control,
		Git:    checkouts,
		Repos:  git.NewResolver(checkouts, cfg, store),
		Fetch:  checkouts,
		Config: cfg,
		// herdr injects the calling pane into every process it starts, which
		// is how prutil knows never to hand work to the terminal it is itself
		// running in.
		SelfPane: os.Getenv("HERDR_PANE_ID"),
	}), nil
}
