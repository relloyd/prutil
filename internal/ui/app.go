// Package ui implements the prutil terminal interface: a list of the user's
// open pull requests on the left and the checks for the selected one on the
// right.
package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/browser"
	"github.com/relloyd/prutil/internal/clipboard"
	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
	"github.com/relloyd/prutil/internal/watch"
)

const (
	// narrowWidth is the point below which the two panes stop fitting side by
	// side and the focused pane takes the whole terminal.
	narrowWidth = 80
	// rowHeight is the fixed number of lines a list row occupies, including the
	// blank separator beneath it.
	rowHeight = 6
	// selectionDebounce delays the check fetch for a newly selected pull
	// request so that holding j or k does not start a request per row.
	selectionDebounce = 120 * time.Millisecond
	// prefetchLimit caps how many pull requests have their checks fetched
	// eagerly after the list loads.
	prefetchLimit = 25
	// statusLifetime is how long a transient message stays on screen.
	statusLifetime = 4 * time.Second
	// autoRefreshInterval is the gap between two automatic refreshes.
	autoRefreshInterval = 30 * time.Second
	// autoRefreshBurst is how many automatic refreshes one press of the
	// auto-refresh key buys. Pressing it again adds another burst, so watching
	// a pull request's checks finish is a matter of topping the counter up
	// rather than turning a mode on and remembering to turn it off.
	autoRefreshBurst = 5
	// requestTimeout bounds a single gh invocation.
	requestTimeout = 60 * time.Second
)

// pane identifies which half of the UI has the keyboard.
type pane int

const (
	paneList pane = iota
	paneDetail
)

// detailSection is the section selected in the normal detail pane.
type detailSection int

const (
	detailChecks detailSection = iota
	detailWatch
)

// detailPage is the level currently shown inside the detail pane.
type detailPage int

const (
	detailOverview detailPage = iota
	detailWatchPage
)

// view selects which list of pull requests the list pane shows. A third view
// for review-requested pull requests would slot in before viewCount.
type view int

const (
	viewOpen view = iota
	viewClosed
	viewCount
)

// next cycles to the following view, wrapping at the end.
func (v view) next() view {
	return (v + 1) % viewCount
}

// String names the view the way the header and the empty-list notice read it.
func (v view) String() string {
	if v == viewClosed {
		return "closed"
	}
	return "open"
}

// viewState is everything belonging to one list of pull requests. Each view
// keeps its own cursor and scroll position, so switching back and forth does
// not lose the reader's place.
type viewState struct {
	prs         []model.PullRequest
	cursor      int
	listOffset  int
	loading     bool
	loaded      bool
	err         error
	lastRefresh time.Time
	// unavailable counts repositories the closed view could not reach, so the
	// header can admit the list is incomplete instead of implying it is whole.
	unavailable int
	// enriching reports that the closed view is showing a first-page partial
	// result while FinishClosedPullRequests fills in the rest in the
	// background. Distinct from loading: the rows on screen are already
	// interactive, only the header's "loading more…" note and the spinner say
	// there is more still coming.
	enriching bool
}

// checkState is the cache entry for one pull request's checks.
type checkState struct {
	checks  []model.Check
	err     error
	loading bool
	loaded  bool
}

// handoffHistoryLimit keeps the durable detail history concise.
const handoffHistoryLimit = 3

type handoffHistoryState struct {
	handoffs   []home.Handoff
	err        error
	loading    bool
	loaded     bool
	generation int
}

// Config wires the application to its collaborators.
type Config struct {
	Client gh.Client
	Opener browser.Opener
	// Clipboard copies URLs to the system clipboard. Empty means the platform's
	// own clipboard program, which is what everything but a test wants.
	Clipboard clipboard.Writer
	Query     string
	Limit     int
	// Closed configures the recently-closed view, which loads the first time
	// that view is shown.
	Closed  gh.ClosedOptions
	Now     func() time.Time
	Version string

	// Store remembers which pull requests are watched, between runs. Nil
	// disables arming; StoreErr says why, and the watch key reports it rather
	// than doing nothing.
	Store    *home.Store
	StoreErr error
	// State is what Store held at startup. Nil is an empty state.
	State *home.State
	// Home is the loaded configuration, which supplies the wait budget the
	// handoff is given.
	Home home.Config
	// Handoff sends a pull request's review feedback to a coding agent. Nil
	// disables the handoff key; HandoffErr says why.
	Handoff    dispatcher
	HandoffErr error
}

