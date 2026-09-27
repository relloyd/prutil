package ui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/model"
)

// bobsPR is a second pull request in alice's repository, opened by somebody
// else again.
func bobsPR() model.PullRequest {
	pr := alicesPR()
	pr.Number, pr.NodeID, pr.Author = 15, "PR_B15", "bob"
	pr.Title = "Document the spin"
	pr.URL = "https://github.com/acme/widgets/pull/15"
	return pr
}

// withWidgets teaches the fake client what is open in acme/widgets.
func withWidgets(client *fakeClient) {
	withAlice(client)
	client.searches = map[string][]model.PullRequest{
		gh.OthersInRepoQuery("acme/widgets"): {alicesPR(), bobsPR()},
	}
}

// openAdoptPane presses + and returns the repositories it offers, in order.
func openAdoptPane(t *testing.T, app *App) []string {
	t.Helper()
	send(t, app, press("+"))
	require.True(t, app.adopt.open)
	return choiceRepos(app)
}

func choiceRepos(app *App) []string {
	var out []string
	for _, c := range app.adopt.choices {
		out = append(out, c.repo)
	}
	return out
}

func choiceKeys(app *App) []model.Key {
	var out []model.Key
	for _, c := range app.adopt.choices {
		out = append(out, c.key)
	}
	return out
}

// enter presses enter in the pane and settles whatever it asked GitHub.
func enter(t *testing.T, app *App) {
	t.Helper()
	pump(t, app, send(t, app, press("enter")))
}

func TestTheAdoptPaneOffersRecentRepositoriesFirstThenTheOnesInTheLists(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.state.TouchRepo("acme/widgets", testNow.Add(-2*24*time.Hour))

	got := openAdoptPane(t, app)

	assert.Equal(t, []string{"acme/widgets", "relloyd/prutil", "relloyd/other", "relloyd/third"}, got,
		"the ones used here, then the lists' own, the most recently active first")
	assert.Equal(t, "used 2d ago", app.adopt.choices[0].note)
	assert.Equal(t, "1 in your list", app.adopt.choices[1].note)
}

func TestTheAdoptPaneOffersRepositoriesFromTheClosedViewToo(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, prsMsg{gen: app.gen, view: viewClosed, prs: []model.PullRequest{{
		Repo: "relloyd/archive", Number: 3, State: model.PRStateMerged,
		ClosedAt: testNow.Add(-time.Hour), UpdatedAt: testNow.Add(-time.Hour),
	}}})

	got := openAdoptPane(t, app)

	assert.Contains(t, got, "relloyd/archive")
}

func TestTypingFiltersTheRepositories(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openAdoptPane(t, app)

	send(t, app, tea.PasteMsg{Content: "othr"})

	require.NotEmpty(t, app.adopt.choices)
	assert.Equal(t, "relloyd/other", app.adopt.choices[0].repo)
}

func TestARepositoryNobodyOfferedCanStillBeTyped(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openAdoptPane(t, app)

	send(t, app, tea.PasteMsg{Content: "acme/gadgets"})

	require.NotEmpty(t, app.adopt.choices)
	assert.Equal(t, adoptChoice{label: "acme/gadgets", search: "acme/gadgets", note: "browse", repo: "acme/gadgets"},
		app.adopt.choices[0], "exactly what was typed comes first")
}

func TestChoosingARepositoryListsWhatOthersHaveOpenThere(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withWidgets(client)
	app.state.TouchRepo("acme/widgets", testNow.Add(-time.Hour))
	openAdoptPane(t, app)

	enter(t, app)

	assert.Equal(t, adoptPulls, app.adopt.stage)
	assert.Contains(t, client.queries, "is:open is:pr repo:acme/widgets -author:@me archived:false sort:updated-desc",
		"the reader's own are already in the list")
	assert.Equal(t, []model.Key{alicesKey, {Repo: "acme/widgets", Number: 15}}, choiceKeys(app))
	assert.Contains(t, plain(app.render()), "acme/widgets · 2 open pull requests by others")
}

func TestTypingFindsAPullRequestByItsAuthor(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withWidgets(client)
	openAdoptPane(t, app)
	send(t, app, tea.PasteMsg{Content: "acme/widgets"})
	enter(t, app)

	send(t, app, tea.PasteMsg{Content: "bob"})

	require.NotEmpty(t, app.adopt.choices)
	assert.Equal(t, model.Key{Repo: "acme/widgets", Number: 15}, app.adopt.choices[0].key)
}

func TestARepositoryThatAnsweredIsRememberedForNextTime(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withWidgets(client)
	openAdoptPane(t, app)
	send(t, app, tea.PasteMsg{Content: "acme/widgets"})
	enter(t, app)
	send(t, app, press("esc"))
	send(t, app, press("esc"))

	got := openAdoptPane(t, app)

	assert.Equal(t, "acme/widgets", got[0])
	stored, err := app.store.LoadState()
	require.NoError(t, err)
	require.NotEmpty(t, stored.Repos, "and it survives a restart")
	assert.Equal(t, "acme/widgets", stored.Repos[0].Repo)
}

