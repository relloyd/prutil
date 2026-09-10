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
	events := append(a.activity[key], watchActivity{at: a.now(), text: text})
	if len(events) > watchActivityLimit {
		events = events[len(events)-watchActivityLimit:]
	}
	a.activity[key] = events
}

// setWatchOperation records the work currently in flight for a pull request.
func (a *App) setWatchOperation(key model.Key, text string) {
	if text == "" {
		delete(a.watching, key)
		return
	}
	a.watching[key] = text
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

	state := a.handoffHistory[key]
	if !refresh && (state.loading || state.loaded) {
		return nil
	}
	state.generation++
	state.loading, state.loaded, state.err = true, false, nil
	a.handoffHistory[key] = state

	store, generation := a.store, state.generation
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
