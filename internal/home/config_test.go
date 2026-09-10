package home_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

func TestTheSelfTestMarkerDefaultsToTheBuiltInOne(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  base_interval: 5m\n"))

	require.NoError(t, err)
	assert.Equal(t, model.DefaultSelfTestMarker, cfg.Watch.Marker(),
		"a configuration that does not mention it keeps the built-in marker")
}

func TestTheSelfTestMarkerCanBeReplacedOrTurnedOff(t *testing.T) {
	// The empty string has to mean off rather than unset, which is why the
	// field is a pointer.
	replaced, err := home.ParseConfig([]byte("watch:\n  self_test_marker: \"<!-- mine -->\"\n"))
	require.NoError(t, err)
	assert.Equal(t, "<!-- mine -->", replaced.Watch.Marker())

	off, err := home.ParseConfig([]byte("watch:\n  self_test_marker: \"\"\n"))
	require.NoError(t, err)
	assert.Empty(t, off.Watch.Marker(), "an explicit empty marker turns the exception off")
}

func TestAZeroWatchConfigStillGetsTheBuiltInMarker(t *testing.T) {
	assert.Equal(t, model.DefaultSelfTestMarker, home.WatchConfig{}.Marker())
}

func TestTheWrittenTemplateNamesTheSelfTestMarker(t *testing.T) {
	assert.Contains(t, string(home.DefaultConfigTemplate()), "self_test_marker:")
}

func TestOneConfigurationCannotChangeAnothersDefaultMarker(t *testing.T) {
	// The YAML decoder writes through a non-nil pointer it finds in the
	// target, so a default pointing at shared memory would let one file
	// rewrite the built-in marker for every configuration parsed after it,
	// and two parsed at once would race over it.
	replaced, err := home.ParseConfig([]byte("watch:\n  self_test_marker: \"<!-- mine -->\"\n"))
	require.NoError(t, err)
	require.Equal(t, "<!-- mine -->", replaced.Watch.Marker())

	after, err := home.ParseConfig([]byte("watch:\n  base_interval: 5m\n"))
	require.NoError(t, err)
	assert.Equal(t, model.DefaultSelfTestMarker, after.Watch.Marker())
	assert.Equal(t, model.DefaultSelfTestMarker, home.DefaultConfig().Watch.Marker())
}
