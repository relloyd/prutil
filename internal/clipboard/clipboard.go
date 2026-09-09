// Package clipboard puts text on the system clipboard. It is a package of its
// own, like browser, so the TUI can be tested against a recording writer
// instead of a real clipboard.
package clipboard

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Writer copies text to the user's clipboard.
type Writer interface {
	Write(text string) error
}

// tool is one clipboard program and the arguments it wants. Every one of them
// reads what to copy from standard input.
type tool struct {
	name string
	args []string
}

// runFunc runs a command with input on its standard input and waits for it. It
// is a field so tests can intercept it.
type runFunc func(input, name string, args ...string) error

// lookFunc reports whether a program is on PATH. It is a field for the same
// reason.
type lookFunc func(name string) (string, error)

// System copies through whichever clipboard program the platform provides.
type System struct {
	// GOOS overrides the detected operating system. Empty means runtime.GOOS.
	GOOS string
	// Run runs the chosen program. Empty means start a real process.
	Run runFunc
	// Look resolves a program on PATH. Empty means exec.LookPath.
	Look lookFunc
}

// New returns a System configured for the current platform.
func New() *System {
	return &System{GOOS: runtime.GOOS}
}

// Write implements Writer. It hands text to the first clipboard program it can
// find, and says which ones it looked for when there is none, because on a bare
// Linux box the fix is to install one rather than anything to do with prutil.
func (s *System) Write(text string) error {
	if text == "" {
		return fmt.Errorf("nothing to copy")
	}

	look := s.Look
	if look == nil {
		look = exec.LookPath
	}
	run := s.Run
	if run == nil {
		run = runProcess
	}

	tools := s.tools()
	missing := make([]string, 0, len(tools))
	for _, t := range tools {
		if _, err := look(t.name); err != nil {
			missing = append(missing, t.name)
			continue
		}
		if err := run(text, t.name, t.args...); err != nil {
			return fmt.Errorf("copying with %s: %w", t.name, err)
		}
		return nil
	}
	return fmt.Errorf("no clipboard program found (looked for %s)", strings.Join(missing, ", "))
}

// tools lists the candidates for this platform, best first. The Unix list ends
// with clip.exe so that a prutil running under WSL still reaches the Windows
// clipboard when no X or Wayland tool is installed.
func (s *System) tools() []tool {
	goos := s.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	switch goos {
	case "darwin":
		return []tool{{name: "pbcopy"}}
	case "windows":
		return []tool{{name: "clip"}}
	default:
		return []tool{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
			{name: "clip.exe"},
		}
	}
}

// runProcess writes input to the program's standard input and waits for it to
// finish. Unlike opening a browser this cannot be fire and forget: the process
// has to have read the text before prutil can say it was copied.
func runProcess(input, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(input)
	return cmd.Run()
}
