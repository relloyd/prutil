package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/handoff"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// alicesKey names the pull request somebody else opened that these tests
// adopt.
var alicesKey = model.Key{Repo: "acme/widgets", Number: 12}

// alicesPR is that pull request as GitHub describes it. It was opened between
// the reader's first and second sample pull requests, so an adopted copy sorts
// into the middle of the list.
func alicesPR() model.PullRequest {
	return model.PullRequest{
		Repo: "acme/widgets", Number: 12, NodeID: "PR_A12",
		Title:   "Make the widget spin",
		URL:     "https://github.com/acme/widgets/pull/12",
		HeadRef: "feat/spin", BaseRef: "main", Author: "alice",
		CreatedAt: testNow.Add(-60 * time.Hour), UpdatedAt: testNow.Add(-time.Hour),
		Rollup: model.StatusSuccess,
	}
}

// withAlice teaches the fake client about alice's pull request, both ways it
// can be read.
func withAlice(client *fakeClient) {
	client.lookups = map[model.Key]gh.Lookup{alicesKey: {PR: alicesPR(), Viewer: "relloyd"}}
	client.adopted = map[string]model.PullRequest{"PR_A12": alicesPR()}
	if client.snapshots == nil {
		client.snapshots = map[string]model.Snapshot{}
	}
	client.snapshots["PR_A12"] = model.Snapshot{NodeID: "PR_A12", HeadOID: "a12", UpdatedAt: testNow}
}

// lookUp opens the adopt pane, types a reference and settles the lookup.
func lookUp(t *testing.T, app *App, ref string) {
	t.Helper()
	send(t, app, press("+"))
	require.True(t, app.adopt.open)
	send(t, app, tea.PasteMsg{Content: ref})
	pump(t, app, send(t, app, press("enter")))
}

// adoptAlice adopts alice's pull request the way a reader would.
func adoptAlice(t *testing.T, app *App, client *fakeClient) {
	t.Helper()
	withAlice(client)
	lookUp(t, app, "https://github.com/acme/widgets/pull/12/files")
	require.NotNil(t, app.adopt.found, "the lookup finds it: %s", app.adopt.problem)
	pump(t, app, send(t, app, press("enter")))
	require.False(t, app.adopt.open, "the second enter adopts it and closes the pane")
}

// reloadOpen loads the open list again, the way r does, without the status line r
// raises about itself.
func reloadOpen(t *testing.T, app *App) {
	t.Helper()
	for _, msg := range drain(app.load(viewOpen)) {
		pump(t, app, send(t, app, msg))
	}
}

// selectKey moves the list cursor onto a pull request.
func selectKey(t *testing.T, app *App, key model.Key) {
	t.Helper()
	i := slices.IndexFunc(app.cur().prs, func(p model.PullRequest) bool { return p.Key() == key })
	require.GreaterOrEqual(t, i, 0, "%s is in the list", key)
	app.cur().cursor = i
	app.clampScroll()
}

// alicesThreads is feedback on alice's pull request in which alice, who is not
// a collaborator on the repository, has spoken.
func alicesThreads() gh.Review {
	return gh.Review{
		Viewer: "relloyd",
		Threads: []model.ReviewThread{{
			ID: "TA1", Path: "spin.go", Opener: "reviewer", Body: "Spin the other way.",
			LatestBy: "alice", LatestID: "CA1", LatestBody: "I meant to, can somebody pick this up?",
			Participants: []model.Participant{
				{Login: "reviewer", Association: "COLLABORATOR"},
				{Login: "alice", Association: "CONTRIBUTOR"},
			},
			ParticipantsComplete: true,
		}},
	}
}

