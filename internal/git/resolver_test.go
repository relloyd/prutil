package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/git"
	"github.com/relloyd/prutil/internal/home"
)

type fakeIdentifier struct {
	checkouts map[string]git.Checkout
	calls     []string
}

func (f *fakeIdentifier) Identify(_ context.Context, dir string) git.Checkout {
	clean := filepath.Clean(dir)
	f.calls = append(f.calls, clean)
	return f.checkouts[clean]
}

func TestResolverPrefersAnExplicitConfigMapping(t *testing.T) {
	base := t.TempDir()
	explicit := filepath.Join(base, "explicit")
	cachePath := filepath.Join(base, "cached")
	discovered := filepath.Join(base, "discovered")
	require.NoError(t, os.MkdirAll(filepath.Join(discovered, ".git"), 0o755))

	store := home.OpenIn(filepath.Join(base, "store"))
	cache := home.NewRepoCache()
	cache.Set("acme/widgets", cachePath)
	require.NoError(t, store.SaveRepoCache(cache))

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		explicit:   {Root: explicit, Repo: "Acme/Widgets", Branch: "explicit"},
		cachePath:  {Root: cachePath, Repo: "acme/widgets", Branch: "cached"},
		discovered: {Root: discovered, Repo: "acme/widgets", Branch: "discovered"},
	}}
	cfg := home.DefaultConfig()
	cfg.Repos["acme/widgets"] = explicit
	cfg.Discovery.Roots = []string{base}

	got, err := git.NewResolver(id, cfg, store).Resolve(context.Background(), "acme/widgets")
	require.NoError(t, err)
	assert.Equal(t, explicit, got.Root)
	assert.Equal(t, []string{explicit}, id.calls, "cache and discovery are skipped once explicit mapping is valid")
}

func TestResolverReportsAnInvalidExplicitConfigMappingClearly(t *testing.T) {
	base := t.TempDir()
	configured := filepath.Join(base, "configured")
	cfg := home.DefaultConfig()
	cfg.Repos["acme/widgets"] = configured

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{}}
	_, err := git.NewResolver(id, cfg, home.OpenIn(filepath.Join(base, "store"))).Resolve(context.Background(), "acme/widgets")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config repos.acme/widgets")
	assert.Contains(t, err.Error(), "not a git checkout")
}

func TestResolverUsesAValidCacheEntryBeforeDiscovery(t *testing.T) {
	base := t.TempDir()
	cached := filepath.Join(base, "cached")
	store := home.OpenIn(filepath.Join(base, "store"))
	cache := home.NewRepoCache()
	cache.Set("acme/widgets", cached)
	require.NoError(t, store.SaveRepoCache(cache))

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		cached: {Root: cached, Repo: "acme/widgets", Branch: "cached"},
	}}
	cfg := home.DefaultConfig()

	got, err := git.NewResolver(id, cfg, store).Resolve(context.Background(), "acme/widgets")
	require.NoError(t, err)
	assert.Equal(t, cached, got.Root)
	assert.Equal(t, []string{cached}, id.calls)
}

func TestResolverDiscardsAStaleCacheEntryAndContinuesDiscovery(t *testing.T) {
	base := t.TempDir()
	store := home.OpenIn(filepath.Join(base, "store"))
	stale := filepath.Join(base, "stale")
	found := filepath.Join(base, "roots", "project")
	require.NoError(t, os.MkdirAll(filepath.Join(found, ".git"), 0o755))

	cache := home.NewRepoCache()
	cache.Set("acme/widgets", stale)
	require.NoError(t, store.SaveRepoCache(cache))

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		stale: {Root: stale, Repo: "acme/other"},
		found: {Root: found, Repo: "acme/widgets", Branch: "main"},
	}}
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{filepath.Join(base, "roots")}

	got, err := git.NewResolver(id, cfg, store).Resolve(context.Background(), "acme/widgets")
	require.NoError(t, err)
	assert.Equal(t, found, got.Root)

	after, err := store.LoadRepoCache()
	require.NoError(t, err)
	entry, ok := after.Lookup("acme/widgets")
	require.True(t, ok)
	assert.Equal(t, found, entry.Path)
}

