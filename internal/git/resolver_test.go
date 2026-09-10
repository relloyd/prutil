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
