package home_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
)

func TestApprovalNotificationsAreOnUntilSomebodySaysOtherwise(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  base_interval: 5m\n"))
	require.NoError(t, err)
	assert.True(t, cfg.Notifications.Enabled(home.NotifyApproved))
	assert.True(t, cfg.Notifications.Any())

	assert.True(t, home.NotificationConfig{}.Enabled(home.NotifyApproved),
		"a zero configuration, which the tests build apps on, still has the defaults")
}

func TestANotificationCanBeTurnedOffInTheFile(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("notifications:\n  events:\n    approved: false\n"))
	require.NoError(t, err)
	assert.False(t, cfg.Notifications.Enabled(home.NotifyApproved))
	assert.False(t, cfg.Notifications.Any())
}

func TestAnEmptyNotificationsSectionKeepsTheDefaults(t *testing.T) {
	for _, src := range []string{"notifications:\n", "notifications:\n  events:\n", "notifications: {}\n"} {
		cfg, err := home.ParseConfig([]byte(src))
		require.NoError(t, err, src)
		assert.True(t, cfg.Notifications.Enabled(home.NotifyApproved), src)
		assert.NotNil(t, cfg.Notifications.Events, "clamp leaves a map to write into")
	}
}

func TestTheListIntervalIsClampedToAMinute(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  list_interval: 1s\n"))
	require.NoError(t, err)
	assert.Equal(t, time.Minute, cfg.Watch.ListInterval.Duration())
}

func TestOneConfigurationCannotChangeAnothersDefaultNotifications(t *testing.T) {
	off, err := home.ParseConfig([]byte("notifications:\n  events:\n    approved: false\n"))
	require.NoError(t, err)
	require.False(t, off.Notifications.Enabled(home.NotifyApproved))

	assert.True(t, home.DefaultConfig().Notifications.Enabled(home.NotifyApproved))
	after, err := home.ParseConfig([]byte(""))
	require.NoError(t, err)
	assert.True(t, after.Notifications.Enabled(home.NotifyApproved))
}

func TestEveryNotificationIsInTheWrittenTemplate(t *testing.T) {
	template := string(home.DefaultConfigTemplate())
	assert.Contains(t, template, "notifications:\n")
	for _, event := range home.NotificationEvents() {
		assert.True(t, event.Known())
		assert.Contains(t, template, "\n    "+string(event)+": ")
	}
	assert.False(t, home.NotificationEvent("aproved").Known())
}

func TestSetNotificationChangesOneLineOfTheTemplateAndCanChangeItBack(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	_, err := store.LoadOrCreateConfig()
	require.NoError(t, err)
	original := readConfig(t, store)

	require.NoError(t, store.SetNotification(home.NotifyApproved, false))
	off := readConfig(t, store)
	assert.Equal(t, strings.Replace(original, "    approved: true\n", "    approved: false\n", 1), off)
	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.False(t, cfg.Notifications.Enabled(home.NotifyApproved))

	require.NoError(t, store.SetNotification(home.NotifyApproved, true))
	assert.Equal(t, original, readConfig(t, store), "turning it back on restores the file byte for byte")
}

func TestSetNotificationAddsTheSettingToAHandWrittenFile(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	written := "# my settings\nherdr:\n  skill: triage  # the one I use\n\nrepos:\n  acme/widgets: ~/src/widgets\n"
	writeConfig(t, store, written, 0o600)

	require.NoError(t, store.SetNotification(home.NotifyApproved, false))
	assert.Equal(t, written+"\nnotifications:\n  events:\n    approved: false\n", readConfig(t, store))
}

func TestSetNotificationCreatesTheTemplateWhenTheFileHasGone(t *testing.T) {
	store := home.OpenIn(filepath.Join(t.TempDir(), "prutil"))

	require.NoError(t, store.SetNotification(home.NotifyApproved, false))
	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.False(t, cfg.Notifications.Enabled(home.NotifyApproved))
	assert.Contains(t, readConfig(t, store), "# prutil configuration", "the rest of the template came with it")
}

