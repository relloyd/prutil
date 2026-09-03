// Package model holds the domain types that prutil renders: pull requests,
// their CI checks, and the small enums that describe their state. Nothing in
// here knows about GitHub's wire format or about the terminal.
package model

import "strings"

// Status is the normalised state of a single check or of a whole pull request's
// check rollup. GitHub expresses this in several different vocabularies
// depending on the API surface, so everything is mapped onto this one type.
type Status int

// Check and rollup states, ordered from least to most interesting so that a
// list of them can be reduced with max().
const (
	StatusUnknown Status = iota
	StatusSkipped
	StatusNeutral
	StatusCancelled
	StatusSuccess
	StatusPending
	StatusFailure
)

// String returns a short lower-case label suitable for the detail pane.
func (s Status) String() string {
	switch s {
	case StatusSuccess:
		return "success"
	case StatusFailure:
		return "failure"
	case StatusPending:
		return "pending"
	case StatusCancelled:
		return "cancelled"
	case StatusNeutral:
		return "neutral"
	case StatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// ParseRollupState maps a GitHub StatusState (the statusCheckRollup.state
// field) onto a Status.
func ParseRollupState(state string) Status {
	switch strings.ToUpper(state) {
	case "SUCCESS":
		return StatusSuccess
	case "FAILURE", "ERROR":
		return StatusFailure
	case "PENDING", "EXPECTED":
		return StatusPending
	default:
		return StatusUnknown
	}
}

// ParseCheckRun maps a CheckRun's status and conclusion onto a Status. A check
// run that has not completed has an empty conclusion, so status wins.
func ParseCheckRun(status, conclusion string) Status {
	switch strings.ToUpper(status) {
	case "QUEUED", "IN_PROGRESS", "WAITING", "PENDING", "REQUESTED":
		return StatusPending
	}
	switch strings.ToUpper(conclusion) {
	case "SUCCESS":
		return StatusSuccess
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED", "STALE":
		return StatusFailure
	case "CANCELLED":
		return StatusCancelled
	case "NEUTRAL":
		return StatusNeutral
	case "SKIPPED":
		return StatusSkipped
	default:
		return StatusUnknown
	}
}

// ParseStatusContext maps a legacy commit status context state onto a Status.
func ParseStatusContext(state string) Status {
	switch strings.ToUpper(state) {
	case "SUCCESS":
		return StatusSuccess
	case "FAILURE", "ERROR":
		return StatusFailure
	case "PENDING", "EXPECTED":
		return StatusPending
	default:
		return StatusUnknown
	}
}

// Mergeable describes whether a pull request still merges cleanly.
type Mergeable int

// Mergeable states.
const (
	MergeUnknown Mergeable = iota
	MergeClean
	MergeConflicting
)

// ParseMergeable maps GitHub's MergeableState onto a Mergeable.
func ParseMergeable(state string) Mergeable {
	switch strings.ToUpper(state) {
	case "MERGEABLE":
		return MergeClean
	case "CONFLICTING":
		return MergeConflicting
	default:
		return MergeUnknown
	}
}

// ReviewDecision describes where a pull request stands with its reviewers.
type ReviewDecision int

// Review decisions.
const (
	ReviewNone ReviewDecision = iota
	ReviewRequired
	ReviewChangesRequested
	ReviewApproved
)

// String returns the badge text shown on a list row, or an empty string when
// GitHub has no opinion (which is the case for repos without review rules).
func (r ReviewDecision) String() string {
	switch r {
	case ReviewApproved:
		return "APPROVED"
	case ReviewChangesRequested:
		return "CHANGES REQUESTED"
	case ReviewRequired:
		return "REVIEW REQUIRED"
	default:
		return ""
	}
}

// ParseReviewDecision maps GitHub's PullRequestReviewDecision onto a
// ReviewDecision.
func ParseReviewDecision(decision string) ReviewDecision {
	switch strings.ToUpper(decision) {
	case "APPROVED":
		return ReviewApproved
	case "CHANGES_REQUESTED":
		return ReviewChangesRequested
	case "REVIEW_REQUIRED":
		return ReviewRequired
	default:
		return ReviewNone
	}
}
