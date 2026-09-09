// Package watch decides when an armed pull request is next worth asking GitHub
// about, and which of the two questions to ask.
//
// The engine holds no clock, opens no connection and starts no goroutine. It
// is a state machine over readings that somebody else took, which is what
// makes a backoff measured in tens of minutes something a test can walk
// through in microseconds.
//
// The shape of the polling is: cheap and often while CI is running, cheap and
// doubling once nothing is in progress, slower still once an agent has been
// given the work, and stopped altogether after a long enough stretch of
// nothing changing. Any change at all puts a pull request back to the top of
// that ladder.
package watch

import (
	"sort"
	"time"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// Tier names why a pull request is polled as often as it is. It exists for the
// benefit of the reader, who should be able to tell a pull request prutil is
// watching closely from one it has given up on.
type Tier int

// The tiers, in the order a pull request usually passes through them.
const (
	// TierActive is a pull request whose checks are still running.
	TierActive Tier = iota
	// TierSettled is one where nothing is in progress.
	TierSettled
	// TierNotified is one whose feedback an agent has been given.
	TierNotified
	// TierDormant is one that has not changed for long enough that prutil has
	// stopped asking. It is still armed, and any manual refresh wakes it.
	TierDormant
)

// String names the tier the way the header should.
func (t Tier) String() string {
	switch t {
	case TierActive:
		return "checks running"
	case TierNotified:
		return "with an agent"
	case TierDormant:
		return "dormant"
	default:
		return "watching"
	}
}

// Engine schedules the polling of every armed pull request.
type Engine struct {
	cfg home.WatchConfig
	prs map[model.Key]*entry
}

// entry is the engine's memory of one armed pull request.
type entry struct {
	// interval is the gap until the next poll, and dueAt when that falls.
	interval time.Duration
	dueAt    time.Time
	// notified means an agent has been given this pull request's feedback, so
	// the slower of the two backoffs applies until the work comes back.
	notified bool
	// dormant means polling has stopped.
	dormant bool
	// sincePrecise counts polls since the last review-thread query, so that a
	// reply inside an existing thread, which moves no counter, is still found
	// eventually.
	sincePrecise int
	// atCap counts consecutive polls at the cap with nothing changing.
	atCap int
	// last is the previous reading, and seen whether there has been one.
	last model.Snapshot
	seen bool
}

// New builds an engine over a configuration whose intervals have already been
// clamped by the home package.
func New(cfg home.WatchConfig) *Engine {
	return &Engine{cfg: cfg, prs: map[model.Key]*entry{}}
}

// Sync reconciles the engine with the set of armed pull requests: anything new
// is scheduled for an immediate poll, and anything no longer armed is
// forgotten along with its backoff.
func (e *Engine) Sync(keys []model.Key, now time.Time) {
	armed := make(map[model.Key]bool, len(keys))
	for _, key := range keys {
		armed[key] = true
		e.arm(key, now)
	}
	for key := range e.prs {
		if !armed[key] {
			delete(e.prs, key)
		}
	}
}

// arm starts watching one pull request, leaving an existing one alone so that
// a reconciliation does not restart a backoff that is part way through.
func (e *Engine) arm(key model.Key, now time.Time) {
	if _, ok := e.prs[key]; ok {
		return
	}
	e.prs[key] = &entry{interval: e.cfg.BaseInterval.Duration(), dueAt: now}
}

// Watching is how many pull requests the engine holds.
func (e *Engine) Watching() int { return len(e.prs) }

// Tier reports why a pull request is polled as it is, and false when it is not
// being watched at all.
func (e *Engine) Tier(key model.Key) (Tier, bool) {
	got, ok := e.prs[key]
	if !ok {
		return TierSettled, false
	}
	switch {
	case got.dormant:
		return TierDormant, true
	case got.seen && got.last.Busy():
		return TierActive, true
	case got.notified:
		return TierNotified, true
	default:
		return TierSettled, true
	}
}

// Due lists the pull requests to poll at now, in GitHub's own order so that a
// batch is built the same way twice.
func (e *Engine) Due(now time.Time) []model.Key {
	var due []model.Key
	for key, got := range e.prs {
		if !got.dormant && !got.dueAt.After(now) {
			due = append(due, key)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].Repo != due[j].Repo {
			return due[i].Repo < due[j].Repo
		}
		return due[i].Number < due[j].Number
	})
	return due
}