func TestAdoptingLooksThePullRequestUpBeforeAgreeingToAnything(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withAlice(client)

	lookUp(t, app, "acme/widgets#12")

	require.NotNil(t, app.adopt.found)
	_, adopted := app.state.Adoption(alicesKey.String())
	assert.False(t, adopted, "the first enter only looks it up")
	screen := plain(app.render())
	assert.Contains(t, screen, "by alice", "the pane shows whose it is")
	assert.Contains(t, screen, "trusts alice on it", "and what adopting it means")
}

func TestAdoptingAPullRequestListsItWithTheReadersOwnAndSelectsIt(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	adoptAlice(t, app, client)

	adoption, ok := app.state.Adoption(alicesKey.String())
	require.True(t, ok)
	assert.Equal(t, home.Adoption{NodeID: "PR_A12", Author: "alice", At: testNow}, adoption)

	keys := make([]model.Key, 0, len(app.cur().prs))
	for _, pr := range app.cur().prs {
		keys = append(keys, pr.Key())
	}
	assert.Equal(t, []model.Key{
		{Repo: "relloyd/prutil", Number: 42}, alicesKey,
		{Repo: "relloyd/other", Number: 7}, {Repo: "relloyd/third", Number: 9},
	}, keys, "it sorts in with the reader's own, newest first")
	assert.Equal(t, alicesKey, app.selectedKey(), "and the cursor lands on it")
	assert.False(t, app.armed(alicesKey), "adopting it does not watch it")

	stored, err := app.store.LoadState()
	require.NoError(t, err)
	_, remembered := stored.Adoption(alicesKey.String())
	assert.True(t, remembered, "the adoption survives a restart")
}

func TestAnAdoptedPullRequestIsMarkedOnItsRowAndCountedInTheHeader(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)

	screen := lines(app)
	assert.Contains(t, screen[0], adoptedGlyph+" 1 adopted")
	body := strings.Join(screen, "\n")
	assert.Contains(t, body, adoptedGlyph+" #12 acme/widgets")
	assert.Contains(t, body, "feat/spin → main by alice")
}

func TestTheDetailPaneSaysWhoseAnAdoptedPullRequestIs(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)

	pr, ok := app.selectedPR()
	require.True(t, ok)
	require.Equal(t, alicesKey, pr.Key(), "adopting selects the pull request")
	assert.Contains(t, plain(strings.Join(app.detailHeader(pr, 40), "\n")), adoptedGlyph+" adopted · by alice",
		"the detail pane keeps what a narrow list row drops")

	mine := samplePRs()[0]
	assert.NotContains(t, plain(strings.Join(app.detailHeader(mine, 40), "\n")), "adopted",
		"the reader's own pull request says nothing about adoption")
}

func TestAdoptingRefusesWhatCannotBeAdopted(t *testing.T) {
	mine := alicesPR()
	mine.Author = "relloyd"
	shipped := alicesPR()
	shipped.State = model.PRStateMerged
	orphaned := alicesPR()
	orphaned.Author = ""

	cases := []struct {
		name   string
		lookup gh.Lookup
		want   string
	}{
		{name: "the reader's own pull request is already in the list",
			lookup: gh.Lookup{PR: mine, Viewer: "relloyd"}, want: "is your own pull request"},
		{name: "a merged pull request has nothing left to work on",
			lookup: gh.Lookup{PR: shipped, Viewer: "relloyd"}, want: "only an open pull request can be adopted"},
		{name: "a pull request whose author GitHub has lost has nobody to trust",
			lookup: gh.Lookup{PR: orphaned, Viewer: "relloyd"}, want: "nobody to trust"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, client, _ := newTestApp(t, 120, 40)
			client.lookups = map[model.Key]gh.Lookup{alicesKey: tc.lookup}

			lookUp(t, app, "acme/widgets#12")
			pump(t, app, send(t, app, press("enter")))

			assert.Nil(t, app.adopt.found, "a second enter looks it up again rather than adopting it")
			assert.Contains(t, app.adopt.problem, tc.want)
			assert.Empty(t, app.state.AdoptedKeys(), "and nothing is adopted")
		})
	}
}