func TestResolverFindsWorktreeGitFilesWithoutWalkingGitInternals(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "roots")
	worktree := filepath.Join(root, "worktree")
	outer := filepath.Join(root, "outer")
	nested := filepath.Join(outer, ".git", "modules", "inner")

	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(nested, ".git"), 0o755))

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		worktree: {Root: worktree, Repo: "Acme/Widgets"},
		outer:    {Root: outer, Repo: "acme/other"},
		nested:   {Root: nested, Repo: "acme/widgets"},
	}}
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{root}

	got, err := git.NewResolver(id, cfg, home.OpenIn(filepath.Join(base, "store"))).
		Resolve(context.Background(), "acme/widgets")
	require.NoError(t, err)
	assert.Equal(t, worktree, got.Root)
	assert.NotContains(t, id.calls, nested, "the walk must skip .git internals")
}

func TestResolverSkipsDiscoveryWhenNoRootsAreConfigured(t *testing.T) {
	id := &fakeIdentifier{checkouts: map[string]git.Checkout{}}
	cfg := home.DefaultConfig()
	_, err := git.NewResolver(id, cfg, home.OpenIn(t.TempDir())).Resolve(context.Background(), "acme/widgets")
	require.Error(t, err)
	assert.True(t, errors.Is(err, git.ErrCheckoutNotFound))
	assert.Empty(t, id.calls)
}

func TestResolverWorksWithoutAnApplicationDirectory(t *testing.T) {
	// cmd/prutil hands the resolver a nil store whenever the application
	// directory could not be opened. A nil *home.Store inside a non-nil
	// CacheStore interface would walk past any nil check and dereference a nil
	// receiver, so the constructor substitutes a cache that keeps nothing.
	base := t.TempDir()
	discovered := filepath.Join(base, "widgets")
	require.NoError(t, os.MkdirAll(filepath.Join(discovered, ".git"), 0o755))

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		discovered: {Root: discovered, Repo: "acme/widgets", Branch: "main"},
	}}
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{base}

	var store *home.Store
	got, err := git.NewResolver(id, cfg, store).Resolve(context.Background(), "acme/widgets")
	require.NoError(t, err)
	assert.Equal(t, discovered, got.Root, "discovery still runs, the result is simply not remembered")
}

func TestResolverReportsAMissingCheckoutWithoutAnApplicationDirectory(t *testing.T) {
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{t.TempDir()}

	var store *home.Store
	_, err := git.NewResolver(&fakeIdentifier{}, cfg, store).Resolve(context.Background(), "acme/widgets")
	assert.ErrorIs(t, err, git.ErrCheckoutNotFound)
}

// writeCheckout lays down a checkout whose config names origin.
func writeCheckout(t *testing.T, dir, origin string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	config := "[core]\n\trepositoryformatversion = 0\n"
	if origin != "" {
		config += "[remote \"origin\"]\n\turl = " + origin + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(config), 0o600))
}

func TestDiscoveryReadsTheOriginRemoteBeforeAskingGit(t *testing.T) {
	// A directory of checkouts used to cost three forked git processes each.
	// The repository's own config answers the same question with one file
	// read, so only the checkout that matches costs a process at all.
	base := t.TempDir()
	want := filepath.Join(base, "widgets")
	writeCheckout(t, want, "git@github.com:acme/widgets.git")
	for _, name := range []string{"gadgets", "sprockets", "cogs"} {
		writeCheckout(t, filepath.Join(base, name), "https://github.com/acme/"+name+".git")
	}

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		want: {Root: want, Repo: "acme/widgets", Branch: "main"},
	}}
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{base}

	got, err := git.NewResolver(id, cfg, home.OpenIn(t.TempDir())).Resolve(context.Background(), "acme/widgets")

	require.NoError(t, err)
	assert.Equal(t, want, got.Root)
	assert.Equal(t, []string{want}, id.calls, "only the matching checkout was asked about")
}