func TestARepositoryThatDidNotAnswerIsNotRemembered(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.searchErr = errors.New("Could not resolve to a Repository")
	openAdoptPane(t, app)
	send(t, app, tea.PasteMsg{Content: "acme/typo"})

	enter(t, app)

	assert.Empty(t, app.state.Repos, "a typo is not worth offering again")
	assert.Contains(t, plain(app.render()), "Could not resolve to a Repository")
}

func TestChoosingAPullRequestPreviewsItAndAnotherEnterAdoptsIt(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withWidgets(client)
	app.state.TouchRepo("acme/widgets", testNow)
	openAdoptPane(t, app)

	enter(t, app)
	enter(t, app)
	require.Equal(t, adoptPreview, app.adopt.stage)
	require.NotNil(t, app.adopt.found)
	assert.Empty(t, app.state.AdoptedKeys(), "the preview adopts nothing")

	enter(t, app)

	assert.False(t, app.adopt.open)
	_, ok := app.state.Adoption(alicesKey.String())
	assert.True(t, ok, "three enters from opening the pane: repository, pull request, adopt")
}

func TestANumberTypedWhileBrowsingLooksThatPullRequestUp(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withWidgets(client)
	app.state.TouchRepo("acme/widgets", testNow)
	openAdoptPane(t, app)
	enter(t, app)

	send(t, app, tea.PasteMsg{Content: "#12"})
	enter(t, app)

	require.NotNil(t, app.adopt.found, "a number reaches a pull request the list did not fetch")
	assert.Equal(t, alicesKey, app.adopt.found.PR.Key())
}

func TestEscStepsBackOneStageAtATime(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withWidgets(client)
	app.state.TouchRepo("acme/widgets", testNow)
	openAdoptPane(t, app)
	enter(t, app)
	enter(t, app)
	require.Equal(t, adoptPreview, app.adopt.stage)
	searches := len(client.queries)

	send(t, app, press("esc"))
	assert.Equal(t, adoptPulls, app.adopt.stage)
	assert.Len(t, app.adopt.choices, 2, "the pull requests are still there")
	assert.Len(t, client.queries, searches, "so going back to them costs no request")

	send(t, app, press("esc"))
	assert.Equal(t, adoptRepos, app.adopt.stage)

	send(t, app, press("esc"))
	assert.False(t, app.adopt.open)
}

func TestABrowseThatLandsAfterTheReaderLeftIsDropped(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withWidgets(client)
	app.state.TouchRepo("acme/widgets", testNow)
	openAdoptPane(t, app)
	msgs := drain(send(t, app, press("enter")))
	send(t, app, press("esc"))

	for _, msg := range msgs {
		send(t, app, msg)
	}

	assert.Equal(t, adoptRepos, app.adopt.stage)
	assert.Empty(t, app.adopt.pulls)
}

func TestABrowseStillReadingWhenTheReaderLooksANumberUpIsNotLost(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withWidgets(client)
	app.state.TouchRepo("acme/widgets", testNow)
	openAdoptPane(t, app)
	browse := drain(send(t, app, press("enter")))
	send(t, app, tea.PasteMsg{Content: "12"})
	enter(t, app)
	require.Equal(t, adoptPreview, app.adopt.stage)

	for _, msg := range browse {
		send(t, app, msg)
	}
	send(t, app, press("esc"))

	assert.Len(t, app.adopt.pulls, 2, "going back finds the browse that finished meanwhile")
	assert.Equal(t, []model.Key{alicesKey}, choiceKeys(app), "still filtered by the number that was typed")
	assert.False(t, app.busy())
}

func TestAPullRequestAlreadyAdoptedIsMarkedWhenBrowsing(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)
	withWidgets(client)
	openAdoptPane(t, app)

	enter(t, app)

	require.Equal(t, adoptPulls, app.adopt.stage, "adopting remembered acme/widgets, so it is offered first")
	assert.Contains(t, app.adopt.choices[0].note, adoptedGlyph+" adopted")
}

func TestTheBrowsingPaneFitsEveryTerminalSize(t *testing.T) {
	sizes := []struct{ width, height int }{
		{120, 40}, {80, 24}, {60, 20}, {40, 12}, {20, 6}, {10, 3},
	}
	for _, size := range sizes {
		app, client, _ := newTestApp(t, size.width, size.height)
		withWidgets(client)
		app.state.TouchRepo("acme/widgets", testNow)
		openAdoptPane(t, app)
		enter(t, app)

		got := lines(app)
		assert.Len(t, got, size.height, "%dx%d", size.width, size.height)
		for i, line := range got {
			assert.LessOrEqual(t, lenOf(line), size.width, "%dx%d line %d: %q", size.width, size.height, i, line)
		}
	}
}

func TestThePullRequestListScrollsWithTheSelection(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 24)
	var many []model.PullRequest
	for i := 1; i <= 30; i++ {
		pr := alicesPR()
		pr.Number = i
		many = append(many, pr)
	}
	client.searches = map[string][]model.PullRequest{gh.OthersInRepoQuery("acme/widgets"): many}
	app.state.TouchRepo("acme/widgets", testNow)
	openAdoptPane(t, app)
	enter(t, app)

	for range 20 {
		send(t, app, press("down"))
	}

	assert.Equal(t, 20, app.adopt.cursor)
	assert.Contains(t, plain(app.render()), "#21 Make the widget spin", "the selected row is drawn")
}
