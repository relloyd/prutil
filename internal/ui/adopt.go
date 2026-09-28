package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// adoptedGlyph marks a pull request somebody else opened that the reader has
// adopted, on its row and in the header's tally.
const adoptedGlyph = "⇄"

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
	a.state.TouchRepo(key.Repo, a.now())
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
