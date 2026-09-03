package ui

import (
	"bytes"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/stretchr/testify/assert"
)

// TestProgramRunsEndToEnd drives the real Bubble Tea program against a fake
// GitHub client: it must load the list, show the checks, and quit on q.
func TestProgramRunsEndToEnd(t *testing.T) {
	client := newFakeClient(samplePRs(), sampleChecks())
	opener := &fakeOpener{}
	app := New(Config{
		Client: client,
		Opener: opener,
		Now:    func() time.Time { return testNow },
	})

	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		screen := ansi.Strip(string(out))
		return bytes.Contains([]byte(screen), []byte("relloyd/prutil")) &&
			bytes.Contains([]byte(screen), []byte("CHECKS (3)"))
	}, teatest.WithDuration(5*time.Second))

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	final, ok := tm.FinalModel(t).(*App)
	if assert.True(t, ok, "the final model is the app") {
		assert.Len(t, final.prs, 3)
		assert.Positive(t, client.listCalls, "the program fetched the list itself")
	}
}
