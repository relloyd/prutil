package home_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
)

func TestAMissingRepoCacheIsAnEmptyCache(t *testing.T) {
	cache, err := home.OpenIn(t.TempDir()).LoadRepoCache()
	require.NoError(t, err)
	_, ok := cache.Lookup("acme/widgets")
	assert.False(t, ok)
}

func TestACorruptRepoCacheIsReported(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	require.NoError(t, os.WriteFile(store.Path(home.RepoCacheFile), []byte("{"), 0o600))

	cache, err := store.LoadRepoCache()
	require.Error(t, err)
	_, ok := cache.Lookup("acme/widgets")
	assert.False(t, ok, "a broken cache falls back to empty")
}

func TestRepoCacheSurvivesARoundTrip(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	cache := home.NewRepoCache()
	cache.Set("acme/widgets", "/workspace/widgets")
	cache.Set("acme/other", "/workspace/other")
	require.NoError(t, store.SaveRepoCache(cache))

	read, err := store.LoadRepoCache()
	require.NoError(t, err)
	got, ok := read.Lookup("acme/widgets")
	require.True(t, ok)
	assert.Equal(t, "/workspace/widgets", got.Path)
}

func TestSavingRepoCacheIsAtomicAndLeavesNoTemporaryFiles(t *testing.T) {
	store := home.OpenIn(filepath.Join(t.TempDir(), "store"))
	cache := home.NewRepoCache()
	cache.Set("acme/widgets", "/workspace/widgets")
	require.NoError(t, store.SaveRepoCache(cache))
	require.NoError(t, store.SaveRepoCache(cache))

	entries, err := os.ReadDir(store.Dir())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, home.RepoCacheFile, entries[0].Name())

	info, err := os.Stat(store.Path(home.RepoCacheFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dir, err := os.Stat(store.Dir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
}

func TestMergingRepoCachesPreservesMappingsFromParallelDiscoveries(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	first := home.NewRepoCache()
	first.Set("acme/widgets", "/workspace/widgets")
	second := home.NewRepoCache()
	second.Set("acme/other", "/workspace/other")

	require.NoError(t, store.MergeRepoCache(first))
	require.NoError(t, store.MergeRepoCache(second))

	got, err := store.LoadRepoCache()
	require.NoError(t, err)
	widgets, ok := got.Lookup("acme/widgets")
	require.True(t, ok)
	assert.Equal(t, "/workspace/widgets", widgets.Path)
	other, ok := got.Lookup("acme/other")
	require.True(t, ok)
	assert.Equal(t, "/workspace/other", other.Path)
}
