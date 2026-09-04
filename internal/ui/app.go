// Package ui implements the prutil terminal interface: a list of the user's
// open pull requests on the left and the checks for the selected one on the
// right.
package ui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/browser"
	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/model"
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
	// requestTimeout bounds a single gh invocation.
	requestTimeout = 60 * time.Second
)

// pane identifies which half of the UI has the keyboard.
type pane int

const (
	paneList pane = iota
	paneDetail
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
}

// checkState is the cache entry for one pull request's checks.
type checkState struct {
	checks  []model.Check
	err     error
	loading bool
	loaded  bool
}

// Config wires the application to its collaborators.
type Config struct {
	Client gh.Client
	Opener browser.Opener
	Query  string
	Limit  int
	// Closed configures the recently-closed view, which loads the first time
	// that view is shown.
	Closed  gh.ClosedOptions
	Now     func() time.Time
	Version string
}

// App is the root Bubble Tea model.
type App struct {
	client  gh.Client
	opener  browser.Opener
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

	focus    pane
	width    int
	height   int
	status   string
	showHelp bool

	// gen is bumped on every refresh; replies carrying an older generation are
	// discarded so a slow request cannot overwrite fresher data.
	gen int
}

// New builds an App ready to be handed to tea.NewProgram.
func New(cfg Config) *App {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	styles := newStyles(true)
	sp.Style = styles.Accent

	a := &App{
		client:  cfg.Client,
		opener:  cfg.Opener,
		query:   cfg.Query,
		limit:   cfg.Limit,
		closed:  cfg.Closed,
		now:     now,
		version: cfg.Version,
		keys:    defaultKeys(),
		styles:  styles,
		help:    help.New(),
		spin:    sp,
		checks:  map[model.Key]checkState{},
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
		a.applyPRs(msg.view, msg.prs)
		if msg.view != a.active {
			// A background view finished loading; leave the visible one alone
			// and warm its checks only once the reader switches to it.
			return a, nil
		}
		return a, a.withSpinner(tea.Batch(a.prefetch()...))

	case errMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		state := &a.views[msg.view]
		state.loading = false
		state.err = msg.err
		return a, nil

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
		return a, a.withSpinner(a.ensureChecks(msg.key))

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
		return a, a.refresh()

	case key.Matches(msg, a.keys.NextTab):
		return a, a.switchView(a.active.next())

	case key.Matches(msg, a.keys.Into):
		if a.focus == paneList && len(a.cur().prs) > 0 {
			a.focus = paneDetail
			a.detailCursor, a.detailOffset = 0, 0
			return a, a.withSpinner(a.ensureChecks(a.selectedKey()))
		}
		return a, nil

	case key.Matches(msg, a.keys.Back):
		a.focus = paneList
		return a, nil

	case key.Matches(msg, a.keys.Open):
		return a, a.open()

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
		a.detailCursor = index
		a.clampScroll()
		return nil
	}

	if index == a.cur().cursor {
		return nil
	}
	a.cur().cursor = index
	a.detailCursor, a.detailOffset = 0, 0
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
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}

	target, label := pr.URL, pr.Key().String()
	if a.focus == paneDetail {
		checks := a.checks[pr.Key()].checks
		if a.detailCursor < len(checks) {
			check := checks[a.detailCursor]
			if check.URL != "" {
				target, label = check.URL, check.Name
			}
		}
	}

	opener := a.opener
	return func() tea.Msg {
		if err := opener.Open(target); err != nil {
			return statusMsg("could not open browser: " + err.Error())
		}
		return statusMsg("opened " + label)
	}
}

// refresh discards everything and reloads the visible view. The other views
// are marked unloaded rather than reloaded now, so that a refresh costs one
// request; they refetch the next time they are shown instead of quietly
// serving data from before the refresh.
func (a *App) refresh() tea.Cmd {
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
	return tea.Batch(a.load(a.active), a.spin.Tick, status("refreshing…"))
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
	a.detailCursor, a.detailOffset = 0, 0
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
func (a *App) applyPRs(v view, prs []model.PullRequest) {
	state := &a.views[v]
	previous, hadSelection := prAt(state.prs, state.cursor)

	state.prs = prs
	state.loading = false
	state.loaded = true
	state.err = nil
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

// loadClosed fetches the recently closed list, which the client groups so that
// no single repository can fill the whole view.
func (a *App) loadClosed(gen int) tea.Cmd {
	client, opts := a.client, a.closed
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		prs, err := client.ListClosedPullRequests(ctx, opts)
		if err != nil {
			return errMsg{gen: gen, view: viewClosed, err: err}
		}
		return prsMsg{gen: gen, view: viewClosed, prs: prs}
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
		return a.detailCursor
	}
	return a.cur().cursor
}

// itemCount is the number of rows in the focused pane.
func (a *App) itemCount() int {
	if a.focus == paneDetail {
		return len(a.selectedChecks().checks)
	}
	return len(a.cur().prs)
}

// busy reports whether anything is still loading, which drives the spinner.
func (a *App) busy() bool {
	for i := range a.views {
		if a.views[i].loading {
			return true
		}
	}
	for _, state := range a.checks {
		if state.loading {
			return true
		}
	}
	return false
}

// narrow reports whether the terminal is too slim for side-by-side panes.
func (a *App) narrow() bool {
	return a.width > 0 && a.width < narrowWidth
}

// clampScroll keeps both scroll offsets consistent with their cursors and the
// current window size.
func (a *App) clampScroll() {
	state := a.cur()
	state.cursor = min(max(state.cursor, 0), max(len(state.prs)-1, 0))
	rows := max(a.bodyHeight()/rowHeight, 1)
	state.listOffset = clampOffset(state.listOffset, state.cursor, rows, len(state.prs))

	checks := a.selectedChecks().checks
	a.detailCursor = min(max(a.detailCursor, 0), max(len(checks)-1, 0))
	a.detailOffset = clampOffset(a.detailOffset, a.detailCursor, a.checksHeight(), len(checks))
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
		gen  int
		view view
		prs  []model.PullRequest
	}
	errMsg struct {
		gen  int
		view view
		err  error
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
	statusMsg      string
	clearStatusMsg struct{}
)
