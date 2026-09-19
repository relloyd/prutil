package home

import (
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
