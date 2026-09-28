package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/model"
)

const (
	// adoptBodyMax is the most lines the pane gives what sits beneath its
	// input. The pane is sized from it rather than from what it is showing, so
	// that it does not jump about as the reader moves between stages.
	adoptBodyMax = 14
	// adoptPullLimit caps how many of a repository's open pull requests one
	// browse reads. It is one document either way; past fifty, the filter or a
	// number typed is the quicker way to the one wanted.
	adoptPullLimit = 50
)

// adoptStage is where the reader is in the adopt pane. Each enter moves one
// stage on and each esc one back, and pasting a pull request's URL goes
// straight to the last.
type adoptStage int

const (
	// adoptRepos offers the repositories the reader is likely to want: the
	// ones they have used here before, and the ones their lists hold.
	adoptRepos adoptStage = iota
	// adoptPulls lists one repository's open pull requests by other people.
	adoptPulls
	// adoptPreview shows one pull request, for the reader to adopt or not.
	adoptPreview
)

// adoptPane is the + prompt: a pull request somebody else opened, found by
// picking its repository and then it, or named outright by URL or
// owner/repo#number, then looked up, shown, and adopted on a further enter.
//
// Looking it up before adopting is the point. Adopting trusts the author on the
// pull request, and the reader should see whose it is before agreeing to that,
// not after.
type adoptPane struct {
	open  bool
	keys  adoptKeyMap
	input textinput.Model
	stage adoptStage
	// seq names the lookup in flight and pullSeq the browse, so that an answer
	// the reader has since moved away from is dropped rather than shown. They
	// are apart because a lookup can start while a browse is still reading,
	// and going back to that browse must still find it.
	seq     int
	pullSeq int

	// repos are the repositories offered, gathered once when the pane opens.
	repos []repoChoice
	// repo is the repository being browsed, pulls what is open in it, and
	// loading and pullsErr where reading them has got to.
	repo     string
	pulls    []model.PullRequest
	loading  bool
	pullsErr string

	// choices is what the list shows in this stage, filtered by the input and
	// ranked; cursor and offset are where the reader is in it.
	choices []adoptChoice
	cursor  int
	offset  int

	// looking is the pull request being looked up, found what the lookup
	// returned when it may be adopted, and problem why the last attempt came to
	// nothing. back and backInput are the stage and the text esc returns to.
	looking   model.Key
	found     *gh.Lookup
	problem   string
	back      adoptStage
	backInput string
}

// repoChoice is one repository the pane offers, and why it is offered.
type repoChoice struct {
	repo string
	note string
}

// adoptChoice is one row of the pane's list: a repository in the first stage,
// a pull request in the second.
type adoptChoice struct {
	label string
	// search is what the filter matches against. It is the label and, for a
	// pull request, its author, so that typing a name finds their work.
	search string
	// hits are the byte offsets of the label characters the filter matched.
	hits []int
	note string
	repo string
	key  model.Key
}

// adoptKeyMap holds the keys the adopt pane reads for itself. Everything else
// goes to its input, which is why it is apart from keyMap: a name is typed,
// and q, w, j and the rest have to reach it.
type adoptKeyMap struct {
	Close  key.Binding
	Submit key.Binding
	Up     key.Binding
	Down   key.Binding
	Quit   key.Binding
}

func defaultAdoptKeys() adoptKeyMap {
	return adoptKeyMap{
		Close: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back"),
		),
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "choose"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "ctrl+p"),
			key.WithHelp("↑", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "ctrl+n"),
			key.WithHelp("↓", "down"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
}

// adoptLookupMsg carries the answer to a lookup the adopt pane asked for.
type adoptLookupMsg struct {
	seq    int
	key    model.Key
	lookup gh.Lookup
	err    error
}

// adoptPullsMsg carries one repository's open pull requests, for the adopt
// pane to list.
type adoptPullsMsg struct {
	seq  int
	repo string
	prs  []model.PullRequest
	err  error
}

