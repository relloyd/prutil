package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// watchActivityLimit keeps diagnostic history useful without turning one long
// running session into an unbounded in-memory log.
const watchActivityLimit = 6

type watchActivity struct {
	at   time.Time
	text string
}

// recordWatchActivity adds one readable watcher event for a pull request.
func (a *App) recordWatchActivity(key model.Key, text string) {
	entry := a.mutate(key)
	entry.activity = append(entry.activity, watchActivity{at: a.now(), text: text})
	if len(entry.activity) > watchActivityLimit {
		entry.activity = entry.activity[len(entry.activity)-watchActivityLimit:]
	}
}

// setWatchOperation records the work currently in flight for a pull request.
// Clearing one that was never set creates nothing, so a screenful of rows the
// watcher has never touched costs no entries.
func (a *App) setWatchOperation(key model.Key, text string) {
	if text == "" {
		if got, ok := a.runtime[key]; ok {
			got.operation = ""
		}
		return
	}
	a.mutate(key).operation = text
}

// loadSelectedHandoffHistory starts a history read for the current list
// selection, when there is one.
func (a *App) loadSelectedHandoffHistory() tea.Cmd {
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}
	return a.loadHandoffHistory(pr.Key(), false)
}

// loadHandoffHistory reads a small durable history outside the render loop.
func (a *App) loadHandoffHistory(key model.Key, refresh bool) tea.Cmd {
	if a.store == nil {
		return nil
	}

	entry := a.mutate(key)
	if !refresh && (entry.history.loading || entry.history.loaded) {
		return nil
	}
	entry.history.generation++
	entry.history.loading, entry.history.loaded, entry.history.err = true, false, nil

	store, generation := a.store, entry.history.generation
	return func() tea.Msg {
		handoffs, err := store.RecentHandoffs(key.String(), handoffHistoryLimit)
		return handoffHistoryMsg{key: key, handoffs: handoffs, err: err, generation: generation}
	}
}

// handoffHistoryMsg carries one asynchronous durable-history read.
type handoffHistoryMsg struct {
	key        model.Key
	handoffs   []home.Handoff
	err        error
	generation int
}
