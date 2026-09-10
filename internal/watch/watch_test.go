package watch_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
	"github.com/relloyd/prutil/internal/watch"
)

var (
	start = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	key   = model.Key{Repo: "relloyd/prutil", Number: 42}
	other = model.Key{Repo: "relloyd/other", Number: 7}
)

// engine returns an engine on the shipped defaults: thirty seconds while
// checks run, two minutes settling, doubling to thirty.
func engine(t *testing.T) *watch.Engine {
	t.Helper()
	return watch.New(home.DefaultConfig().Watch)
}

// snap builds a reading. The zero rollup is unknown, which is not busy.
func snap(updated time.Time, rollup model.Status) model.Snapshot {
	return model.Snapshot{Key: key, NodeID: "PR_1", UpdatedAt: updated, HeadOID: "abc", Rollup: rollup}
}

// step feeds one reading and returns the pull request's next due time.
func step(t *testing.T, e *watch.Engine, s model.Snapshot, now time.Time) time.Time {
	t.Helper()
	e.Observe([]model.Snapshot{s}, now)
	next, ok := e.NextDue()
	require.True(t, ok)
	return next
}

func TestAnArmedPullRequestIsDueStraightAway(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	assert.Equal(t, []model.Key{key}, e.Due(start))
	assert.Equal(t, 1, e.Watching())
}

func TestStatusExplainsAnArmedPullRequestsScheduleWithoutLeakingItsEntry(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	before, ok := e.Status(key)
	require.True(t, ok)
	assert.Equal(t, watch.TierSettled, before.Tier)
	assert.Equal(t, 2*time.Minute, before.Interval)
	assert.Equal(t, start, before.NextDue)
	assert.False(t, before.SnapshotSeen)
	assert.Equal(t, 5, before.PollsUntilPrecise)

	e.Observe([]model.Snapshot{snap(start, model.StatusPending)}, start)
	after, ok := e.Status(key)
	require.True(t, ok)
	assert.Equal(t, watch.TierActive, after.Tier)
	assert.Equal(t, 30*time.Second, after.Interval)
	assert.True(t, after.SnapshotSeen)
	assert.Equal(t, 5, after.PollsUntilPrecise, "the first poll was precise and reset the counter")
}

func TestStatusDoesNotExistForAnUnarmedPullRequest(t *testing.T) {
	e := engine(t)

	_, ok := e.Status(key)
	assert.False(t, ok)
}

func TestSyncForgetsWhatIsNoLongerArmedAndLeavesTheRestAlone(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key, other}, start)
	e.Observe([]model.Snapshot{snap(start, model.StatusSuccess)}, start)

	e.Sync([]model.Key{key}, start.Add(time.Hour))

	assert.Equal(t, 1, e.Watching())
	// The surviving pull request keeps the schedule it already had rather than
	// being restarted by the reconciliation.
	assert.Equal(t, start.Add(2*time.Minute), mustNextDue(t, e))
}

func TestTheFirstSightingAlwaysAsksTheExpensiveQuestion(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	precise := e.Observe([]model.Snapshot{snap(start, model.StatusSuccess)}, start)
	assert.Equal(t, []model.Key{key}, precise)
}

func TestChecksStillRunningArePolledEveryThirtySeconds(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	now := start
	next := step(t, e, snap(start, model.StatusPending), now)
	assert.Equal(t, now.Add(30*time.Second), next)

	// Nothing changed, but checks are still running, so the gap does not grow.
	now = next
	next = step(t, e, snap(start, model.StatusPending), now)
	assert.Equal(t, now.Add(30*time.Second), next)

	tier, ok := e.Tier(key)
	require.True(t, ok)
	assert.Equal(t, watch.TierActive, tier)
}

func TestAnUnchangingPullRequestBacksOffByDoublingUpToTheCap(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	reading := snap(start, model.StatusSuccess)
	now := start

	var gaps []time.Duration
	for range 6 {
		next := step(t, e, reading, now)
		gaps = append(gaps, next.Sub(now))
		now = next
	}

	// Two hours of nothing, spread over six requests. A seventh would find the
	// pull request dormant.
	assert.Equal(t, []time.Duration{
		2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 30 * time.Minute, 30 * time.Minute,
	}, gaps)
}