func TestSetNotificationLeavesAFileThatDoesNotParseAlone(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	broken := "herdr:\n  skill: [unclosed\n"
	writeConfig(t, store, broken, 0o600)

	err := store.SetNotification(home.NotifyApproved, false)
	require.Error(t, err)
	assert.Equal(t, broken, readConfig(t, store))
}

func TestSetNotificationRefusesAnEditItCannotReadBack(t *testing.T) {
	// Only the first document is configuration, so a setting appended at the
	// end of the file would land in the second and change nothing. Reading
	// the result back is what catches it.
	store := home.OpenIn(t.TempDir())
	written := "herdr:\n  skill: triage\n---\nother: true\n"
	writeConfig(t, store, written, 0o600)

	err := store.SetNotification(home.NotifyApproved, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without disturbing the rest of it")
	assert.Contains(t, err.Error(), "set notifications.events.approved to false")
	assert.Contains(t, err.Error(), "by hand")
	assert.Equal(t, written, readConfig(t, store))
}

func TestSetNotificationRefusesAShapeItDoesNotEdit(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	written := "notifications: {events: {approved: true}}\n"
	writeConfig(t, store, written, 0o600)

	err := store.SetNotification(home.NotifyApproved, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "by hand")
	assert.Equal(t, written, readConfig(t, store))
}

func TestSetNotificationRefusesAnEventPrutilDoesNotKnow(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	assert.Error(t, store.SetNotification("aproved", false))
	_, err := os.Stat(store.Path(home.ConfigFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "nothing was written for it")
}

func TestSetNotificationKeepsTheFilesPermissions(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	writeConfig(t, store, "herdr:\n  skill: triage\n", 0o644)

	require.NoError(t, store.SetNotification(home.NotifyApproved, false))
	info, err := os.Stat(store.Path(home.ConfigFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestSetNotificationWritesThroughASymbolicLink(t *testing.T) {
	// A dotfiles manager keeps the real file elsewhere and links it in.
	// Replacing the link with a copy would quietly take the file out of the
	// reader's dotfiles.
	store := home.OpenIn(t.TempDir())
	real := filepath.Join(t.TempDir(), "dotfiles-config.yaml")
	require.NoError(t, os.WriteFile(real, []byte("herdr:\n  skill: triage\n"), 0o600))
	require.NoError(t, os.Symlink(real, store.Path(home.ConfigFile)))

	require.NoError(t, store.SetNotification(home.NotifyApproved, false))

	info, err := os.Lstat(store.Path(home.ConfigFile))
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, info.Mode().Type(), "the link is still a link")
	data, err := os.ReadFile(real)
	require.NoError(t, err)
	assert.Contains(t, string(data), "approved: false")

	entries, err := os.ReadDir(filepath.Dir(real))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file is left beside the real one")
}

func TestSetNotificationPreservesSelfReviewAndOtherCustomWatchFields(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	written := "watch:\n  self_review: true\n  self_test_marker: \"<!-- prutil:test -->\"\n"
	writeConfig(t, store, written, 0o600)

	require.NoError(t, store.SetNotification(home.NotifyApproved, false))
	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.True(t, cfg.Watch.SelfReview)
	assert.Equal(t, "<!-- prutil:test -->", cfg.Watch.Marker())
	assert.False(t, cfg.Notifications.Enabled(home.NotifyApproved))
}

func TestSetWatchSelfReviewCanToggleAndPreservesConfig(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	_, err := store.LoadOrCreateConfig()
	require.NoError(t, err)

	require.NoError(t, store.SetWatchSelfReview(true))
	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.True(t, cfg.Watch.SelfReview)

	require.NoError(t, store.SetWatchSelfReview(false))
	cfg, err = store.LoadConfig()
	require.NoError(t, err)
	assert.False(t, cfg.Watch.SelfReview)
}

func writeConfig(t *testing.T, store *home.Store, text string, perm os.FileMode) {
	t.Helper()
	require.NoError(t, os.WriteFile(store.Path(home.ConfigFile), []byte(text), perm))
	require.NoError(t, os.Chmod(store.Path(home.ConfigFile), perm))
}

func readConfig(t *testing.T, store *home.Store) string {
	t.Helper()
	data, err := os.ReadFile(store.Path(home.ConfigFile))
	require.NoError(t, err)
	return string(data)
}