// App is the root Bubble Tea model.
type App struct {
	client  gh.Client
	opener  browser.Opener
	clip    clipboard.Writer
	query   string
	limit   int
	closed  gh.ClosedOptions
	now     func() time.Time
	version string

	keys   keyMap
	styles Styles
	help   help.Model
	spin   spinner.Model

	views  [viewCount]viewState
	active view

	// checks is shared by every view. It is keyed by repository and number, so
	// a pull request that appears in two views is only ever fetched once.
	checks map[model.Key]checkState

	detailCursor int
	detailOffset int
	detailSection
	detailPage
	watchOffset int

	focus    pane
	width    int
	height   int
	status   string
	showHelp bool

	// gen is bumped on every refresh; replies carrying an older generation are
	// discarded so a slow request cannot overwrite fresher data.
	gen int

	// autoLeft is how many automatic refreshes are still owed. Zero means
	// auto-refresh is off and the view only reloads when the reader asks.
	autoLeft int
	// autoSeq names the run of ticks currently in flight. A tick left over
	// from a spent run carries an older number and is dropped, so a run that
	// has ended cannot restart itself.
	autoSeq int

	// store and state hold which pull requests are armed for watching, and
	// storeErr explains a store that could not be opened.
	store    *home.Store
	state    *home.State
	storeErr error
	// homeCfg is the loaded configuration.
	homeCfg home.Config
	// hand gives a pull request's review feedback to a coding agent, and
	// handErr explains its absence.
	hand    dispatcher
	handErr error
	// handing names the pull requests with a handoff in flight, so that
	// holding the key down cannot send the same work twice.
	handing map[model.Key]bool
	// reviewing names pull requests whose precise review-thread query is in
	// flight, which keeps manual discovery from racing itself or the watcher.
	reviewing map[model.Key]bool
	// engine schedules the polling of every armed pull request.
	engine *watch.Engine
	// watchSeq names the watch schedule currently in flight, the same way
	// autoSeq names a run of auto-refresh ticks.
	watchSeq int
	// feedback is the last known count of review threads still waiting on the
	// reader, per pull request, which is what a watched row shows.
	feedback map[model.Key]int
	// activity holds a bounded, session-only explanation of what the watcher
	// has done for a pull request, while watching names work still in flight.
	activity map[model.Key][]watchActivity
	watching map[model.Key]string
	// handoffHistory is loaded outside rendering, then cached by pull request.
	handoffHistory map[model.Key]handoffHistoryState
}

