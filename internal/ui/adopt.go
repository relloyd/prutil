package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// adoptedGlyph marks a pull request somebody else opened that the reader has
// adopted, on its row and in the header's tally.
const adoptedGlyph = "⇄"

// adoptBodyLines is how many lines the adopt pane gives what it has to say
// beneath the input: the pull request it found, the warnings about it, and
// what adopting it means.
const adoptBodyLines = 9

// adoptPane is the + prompt: a pull request somebody else opened, named by URL
// or owner/repo#number, looked up, shown, and adopted on a second enter.
//
// Looking it up first is the point. Adopting trusts the author on the pull
// request, and the reader should see whose it is before agreeing to that, not
// after.
type adoptPane struct {
	open  bool
	keys  adoptKeyMap
	input textinput.Model
	// seq names the lookup in flight, so that the answer to a reference the
	// reader has since replaced is dropped rather than shown against it.
	seq     int
	looking model.Key
	// found is the pull request the last lookup returned, when it may be
	// adopted, and problem why the last attempt came to nothing.
	found   *gh.Lookup
	problem string
}

// adoptKeyMap holds the keys the adopt pane reads for itself. Everything else
// goes to its input, which is why it is apart from keyMap: a reference is
// typed, and q, w and the rest have to reach it.
type adoptKeyMap struct {
	Close  key.Binding
	Submit key.Binding
	Quit   key.Binding
}

func defaultAdoptKeys() adoptKeyMap {
	return adoptKeyMap{
		Close: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "look up · adopt"),
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

// openAdopt shows the adopt pane with an empty input that already has the
// keyboard, so a pasted URL lands in it.
func (a *App) openAdopt() tea.Cmd {
	if a.store == nil {
		return status("cannot remember what is adopted: " + a.storeErr.Error())
	}
	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "https://github.com/owner/repo/pull/12 or owner/repo#12"
	input.SetStyles(a.styles.helpInput())
	a.adopt = adoptPane{open: true, keys: defaultAdoptKeys(), input: input, seq: a.adopt.seq}
	a.resizeAdopt()
	return a.adopt.input.Focus()
}

// closeAdopt puts the pane away. The sequence number survives it, so a lookup
// still in flight cannot land in a pane opened afterwards.
func (a *App) closeAdopt() {
	a.adopt = adoptPane{seq: a.adopt.seq + 1}
}

// resizeAdopt fits the input to a new terminal size.
func (a *App) resizeAdopt() {
	if !a.adopt.open {
		return
	}
	a.adopt.input.SetWidth(max(a.adoptLayout().inner-lenOf(a.adopt.input.Prompt)-1, 1))
}

// updateAdopt handles the messages the pane takes for itself while it is open,
// and reports whether it took this one.
func (a *App) updateAdopt(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return a.handleAdoptKey(msg), true
	case tea.PasteMsg:
		return a.editAdopt(msg), true
	case tea.MouseWheelMsg, tea.MouseClickMsg:
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
		a.closeAdopt()
		return nil
	case key.Matches(msg, keys.Submit):
		return a.submitAdopt()
	}
	return a.editAdopt(msg)
}

// editAdopt hands a message to the input. Changing what is typed forgets what
// the last lookup found, so that enter never adopts a pull request other than
// the one the input names.
func (a *App) editAdopt(msg tea.Msg) tea.Cmd {
	p := &a.adopt
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.found, p.problem = nil, ""
	}
	return cmd
}

