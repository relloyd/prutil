package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

func TestOldFailuresOnTheCurrentHeadDoNotAppearOrReachAnAgent(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "abc"
	app.views[viewOpen].prs[0].Rollup = model.StatusFailure
	delete(app.checks, key)

	assert.Equal(t, model.StatusUnknown, app.rollupFor(app.views[viewOpen].prs[0]),
		"the raw historical failure is provisional until detail is complete")

	current := []model.Check{{Name: "Validate PR title", Status: model.StatusSuccess}}
	send(t, app, press("w"))
	// The initial check command was not run; let the explicit poll own the read.
	app.mutate(key).handing = false
	setRollup(client, "abc", model.StatusFailure)
	client.checks[key] = current
	poll(t, app)

	assert.Equal(t, model.CheckCounts{Success: 1, Total: 1}, model.CountChecks(app.checks[key].checks))
	assert.Equal(t, model.StatusSuccess, app.rollupFor(app.views[viewOpen].prs[0]))
	assert.NotContains(t, plain(app.render()), "✗2")
	assert.Zero(t, checkHandoffs(dispatcherOf(t, app)),
		"a raw FAILURE containing only superseded failures must not take an agent off feedback")
	assert.NotEmpty(t, dispatcherOf(t, app).requests(), "review feedback still reaches the agent")
}

func TestPartialChecksStayVisibleButCannotTriggerAutomaticActions(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "abc"
	app.views[viewOpen].prs[0].Rollup = model.StatusFailure
	app.homeCfg.ChecksPassed.Comment = "/deploy staging"
	send(t, app, press("w"))
	poll(t, app)
	app.state.TogglePostOnPass(key.String())

	send(t, app, checksErrMsg{
		gen: app.gen, key: key, headOID: "abc", err: errors.New("page two unavailable"),
		checkHandoff: true, checks: []model.Check{{Name: "Validate PR title", Status: model.StatusFailure}},
	})

	assert.Contains(t, plain(app.render()), "partial checks")
	assert.Contains(t, plain(app.render()), "Validate PR title")
	assert.Equal(t, model.StatusUnknown, app.rollupFor(app.views[viewOpen].prs[0]))
	assert.Zero(t, checkHandoffs(dispatcherOf(t, app)))

	send(t, app, checksErrMsg{
		gen: app.gen, key: key, headOID: "abc", err: errors.New("page two unavailable"),
		checks: []model.Check{{Name: "Validate PR title", Status: model.StatusSuccess}},
	})
	assert.Empty(t, passComments(app.client.(*fakeClient)), "a partial success does not authorize a deployment comment")
}

func TestManuallySendingPartialChecksNeedsFreshConfirmation(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "abc"
	app.checks[key] = checkState{
		headOID: "abc", loaded: true, err: errors.New("page two unavailable"),
		checks: []model.Check{{Name: "build", Status: model.StatusFailure}},
	}

	ask := send(t, app, press("F"))
	require.NotNil(t, ask)
	assert.Contains(t, string(ask().(statusMsg)), "press F again to send only the visible failures")
	assert.Zero(t, checkHandoffs(dispatcherOf(t, app)))

	app.checks[key] = checkState{
		headOID: "abc", loaded: true, err: errors.New("page two unavailable"),
		checks: []model.Check{{Name: "lint", Status: model.StatusFailure}},
	}
	changed := send(t, app, press("F"))
	require.NotNil(t, changed)
	assert.Contains(t, string(changed().(statusMsg)), "checks changed")
	assert.Zero(t, checkHandoffs(dispatcherOf(t, app)))

	ask = send(t, app, press("F"))
	require.NotNil(t, ask)
	assert.Contains(t, string(ask().(statusMsg)), "press F again")
	investigate(t, app, "F")
	require.Equal(t, 1, checkHandoffs(dispatcherOf(t, app)))
	assert.Equal(t, "lint", lastRequest(t, dispatcherOf(t, app), true).Checks[0].Name)
}

func TestIncompleteChecksWithoutVisibleFailuresCannotBeSentManually(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "abc"
	app.checks[key] = checkState{
		headOID: "abc", loaded: true, err: errors.New("page two unavailable"),
		checks: []model.Check{{Name: "build", Status: model.StatusSuccess}},
	}

	reply := send(t, app, press("F"))

	require.NotNil(t, reply)
	assert.Contains(t, string(reply().(statusMsg)), "no visible failed checks")
	assert.Zero(t, client.callsFor(key), "an incomplete result must not become an unconfirmed new handoff")
	assert.Zero(t, checkHandoffs(dispatcherOf(t, app)))
}