// New builds an App ready to be handed to tea.NewProgram.
func New(cfg Config) *App {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	clip := cfg.Clipboard
	if clip == nil {
		clip = clipboard.New()
	}
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	styles := newStyles(true)
	sp.Style = styles.Accent

	state := cfg.State
	if state == nil {
		state = home.NewState()
	}
	storeErr := cfg.StoreErr
	if cfg.Store == nil && storeErr == nil {
		storeErr = errors.New("prutil has no application directory")
	}
	handErr := cfg.HandoffErr
	if cfg.Handoff == nil && handErr == nil {
		handErr = errors.New("herdr is not configured")
	}

	a := &App{
		client:         cfg.Client,
		opener:         cfg.Opener,
		clip:           clip,
		query:          cfg.Query,
		limit:          cfg.Limit,
		closed:         cfg.Closed,
		now:            now,
		version:        cfg.Version,
		keys:           defaultKeys(),
		styles:         styles,
		help:           help.New(),
		spin:           sp,
		checks:         map[model.Key]checkState{},
		store:          cfg.Store,
		state:          state,
		storeErr:       storeErr,
		homeCfg:        cfg.Home,
		hand:           cfg.Handoff,
		handErr:        handErr,
		handing:        map[model.Key]bool{},
		reviewing:      map[model.Key]bool{},
		engine:         watch.New(cfg.Home.Watch),
		feedback:       map[model.Key]int{},
		activity:       map[model.Key][]watchActivity{},
		watching:       map[model.Key]string{},
		handoffHistory: map[model.Key]handoffHistoryState{},
	}
	a.views[viewOpen].loading = true
	return a
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tea.Cmd(tea.RequestBackgroundColor),
		a.spin.Tick,
		a.loadOpen(a.gen),
	)
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.help.SetWidth(msg.Width)
		if a.narrow() {
			// The detail pane is only reachable by focusing it once the panes
			// no longer sit side by side, but a resize should never leave the
			// user stranded on an empty pane.
			if len(a.cur().prs) == 0 {
				a.focus = paneList
			}
		}
		a.clampScroll()
		return a, nil

	case tea.BackgroundColorMsg:
		a.styles = newStyles(msg.IsDark())
		a.spin.Style = a.styles.Accent
		return a, nil

	case tea.KeyPressMsg:
		return a.handleKey(msg)

	case tea.MouseClickMsg:
		return a.handleMouse(msg)

	case spinner.TickMsg:
		if !a.busy() {
			return a, nil
		}
		var cmd tea.Cmd
		a.spin, cmd = a.spin.Update(msg)
		return a, cmd

	case prsMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		a.applyPRs(msg.view, msg.prs, msg.unavailable)
		watching := a.watchAfterLoad(msg.view)
		history := a.loadSelectedHandoffHistory()
		if msg.partial {
			// The sweep's first page is on screen and already interactive;
			// the rest is filled in behind it without blocking the reader.
			a.views[msg.view].enriching = true
			return a, tea.Batch(watching, history, a.loadClosedFinish(msg.gen, msg.sweepState))
		}
		a.views[msg.view].enriching = false
		if msg.view != a.active {
			// A background view finished loading; leave the visible one alone
			// and warm its checks only once the reader switches to it.
			return a, tea.Batch(watching, history)
		}
		return a, tea.Batch(watching, history, a.withSpinner(tea.Batch(a.prefetch()...)))

	case errMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		state := &a.views[msg.view]
		state.loading = false
		state.err = msg.err
		return a, nil

	case closedFinishErrMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		// The sweep's partial result is still valid and stays on screen; only
		// the "loading more…" note clears, with a transient explanation.
		a.views[viewClosed].enriching = false
		return a, status("closed list may be incomplete: " + msg.err.Error())

	case checksMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		a.checks[msg.key] = checkState{checks: msg.checks, loaded: true}
		a.clampScroll()
		return a, nil

	case checksErrMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		a.checks[msg.key] = checkState{err: msg.err, loaded: true}
		return a, nil

	case selectionMsg:
		if msg.gen != a.gen || a.selectedKey() != msg.key {
			return a, nil
		}
		return a, tea.Batch(a.withSpinner(a.ensureChecks(msg.key)), a.loadHandoffHistory(msg.key, false))

	case autoRefreshMsg:
		if msg.seq != a.autoSeq || a.autoLeft <= 0 {
			return a, nil
		}
		a.autoLeft--
		cmds := []tea.Cmd{a.refresh(a.autoNote())}
		if a.autoLeft > 0 {
			cmds = append(cmds, a.scheduleAutoRefresh(msg.seq))
		}
		return a, tea.Batch(cmds...)

	case watchTickMsg:
		if msg.seq != a.watchSeq {
			return a, nil
		}
		return a, a.pollWatched()

	case watchSnapshotMsg:
		return a, a.applyWatch(msg)

	case watchReviewMsg:
		return a, a.applyReview(msg)

	case watchErrMsg:
		a.engine.Defer(msg.keys, a.watchRetry(), a.now())
		for _, key := range msg.keys {
			a.setWatchOperation(key, "")
			a.recordWatchActivity(key, "could not check for changes: "+msg.err.Error())
		}
		return a, tea.Batch(a.scheduleWatch(), status("could not poll the watched pull requests: "+msg.err.Error()))

	case handoffMsg:
		history := a.loadHandoffHistory(msg.pr.Key(), true)
		if err := a.applyHandoff(msg); err != nil {
			return a, tea.Batch(status("could not record handoff: "+err.Error()), history)
		}
		return a, tea.Batch(status(a.handoffNote(msg)), history)

	case handoffHistoryMsg:
		state, ok := a.handoffHistory[msg.key]
		if !ok || msg.generation != state.generation {
			return a, nil
		}
		state.handoffs, state.err = msg.handoffs, msg.err
		state.loading, state.loaded = false, true
		a.handoffHistory[msg.key] = state
		// The reply is what decides whether the WATCH section has anything to
		// show, so the detail selection is reconciled against the answer.
		a.clampScroll()
		return a, nil

	case statusMsg:
		a.status = string(msg)
		return a, tea.Tick(statusLifetime, func(time.Time) tea.Msg { return clearStatusMsg{} })

	case clearStatusMsg:
		a.status = ""
		return a, nil
	}

	return a, nil
}