func TestPollingStopsAfterLongEnoughAtTheCapWithNothingMoving(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	reading := snap(start, model.StatusSuccess)
	now := start
	for range 20 {
		e.Observe([]model.Snapshot{reading}, now)
		next, ok := e.NextDue()
		if !ok {
			break
		}
		now = next
	}

	_, awake := e.NextDue()
	assert.False(t, awake, "nothing left to poll")
	assert.Empty(t, e.Due(now.Add(24*time.Hour)))

	tier, ok := e.Tier(key)
	require.True(t, ok)
	assert.Equal(t, watch.TierDormant, tier, "the pull request is still armed, only quiet")
}

func TestAnyChangeAtAllPutsAPullRequestBackToItsBaseInterval(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	reading := snap(start, model.StatusSuccess)
	now, gap := start, time.Duration(0)
	for range 3 {
		next := step(t, e, reading, now)
		gap, now = next.Sub(now), next
	}
	require.Equal(t, 8*time.Minute, gap, "three quiet polls have stretched the gap")

	moved := snap(start.Add(time.Hour), model.StatusSuccess)
	next := step(t, e, moved, now)
	assert.Equal(t, now.Add(2*time.Minute), next)
}

func TestEveryFieldTheTripwireSelectsCountsAsAChange(t *testing.T) {
	base := snap(start, model.StatusSuccess)
	for _, tc := range []struct {
		name string
		next model.Snapshot
	}{
		{"a push", model.Snapshot{Key: key, UpdatedAt: start, HeadOID: "def", Rollup: model.StatusSuccess}},
		{"a check state", model.Snapshot{Key: key, UpdatedAt: start, HeadOID: "abc", Rollup: model.StatusFailure}},
		{"a timestamp", model.Snapshot{Key: key, UpdatedAt: start.Add(time.Second), HeadOID: "abc", Rollup: model.StatusSuccess}},
		{"an issue comment", model.Snapshot{Key: key, UpdatedAt: start, HeadOID: "abc", Rollup: model.StatusSuccess, Comments: 1}},
		{"a review thread", model.Snapshot{Key: key, UpdatedAt: start, HeadOID: "abc", Rollup: model.StatusSuccess, Threads: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := engine(t)
			e.Sync([]model.Key{key}, start)
			e.Observe([]model.Snapshot{base}, start)

			precise := e.Observe([]model.Snapshot{tc.next}, start.Add(2*time.Minute))
			assert.Equal(t, []model.Key{key}, precise)
		})
	}
}

func TestTheExpensiveQuestionIsAskedEveryFifthPollEvenWhenNothingMoved(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	reading := snap(start, model.StatusSuccess)
	now := start
	e.Observe([]model.Snapshot{reading}, now)

	// A reply inside an existing thread moves neither counter and can leave
	// updatedAt behind, so an unmoved reading is not proof that nothing was
	// said. The poll times are stepped by hand here, because the point is the
	// count of polls rather than the gaps between them.
	var asked []int
	for i := 1; i <= 10; i++ {
		now = now.Add(time.Minute)
		if len(e.Observe([]model.Snapshot{reading}, now)) > 0 {
			asked = append(asked, i)
		}
	}
	assert.Equal(t, []int{5, 10}, asked)
}

func TestHandingWorkToAnAgentMovesAPullRequestOntoTheSlowerBackoff(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)
	e.Observe([]model.Snapshot{snap(start, model.StatusSuccess)}, start)

	e.Precise(key, 3, true, start)

	tier, ok := e.Tier(key)
	require.True(t, ok)
	assert.Equal(t, watch.TierNotified, tier)
	assert.Equal(t, start.Add(10*time.Minute), mustNextDue(t, e))
}

func TestTheSlowerBackoffDoublesToItsOwnCap(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)
	reading := snap(start, model.StatusSuccess)
	e.Observe([]model.Snapshot{reading}, start)
	e.Precise(key, 3, true, start)

	// The handoff itself scheduled the first look ten minutes out. From there
	// each look that finds the agent still at work doubles the gap.
	now := start
	require.Equal(t, start.Add(10*time.Minute), mustNextDue(t, e))

	var gaps []time.Duration
	for range 4 {
		next := step(t, e, reading, now)
		gaps = append(gaps, next.Sub(now))
		now = next
	}
	assert.Equal(t, []time.Duration{
		20 * time.Minute, 40 * time.Minute, 60 * time.Minute, 60 * time.Minute,
	}, gaps)
}