func TestPartialChecksAndAnUntrustedReviewNeedSeparateConfirmations(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.review = hostileReview()
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "abc"
	send(t, app, press("w"))
	poll(t, app)
	require.True(t, app.runtimeOf(key).hold.Held())
	app.checks[key] = checkState{
		headOID: "abc", loaded: true, err: errors.New("page two unavailable"),
		checks: []model.Check{{Name: "build", Status: model.StatusFailure}},
	}

	first := send(t, app, press("F"))
	assert.Contains(t, string(first().(statusMsg)), "is held")
	second := send(t, app, press("F"))
	assert.Contains(t, string(second().(statusMsg)), "incomplete checks")
	assert.Zero(t, checkHandoffs(dispatcherOf(t, app)))
	investigate(t, app, "F")
	assert.Equal(t, 1, checkHandoffs(dispatcherOf(t, app)))
}

func TestStaleHeadDetailCannotOverwriteCurrentChecks(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "new-head"
	app.checks[key] = checkState{
		headOID: "new-head", loaded: true,
		checks: []model.Check{{Name: "build", Status: model.StatusSuccess}},
	}

	send(t, app, checksMsg{
		gen: app.gen, key: key, headOID: "old-head",
		checks: []model.Check{{Name: "build", Status: model.StatusFailure}},
	})

	assert.Equal(t, model.StatusSuccess, app.rollupFor(app.views[viewOpen].prs[0]))
	require.Len(t, app.checks[key].checks, 1, "a stale reply must not discard a newer complete cache entry")
	assert.Equal(t, "new-head", app.checks[key].headOID)
	assert.Zero(t, checkHandoffs(dispatcherOf(t, app)))
}

func TestAResolvedHistoricalFailureCanRaiseAChecksPassedNotification(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "abc"
	app.homeCfg.Notifications.Set(home.NotifyChecksPassed, true)
	send(t, app, checksMsg{
		gen: app.gen, key: key, headOID: "abc",
		checks: []model.Check{{Name: "Validate PR title", Status: model.StatusFailure}},
	})
	setRollup(client, "abc", model.StatusFailure)
	snap := client.snapshots["PR_42"]
	snap.Key = key
	send(t, app, notifyPollMsg{at: app.now(), snaps: []model.Snapshot{snap}})

	reply := send(t, app, checksMsg{
		gen: app.gen, key: key, headOID: "abc",
		checks: []model.Check{{Name: "Validate PR title", Status: model.StatusSuccess}},
	})
	require.NotNil(t, reply)
	assert.Contains(t, statuses(drain(reply)), statusMsg("relloyd/prutil#42 checks passed"))
	assert.Len(t, notifierOf(t, app).notifications(), 1)

	send(t, app, notifyPollMsg{at: app.now(), snaps: []model.Snapshot{snap}})
	assert.Len(t, notifierOf(t, app).notifications(), 1, "the unchanged raw failure must not announce a second pass")
}

func TestAClosedRowSettlesOnceItsChecksAreRead(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	pr := sampleClosedPRs()[1]
	require.Empty(t, pr.HeadOID, "the closed list does not select the head commit")

	send(t, app, checksMsg{
		gen: app.gen, key: pr.Key(), headOID: "abc", at: app.now(),
		checks: []model.Check{{Name: "build", Status: model.StatusSuccess}},
	})

	assert.NotEqual(t, "checks…", app.checkSeg(pr).text,
		"a row that never knew its head cannot disagree with the head the read found")
	assert.Equal(t, model.StatusSuccess, app.rollupFor(pr))
}

func TestAnEmptyChecksReadDoesNotSilenceTheChecksPassedNotification(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.homeCfg.Notifications.Set(home.NotifyChecksPassed, true)
	reload(t, app, withRollup(pr7, "c1", model.StatusPending))
	// Read straight after the push, before Actions had queued anything.
	send(t, app, checksMsg{gen: app.gen, key: pr7, headOID: "c1", at: app.now()})

	reload(t, app, withRollup(pr7, "c1", model.StatusSuccess))
	require.Len(t, notifierOf(t, app).notifications(), 1,
		"a read that found no checks yet says nothing about whether they passed")
	assert.Equal(t, "relloyd/other#7 checks passed", notifierOf(t, app).notifications()[0].Title)
}

func TestAnEmptyChecksReadGivesWayToTheListsRollupAndIsAskedAgain(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	reload(t, app, withRollup(pr7, "c1", model.StatusPending))
	send(t, app, checksMsg{gen: app.gen, key: pr7, headOID: "c1", at: app.now()})
	before := client.checkCalls[pr7]

	reload(t, app, withRollup(pr7, "c1", model.StatusSuccess))
	pr, ok := app.prByKey(pr7)
	require.True(t, ok)
	assert.Equal(t, model.StatusSuccess, app.rollupFor(pr), "the dot follows the list until the checks are read")
	assert.Greater(t, client.checkCalls[pr7], before, "checks that have since registered are read")
}