// handleKey applies a key press to whichever pane has focus.
func (a *App) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, a.keys.Quit):
		return a, tea.Quit

	case key.Matches(msg, a.keys.Help):
		a.showHelp = !a.showHelp
		a.help.ShowAll = a.showHelp
		return a, nil

	case key.Matches(msg, a.keys.Refresh):
		return a, a.refresh("refreshing…")

	case key.Matches(msg, a.keys.Auto):
		return a, a.extendAutoRefresh()

	case key.Matches(msg, a.keys.Watch):
		return a, a.toggleWatch()

	case key.Matches(msg, a.keys.Handoff):
		return a, a.handOff()

	case key.Matches(msg, a.keys.Notify):
		return a, a.notifyNewFeedback()

	case key.Matches(msg, a.keys.NextTab):
		return a, a.switchView(a.active.next())

	case key.Matches(msg, a.keys.Into):
		if a.focus == paneList && len(a.cur().prs) > 0 {
			a.focus = paneDetail
			a.resetDetailNavigation()
			return a, a.withSpinner(a.ensureChecks(a.selectedKey()))
		}
		if a.focus == paneDetail && a.detailPage == detailOverview && a.detailSection == detailWatch {
			// Gated on the section still being there. clampScroll normalises a
			// stale selection, but this key can arrive before it has run.
			if pr, ok := a.selectedPR(); ok && a.hasWatchSection(pr) {
				a.detailPage = detailWatchPage
				a.watchOffset = 0
				a.clampScroll()
			}
		}
		return a, nil

	case key.Matches(msg, a.keys.Back):
		if a.focus == paneDetail && a.detailPage == detailWatchPage {
			a.detailPage = detailOverview
			a.watchOffset = 0
			a.clampScroll()
			return a, nil
		}
		a.focus = paneList
		a.resetDetailNavigation()
		return a, nil

	case key.Matches(msg, a.keys.Open):
		return a, a.open()

	case key.Matches(msg, a.keys.Copy):
		return a, a.copyURL()

	case key.Matches(msg, a.keys.Up):
		return a, a.move(-1)

	case key.Matches(msg, a.keys.Down):
		return a, a.move(1)

	case key.Matches(msg, a.keys.Top):
		return a, a.jump(0)

	case key.Matches(msg, a.keys.Bottom):
		return a, a.jump(a.itemCount() - 1)
	}
	return a, nil
}

// handleMouse handles the left click that selects a pull request in the list.
func (a *App) handleMouse(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return a, nil
	}

	index, ok := a.listIndexAt(msg.X, msg.Y)
	if !ok {
		return a, nil
	}

	a.focus = paneList
	a.resetDetailNavigation()
	return a, a.selectList(index)
}

// listIndexAt maps a terminal coordinate to a visible pull-request row.
func (a *App) listIndexAt(x, y int) (int, bool) {
	if a.narrow() && a.focus != paneList {
		return 0, false
	}

	listWidth, _ := a.paneWidths()
	if x < 0 || x >= listWidth {
		return 0, false
	}

	bodyY := y - headerHeight
	if bodyY < 0 {
		return 0, false
	}

	row := bodyY / rowHeight
	if row >= listRows(a.bodyHeight()) {
		return 0, false
	}

	state := a.cur()
	start := min(state.listOffset, max(len(state.prs)-1, 0))
	index := start + row
	if index < 0 || index >= len(state.prs) {
		return 0, false
	}
	return index, true
}

// move steps the cursor of the focused pane by delta.
func (a *App) move(delta int) tea.Cmd {
	return a.jump(a.cursorIndex() + delta)
}

// jump moves the cursor of the focused pane to an absolute index, clamped to
// the bounds of that pane's list.
func (a *App) jump(index int) tea.Cmd {
	count := a.itemCount()
	if count == 0 {
		return nil
	}
	index = min(max(index, 0), count-1)

	if a.focus == paneDetail {
		if a.detailPage == detailWatchPage {
			a.watchOffset = index
			a.clampScroll()
			return nil
		}
		a.setDetailIndex(index)
		a.clampScroll()
		return nil
	}

	return a.selectList(index)
}