// openAdopt shows the adopt pane on its list of repositories, with an empty
// input that already has the keyboard, so a pasted URL lands in it.
func (a *App) openAdopt() tea.Cmd {
	if a.store == nil {
		return status("cannot remember what is adopted: " + a.storeErr.Error())
	}
	input := textinput.New()
	input.Prompt = "› "
	input.SetStyles(a.styles.helpInput())
	a.adopt = adoptPane{
		open: true, keys: defaultAdoptKeys(), input: input,
		seq: a.adopt.seq, pullSeq: a.adopt.pullSeq, repos: a.repoChoices(),
	}
	a.enterAdoptStage(adoptRepos, "")
	return a.adopt.input.Focus()
}

// closeAdopt puts the pane away. The sequence number survives it, so a request
// still in flight cannot land in a pane opened afterwards.
func (a *App) closeAdopt() {
	a.adopt = adoptPane{seq: a.adopt.seq + 1, pullSeq: a.adopt.pullSeq + 1}
}

// enterAdoptStage moves the pane to a stage with text in its input, and
// forgets whatever the stage being left had in flight.
func (a *App) enterAdoptStage(stage adoptStage, text string) {
	p := &a.adopt
	p.seq++
	p.stage = stage
	p.found, p.problem, p.looking = nil, "", model.Key{}
	switch stage {
	case adoptRepos:
		p.pullSeq++
		p.repo, p.pulls, p.loading, p.pullsErr = "", nil, false, ""
		p.input.Placeholder = "owner/repo, or paste a pull request's URL"
	case adoptPulls:
		p.input.Placeholder = "filter by title or author, or type a number"
	}
	p.input.SetValue(text)
	p.input.CursorEnd()
	a.resizeAdopt()
	a.refilterAdopt()
}

// resizeAdopt fits the input to a new terminal size.
func (a *App) resizeAdopt() {
	if !a.adopt.open {
		return
	}
	a.adopt.input.SetWidth(max(a.adoptLayout().inner-lenOf(a.adopt.input.Prompt)-1, 1))
	a.clampAdoptScroll()
}

// updateAdopt handles the messages the pane takes for itself while it is open,
// and reports whether it took this one.
func (a *App) updateAdopt(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return a.handleAdoptKey(msg), true
	case tea.PasteMsg:
		return a.editAdopt(msg), true
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			a.moveAdopt(-wheelStep)
		case tea.MouseWheelDown:
			a.moveAdopt(wheelStep)
		}
		return nil, true
	case tea.MouseClickMsg:
		return nil, true
	}
	return nil, false
}

// handleAdoptKey applies a key press to the open pane.
func (a *App) handleAdoptKey(msg tea.KeyPressMsg) tea.Cmd {
	keys := a.adopt.keys
	switch {
	case key.Matches(msg, keys.Quit):
		return tea.Quit
	case key.Matches(msg, keys.Close):
		a.backAdopt()
		return nil
	case key.Matches(msg, keys.Up):
		a.moveAdopt(-1)
		return nil
	case key.Matches(msg, keys.Down):
		a.moveAdopt(1)
		return nil
	case key.Matches(msg, keys.Submit):
		return a.submitAdopt()
	}
	return a.editAdopt(msg)
}

// backAdopt is esc: one stage back, and out of the pane from the first.
func (a *App) backAdopt() {
	p := &a.adopt
	switch p.stage {
	case adoptPreview:
		back, text := p.back, p.backInput
		if back == adoptPulls {
			// The pull requests are still the ones on screen before the
			// lookup, so going back to them costs no request.
			p.seq++
			p.stage = adoptPulls
			p.found, p.problem, p.looking = nil, "", model.Key{}
			p.input.Placeholder = "filter by title or author, or type a number"
			p.input.SetValue(text)
			p.input.CursorEnd()
			a.refilterAdopt()
			return
		}
		a.enterAdoptStage(adoptRepos, text)
	case adoptPulls:
		a.enterAdoptStage(adoptRepos, "")
	default:
		a.closeAdopt()
	}
}