func TestAdoptingSaysWhenTheReferenceCannotBeRead(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withAlice(client)

	lookUp(t, app, "not a pull request")

	assert.Equal(t, "type owner/repo to browse it, or paste a pull request's URL", app.adopt.problem)
}

func TestAdoptingSaysWhenGitHubCannotFindIt(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.lookupErr = errors.New("Could not resolve to a PullRequest")

	lookUp(t, app, "acme/widgets#12")

	assert.Contains(t, app.adopt.problem, "could not look up acme/widgets#12")
}

func TestAdoptingTheSamePullRequestTwiceIsRefused(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)

	lookUp(t, app, "acme/widgets#12")

	assert.Contains(t, app.adopt.problem, "already adopted")
}

func TestEditingTheReferenceForgetsWhatTheLookupFound(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withAlice(client)
	lookUp(t, app, "acme/widgets#12")
	require.NotNil(t, app.adopt.found)

	send(t, app, press("3"))
	send(t, app, press("enter"))

	assert.Empty(t, app.state.AdoptedKeys(),
		"enter must never adopt a pull request other than the one the input names")
}

func TestKeysTypedIntoTheAdoptPaneAreNotShortcuts(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("+"))

	cmd := send(t, app, press("q"))

	assert.Nil(t, cmd, "q types rather than quits")
	assert.Equal(t, "q", app.adopt.input.Value())
	send(t, app, press("esc"))
	assert.False(t, app.adopt.open, "esc backs out")
}

func TestALookupThatLandsAfterThePaneClosedIsDropped(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	withAlice(client)
	send(t, app, press("+"))
	send(t, app, tea.PasteMsg{Content: "acme/widgets#12"})
	msgs := drain(send(t, app, press("enter")))
	send(t, app, press("esc"))
	send(t, app, press("+"))

	for _, msg := range msgs {
		send(t, app, msg)
	}

	assert.Nil(t, app.adopt.found, "a pane opened since did not ask for it")
}

func TestTheOpenListReadsAdoptedPullRequestsBackByID(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)

	reloadOpen(t, app)

	assert.Equal(t, [][]string{{"PR_A12"}}, client.adoptedIDs)
	assert.True(t, slices.ContainsFunc(app.cur().prs, func(p model.PullRequest) bool { return p.Key() == alicesKey }),
		"no search of the reader's own finds it, so it has to be read back")
}

func TestTheOpenListAsksNothingExtraWhenNothingIsAdopted(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	reloadOpen(t, app)

	assert.Empty(t, client.adoptedIDs)
}

func TestAFailedAdoptedReadKeepsTheReadersOwnList(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)
	client.adoptedErr = errors.New("HTTP 502")

	reloadOpen(t, app)

	assert.Len(t, app.cur().prs, 3, "the reader's own list is the one thing the load must not lose")
	assert.NoError(t, app.cur().err)
	assert.Equal(t, "could not read adopted pull requests: HTTP 502", app.status)
	_, still := app.state.Adoption(alicesKey.String())
	assert.True(t, still, "not being told is not being told it is gone")
}

func TestAnAdoptedPullRequestThatFinishedIsReleased(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)
	shipped := alicesPR()
	shipped.State = model.PRStateMerged
	client.adopted["PR_A12"] = shipped

	reloadOpen(t, app)

	assert.Empty(t, app.state.AdoptedKeys())
	assert.Len(t, app.cur().prs, 3, "and it leaves the list")
	assert.Contains(t, app.status, "released 1 finished adopted pull request: acme/widgets#12")
}

func TestAnAdoptedPullRequestGitHubNoLongerShowsIsReleased(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)
	delete(client.adopted, "PR_A12")

	reloadOpen(t, app)

	assert.Empty(t, app.state.AdoptedKeys(),
		"kept, it would be counted in the header with no row to release it from")
}