func TestAPullRequestComesOffTheSlowerBackoffOnceNothingIsWaiting(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)
	e.Observe([]model.Snapshot{snap(start, model.StatusSuccess)}, start)
	e.Precise(key, 3, true, start)

	// The agent resolved everything, so the next look finds no open feedback.
	e.Observe([]model.Snapshot{snap(start.Add(time.Hour), model.StatusSuccess)}, start.Add(10*time.Minute))
	e.Precise(key, 0, false, start.Add(10*time.Minute))

	tier, ok := e.Tier(key)
	require.True(t, ok)
	assert.Equal(t, watch.TierSettled, tier)
}

func TestFeedbackLeftOpenKeepsAPullRequestOnTheSlowerBackoff(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)
	e.Observe([]model.Snapshot{snap(start, model.StatusSuccess)}, start)
	e.Precise(key, 3, true, start)

	e.Precise(key, 3, false, start.Add(10*time.Minute))

	tier, _ := e.Tier(key)
	assert.Equal(t, watch.TierNotified, tier, "the work is still with the agent")
}

func TestAForcedLookThatFindsNothingDoesNotKeepAPullRequestAwakeForever(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	reading := snap(start, model.StatusSuccess)
	now := start
	for range 20 {
		e.Observe([]model.Snapshot{reading}, now)
		// Every forced look reports nothing waiting, which must not reset the
		// run of quiet polls that leads to dormancy.
		e.Precise(key, 0, false, now)
		next, ok := e.NextDue()
		if !ok {
			break
		}
		now = next
	}

	tier, _ := e.Tier(key)
	assert.Equal(t, watch.TierDormant, tier)
}

func TestWakingBringsEverythingForwardIncludingWhatHadGoneDormant(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	reading := snap(start, model.StatusSuccess)
	now := start
	for range 20 {
		e.Observe([]model.Snapshot{reading}, now)
		next, ok := e.NextDue()
		if !ok {
			break
		}
		now = next
	}
	require.Equal(t, watch.TierDormant, tierOf(t, e, key))

	e.Wake(now)
	assert.Equal(t, []model.Key{key}, e.Due(now))
	assert.Equal(t, watch.TierSettled, tierOf(t, e, key))
}

func TestReadingsForPullRequestsNobodyArmedAreIgnored(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key}, start)

	precise := e.Observe([]model.Snapshot{{Key: other, NodeID: "PR_2"}}, start)
	assert.Empty(t, precise)
	assert.Equal(t, 1, e.Watching())
}

func TestDueIsBatchedInAStableOrder(t *testing.T) {
	e := engine(t)
	third := model.Key{Repo: "relloyd/prutil", Number: 9}
	e.Sync([]model.Key{key, other, third}, start)

	assert.Equal(t, []model.Key{other, third, key}, e.Due(start))
}

func TestForgetStopsWatchingOnePullRequest(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key, other}, start)

	e.Forget(key)

	assert.Equal(t, 1, e.Watching())
	_, ok := e.Tier(key)
	assert.False(t, ok)
}

func TestNothingArmedMeansNothingToWakeFor(t *testing.T) {
	e := engine(t)
	_, ok := e.NextDue()
	assert.False(t, ok)
}

func mustNextDue(t *testing.T, e *watch.Engine) time.Time {
	t.Helper()
	next, ok := e.NextDue()
	require.True(t, ok)
	return next
}

func tierOf(t *testing.T, e *watch.Engine, k model.Key) watch.Tier {
	t.Helper()
	tier, ok := e.Tier(k)
	require.True(t, ok)
	return tier
}

func TestAFailedRequestPushesThePollOutRatherThanRetryingAtOnce(t *testing.T) {
	e := engine(t)
	e.Sync([]model.Key{key, other}, start)
	require.Len(t, e.Due(start), 2)

	e.Defer([]model.Key{key, other}, time.Minute, start)

	assert.Empty(t, e.Due(start))
	assert.Equal(t, start.Add(time.Minute), mustNextDue(t, e))
}