// selectList moves the list cursor and schedules the existing debounced check
// fetch for the newly selected pull request.
func (a *App) selectList(index int) tea.Cmd {
	if index == a.cur().cursor {
		return nil
	}
	a.cur().cursor = index
	a.resetDetailNavigation()
	a.clampScroll()

	// Wait before fetching so that a burst of movement costs one request.
	key := a.selectedKey()
	gen := a.gen
	return tea.Tick(selectionDebounce, func(time.Time) tea.Msg {
		return selectionMsg{gen: gen, key: key}
	})
}

// open launches the selected pull request or check in the browser.
func (a *App) open() tea.Cmd {
	target, label, ok := a.selectedTarget()
	if !ok {
		return nil
	}

	opener := a.opener
	return func() tea.Msg {
		if err := opener.Open(target); err != nil {
			return statusMsg("could not open browser: " + err.Error())
		}
		return statusMsg("opened " + label)
	}
}

// copyURL puts the selected pull request's GitHub URL on the system clipboard,
// which is what the reader wants when a pull request is ready to be sent to
// somebody. It follows the focus the same way enter does, so with the checks
// pane in front it copies the link to the selected check instead.
func (a *App) copyURL() tea.Cmd {
	target, label, ok := a.selectedTarget()
	if !ok {
		return nil
	}

	clip := a.clip
	return func() tea.Msg {
		if err := clip.Write(target); err != nil {
			return statusMsg("could not copy: " + err.Error())
		}
		return statusMsg("copied " + label + " · " + target)
	}
}

// selectedTarget is the URL the enter and copy keys act on: the pull request
// under the list cursor, or the check under the detail cursor when that pane
// has focus and the check has a link of its own.
func (a *App) selectedTarget() (target, label string, ok bool) {
	pr, ok := a.selectedPR()
	if !ok {
		return "", "", false
	}

	target, label = pr.URL, pr.Key().String()
	if a.focus == paneDetail && a.detailPage == detailOverview && a.detailSection == detailChecks {
		checks := a.checks[pr.Key()].checks
		if a.detailCursor < len(checks) {
			check := checks[a.detailCursor]
			if check.URL != "" {
				target, label = check.URL, check.Name
			}
		}
	}
	return target, label, true
}

// refresh discards everything and reloads the visible view, announcing itself
// with note. The other views are marked unloaded rather than reloaded now, so
// that a refresh costs one request; they refetch the next time they are shown
// instead of quietly serving data from before the refresh.
func (a *App) refresh(note string) tea.Cmd {
	a.gen++
	a.checks = map[model.Key]checkState{}

	for i := range a.views {
		if view(i) == a.active {
			continue
		}
		// The list and the reader's place in it stay put, so switching over
		// shows something familiar rather than an empty pane. Only the load
		// state is cleared, which is what makes the view refetch when shown.
		a.views[i].loaded = false
		a.views[i].loading = false
		a.views[i].err = nil
		a.views[i].lastRefresh = time.Time{}
	}
	// A refresh is the reader saying they want to know now, so anything the
	// watcher had backed off or given up on is brought forward with it.
	a.engine.Wake(a.now())
	return tea.Batch(a.load(a.active), a.scheduleWatch(), a.spin.Tick, status(note))
}

// extendAutoRefresh grants another burst of automatic refreshes. The first
// press starts the run of ticks; a press while one is already running only
// adds to the counter, so the reader tops up a countdown rather than
// restarting the clock they are already waiting on.
func (a *App) extendAutoRefresh() tea.Cmd {
	running := a.autoLeft > 0
	a.autoLeft += autoRefreshBurst
	if running {
		return status(a.autoNote())
	}
	a.autoSeq++
	return tea.Batch(a.scheduleAutoRefresh(a.autoSeq), status(a.autoNote()))
}

// scheduleAutoRefresh waits one interval and then asks for a refresh. seq ties
// the tick to the run that scheduled it.
func (a *App) scheduleAutoRefresh(seq int) tea.Cmd {
	return tea.Tick(autoRefreshInterval, func(time.Time) tea.Msg {
		return autoRefreshMsg{seq: seq}
	})
}

// autoNote describes how much auto-refresh is left, for the status line.
func (a *App) autoNote() string {
	return fmt.Sprintf("auto-refresh every %s, %d to go",
		model.HumanDuration(autoRefreshInterval), a.autoLeft)
}

