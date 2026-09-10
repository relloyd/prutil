package model

import (
	"fmt"
	"sort"
	"time"
)

// Key identifies a pull request across repositories.
type Key struct {
	Repo   string // owner/name
	Number int
}

// String renders the key the way GitHub writes it, e.g. "relloyd/prutil#12".
func (k Key) String() string {
	return fmt.Sprintf("%s#%d", k.Repo, k.Number)
}

// Owner splits the repo half of the key into its owner and name parts.
func (k Key) Owner() (owner, name string) {
	for i := 0; i < len(k.Repo); i++ {
		if k.Repo[i] == '/' {
			return k.Repo[:i], k.Repo[i+1:]
		}
	}
	return "", k.Repo
}

// Check is one GitHub Actions check run or legacy commit status attached to the
// head commit of a pull request.
type Check struct {
	Name        string
	Workflow    string
	URL         string
	Description string
	Status      Status
	StartedAt   time.Time
	CompletedAt time.Time
}

// Duration reports how long the check took, or how long it has been running
// when it has not completed yet. It returns zero when GitHub reported no
// timings, which is normal for legacy status contexts.
func (c Check) Duration(now time.Time) time.Duration {
	if c.StartedAt.IsZero() {
		return 0
	}
	end := c.CompletedAt
	if end.IsZero() {
		end = now
	}
	if end.Before(c.StartedAt) {
		return 0
	}
	return end.Sub(c.StartedAt)
}

// PullRequest is the headline information prutil shows for one pull request,
// open or closed. Checks are fetched separately and cached by the UI. The
// closed-only fields stay zero for an open pull request.
type PullRequest struct {
	Repo   string
	Number int
	// NodeID is GitHub's global identifier for the pull request. It is what
	// lets the watcher ask about every armed pull request in one request,
	// whatever repositories they are spread across.
	NodeID         string
	Title          string
	URL            string
	HeadRef        string
	BaseRef        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ClosedAt       time.Time
	MergedAt       time.Time
	IsDraft        bool
	State          PRState
	Mergeable      Mergeable
	ReviewDecision ReviewDecision
	Additions      int
	Deletions      int
	ChangedFiles   int
	// Comments counts pull request conversation comments. It excludes both
	// code-review threads and review submissions.
	Comments int
	// ReviewThreadCount is the total number of code-review threads, including
	// resolved ones. Nil means the query that built this pull request did not
	// select it, as happens in the performance-sensitive closed view.
	ReviewThreadCount *int
	Rollup            Status
}

// Key returns the identity of the pull request.
func (p PullRequest) Key() Key {
	return Key{Repo: p.Repo, Number: p.Number}
}

// Snapshot is the cheap reading the watcher takes of a pull request: enough to
// notice that something moved, and nothing more.
//
// It is deliberately not the whole pull request. Asking GitHub what changed is
// a question the watcher repeats for as long as a pull request is armed, so it
// selects only fields that cost nothing to resolve and leaves the expensive,
// precise question of what the change was to a second request aimed at the few
// pull requests this one flagged.
type Snapshot struct {
	// Key names the pull request, and NodeID is how GitHub was asked about it.
	Key    Key
	NodeID string
	// UpdatedAt is the field that catches what the counts miss: a reply inside
	// an existing review thread changes neither count, and resolving a thread
	// moves the thread count the wrong way.
	UpdatedAt time.Time
	// HeadOID is the head commit, which changing means the work was pushed to.
	HeadOID string
	// Rollup is the check state, which is what says whether CI is still busy.
	Rollup Status
	// Comments is the issue-comment total and Threads the review-thread total,
	// resolved ones included.
	Comments int
	Threads  int
}

// Moved reports whether anything prutil watches for differs between two
// readings.
func (s Snapshot) Moved(previous Snapshot) bool {
	return s.HeadOID != previous.HeadOID ||
		s.Rollup != previous.Rollup ||
		!s.UpdatedAt.Equal(previous.UpdatedAt) ||
		s.Comments != previous.Comments ||
		s.Threads != previous.Threads
}

// Busy reports whether the pull request has checks still running, which is the
// one state worth polling quickly.
func (s Snapshot) Busy() bool { return s.Rollup == StatusPending }

// CheckCounts is the breakdown shown on a list row so a red dot can be sized up
// without opening the detail pane.
type CheckCounts struct {
	Success int
	Failure int
	Pending int
	Other   int
	Total   int
}

// CountChecks aggregates a slice of checks into a CheckCounts.
func CountChecks(checks []Check) CheckCounts {
	var c CheckCounts
	for _, check := range checks {
		c.Total++
		switch check.Status {
		case StatusSuccess:
			c.Success++
		case StatusFailure:
			c.Failure++
		case StatusPending:
			c.Pending++
		default:
			c.Other++
		}
	}
	return c
}

// Rollup reduces the counts to the single status that should colour the dot.
func (c CheckCounts) Rollup() Status {
	switch {
	case c.Total == 0:
		return StatusUnknown
	case c.Failure > 0:
		return StatusFailure
	case c.Pending > 0:
		return StatusPending
	case c.Success > 0:
		return StatusSuccess
	default:
		return StatusNeutral
	}
}

// SortByCreatedDesc orders pull requests newest first, falling back to the key
// so that the order is stable when timestamps tie.
func SortByCreatedDesc(prs []PullRequest) {
	sort.SliceStable(prs, func(i, j int) bool {
		if !prs[i].CreatedAt.Equal(prs[j].CreatedAt) {
			return prs[i].CreatedAt.After(prs[j].CreatedAt)
		}
		if prs[i].Repo != prs[j].Repo {
			return prs[i].Repo < prs[j].Repo
		}
		return prs[i].Number < prs[j].Number
	})
}

// SortByClosedDesc orders pull requests most recently closed first, falling
// back to the key so that the order is stable when timestamps tie.
func SortByClosedDesc(prs []PullRequest) {
	sort.SliceStable(prs, func(i, j int) bool {
		if !prs[i].ClosedAt.Equal(prs[j].ClosedAt) {
			return prs[i].ClosedAt.After(prs[j].ClosedAt)
		}
		if prs[i].Repo != prs[j].Repo {
			return prs[i].Repo < prs[j].Repo
		}
		return prs[i].Number < prs[j].Number
	})
}

// TopNPerRepo keeps at most n pull requests from any one repository and drops
// the rest, preserving the order of the input. Callers sort first, so "at most
// n" means the n most interesting by whichever order they chose.
func TopNPerRepo(prs []PullRequest, n int) []PullRequest {
	if n <= 0 {
		return nil
	}
	kept := make(map[string]int, len(prs))
	out := make([]PullRequest, 0, len(prs))
	for _, pr := range prs {
		if kept[pr.Repo] >= n {
			continue
		}
		kept[pr.Repo]++
		out = append(out, pr)
	}
	return out
}

// HumanAge renders a duration the way a dashboard should: coarse, short, and
// never more than one unit wide.
func HumanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/(24*365)))
	}
}

// HumanDuration renders how long a check ran. Unlike HumanAge it keeps seconds,
// because most checks finish in well under a minute.
func HumanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
