package home_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
)

func TestLoadOrCreateConfigWritesTheDefaultTemplateOnFirstRun(t *testing.T) {
	store := home.OpenIn(t.TempDir())

	got, err := store.LoadOrCreateConfig()
	require.NoError(t, err)
	assert.Equal(t, home.DefaultConfig(), got)

	data, err := os.ReadFile(store.Path(home.ConfigFile))
	require.NoError(t, err)
	assert.Contains(t, string(data), "agent_kind")
	assert.Contains(t, string(data), "repos")
	assert.Contains(t, string(data), "discovery")

	parsed, err := home.ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, home.DefaultConfig(), parsed)
}

func TestLoadOrCreateConfigKeepsStorePrivate(t *testing.T) {
	store := home.OpenIn(filepath.Join(t.TempDir(), "nested", "config"))

	_, err := store.LoadOrCreateConfig()
	require.NoError(t, err)

	dir, err := os.Stat(store.Dir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())

	cfg, err := os.Stat(store.Path(home.ConfigFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), cfg.Mode().Perm())
}

func TestLoadOrCreateConfigNeverOverwritesAnExistingFile(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	want := []byte("herdr:\n  skill: existing-skill\n")
	require.NoError(t, os.WriteFile(store.Path(home.ConfigFile), want, 0o600))

	got, err := store.LoadOrCreateConfig()
	require.NoError(t, err)
	assert.Equal(t, "existing-skill", got.Herdr.Skill)

	data, err := os.ReadFile(store.Path(home.ConfigFile))
	require.NoError(t, err)
	assert.Equal(t, want, data)
}

func TestLoadOrCreateConfigIsSafeUnderConcurrentFirstRun(t *testing.T) {
	store := home.OpenIn(t.TempDir())

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	errs := make(chan error, workers)
	for range workers {
		go func() {
			defer wg.Done()
			_, err := store.LoadOrCreateConfig()
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	data, err := os.ReadFile(store.Path(home.ConfigFile))
	require.NoError(t, err)
	parsed, err := home.ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, home.DefaultConfig(), parsed)
}