// switchView moves the list pane to another view, loading it the first time it
// is shown. Focus returns to the list, because the detail pane belongs to the
// pull request selected in the view being left.
func (a *App) switchView(v view) tea.Cmd {
	if v == a.active {
		return nil
	}
	a.active = v
	a.focus = paneList
	a.resetDetailNavigation()
	a.clampScroll()

	// A view that has never been fetched loads now. One that failed keeps its
	// error on screen instead of retrying on every switch, so the reader can
	// see what went wrong and retry deliberately with r.
	if state := a.cur(); !state.loaded && !state.loading && state.err == nil {
		return tea.Batch(a.load(v), a.spin.Tick)
	}
	return a.withSpinner(tea.Batch(a.prefetch()...))
}

// cur is the state of the view currently on screen.
func (a *App) cur() *viewState {
	return &a.views[a.active]
}

// applyPRs installs a freshly loaded list into one view, keeping the cursor on
// the same pull request when it is still in the list.
func (a *App) applyPRs(v view, prs []model.PullRequest, unavailable int) {
	state := &a.views[v]
	previous, hadSelection := prAt(state.prs, state.cursor)

	state.prs = prs
	state.loading = false
	state.loaded = true
	state.err = nil
	state.unavailable = unavailable
	state.lastRefresh = a.now()

	state.cursor = 0
	if hadSelection {
		for i, pr := range prs {
			if pr.Key() == previous.Key() {
				state.cursor = i
				break
			}
		}
	}
	if len(prs) == 0 && v == a.active {
		a.focus = paneList
	}
	a.clampScroll()
}

// prefetch returns commands that warm the check cache for the top of the list.
func (a *App) prefetch() []tea.Cmd {
	prs := a.cur().prs
	limit := min(len(prs), prefetchLimit)
	cmds := make([]tea.Cmd, 0, limit)

	// The selected pull request goes first so its detail pane fills in before
	// the background rows.
	if cmd := a.ensureChecks(a.selectedKey()); cmd != nil {
		cmds = append(cmds, cmd)
	}
	for _, pr := range prs[:limit] {
		if cmd := a.ensureChecks(pr.Key()); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// watchAfterLoad reconciles the watcher with a freshly loaded open list. It is
// the only place the engine learns about node ids, which is what lets one
// request cover every armed pull request at once.
func (a *App) watchAfterLoad(v view) tea.Cmd {
	if v != viewOpen {
		return nil
	}
	return a.syncWatch()
}

// withSpinner pairs a fetch with a spinner tick, restarting the animation when
// work begins between refreshes. Duplicate ticks are dropped by the spinner
// itself, so batching one in is always safe.
func (a *App) withSpinner(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return tea.Batch(cmd, a.spin.Tick)
}

// ensureChecks returns a command that loads a pull request's checks, or nil
// when they are already cached or in flight.
func (a *App) ensureChecks(prKey model.Key) tea.Cmd {
	if prKey.Repo == "" {
		return nil
	}
	if state, ok := a.checks[prKey]; ok && (state.loaded || state.loading) {
		return nil
	}
	a.checks[prKey] = checkState{loading: true}
	return a.loadChecks(a.gen, prKey)
}

// load returns the command that fills one view and marks it in flight.
func (a *App) load(v view) tea.Cmd {
	a.views[v].loading = true
	a.views[v].enriching = false
	a.views[v].err = nil
	if v == viewClosed {
		return a.loadClosed(a.gen)
	}
	return a.loadOpen(a.gen)
}

// loadOpen fetches the open pull request list.
func (a *App) loadOpen(gen int) tea.Cmd {
	client, query, limit := a.client, a.query, a.limit
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		prs, err := client.ListPullRequests(ctx, query, limit)
		if err != nil {
			return errMsg{gen: gen, view: viewOpen, err: err}
		}
		return prsMsg{gen: gen, view: viewOpen, prs: prs}
	}
}

// loadClosed fetches the first page of the recently closed sweep and returns
// it immediately, so the view has something to show without waiting on the
// discovery and per-repo fill that a large organisation can otherwise turn
// into a ten-second wait. Update dispatches loadClosedFinish behind it
// whenever the reply says the sweep was not already exhausted.
func (a *App) loadClosed(gen int) tea.Cmd {
	client, opts := a.client, a.closed
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		res, state, err := client.SweepClosedPullRequests(ctx, opts)
		if err != nil {
			return errMsg{gen: gen, view: viewClosed, err: err}
		}
		return prsMsg{
			gen:         gen,
			view:        viewClosed,
			prs:         res.PRs,
			unavailable: res.Unavailable,
			partial:     !state.Exhausted(),
			sweepState:  state,
		}
	}
}