// NextDue is when the engine next wants waking, and false when nothing is
// armed or everything armed has gone dormant.
func (e *Engine) NextDue() (time.Time, bool) {
	var next time.Time
	for _, got := range e.prs {
		if got.dormant {
			continue
		}
		if next.IsZero() || got.dueAt.Before(next) {
			next = got.dueAt
		}
	}
	return next, !next.IsZero()
}

// Observe applies a batch of readings and reports which pull requests are
// worth the precise review-thread query. That is any whose reading moved, plus
// any that has gone long enough without one, because a reply inside an
// existing thread changes no counter and would otherwise never be noticed.
func (e *Engine) Observe(snaps []model.Snapshot, now time.Time) []model.Key {
	var precise []model.Key

	for _, snap := range snaps {
		got, ok := e.prs[snap.Key]
		if !ok {
			continue
		}

		moved := !got.seen || snap.Moved(got.last)
		got.last, got.seen = snap, true
		got.sincePrecise++

		e.reschedule(got, snap, moved, now)

		if moved || got.sincePrecise >= e.cfg.ForcePreciseEvery {
			got.sincePrecise = 0
			precise = append(precise, snap.Key)
		}
	}
	return precise
}

// reschedule moves one pull request up or down the ladder after a reading.
func (e *Engine) reschedule(got *entry, snap model.Snapshot, moved bool, now time.Time) {
	base, ceiling := e.cfg.BaseInterval.Duration(), e.cfg.MaxInterval.Duration()
	if got.notified {
		base, ceiling = e.cfg.NotifiedInterval.Duration(), e.cfg.MaxNotifiedInterval.Duration()
	}

	switch {
	case snap.Busy():
		// Checks running is the one state worth watching closely, and the one
		// the reader is most likely to be sitting in front of.
		got.interval = e.cfg.ActiveInterval.Duration()
		got.atCap, got.dormant = 0, false

	case moved:
		got.interval = base
		got.atCap, got.dormant = 0, false

	default:
		got.interval = min(got.interval*2, ceiling)
		if got.interval >= ceiling {
			got.atCap++
			// Long enough at the cap with nothing moving and no checks in
			// flight, and prutil stops asking. The pull request stays armed:
			// a refresh wakes it, and so does anything the reader does.
			got.dormant = got.atCap >= e.cfg.DormantAfter
		}
	}
	got.dueAt = now.Add(got.interval)
}

// Precise records what the review-thread query found. handedOff says the
// feedback went to an agent, which is what moves a pull request onto the
// slower backoff; open is how many threads are still waiting, and none left
// waiting is what moves it back off again.
func (e *Engine) Precise(key model.Key, open int, handedOff bool, now time.Time) {
	got, ok := e.prs[key]
	if !ok {
		return
	}
	if handedOff {
		// An agent will be minutes over this, so there is nothing to learn by
		// asking GitHub at the pace of a pull request nobody has touched.
		got.notified = true
		got.atCap, got.dormant = 0, false
		got.interval = e.cfg.NotifiedInterval.Duration()
		got.dueAt = now.Add(got.interval)
		return
	}
	if open == 0 {
		// Whatever was waiting has been dealt with, so the ordinary cadence
		// applies again. The schedule itself is left where Observe put it, or
		// a pull request nothing ever happens to could never go dormant.
		got.notified = false
	}
}

// Wake brings every armed pull request forward to now and clears any dormancy,
// which is what a manual refresh should do: the reader has asked, so prutil
// stops economising.
func (e *Engine) Wake(now time.Time) {
	for _, got := range e.prs {
		got.dormant = false
		got.atCap = 0
		got.interval = e.cfg.BaseInterval.Duration()
		got.dueAt = now
	}
}

// Forget drops one pull request, which is what disarming does.
func (e *Engine) Forget(key model.Key) { delete(e.prs, key) }

// Defer pushes the next poll of the given pull requests out by d. It is what a
// failed request should do: retrying a refused GraphQL call as fast as the
// loop can go is how a dashboard becomes a problem for somebody else.
func (e *Engine) Defer(keys []model.Key, d time.Duration, now time.Time) {
	for _, key := range keys {
		if got, ok := e.prs[key]; ok {
			got.dueAt = now.Add(d)
		}
	}
}