func TestDiscoveryStillAsksGitAboutACheckoutItCannotRead(t *testing.T) {
	// The config filter is a filter, not an answer. Anything it cannot parse
	// is passed through, so an unusual layout costs a process rather than a
	// checkout nobody finds.
	base := t.TempDir()
	odd := filepath.Join(base, "odd")
	require.NoError(t, os.MkdirAll(filepath.Join(odd, ".git"), 0o755))

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		odd: {Root: odd, Repo: "acme/widgets", Branch: "main"},
	}}
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{base}

	got, err := git.NewResolver(id, cfg, home.OpenIn(t.TempDir())).Resolve(context.Background(), "acme/widgets")

	require.NoError(t, err)
	assert.Equal(t, odd, got.Root)
}

func TestDiscoveryDoesNotWalkForever(t *testing.T) {
	// Somebody pointing at a home directory should not pay for every vendored
	// tree beneath it.
	base := t.TempDir()
	deep := filepath.Join(base, "a", "b", "c", "d", "e", "f", "widgets")
	writeCheckout(t, deep, "https://github.com/acme/widgets.git")

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		deep: {Root: deep, Repo: "acme/widgets", Branch: "main"},
	}}
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{base}

	_, err := git.NewResolver(id, cfg, home.OpenIn(t.TempDir())).Resolve(context.Background(), "acme/widgets")

	assert.ErrorIs(t, err, git.ErrCheckoutNotFound, "past the depth prutil is willing to walk")
	assert.Empty(t, id.calls, "and nothing down there was asked about")
}

func TestDiscoveryFindsACheckoutWithinTheDepthItWalks(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "work", "acme", "widgets")
	writeCheckout(t, nested, "https://github.com/acme/widgets.git")

	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		nested: {Root: nested, Repo: "acme/widgets", Branch: "main"},
	}}
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{base}

	got, err := git.NewResolver(id, cfg, home.OpenIn(t.TempDir())).Resolve(context.Background(), "acme/widgets")

	require.NoError(t, err)
	assert.Equal(t, nested, got.Root)
}

func TestAMappingSavedAfterStartupIsFoundWithoutARestart(t *testing.T) {
	base := t.TempDir()
	clone := filepath.Join(base, "dummy-repo")
	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		clone: {Root: clone, Repo: "relloyd/dummy-repo", Branch: "main"},
	}}
	resolver := git.NewResolver(id, home.DefaultConfig(), home.OpenIn(filepath.Join(base, "store")))

	_, err := resolver.Resolve(context.Background(), "relloyd/dummy-repo")
	require.ErrorIs(t, err, git.ErrCheckoutNotFound, "nothing is mapped at startup")

	cfg := home.DefaultConfig()
	cfg.Repos["relloyd/dummy-repo"] = clone
	resolver.Configure(cfg)
	cfg.Repos["relloyd/dummy-repo"] = "/somewhere/else"

	got, err := resolver.Resolve(context.Background(), "relloyd/dummy-repo")
	require.NoError(t, err, "the mapping the settings pane saved is the one used")
	assert.Equal(t, clone, got.Root, "and the caller editing its own copy afterwards changes nothing")
}

// clones makes directories that look like a clone (a .git directory) and a
// linked worktree (a .git file), which is the distinction candidates rank by.
func clones(t *testing.T, base string) (clone, worktree string) {
	t.Helper()
	clone, worktree = filepath.Join(base, "dummy-repo"), filepath.Join(base, "prutil-dummy-1")
	require.NoError(t, os.MkdirAll(filepath.Join(clone, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+clone+"/.git/worktrees/x\n"), 0o644))
	return clone, worktree
}