// loadClosedFinish resumes the sweep from state in the background: any
// remaining sweep pages, then repository discovery and the per-repo fill,
// exactly as ListClosedPullRequests would have done from the start. Its reply
// replaces the partial list loadClosed already applied.
func (a *App) loadClosedFinish(gen int, state gh.ClosedSweepState) tea.Cmd {
	client, opts := a.client, a.closed
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		res, err := client.FinishClosedPullRequests(ctx, opts, state)
		if err != nil {
			return closedFinishErrMsg{gen: gen, err: err}
		}
		return prsMsg{gen: gen, view: viewClosed, prs: res.PRs, unavailable: res.Unavailable}
	}
}

// loadChecks fetches the checks for one pull request.
func (a *App) loadChecks(gen int, prKey model.Key) tea.Cmd {
	client := a.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		checks, err := client.Checks(ctx, prKey)
		if err != nil {
			return checksErrMsg{gen: gen, key: prKey, err: err}
		}
		return checksMsg{gen: gen, key: prKey, checks: checks}
	}
}

// selectedPR returns the pull request under the list cursor of the visible
// view.
func (a *App) selectedPR() (model.PullRequest, bool) {
	state := a.cur()
	return prAt(state.prs, state.cursor)
}

// prAt returns the pull request at index i, reporting false when the index is
// out of range.
func prAt(prs []model.PullRequest, i int) (model.PullRequest, bool) {
	if i < 0 || i >= len(prs) {
		return model.PullRequest{}, false
	}
	return prs[i], true
}

// selectedKey returns the key of the pull request under the list cursor.
func (a *App) selectedKey() model.Key {
	pr, ok := a.selectedPR()
	if !ok {
		return model.Key{}
	}
	return pr.Key()
}

// selectedChecks returns the cache entry for the selected pull request.
func (a *App) selectedChecks() checkState {
	return a.checks[a.selectedKey()]
}

// cursorIndex is the cursor of the focused pane.
func (a *App) cursorIndex() int {
	if a.focus == paneDetail {
		if a.detailPage == detailWatchPage {
			return a.watchOffset
		}
		return a.detailIndex()
	}
	return a.cur().cursor
}

// itemCount is the number of rows in the focused pane.
func (a *App) itemCount() int {
	if a.focus == paneDetail {
		if a.detailPage == detailWatchPage {
			return a.watchLineCount()
		}
		return a.detailItemCount()
	}
	return len(a.cur().prs)
}

// busy reports whether anything is still loading, which drives the spinner.
func (a *App) busy() bool {
	for i := range a.views {
		if a.views[i].loading || a.views[i].enriching {
			return true
		}
	}
	for _, state := range a.checks {
		if state.loading {
			return true
		}
	}
	// A handoff can spend minutes waiting for an agent to finish what it is
	// doing, which is exactly the stretch the reader needs to be told is not a
	// hang.
	return len(a.reviewing) > 0 || len(a.handing) > 0
}

// narrow reports whether the terminal is too slim for side-by-side panes.
func (a *App) narrow() bool {
	return a.width > 0 && a.width < narrowWidth
}

// clampScroll keeps both scroll offsets consistent with their cursors and the
// current window size.
func (a *App) clampScroll() {
	a.normalizeDetailSelection()

	state := a.cur()
	state.cursor = min(max(state.cursor, 0), max(len(state.prs)-1, 0))
	state.listOffset = clampOffset(state.listOffset, state.cursor, listRows(a.bodyHeight()), len(state.prs))

	if a.focus == paneDetail && a.detailPage == detailWatchPage {
		a.watchOffset = clampOffset(a.watchOffset, a.watchOffset, a.watchWindow(), a.watchLineCount())
		return
	}

	checks := a.selectedChecks().checks
	a.detailCursor = min(max(a.detailCursor, 0), max(len(checks)-1, 0))
	a.detailOffset = clampOffset(a.detailOffset, a.detailCursor, a.checksHeight(), len(checks))
}

// normalizeDetailSelection keeps the detail pane pointing at a section that is
// actually drawn. WATCH comes and goes with the state behind it, and a
// selection left on it once it has gone highlights nothing, because the
// heading it would mark is not rendered, and drills into a page with no lines
// in it.
func (a *App) normalizeDetailSelection() {
	if pr, ok := a.selectedPR(); ok && a.hasWatchSection(pr) {
		return
	}
	if a.detailSection == detailWatch {
		a.detailSection = detailChecks
		a.detailCursor, a.detailOffset = 0, 0
	}
	if a.detailPage == detailWatchPage {
		a.detailPage = detailOverview
		a.watchOffset = 0
	}
}