// editAdopt hands a message to the input. Changing what is typed forgets what
// the last lookup found, so that enter never adopts a pull request other than
// the one the input names, and editing a reference being previewed goes back
// to the list it came from, filtered by the new text.
func (a *App) editAdopt(msg tea.Msg) tea.Cmd {
	p := &a.adopt
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() == before {
		return cmd
	}
	p.found, p.problem = nil, ""
	if p.stage == adoptPreview {
		p.seq++
		p.looking = model.Key{}
		p.stage = p.back
	}
	a.refilterAdopt()
	return cmd
}

// submitAdopt is enter, whose meaning follows the stage: browse the chosen
// repository, look up the chosen pull request, or adopt the one previewed. A
// full reference typed or pasted anywhere goes straight to its preview.
func (a *App) submitAdopt() tea.Cmd {
	p := &a.adopt
	text := strings.TrimSpace(p.input.Value())
	switch p.stage {
	case adoptPreview:
		ref, err := model.ParseReference(text)
		if err != nil {
			p.problem = err.Error()
			return nil
		}
		if p.found != nil && p.found.PR.Key() == ref {
			return a.adoptFound()
		}
		if p.looking == ref {
			return nil
		}
		return a.lookUpAdopt(ref, p.back, p.backInput)

	case adoptPulls:
		if ref, ok := a.pullReference(text); ok {
			return a.lookUpAdopt(ref, adoptPulls, text)
		}
		if choice, ok := a.adoptSelection(); ok {
			return a.lookUpAdopt(choice.key, adoptPulls, text)
		}
		if !p.loading {
			p.problem = "nothing matches · type a pull request's number to look it up"
		}
		return nil

	default:
		if ref, err := model.ParseReference(text); err == nil {
			return a.lookUpAdopt(ref, adoptRepos, text)
		}
		if choice, ok := a.adoptSelection(); ok {
			return a.browseRepo(choice.repo)
		}
		p.problem = "type owner/repo to browse it, or paste a pull request's URL"
		return nil
	}
}

// pullReference reads what was typed while browsing a repository as a pull
// request: a whole reference, or a number, which is taken to be in the
// repository on screen. A number reaches pull requests the list did not fetch.
func (a *App) pullReference(text string) (model.Key, bool) {
	if ref, err := model.ParseReference(text); err == nil {
		return ref, true
	}
	n, err := strconv.Atoi(strings.TrimPrefix(text, "#"))
	if err != nil || n < 1 {
		return model.Key{}, false
	}
	return model.Key{Repo: a.adopt.repo, Number: n}, true
}

// lookUpAdopt asks GitHub about one pull request and shows the preview while
// it does. back and backInput are what esc returns to.
func (a *App) lookUpAdopt(ref model.Key, back adoptStage, backInput string) tea.Cmd {
	p := &a.adopt
	p.seq++
	p.stage, p.back, p.backInput = adoptPreview, back, backInput
	p.found, p.problem, p.looking = nil, "", ref
	p.choices = nil
	p.input.SetValue(ref.String())
	p.input.CursorEnd()

	seq, client := p.seq, a.client
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		lookup, err := client.LookupPullRequest(ctx, ref)
		return adoptLookupMsg{seq: seq, key: ref, lookup: lookup, err: err}
	}, a.spin.Tick)
}

// applyAdoptLookup shows what a lookup found, or why the pull request cannot
// be adopted.
func (a *App) applyAdoptLookup(msg adoptLookupMsg) {
	p := &a.adopt
	if !p.open || p.stage != adoptPreview || msg.seq != p.seq {
		return
	}
	p.looking = model.Key{}
	if msg.lookup.Viewer != "" {
		a.viewer = msg.lookup.Viewer
	}
	if err := a.adoptable(msg.key, msg.lookup, msg.err); err != nil {
		p.problem = err.Error()
		return
	}
	lookup := msg.lookup
	p.found = &lookup
}

