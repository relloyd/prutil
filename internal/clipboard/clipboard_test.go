package clipboard_test

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/clipboard"
)

// recorder captures the command a writer would have run, and answers which
// programs it should pretend are installed.
type recorder struct {
	input   string
	name    string
	args    []string
	err     error
	present map[string]bool
}

func (r *recorder) run(input, name string, args ...string) error {
	r.input, r.name, r.args = input, name, args
	return r.err
}

func (r *recorder) look(name string) (string, error) {
	if r.present[name] {
		return "/usr/bin/" + name, nil
	}
	return "", exec.ErrNotFound
}

func TestWritePicksThePlatformProgram(t *testing.T) {
	cases := []struct {
		name     string
		goos     string
		present  map[string]bool
		wantName string
		wantArgs []string
	}{
		{
			name:     "macOS copies with pbcopy",
			goos:     "darwin",
			present:  map[string]bool{"pbcopy": true},
			wantName: "pbcopy",
		},
		{
			name:     "Windows copies with clip",
			goos:     "windows",
			present:  map[string]bool{"clip": true},
			wantName: "clip",
		},
		{
			name:     "Wayland is preferred where it is installed",
			goos:     "linux",
			present:  map[string]bool{"wl-copy": true, "xclip": true},
			wantName: "wl-copy",
		},
		{
			name:     "xclip is next, and needs telling which selection",
			goos:     "linux",
			present:  map[string]bool{"xclip": true, "xsel": true},
			wantName: "xclip",
			wantArgs: []string{"-selection", "clipboard"},
		},
		{
			name:     "xsel is the last of the X tools",
			goos:     "linux",
			present:  map[string]bool{"xsel": true},
			wantName: "xsel",
			wantArgs: []string{"--clipboard", "--input"},
		},
		{
			name:     "under WSL the Windows clipboard is still reachable",
			goos:     "linux",
			present:  map[string]bool{"clip.exe": true},
			wantName: "clip.exe",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &recorder{present: c.present}
			w := &clipboard.System{GOOS: c.goos, Run: rec.run, Look: rec.look}

			require.NoError(t, w.Write("https://github.com/relloyd/prutil/pull/42"))
			assert.Equal(t, c.wantName, rec.name)
			assert.Equal(t, c.wantArgs, rec.args)
			assert.Equal(t, "https://github.com/relloyd/prutil/pull/42", rec.input,
				"the text is handed over on standard input")
		})
	}
}

func TestWriteSaysWhatItLookedForWhenNothingIsInstalled(t *testing.T) {
	rec := &recorder{present: map[string]bool{}}
	w := &clipboard.System{GOOS: "linux", Run: rec.run, Look: rec.look}

	err := w.Write("https://example.com/pr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wl-copy")
	assert.Contains(t, err.Error(), "xclip")
	assert.Contains(t, err.Error(), "xsel")
	assert.Empty(t, rec.name, "nothing is executed when there is nothing to execute")
}

func TestWriteWrapsAProgramFailure(t *testing.T) {
	rec := &recorder{present: map[string]bool{"pbcopy": true}, err: errors.New("broken pipe")}
	w := &clipboard.System{GOOS: "darwin", Run: rec.run, Look: rec.look}

	err := w.Write("https://example.com/pr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pbcopy")
	assert.Contains(t, err.Error(), "broken pipe")
}

func TestWriteRefusesEmptyText(t *testing.T) {
	rec := &recorder{present: map[string]bool{"pbcopy": true}}
	w := &clipboard.System{GOOS: "darwin", Run: rec.run, Look: rec.look}

	require.Error(t, w.Write(""))
	assert.Empty(t, rec.name, "an empty clipboard is never worth writing")
}

func TestNewUsesTheRunningPlatform(t *testing.T) {
	w := clipboard.New()
	require.NotNil(t, w)
	assert.NotEmpty(t, w.GOOS)
}