// submitAdopt is enter: adopt what the input names when it has already been
// looked up and found adoptable, and look it up otherwise.
func (a *App) submitAdopt() tea.Cmd {
	p := &a.adopt
	ref, err := model.ParseReference(p.input.Value())
	if err != nil {
		p.found, p.problem = nil, err.Error()
		return nil
	}
	if p.found != nil && p.found.PR.Key() == ref {
		return a.adoptFound()
	}
	if p.looking == ref {
		return nil
	}

	p.seq++
	p.found, p.problem, p.looking = nil, "", ref
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
	if !p.open || msg.seq != p.seq {
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

// adoptable reports why a pull request cannot be adopted, or nil when it can.
func (a *App) adoptable(ref model.Key, lookup gh.Lookup, err error) error {
	if err != nil {
		return fmt.Errorf("could not look up %s: %w", ref, err)
	}
	pr := lookup.PR
	switch {
	case pr.State != model.PRStateOpen:
		return fmt.Errorf("%s is %s; only an open pull request can be adopted", ref, strings.ToLower(pr.State.String()))
	case strings.TrimSpace(pr.Author) == "":
		// There is nobody left to trust, and trusting nobody is what an empty
		// login must never come to mean.
		return fmt.Errorf("%s was opened by an account GitHub no longer has, so there is nobody to trust on it", ref)
	case lookup.Viewer != "" && strings.EqualFold(pr.Author, lookup.Viewer):
		return fmt.Errorf("%s is your own pull request, so it is already in your list", ref)
	}
	if _, ok := a.state.Adoption(ref.String()); ok {
		return fmt.Errorf("%s is already adopted", ref)
	}
	return nil
}

// adoptFound records the adoption the pane is showing, puts the pull request
// in the open list and selects it there.
func (a *App) adoptFound() tea.Cmd {
	found := a.adopt.found
	pr := found.PR
	key := pr.Key()
	a.state.Adopt(key.String(), home.Adoption{NodeID: pr.NodeID, Author: pr.Author, At: a.now()})
	if err := a.saveState(); err != nil {
		a.state.Release(key.String())
		a.adopt.found, a.adopt.problem = nil, err.Error()
		return nil
	}
	a.closeAdopt()

	open := &a.views[viewOpen]
	open.prs = mergeAdopted(open.prs, []model.PullRequest{pr})
	cmd := a.switchView(viewOpen)
	if i := slices.IndexFunc(open.prs, func(p model.PullRequest) bool { return p.Key() == key }); i >= 0 {
		a.focus = paneList
		cmd = tea.Batch(cmd, a.selectList(i))
	}
	a.clampScroll()
	a.recordWatchActivity(key, "adopted from "+pr.Author)

	note := fmt.Sprintf("adopted %s from %s · w to watch it, - to release it", key, pr.Author)
	if found.Fork && !found.MaintainerCanModify {
		note = fmt.Sprintf("adopted %s from %s · its fork does not let maintainers push, so an agent cannot push to it", key, pr.Author)
	}
	return tea.Batch(cmd, a.withSpinner(a.ensureChecks(key)), status(note))
}

// releaseAdopted is -: stop working on a pull request somebody else opened. It
// asks for a second press, because it also withdraws the trust adopting it
// granted, and a watch the reader forgot about is exactly what it is for.
func (a *App) releaseAdopted() tea.Cmd {
	pr, ok := a.selectedPR()
	if !ok {
		return nil
	}
	key := pr.Key()
	adoption, adopted := a.state.Adoption(key.String())
	if !adopted {
		return status(key.String() + " is not adopted · + adopts somebody else's pull request")
	}
	if a.runtimeOf(key).handing {
		return status("already handing " + key.String() + " over")
	}
	if !a.confirms(key, confirmRelease) {
		return status(fmt.Sprintf("press %s again to release %s: it stops being watched, and %s stops being trusted on it",
			a.keys.Release.Help().Key, key, adoption.Author))
	}

	a.state.Release(key.String())
	if err := a.saveState(); err != nil {
		return status(err.Error())
	}
	a.engine.Forget(key)
	a.clearWatch(key, "released")
	if !a.own[key] {
		open := &a.views[viewOpen]
		open.prs = slices.DeleteFunc(slices.Clone(open.prs), func(p model.PullRequest) bool { return p.Key() == key })
	}
	a.clampScroll()
	return tea.Batch(a.scheduleWatch(), status("released "+key.String()))
}

// adoptedAuthor is who the reader agreed to trust on a pull request by adopting
// it, or the empty string for one they have not adopted.
func (a *App) adoptedAuthor(key model.Key) string {
	adoption, _ := a.state.Adoption(key.String())
	return adoption.Author
}

// trustPolicyFor is the trust question for one pull request: the configured
// policy, with the adopted author added on an adopted pull request and on no
// other.
func (a *App) trustPolicyFor(key model.Key) model.TrustPolicy {
	policy := a.homeCfg.TrustPolicy()
	if author := a.adoptedAuthor(key); author != "" {
		policy.Authors = append(slices.Clone(policy.Authors), author)
	}
	return policy
}

// adoptedIDs names the node id of every adopted pull request, for the open
// list's load to read them back by.
func (a *App) adoptedIDs() []string {
	keys := a.state.AdoptedKeys()
	ids := make([]string, 0, len(keys))
	for _, key := range keys {
		if adoption, ok := a.state.Adoption(key); ok && adoption.NodeID != "" {
			ids = append(ids, adoption.NodeID)
		}
	}
	return ids
}

// adoptedByID finds the adopted pull request a node id was remembered for.
func (a *App) adoptedByID(id string) (model.Key, bool) {
	for _, name := range a.state.AdoptedKeys() {
		adoption, _ := a.state.Adoption(name)
		if adoption.NodeID != id {
			continue
		}
		if key, err := model.ParseReference(name); err == nil {
			return key, true
		}
	}
	return model.Key{}, false
}

// mergeAdopted adds adopted pull requests to the reader's own, newest first as
// the open list always is. A pull request that is in both is the reader's own
// row, which is the one the search returned.
func mergeAdopted(own, adopted []model.PullRequest) []model.PullRequest {
	out := slices.Clone(own)
	for _, pr := range adopted {
		if !slices.ContainsFunc(out, func(p model.PullRequest) bool { return p.Key() == pr.Key() }) {
			out = append(out, pr)
		}
	}
	model.SortByCreatedDesc(out)
	return out
}

// applyAdoptedLoad settles what the open list's load learned about the adopted
// pull requests: a failed read is said once, and one that has merged or closed
// is released, since there is nothing left to work on and a watch on it would
// sit in the file for good.
//
// One GitHub no longer shows the reader is released too. Kept, it would be
// counted in the header with no row to select, and so no way to release it.
// A failed read is different: it releases nothing, because not being told is
// not being told the pull request is gone.
func (a *App) applyAdoptedLoad(msg prsMsg) tea.Cmd {
	if msg.view != viewOpen {
		return nil
	}
	if msg.adoptErr != nil {
		return status("could not read adopted pull requests: " + msg.adoptErr.Error())
	}
	var done []string
	release := func(key model.Key, why string) {
		if !a.state.Release(key.String()) {
			return
		}
		a.engine.Forget(key)
		a.clearWatch(key, "released: "+why)
		done = append(done, key.String())
	}
	for _, pr := range msg.finished {
		release(pr.Key(), strings.ToLower(pr.State.String()))
	}
	for _, id := range msg.vanished {
		if key, ok := a.adoptedByID(id); ok {
			release(key, "GitHub no longer shows it to this account")
		}
	}
	if len(done) == 0 {
		return nil
	}
	if err := a.saveState(); err != nil {
		return status(err.Error())
	}
	slices.Sort(done)
	return tea.Batch(a.scheduleWatch(), status(fmt.Sprintf("released %d finished adopted %s: %s",
		len(done), plural(len(done), "pull request"), strings.Join(done, ", "))))
}

// adoptedSeg marks an adopted pull request on its list row.
func (a *App) adoptedSeg(pr model.PullRequest) seg {
	if _, ok := a.state.Adoption(pr.Key().String()); !ok {
		return seg{}
	}
	return seg{text: adoptedGlyph, style: a.styles.Adopted}
}

// authorSeg names who opened an adopted pull request, on its row.
func (a *App) authorSeg(pr model.PullRequest) seg {
	adoption, ok := a.state.Adoption(pr.Key().String())
	if !ok {
		return seg{}
	}
	author := pr.Author
	if author == "" {
		author = adoption.Author
	}
	return seg{text: "by " + author, style: a.styles.Adopted}
}

// adoptNote is what the header says about adopted pull requests, or the empty
// string when there are none. It is there so that a watch on somebody else's
// pull request is never out of sight just because its row has scrolled away.
func (a *App) adoptNote() string {
	n := len(a.state.AdoptedKeys())
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" · %s %d adopted", adoptedGlyph, n)
}

// adoptLayout is where the pane sits and how wide its text runs.
type adoptLayout struct {
	x, y          int
	width, height int
	inner         int
}

func (a *App) adoptLayout() adoptLayout {
	l := adoptLayout{width: a.width}
	if a.floating() {
		l.width = min(a.width-4, overlayMaxWidth)
	}
	l.inner = max(l.width-4, 1)
	// The top edge, the input, the rule, the body, a blank line and the
	// bottom edge.
	l.height = min(5+adoptBodyLines, max(a.height, 1))
	l.x = max((a.width-l.width)/2, 0)
	l.y = max((a.height-l.height)/2, 0)
	return l
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
	body := a.adoptBody(l.inner)
	for i := 0; i < adoptBodyLines; i++ {
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
		box = append(box[:l.height-1], box[len(box)-1])
	}
	return box
}

// adoptBody is what the pane says beneath its input, styled and wrapped to
// width.
func (a *App) adoptBody(width int) []string {
	p := &a.adopt
	var out []string
	para := func(text string, style func(...string) string, lines int) {
		for _, line := range wrapLines(text, width, lines) {
			out = append(out, style(line))
		}
	}
	muted, warn := a.styles.Muted.Render, a.styles.Error.Render

	switch {
	case p.looking != model.Key{}:
		para(a.spin.View()+" looking up "+p.looking.String()+"…", a.styles.Meta.Render, 1)
	case p.problem != "":
		para(p.problem, warn, 3)
	case p.found != nil:
		pr := p.found.PR
		out = append(out, fitSegs(width, " ",
			seg{text: adoptedGlyph, style: a.styles.Adopted},
			seg{text: "#" + fmt.Sprint(pr.Number), style: a.styles.Number},
			seg{text: pr.Repo, style: a.styles.Repo},
			seg{text: "by " + pr.Author, style: a.styles.Adopted},
		))
		para(pr.Title, a.styles.Title.Render, 2)
		out = append(out, fitSegs(width, " ",
			seg{text: pr.HeadRef, style: a.styles.Branch},
			seg{text: "→", style: a.styles.Arrow},
			seg{text: pr.BaseRef, style: a.styles.Branch},
		))
		if p.found.Fork && !p.found.MaintainerCanModify {
			para("Its branch is in a fork that does not let maintainers push, so an agent can read it but not push to it.", warn, 2)
		}
		para(fmt.Sprintf("Adopting it lists it with your own and trusts %s on it: prutil may check their branch out and hand its feedback to an agent. It is not watched until you press w.", pr.Author), muted, 3)
	default:
		para("Paste a pull request's URL, or type owner/repo#12, and press enter to look it up.", muted, 2)
		para("An adopted pull request is listed with your own, and its author is trusted on it and nowhere else. - releases it.", muted, 3)
	}
	return out
}

// adoptHints names the pane's own keys, for its bottom edge.
func (a *App) adoptHints() string {
	k := a.adopt.keys
	submit := k.Submit.Help()
	switch {
	case a.adopt.found != nil:
		submit.Desc = "adopt"
	default:
		submit.Desc = "look up"
	}
	parts := []string{}
	for _, h := range []key.Help{submit, k.Close.Help()} {
		parts = append(parts, a.styles.OverlayKey.Render(h.Key)+" "+a.styles.Muted.Render(h.Desc))
	}
	return strings.Join(parts, a.styles.Muted.Render(" · "))
}
