package browser_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/browser"
)

// recorder captures the command an opener would have run.
type recorder struct {
	name string
	args []string
	err  error
}

func (r *recorder) start(name string, args ...string) error {
	r.name, r.args = name, args
	return r.err
}

func TestOpenPicksThePlatformHandler(t *testing.T) {
	cases := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{"darwin", "open", []string{"https://github.com/relloyd/prutil/pull/1"}},
		{"linux", "xdg-open", []string{"https://github.com/relloyd/prutil/pull/1"}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", "https://github.com/relloyd/prutil/pull/1"}},
	}
	for _, c := range cases {
		t.Run(c.goos, func(t *testing.T) {
			rec := &recorder{}
			opener := &browser.System{GOOS: c.goos, Start: rec.start}

			require.NoError(t, opener.Open("https://github.com/relloyd/prutil/pull/1"))
			assert.Equal(t, c.wantName, rec.name)
			assert.Equal(t, c.wantArgs, rec.args)
		})
	}
}

func TestOpenPrefersTheBrowserEnvironmentVariable(t *testing.T) {
	rec := &recorder{}
	opener := &browser.System{GOOS: "linux", Browser: "firefox --new-tab", Start: rec.start}

	require.NoError(t, opener.Open("https://example.com/pr"))
	assert.Equal(t, "firefox", rec.name)
	assert.Equal(t, []string{"--new-tab", "https://example.com/pr"}, rec.args)
}

func TestOpenRejectsNonWebURLs(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"file scheme":   "file:///etc/passwd",
		"shell attempt": "; rm -rf /",
		"no host":       "https://",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			opener := &browser.System{GOOS: "linux", Start: rec.start}

			require.Error(t, opener.Open(raw))
			assert.Empty(t, rec.name, "nothing is executed for a rejected URL")
		})
	}
}

func TestOpenWrapsStartFailures(t *testing.T) {
	rec := &recorder{err: errors.New("no such file")}
	opener := &browser.System{GOOS: "linux", Start: rec.start}

	err := opener.Open("https://example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "xdg-open")
	assert.Contains(t, err.Error(), "no such file")
}

func TestNewUsesTheRunningPlatform(t *testing.T) {
	opener := browser.New("")
	require.NotNil(t, opener)
	assert.NotEmpty(t, opener.GOOS)
}