// browseRepo lists a repository's open pull requests by other people.
func (a *App) browseRepo(repo string) tea.Cmd {
	a.enterAdoptStage(adoptPulls, "")
	p := &a.adopt
	p.pullSeq++
	p.repo, p.loading = repo, true

	seq, client := p.pullSeq, a.client
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		prs, err := client.ListPullRequests(ctx, gh.OthersInRepoQuery(repo), adoptPullLimit)
		return adoptPullsMsg{seq: seq, repo: repo, prs: prs, err: err}
	}, a.spin.Tick)
}

// applyAdoptPulls lists what a browse found. A repository that answered is
// remembered as a recent one, which is what puts it at the top of the list
// the next time; one that did not is not, so a typo is not remembered.
func (a *App) applyAdoptPulls(msg adoptPullsMsg) {
	p := &a.adopt
	if !p.open || msg.seq != p.pullSeq || msg.repo != p.repo {
		return
	}
	p.loading = false
	if msg.err != nil {
		p.pullsErr = msg.err.Error()
		return
	}
	p.pulls = msg.prs
	a.state.TouchRepo(msg.repo, a.now())
	// Losing a recent repository costs a few keystrokes next time, which is
	// not worth interrupting the reader over.
	_ = a.saveState()
	a.refilterAdopt()
}

// repoChoices gathers the repositories the pane offers: the ones the reader has
// used here, most recent first, then every repository their lists hold, most
// recently active first. Nothing is fetched to build it.
func (a *App) repoChoices() []repoChoice {
	now := a.now()
	var out []repoChoice
	seen := map[string]bool{}
	add := func(repo, note string) {
		if seen[strings.ToLower(repo)] || !model.ValidRepo(repo) {
			return
		}
		seen[strings.ToLower(repo)] = true
		out = append(out, repoChoice{repo: repo, note: note})
	}
	for _, recent := range a.state.Repos {
		add(recent.Repo, "used "+model.HumanAge(now.Sub(recent.At))+" ago")
	}

	type listed struct {
		repo string
		at   time.Time
		open int
	}
	var repos []*listed
	byName := map[string]*listed{}
	note := func(pr model.PullRequest, at time.Time, open bool) {
		entry, ok := byName[pr.Repo]
		if !ok {
			entry = &listed{repo: pr.Repo}
			byName[pr.Repo] = entry
			repos = append(repos, entry)
		}
		if at.After(entry.at) {
			entry.at = at
		}
		if open {
			entry.open++
		}
	}
	for _, pr := range a.views[viewOpen].prs {
		note(pr, pr.UpdatedAt, true)
	}
	for _, pr := range a.views[viewClosed].prs {
		note(pr, pr.ClosedAt, false)
	}
	slices.SortStableFunc(repos, func(x, y *listed) int { return y.at.Compare(x.at) })
	for _, entry := range repos {
		if entry.open > 0 {
			add(entry.repo, fmt.Sprintf("%d in your list", entry.open))
		} else {
			add(entry.repo, "recently closed")
		}
	}
	return out
}

