// Package browser opens URLs in the user's web browser. It is a package of its
// own so the TUI can be tested against a recording opener.
package browser

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// Opener opens a URL for the user.
type Opener interface {
	Open(rawURL string) error
}

// commandFunc starts a command. It is a field so tests can intercept it.
type commandFunc func(name string, args ...string) error

// System opens URLs with the platform's handler, honouring $BROWSER when set.
type System struct {
	// GOOS overrides the detected operating system. Empty means runtime.GOOS.
	GOOS string
	// Browser overrides the $BROWSER environment variable.
	Browser string
	// Start runs the command. Empty means start a real process.
	Start commandFunc
}

// New returns a System opener configured for the current platform.
func New(browserEnv string) *System {
	return &System{GOOS: runtime.GOOS, Browser: browserEnv}
}

// Open implements Opener. It refuses anything that is not an absolute http or
// https URL, so that a malformed API response can never turn into an arbitrary
// command argument.
func (s *System) Open(rawURL string) error {
	if err := validate(rawURL); err != nil {
		return err
	}

	name, args := s.command(rawURL)
	start := s.Start
	if start == nil {
		start = func(name string, args ...string) error {
			return exec.Command(name, args...).Start()
		}
	}
	if err := start(name, args...); err != nil {
		return fmt.Errorf("opening %s with %s: %w", rawURL, name, err)
	}
	return nil
}

// command picks the program that should handle the URL.
func (s *System) command(rawURL string) (name string, args []string) {
	if browser := strings.TrimSpace(s.Browser); browser != "" {
		fields := strings.Fields(browser)
		return fields[0], append(fields[1:], rawURL)
	}

	goos := s.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	switch goos {
	case "darwin":
		return "open", []string{rawURL}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		return "xdg-open", []string{rawURL}
	}
}

// validate rejects URLs that are not safe to hand to a browser.
func validate(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return fmt.Errorf("no URL to open")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("cannot open %q: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("refusing to open non-web URL %q", rawURL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("refusing to open URL without a host: %q", rawURL)
	}
	return nil
}