func TestAPanesCheckoutIsFoundWhenNothingIsConfiguredAndRemembered(t *testing.T) {
	base := t.TempDir()
	clone, _ := clones(t, base)
	other := filepath.Join(base, "prutil")
	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		clone: {Root: clone, Repo: "relloyd/dummy-repo", Branch: "main"},
		other: {Root: other, Repo: "relloyd/prutil", Branch: "main"},
	}}
	store := home.OpenIn(filepath.Join(base, "store"))

	got, err := git.NewResolver(id, home.DefaultConfig(), store).
		Resolve(context.Background(), "relloyd/dummy-repo", other, "/not/a/repo", clone)

	require.NoError(t, err, "a first-run configuration has no roots, and a shell was sitting in the clone")
	assert.Equal(t, clone, got.Root)
	cache, err := store.LoadRepoCache()
	require.NoError(t, err)
	entry, ok := cache.Lookup("relloyd/dummy-repo")
	require.True(t, ok, "remembered, so it is found again once that pane has gone")
	assert.Equal(t, clone, entry.Path)
}

func TestTheMainCloneIsPreferredToAWorktreeOfIt(t *testing.T) {
	base := t.TempDir()
	clone, worktree := clones(t, base)
	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		clone:    {Root: clone, Repo: "relloyd/dummy-repo", Branch: "main"},
		worktree: {Root: worktree, Repo: "relloyd/dummy-repo", Branch: "prutil/dummy-repo-1"},
	}}

	got, err := git.NewResolver(id, home.DefaultConfig(), home.OpenIn(filepath.Join(base, "store"))).
		Resolve(context.Background(), "relloyd/dummy-repo", worktree, clone)
	require.NoError(t, err)
	assert.Equal(t, clone, got.Root, "a worktree prutil made is a poor base for the next one")

	got, err = git.NewResolver(id, home.DefaultConfig(), home.OpenIn(filepath.Join(base, "store2"))).
		Resolve(context.Background(), "relloyd/dummy-repo", worktree)
	require.NoError(t, err)
	assert.Equal(t, worktree, got.Root, "but a worktree is still taken when it is all there is")
}

func TestAnExplicitMappingStillWinsOverAPane(t *testing.T) {
	base := t.TempDir()
	clone, _ := clones(t, base)
	explicit := filepath.Join(base, "explicit")
	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		clone:    {Root: clone, Repo: "relloyd/dummy-repo"},
		explicit: {Root: explicit, Repo: "relloyd/dummy-repo"},
	}}
	cfg := home.DefaultConfig()
	cfg.Repos["relloyd/dummy-repo"] = explicit

	got, err := git.NewResolver(id, cfg, home.OpenIn(filepath.Join(base, "store"))).
		Resolve(context.Background(), "relloyd/dummy-repo", clone)
	require.NoError(t, err)
	assert.Equal(t, explicit, got.Root)
}

func TestNotFindingACloneSaysWhereItLookedAndWhatToDo(t *testing.T) {
	base := t.TempDir()
	id := &fakeIdentifier{checkouts: map[string]git.Checkout{}}
	store := home.OpenIn(filepath.Join(base, "store"))

	cases := []struct {
		name       string
		roots      []string
		candidates []string
		want       string
	}{
		{
			name:       "a first-run configuration, with herdr's panes looked at",
			candidates: []string{"/Users/p/prutil"},
			want: "no local checkout found for relloyd/dummy-repo: no herdr pane is working in a clone of it " +
				"and no discovery root is configured; add it under Explicit repository paths in the settings pane (s), or open a shell in its clone",
		},
		{
			name:  "roots configured, but none holds it",
			roots: []string{base},
			want: "no local checkout found for relloyd/dummy-repo: none of the discovery roots holds one; " +
				"add it under Explicit repository paths in the settings pane (s), or open a shell in its clone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := home.DefaultConfig()
			cfg.Discovery.Roots = tc.roots

			_, err := git.NewResolver(id, cfg, store).Resolve(context.Background(), "relloyd/dummy-repo", tc.candidates...)

			require.ErrorIs(t, err, git.ErrCheckoutNotFound)
			assert.Equal(t, tc.want, err.Error())
		})
	}
}