// refilterAdopt rebuilds the list from the input and returns the selection to
// the top of it.
func (a *App) refilterAdopt() {
	p := &a.adopt
	query := strings.TrimSpace(p.input.Value())
	p.cursor, p.offset = 0, 0

	switch p.stage {
	case adoptRepos:
		if _, err := model.ParseReference(query); err == nil {
			// A whole reference goes straight to its preview; a list of
			// repositories beneath it would only suggest otherwise.
			p.choices = nil
			return
		}
		all := make([]adoptChoice, 0, len(p.repos))
		exact := false
		for _, r := range p.repos {
			all = append(all, adoptChoice{label: r.repo, search: r.repo, note: r.note, repo: r.repo})
			exact = exact || strings.EqualFold(r.repo, query)
		}
		p.choices = rankAdopt(query, all)
		if !exact && model.ValidRepo(query) {
			// A repository nobody has offered is still one the reader can
			// name, and it comes first because it is exactly what they typed.
			p.choices = append([]adoptChoice{{label: query, search: query, note: "browse", repo: query}}, p.choices...)
		}

	case adoptPulls:
		all := make([]adoptChoice, 0, len(p.pulls))
		for _, pr := range p.pulls {
			label := fmt.Sprintf("#%d %s", pr.Number, model.SafeLine(pr.Title))
			note := pr.Author + " · " + model.HumanAge(a.now().Sub(pr.UpdatedAt))
			if _, ok := a.state.Adoption(pr.Key().String()); ok {
				note = adoptedGlyph + " adopted · " + note
			}
			all = append(all, adoptChoice{label: label, search: label + " " + pr.Author, note: note, key: pr.Key()})
		}
		p.choices = rankAdopt(strings.TrimPrefix(query, "#"), all)

	default:
		p.choices = nil
	}
	a.clampAdoptScroll()
}

// rankAdopt orders the choices a query matches, best first, keeping the
// offered order for an empty query and among equals.
func rankAdopt(query string, all []adoptChoice) []adoptChoice {
	if query == "" {
		return all
	}
	names := make([]string, len(all))
	for i, c := range all {
		names[i] = c.search
	}
	found := fuzzy.FindNoSort(query, names)
	slices.SortStableFunc(found, func(x, y fuzzy.Match) int { return y.Score - x.Score })
	out := make([]adoptChoice, 0, len(found))
	for _, f := range found {
		c := all[f.Index]
		c.hits = nil
		for _, i := range f.MatchedIndexes {
			if i < len(c.label) {
				c.hits = append(c.hits, i)
			}
		}
		out = append(out, c)
	}
	return out
}

// adoptSelection is the row under the cursor, when there is one.
func (a *App) adoptSelection() (adoptChoice, bool) {
	p := &a.adopt
	if p.cursor < 0 || p.cursor >= len(p.choices) {
		return adoptChoice{}, false
	}
	return p.choices[p.cursor], true
}

// moveAdopt steps the selection by delta, clamped to the list.
func (a *App) moveAdopt(delta int) {
	p := &a.adopt
	if len(p.choices) == 0 {
		return
	}
	p.cursor = min(max(p.cursor+delta, 0), len(p.choices)-1)
	a.clampAdoptScroll()
}

// clampAdoptScroll keeps the selected row inside the drawn window.
func (a *App) clampAdoptScroll() {
	p := &a.adopt
	if len(p.choices) == 0 {
		p.cursor, p.offset = 0, 0
		return
	}
	p.cursor = min(max(p.cursor, 0), len(p.choices)-1)
	p.offset = clampOffset(p.offset, p.cursor, a.adoptWindow(), len(p.choices))
}

// adoptLayout is where the pane sits and how its height is spent.
type adoptLayout struct {
	x, y          int
	width, height int
	inner         int
	body          int
}

// adoptChrome is the lines the pane spends around its body: the top edge, the
// input, the rule beneath it, a blank line and the bottom edge.
const adoptChrome = 5

func (a *App) adoptLayout() adoptLayout {
	l := adoptLayout{width: a.width}
	room := a.height
	if a.floating() {
		l.width = min(a.width-4, overlayMaxWidth)
		room -= 2
	}
	l.inner = max(l.width-4, 1)
	l.body = max(min(room-adoptChrome, adoptBodyMax), 1)
	l.height = min(adoptChrome+l.body, max(a.height, 1))
	l.x = max((a.width-l.width)/2, 0)
	l.y = max((a.height-l.height)/2, 0)
	return l
}