func TestReleasingAnAdoptedPullRequestAsksFirst(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)

	asked := send(t, app, press("-"))
	require.NotNil(t, asked)
	assert.Contains(t, string(asked().(statusMsg)), "alice stops being trusted on it")
	_, still := app.state.Adoption(alicesKey.String())
	require.True(t, still, "the first press only asks")

	pump(t, app, send(t, app, press("-")))

	assert.Empty(t, app.state.AdoptedKeys())
	assert.False(t, slices.ContainsFunc(app.cur().prs, func(p model.PullRequest) bool { return p.Key() == alicesKey }),
		"it leaves the list, since the reader's own search never found it")
}

func TestReleasingStopsTheWatchOnIt(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)
	send(t, app, press("w"))
	pump(t, app, send(t, app, press("w")))
	require.True(t, app.armed(alicesKey))

	send(t, app, press("-"))
	pump(t, app, send(t, app, press("-")))

	assert.False(t, app.armed(alicesKey))
	assert.Zero(t, app.state.ArmedCount())
}

func TestOnlyAnAdoptedPullRequestCanBeReleased(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	cmd := send(t, app, press("-"))

	require.NotNil(t, cmd)
	assert.Contains(t, string(cmd().(statusMsg)), "is not adopted")
}

func TestWatchingAnAdoptedPullRequestAsksFirstAndNamesItsAuthor(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)

	asked := send(t, app, press("w"))
	require.NotNil(t, asked)
	question := string(asked().(statusMsg))
	assert.Contains(t, question, "opened by alice, who may still be working on it")
	assert.Contains(t, question, "press w again")
	require.False(t, app.armed(alicesKey))

	pump(t, app, send(t, app, press("w")))
	assert.True(t, app.armed(alicesKey), "the second press watches it")
}

func TestTheAdoptedAuthorIsTrustedOnTheAdoptedPullRequest(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)
	client.review = alicesThreads()
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w1:p1", Kind: "claude"}

	send(t, app, press("w"))
	pump(t, app, send(t, app, press("w")))
	poll(t, app)

	require.Len(t, dispatcher.requests(), 1, "alice's comment is not held on her own pull request")
	assert.Equal(t, "alice", dispatcher.requests()[0].AdoptedAuthor,
		"and the dispatcher is told, so it may check her branch out")
}

func TestTheAdoptedAuthorIsTrustedNowhereElse(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	adoptAlice(t, app, client)
	client.review = alicesThreads()
	dispatcher := dispatcherOf(t, app)

	selectKey(t, app, model.Key{Repo: "relloyd/prutil", Number: 42})
	send(t, app, press("w"))
	poll(t, app)

	assert.Empty(t, dispatcher.requests(), "adopting alice's pull request does not trust her on the reader's own")
	assert.Equal(t, []string{"alice"}, app.runtimeOf(model.Key{Repo: "relloyd/prutil", Number: 42}).hold.Authors)
}

func TestAHandoffOnTheReadersOwnPullRequestCarriesNoAdoptedAuthor(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w1:p1", Kind: "claude"}

	handOver(t, app)

	require.Len(t, dispatcher.requests(), 1)
	assert.Empty(t, dispatcher.requests()[0].AdoptedAuthor)
}

func TestTheAdoptPaneFitsEveryTerminalSize(t *testing.T) {
	sizes := []struct{ width, height int }{
		{120, 40}, {80, 24}, {60, 20}, {40, 12}, {20, 6}, {10, 3},
	}
	for _, size := range sizes {
		app, client, _ := newTestApp(t, size.width, size.height)
		withAlice(client)
		lookUp(t, app, "acme/widgets#12")

		got := lines(app)
		assert.Len(t, got, size.height, "%dx%d", size.width, size.height)
		for i, line := range got {
			assert.LessOrEqual(t, lenOf(line), size.width, "%dx%d line %d: %q", size.width, size.height, i, line)
		}
	}
}
