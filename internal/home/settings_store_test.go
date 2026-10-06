package home

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreSaveSettingUpdatesDuration(t *testing.T) {
	dir := t.TempDir()
	store := OpenIn(dir)

	err := store.SaveSetting([]string{"watch", "active_interval"}, "45s", func(c *Config) {
		c.Watch.ActiveInterval = Duration(45 * time.Second)
	})
	require.NoError(t, err)

	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, Duration(45*time.Second), cfg.Watch.ActiveInterval)
}

func TestStoreSaveBlockScalarUpdatesPrompt(t *testing.T) {
	dir := t.TempDir()
	store := OpenIn(dir)

	customPrompt := "Custom prompt line 1\nCustom prompt line 2"
	err := store.SaveBlockScalar([]string{"herdr", "prompt"}, customPrompt, func(c *Config) {
		c.Herdr.Prompt = customPrompt
	})
	require.NoError(t, err)

	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, customPrompt, cfg.Herdr.Prompt)
}

func TestStoreSaveMapEntryAndDeletion(t *testing.T) {
	dir := t.TempDir()
	store := OpenIn(dir)

	err := store.SaveMapEntry([]string{"repos"}, "acme/widget", "~/src/widget", func(c *Config) {
		if c.Repos == nil {
			c.Repos = map[string]string{}
		}
		c.Repos["acme/widget"] = "~/src/widget"
	})
	require.NoError(t, err)

	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "~/src/widget", cfg.Repos["acme/widget"])

	err = store.DeleteMapEntry([]string{"repos"}, "acme/widget", func(c *Config) {
		delete(c.Repos, "acme/widget")
	})
	require.NoError(t, err)

	cfg2, err := store.LoadConfig()
	require.NoError(t, err)
	assert.Empty(t, cfg2.Repos["acme/widget"])
}

func TestStoreSaveSequence(t *testing.T) {
	dir := t.TempDir()
	store := OpenIn(dir)

	roots := []string{"/workspace", "/src"}
	err := store.SaveSequence([]string{"discovery", "roots"}, roots, func(c *Config) {
		c.Discovery.Roots = roots
	})
	require.NoError(t, err)

	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, roots, cfg.Discovery.Roots)
}

// oldIntervals is a configuration written before watch.list_interval
// replaced the two keys in it. The comment is the reader's note on one of
// them, so it goes with it.
const oldIntervals = `watch:
  # mine
  auto_watch_interval: 5m
  base_interval: 3m
notifications:
  interval: 2m
  events:
    approved: true
`

func TestWritingTheListIntervalTakesOutTheKeysItReplaced(t *testing.T) {
	store := OpenIn(t.TempDir())
	require.NoError(t, os.WriteFile(store.Path(ConfigFile), []byte(oldIntervals), 0o600))

	err := store.SaveSetting([]string{"watch", "list_interval"}, "4m", func(c *Config) {
		c.Watch.ListInterval = Duration(4 * time.Minute)
	})
	require.NoError(t, err)

	data, err := os.ReadFile(store.Path(ConfigFile))
	require.NoError(t, err)
	assert.Equal(t, `watch:
  base_interval: 3m
  list_interval: 4m
notifications:
  events:
    approved: true
`, string(data), "the old keys go, and everything else stays as the reader wrote it")
}

func TestResettingTheListIntervalLandsOnTheDefaultNotOnAnOldKey(t *testing.T) {
	store := OpenIn(t.TempDir())
	require.NoError(t, os.WriteFile(store.Path(ConfigFile),
		[]byte("watch:\n  auto_watch_interval: 5m\nnotifications:\n  interval: 3m\n"), 0o600))

	err := store.ResetSetting([]string{"watch", "list_interval"}, func(c *Config) {
		c.Watch.ListInterval = Duration(DefaultListInterval)
	})
	require.NoError(t, err)

	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, Duration(DefaultListInterval), cfg.Watch.ListInterval)
}