// adoptWindow is how many list rows fit: the body, less the line beneath the
// rows, and in the second stage the line above them naming the repository.
func (a *App) adoptWindow() int {
	window := a.adoptLayout().body - 1
	if a.adopt.stage == adoptPulls {
		window--
	}
	return max(window, 1)
}

// renderAdopt draws the pane over a finished screen.
func (a *App) renderAdopt(base []string) []string {
	l := a.adoptLayout()
	return a.floatOver(base, a.adoptBox(l), l.x, l.y, l.width)
}

// adoptBox draws the pane, frame and all, as l.height lines.
func (a *App) adoptBox(l adoptLayout) []string {
	p := &a.adopt
	row := func(content string) string { return a.frameRow(content, l.inner) }

	box := make([]string, 0, l.height)
	box = append(box,
		a.edge(l.width, "╭", "╮", a.styles.OverlayTitle.Render("Adopt a pull request"), ""),
		row(p.input.View()),
		a.frameRule(l.width),
	)
	body := a.adoptBody(l.inner, l.body)
	for i := 0; i < l.body; i++ {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		box = append(box, row(line))
	}
	box = append(box, row(""), a.edge(l.width, "╰", "╯", a.adoptHints(), ""))
	if len(box) > l.height {
		// A terminal too short for the whole pane keeps the input and the
		// bottom edge, which says how to get out.
		box = append(box[:max(l.height-1, 0)], box[len(box)-1])
	}
	return box
}

// adoptBody is what the pane says beneath its input, styled and fitted to
// width columns and at most lines lines.
func (a *App) adoptBody(width, lines int) []string {
	p := &a.adopt
	var out []string
	para := func(text string, style func(...string) string, most int) {
		for _, line := range wrapLines(text, width, most) {
			out = append(out, style(line))
		}
	}
	muted, warn := a.styles.Muted.Render, a.styles.Error.Render

	switch p.stage {
	case adoptPreview:
		return a.adoptPreview(width)

	case adoptPulls:
		var header string
		switch {
		case p.loading:
			header = a.spin.View() + " reading the open pull requests in " + p.repo + "…"
		case p.pullsErr != "":
			header = p.repo + ": " + p.pullsErr
		default:
			header = fmt.Sprintf("%s · %d open %s by others", p.repo, len(p.pulls), plural(len(p.pulls), "pull request"))
		}
		out = append(out, a.styles.SectionHdr.Render(truncatePlain(header, width)))
		if !p.loading && p.pullsErr == "" && len(p.choices) == 0 {
			if len(p.pulls) == 0 {
				para("Nobody else has an open pull request here. esc to pick another repository.", muted, 2)
			} else {
				para(fmt.Sprintf("No pull request matches “%s” · enter looks up a number", p.input.Value()), muted, 2)
			}
		}

	default:
		query := strings.TrimSpace(p.input.Value())
		if ref, err := model.ParseReference(query); err == nil {
			para("enter looks up "+ref.String(), muted, 1)
			break
		}
		if len(p.choices) == 0 {
			if query == "" {
				para("Type owner/repo to browse its open pull requests, or paste a pull request's URL.", muted, 2)
				para("Repositories you browse are remembered here, with every one your lists already hold.", muted, 2)
			} else {
				para(fmt.Sprintf("No repository matches “%s” · type owner/repo to browse one", query), muted, 2)
			}
		}
	}

	window := a.adoptWindow()
	end := min(p.offset+window, len(p.choices))
	for i := p.offset; i < end; i++ {
		out = append(out, a.adoptRow(p.choices[i], i == p.cursor, width))
	}
	for len(out) < lines-1 {
		out = append(out, "")
	}
	switch {
	case p.problem != "":
		out = append(out, warn(truncatePlain(p.problem, width)))
	case len(p.choices) > window:
		out = append(out, muted(fmt.Sprintf("%d–%d of %d", p.offset+1, end, len(p.choices))))
	}
	return out
}