// resetDetailNavigation returns the detail pane to its normal CHECKS view.
func (a *App) resetDetailNavigation() {
	a.detailCursor = 0
	a.detailOffset = 0
	a.detailSection = detailChecks
	a.detailPage = detailOverview
	a.watchOffset = 0
}

// detailItemCount counts the selectable WATCH section and check rows.
func (a *App) detailItemCount() int {
	count := len(a.selectedChecks().checks)
	if pr, ok := a.selectedPR(); ok && a.hasWatchSection(pr) {
		count++
	}
	return count
}

// detailIndex returns the normal-detail selection as one contiguous index.
func (a *App) detailIndex() int {
	pr, hasPR := a.selectedPR()
	if hasPR && a.detailSection == detailWatch && a.hasWatchSection(pr) {
		return 0
	}
	if hasPR && a.hasWatchSection(pr) {
		return a.detailCursor + 1
	}
	return a.detailCursor
}

// setDetailIndex selects WATCH or one of the check rows from a contiguous
// normal-detail index.
func (a *App) setDetailIndex(index int) {
	pr, hasPR := a.selectedPR()
	hasWatch := hasPR && a.hasWatchSection(pr)
	if hasWatch && index == 0 {
		a.detailSection = detailWatch
		a.detailCursor = 0
		a.detailOffset = 0
		return
	}

	a.detailSection = detailChecks
	if hasWatch {
		index--
	}
	a.detailCursor = max(index, 0)
}

// watchWindow is the number of expanded WATCH lines that fit while retaining
// one line for a position indicator.
func (a *App) watchWindow() int {
	return max(a.bodyHeight()-1, 1)
}

// watchLineCount returns the number of expanded WATCH lines for the selected
// pull request at the current detail width.
func (a *App) watchLineCount() int {
	pr, ok := a.selectedPR()
	if !ok {
		return 0
	}
	_, width := a.paneWidths()
	return len(a.watchPageLines(pr, width))
}

// listRows is how many pull request rows fit in a body of the given height.
// One line is held back for the position indicator renderList draws beneath
// them, so it is never the row that gets clipped.
//
// Every caller that reasons about which rows exist must use this: renderList to
// decide what to draw, clampScroll to keep the cursor among them, and
// listIndexAt to map a click back. They disagreed before, and a cursor the
// scroll arithmetic believed was on screen was not.
func listRows(height int) int {
	return max((height-1)/rowHeight, 1)
}

// clampOffset returns the smallest scroll adjustment that keeps cursor visible
// in a window of the given size.
func clampOffset(offset, cursor, window, count int) int {
	if count == 0 || window <= 0 {
		return 0
	}
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+window {
		offset = cursor - window + 1
	}
	return min(max(offset, 0), max(count-window, 0))
}

// status returns a command that shows a transient message.
func status(text string) tea.Cmd {
	return func() tea.Msg { return statusMsg(text) }
}

// Messages exchanged inside the app.
type (
	prsMsg struct {
		gen         int
		view        view
		prs         []model.PullRequest
		unavailable int
		// partial marks a closed-view reply that is only the sweep's first
		// page. The rows apply immediately; sweepState carries what
		// FinishClosedPullRequests needs to fetch the rest in the background.
		partial    bool
		sweepState gh.ClosedSweepState
	}
	errMsg struct {
		gen  int
		view view
		err  error
	}
	// closedFinishErrMsg reports that the background enrichment stage failed.
	// Unlike errMsg it does not replace the list: the sweep's partial result
	// is still valid and stays on screen, only the header's "loading more…"
	// note clears, and a transient status line explains what happened.
	closedFinishErrMsg struct {
		gen int
		err error
	}
	checksMsg struct {
		gen    int
		key    model.Key
		checks []model.Check
	}
	checksErrMsg struct {
		gen int
		key model.Key
		err error
	}
	selectionMsg struct {
		gen int
		key model.Key
	}
	// autoRefreshMsg is one beat of auto-refresh. seq names the run that
	// scheduled it, so a tick from a run that has already been spent is
	// discarded rather than reviving it.
	autoRefreshMsg struct {
		seq int
	}
	statusMsg      string
	clearStatusMsg struct{}
)
