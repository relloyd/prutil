package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

func TestParseRollupState(t *testing.T) {
	cases := map[string]model.Status{
		"SUCCESS":  model.StatusSuccess,
		"FAILURE":  model.StatusFailure,
		"ERROR":    model.StatusFailure,
		"PENDING":  model.StatusPending,
		"EXPECTED": model.StatusPending,
		"":         model.StatusUnknown,
		"nonsense": model.StatusUnknown,
		"success":  model.StatusSuccess,
	}
	for state, want := range cases {
		t.Run(state, func(t *testing.T) {
			assert.Equal(t, want, model.ParseRollupState(state))
		})
	}
}

func TestParseCheckRun(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		conclusion string
		want       model.Status
	}{
		{"queued run is pending", "QUEUED", "", model.StatusPending},
		{"running run is pending", "IN_PROGRESS", "", model.StatusPending},
		{"running run ignores a stale conclusion", "IN_PROGRESS", "SUCCESS", model.StatusPending},
		{"completed success", "COMPLETED", "SUCCESS", model.StatusSuccess},
		{"completed failure", "COMPLETED", "FAILURE", model.StatusFailure},
		{"timed out counts as failure", "COMPLETED", "TIMED_OUT", model.StatusFailure},
		{"action required counts as failure", "COMPLETED", "ACTION_REQUIRED", model.StatusFailure},
		{"cancelled", "COMPLETED", "CANCELLED", model.StatusCancelled},
		{"neutral", "COMPLETED", "NEUTRAL", model.StatusNeutral},
		{"skipped", "COMPLETED", "SKIPPED", model.StatusSkipped},
		{"unrecognised", "COMPLETED", "SOMETHING_NEW", model.StatusUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, model.ParseCheckRun(c.status, c.conclusion))
		})
	}
}

func TestParseMergeableAndReviewDecision(t *testing.T) {
	assert.Equal(t, model.MergeClean, model.ParseMergeable("MERGEABLE"))
	assert.Equal(t, model.MergeConflicting, model.ParseMergeable("CONFLICTING"))
	assert.Equal(t, model.MergeUnknown, model.ParseMergeable("UNKNOWN"))

	assert.Equal(t, model.ReviewApproved, model.ParseReviewDecision("APPROVED"))
	assert.Equal(t, model.ReviewChangesRequested, model.ParseReviewDecision("CHANGES_REQUESTED"))
	assert.Equal(t, model.ReviewRequired, model.ParseReviewDecision("REVIEW_REQUIRED"))
	assert.Equal(t, model.ReviewNone, model.ParseReviewDecision(""))

	assert.Equal(t, "APPROVED", model.ReviewApproved.String())
	assert.Empty(t, model.ReviewNone.String(), "no decision means no badge")
}

func TestCountChecksAndRollup(t *testing.T) {
	checks := []model.Check{
		{Status: model.StatusSuccess},
		{Status: model.StatusSuccess},
		{Status: model.StatusFailure},
		{Status: model.StatusPending},
		{Status: model.StatusSkipped},
	}

	counts := model.CountChecks(checks)
	assert.Equal(t, model.CheckCounts{Success: 2, Failure: 1, Pending: 1, Other: 1, Total: 5}, counts)
	assert.Equal(t, model.StatusFailure, counts.Rollup(), "a failure outranks everything")

	assert.Equal(t, model.StatusPending, model.CountChecks([]model.Check{
		{Status: model.StatusSuccess},
		{Status: model.StatusPending},
	}).Rollup())

	assert.Equal(t, model.StatusSuccess, model.CountChecks([]model.Check{
		{Status: model.StatusSuccess},
	}).Rollup())

	assert.Equal(t, model.StatusUnknown, model.CountChecks(nil).Rollup())

	assert.Equal(t, model.StatusNeutral, model.CountChecks([]model.Check{
		{Status: model.StatusSkipped},
	}).Rollup(), "skipped-only checks are neither good nor bad")
}

func TestSortByCreatedDescIsStable(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	prs := []model.PullRequest{
		{Repo: "b/one", Number: 1, CreatedAt: now.Add(-48 * time.Hour)},
		{Repo: "a/two", Number: 9, CreatedAt: now},
		{Repo: "a/two", Number: 2, CreatedAt: now},
		{Repo: "c/three", Number: 3, CreatedAt: now.Add(-time.Hour)},
	}

	model.SortByCreatedDesc(prs)

	assert.Equal(t, []int{2, 9, 3, 1}, []int{prs[0].Number, prs[1].Number, prs[2].Number, prs[3].Number},
		"newest first, ties broken by repo then number")
}

func TestHumanAge(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Hour, "just now"},
		{30 * time.Second, "just now"},
		{45 * time.Minute, "45m"},
		{6 * time.Hour, "6h"},
		{23*time.Hour + 59*time.Minute, "23h"},
		{3 * 24 * time.Hour, "3d"},
		{59 * 24 * time.Hour, "59d"},
		{90 * 24 * time.Hour, "3mo"},
		{400 * 24 * time.Hour, "1y"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			assert.Equal(t, c.want, model.HumanAge(c.in))
		})
	}
}

func TestHumanDuration(t *testing.T) {
	assert.Empty(t, model.HumanDuration(0))
	assert.Equal(t, "12s", model.HumanDuration(12*time.Second))
	assert.Equal(t, "1m20s", model.HumanDuration(80*time.Second))
	assert.Equal(t, "1h5m", model.HumanDuration(65*time.Minute))
}

func TestCheckDuration(t *testing.T) {
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	now := start.Add(5 * time.Minute)

	finished := model.Check{StartedAt: start, CompletedAt: start.Add(30 * time.Second)}
	assert.Equal(t, 30*time.Second, finished.Duration(now))

	running := model.Check{StartedAt: start}
	assert.Equal(t, 5*time.Minute, running.Duration(now), "a running check is timed against now")

	assert.Zero(t, model.Check{}.Duration(now), "a check without timings has no duration")

	skewed := model.Check{StartedAt: now, CompletedAt: start}
	assert.Zero(t, skewed.Duration(now), "clock skew never yields a negative duration")
}

func TestKeyOwner(t *testing.T) {
	owner, name := model.Key{Repo: "relloyd/prutil"}.Owner()
	assert.Equal(t, "relloyd", owner)
	assert.Equal(t, "prutil", name)

	owner, name = model.Key{Repo: "prutil"}.Owner()
	assert.Empty(t, owner)
	assert.Equal(t, "prutil", name)

	require.Equal(t, "relloyd/prutil#42", model.Key{Repo: "relloyd/prutil", Number: 42}.String())
}