// adoptPreview is the preview of the pull request being looked up, fitted to
// width columns.
func (a *App) adoptPreview(width int) []string {
	p := &a.adopt
	var out []string
	para := func(text string, style func(...string) string, most int) {
		for _, line := range wrapLines(text, width, most) {
			out = append(out, style(line))
		}
	}
	line := func(s string) { out = append(out, s) }
	muted, warn := a.styles.Muted.Render, a.styles.Error.Render

	switch {
	case p.looking != model.Key{}:
		para(a.spin.View()+" looking up "+p.looking.String()+"…", a.styles.Meta.Render, 1)
	case p.problem != "":
		para(p.problem, warn, 3)
	case p.found != nil:
		pr := p.found.PR
		line(fitSegs(width, " ",
			seg{text: adoptedGlyph, style: a.styles.Adopted},
			seg{text: "#" + fmt.Sprint(pr.Number), style: a.styles.Number},
			seg{text: pr.Repo, style: a.styles.Repo},
			seg{text: "by " + pr.Author, style: a.styles.Adopted},
		))
		para(model.SafeLine(pr.Title), a.styles.Title.Render, 2)
		line(fitSegs(width, " ",
			seg{text: pr.HeadRef, style: a.styles.Branch},
			seg{text: "→", style: a.styles.Arrow},
			seg{text: pr.BaseRef, style: a.styles.Branch},
		))
		if p.found.Fork && !p.found.MaintainerCanModify {
			para("Its branch is in a fork that does not let maintainers push, so an agent can read it but not push to it.", warn, 2)
		}
		para(fmt.Sprintf("Adopting it lists it with your own and trusts %s on it: prutil may check their branch out and hand its feedback to an agent. It is not watched until you press w.", pr.Author), muted, 3)
	}
	return out
}

// adoptRow draws one row of the list: the selection bar, the label with the
// characters the filter matched picked out, and the note on the right.
func (a *App) adoptRow(c adoptChoice, selected bool, width int) string {
	prefix, style := "  ", a.styles.Text
	if selected {
		prefix, style = a.styles.SelectBar.Render("▌")+" ", a.styles.Title
	}
	room := max(width-2, 1)
	note := ""
	if c.note != "" && lenOf(c.note)+2+min(lenOf(c.label), 16) <= room {
		note = a.styles.Muted.Render(c.note)
	}
	labelRoom := room
	if note != "" {
		labelRoom = room - lenOf(c.note) - 2
	}
	return prefix + justify(room, highlight(c.label, c.hits, labelRoom, style, a.styles.MatchHit), note)
}

// adoptHints names the pane's own keys for its bottom edge, saying what enter
// and esc do in the stage the reader is in.
func (a *App) adoptHints() string {
	p := &a.adopt
	k := p.keys
	submit, back := k.Submit.Help(), k.Close.Help()
	query := strings.TrimSpace(p.input.Value())
	_, isRef := model.ParseReference(query)

	switch p.stage {
	case adoptPreview:
		submit.Desc = "look up"
		if p.found != nil {
			submit.Desc = "adopt"
		}
	case adoptPulls:
		submit.Desc = "look up"
	default:
		back.Desc = "cancel"
		submit.Desc = "browse"
		if isRef == nil {
			submit.Desc = "look up"
		}
	}
	pairs := []key.Help{submit, back}
	if p.stage != adoptPreview && len(p.choices) > 1 {
		pairs = append([]key.Help{{Key: "↑↓", Desc: "select"}}, pairs...)
	}
	parts := make([]string, 0, len(pairs))
	for _, h := range pairs {
		parts = append(parts, a.styles.OverlayKey.Render(h.Key)+" "+a.styles.Muted.Render(h.Desc))
	}
	return strings.Join(parts, a.styles.Muted.Render(" · "))
}