func TestACheckReadOlderThanTheWatchersReadingIsKeptNotLost(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "abc"
	asked := app.now()
	app.checks[key] = checkState{loading: true, askedAt: asked}
	entry := app.mutate(key)
	entry.headOID, entry.headAt = "abc", asked.Add(time.Second)
	entry.handing = true
	app.setWatchOperation(key, "reading checks")

	send(t, app, checksMsg{
		gen: app.gen, key: key, headOID: "abc", at: asked, checkHandoff: true,
		checks: []model.Check{{Name: "build", Status: model.StatusSuccess}},
	})

	assert.True(t, app.checks[key].loaded, "the detail is for the same head, so the pane shows it rather than loading forever")
	assert.Empty(t, app.runtimeOf(key).operation, "the handoff it was read for is over")
	assert.False(t, app.runtimeOf(key).handing)
}

func TestTheCheckScrollClampCountsThePartialWarning(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 30)
	key := samplePRs()[0].Key()
	checks := make([]model.Check, 0, 40)
	for i := range 40 {
		checks = append(checks, model.Check{Name: fmt.Sprintf("check-%02d", i), Status: model.StatusSuccess})
	}
	app.checks[key] = checkState{
		loaded: true, headOID: app.views[viewOpen].prs[0].HeadOID, checks: checks,
		err: errors.New("page two of the checks timed out after thirty seconds, so the rest of this list was never read"),
	}

	_, width := app.paneWidths()
	drawn, warned := 0, false
	for _, line := range app.renderDetail(width, app.bodyHeight()) {
		if strings.HasPrefix(plain(line), "  ✓ check-") {
			drawn++
		}
		if strings.Contains(plain(line), "partial checks") {
			warned = true
			assert.True(t, strings.HasPrefix(plain(line), sectionPad+"partial checks"),
				"the warning lines up with the section headings: %q", plain(line))
		}
	}
	require.True(t, warned, "the partial warning is drawn")
	assert.Equal(t, drawn, app.checksHeight(), "the clamp scrolls by the rows actually drawn")
}

func TestAFailingRowBeyondThePrefetchIsStillResolved(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	require.False(t, app.homeCfg.Notifications.Enabled(home.NotifyChecksPassed))
	prs := samplePRs()
	for i := range prefetchLimit + 5 {
		pr := prs[0]
		pr.Number, pr.NodeID, pr.HeadOID = 1000+i, fmt.Sprintf("PR_%d", 1000+i), fmt.Sprintf("h%d", i)
		pr.Rollup = model.StatusSuccess
		prs = append(prs, pr)
	}
	prs[len(prs)-1].Rollup = model.StatusFailure
	deep := prs[len(prs)-1].Key()

	reload(t, app, prs)

	assert.Positive(t, client.checkCalls[deep],
		"a raw failure is only provisional, so it is read wherever it sits in the list")
}

func TestAVerifiedFailureIsNotReadAgainOnEveryLoad(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	app.homeCfg.Notifications.Set(home.NotifyChecksPassed, true)
	client.checks[pr7] = []model.Check{{Name: "build", Status: model.StatusFailure}}
	reload(t, app, withRollup(pr7, "c1", model.StatusFailure))
	send(t, app, checksMsg{gen: app.gen, key: pr7, headOID: "c1", at: app.now(), checks: client.checks[pr7]})
	before := client.checkCalls[pr7]

	reload(t, app, withRollup(pr7, "c1", model.StatusFailure))
	reload(t, app, withRollup(pr7, "c1", model.StatusFailure))

	assert.Equal(t, before, client.checkCalls[pr7], "the detail already settled this head's verdict")
}

func TestARowKeepsItsCountsWhileTheyAreReadAgain(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	pr := app.views[viewOpen].prs[0]
	app.checks[pr.Key()] = checkState{
		loaded: true, loading: true, headOID: pr.HeadOID,
		checks: []model.Check{{Name: "build", Status: model.StatusSuccess}},
	}
	assert.NotEqual(t, "checks…", app.checkSeg(pr).text, "a background re-read does not blank the row")
}

func TestConfirmingPartialChecksDoesNotStartASecondHandoff(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	app.views[viewOpen].prs[0].HeadOID = "abc"
	app.checks[key] = checkState{
		headOID: "abc", loaded: true, err: errors.New("page two unavailable"),
		checks: []model.Check{{Name: "build", Status: model.StatusFailure}},
	}
	app.mutate(key).handing = true

	drain(send(t, app, press("F")))
	drain(send(t, app, press("F")))

	assert.Zero(t, checkHandoffs(dispatcherOf(t, app)), "one is already waiting on an agent")
}

func TestABackgroundCheckReadErrorLeavesTheStatusLineAlone(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()

	cmd := send(t, app, checksErrMsg{
		gen: app.gen, key: key, headOID: app.views[viewOpen].prs[0].HeadOID, at: app.now(),
		err: errors.New("timeout"),
	})

	assert.Empty(t, statuses(drain(cmd)),
		"the row and the pane say so, and the status line may be holding a question")
}
