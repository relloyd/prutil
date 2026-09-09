package gh

import (
	"strings"
	"time"

	"github.com/relloyd/prutil/internal/model"
)

// listResponse mirrors the data envelope returned by listQuery.
type listResponse struct {
	Search struct {
		IssueCount int `json:"issueCount"`
		PageInfo   struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []prNode `json:"nodes"`
	} `json:"search"`
}

// detailResponse mirrors the data envelope returned by detailQuery.
type detailResponse struct {
	Repository struct {
		PullRequest struct {
			Commits commitConnection `json:"commits"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// repoBatchResponse mirrors the data envelope returned by a batched per-repo
// query. Its aliases are generated at request time, so the envelope decodes
// into a map keyed by alias rather than into a struct.
type repoBatchResponse map[string]struct {
	Nodes []prNode `json:"nodes"`
}

// repoNamesResponse mirrors the data envelope returned by repoNamesQuery.
type repoNamesResponse struct {
	Search struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []struct {
			Repository struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"repository"`
		} `json:"nodes"`
	} `json:"search"`
}

type prNode struct {
	TypeName       string     `json:"__typename"`
	ID             string     `json:"id"`
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	IsDraft        bool       `json:"isDraft"`
	CreatedAt      *time.Time `json:"createdAt"`
	UpdatedAt      *time.Time `json:"updatedAt"`
	ClosedAt       *time.Time `json:"closedAt"`
	MergedAt       *time.Time `json:"mergedAt"`
	State          string     `json:"state"`
	Mergeable      string     `json:"mergeable"`
	ReviewDecision string     `json:"reviewDecision"`
	Additions      int        `json:"additions"`
	Deletions      int        `json:"deletions"`
	ChangedFiles   int        `json:"changedFiles"`
	HeadRefName    string     `json:"headRefName"`
	BaseRefName    string     `json:"baseRefName"`
	Repository     struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	Commits commitConnection `json:"commits"`
}

// toPullRequest converts a search node, reporting false for anything that is
// not a pull request. The search API can only be asked for ISSUE nodes, so an
// issue can appear if the caller's query drops the is:pr qualifier.
func (n prNode) toPullRequest() (model.PullRequest, bool) {
	if n.TypeName != "" && n.TypeName != "PullRequest" {
		return model.PullRequest{}, false
	}
	if n.Repository.NameWithOwner == "" || n.Number == 0 {
		return model.PullRequest{}, false
	}

	pr := model.PullRequest{
		Repo:           n.Repository.NameWithOwner,
		Number:         n.Number,
		Title:          strings.TrimSpace(n.Title),
		URL:            n.URL,
		HeadRef:        n.HeadRefName,
		BaseRef:        n.BaseRefName,
		IsDraft:        n.IsDraft,
		State:          model.ParsePRState(n.State),
		Mergeable:      model.ParseMergeable(n.Mergeable),
		ReviewDecision: model.ParseReviewDecision(n.ReviewDecision),
		Additions:      n.Additions,
		Deletions:      n.Deletions,
		ChangedFiles:   n.ChangedFiles,
		Comments:       n.Comments.TotalCount,
		NodeID:         n.ID,
		Rollup:         model.StatusUnknown,
	}
	if n.CreatedAt != nil {
		pr.CreatedAt = *n.CreatedAt
	}
	if n.UpdatedAt != nil {
		pr.UpdatedAt = *n.UpdatedAt
	}
	if n.ClosedAt != nil {
		pr.ClosedAt = *n.ClosedAt
	}
	if n.MergedAt != nil {
		pr.MergedAt = *n.MergedAt
	}
	if rollup := n.Commits.rollup(); rollup != nil {
		pr.Rollup = model.ParseRollupState(rollup.State)
	}
	return pr, true
}

type commitConnection struct {
	Nodes []struct {
		Commit struct {
			StatusCheckRollup *statusCheckRollup `json:"statusCheckRollup"`
		} `json:"commit"`
	} `json:"nodes"`
}

// rollup returns the head commit's check rollup, or nil when the commit has no
// checks configured at all.
func (c commitConnection) rollup() *statusCheckRollup {
	if len(c.Nodes) == 0 {
		return nil
	}
	return c.Nodes[len(c.Nodes)-1].Commit.StatusCheckRollup
}

type statusCheckRollup struct {
	State    string `json:"state"`
	Contexts struct {
		Nodes []contextNode `json:"nodes"`
	} `json:"contexts"`
}

// contextNode is the union of CheckRun and StatusContext. GraphQL returns only
// the fields belonging to the concrete type, so the unused half stays zero.
type contextNode struct {
	TypeName string `json:"__typename"`

	// CheckRun.
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	DetailsURL  string     `json:"detailsUrl"`
	StartedAt   *time.Time `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
	CheckSuite  struct {
		WorkflowRun *struct {
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`

	// StatusContext.
	Context     string     `json:"context"`
	State       string     `json:"state"`
	TargetURL   string     `json:"targetUrl"`
	Description string     `json:"description"`
	CreatedAt   *time.Time `json:"createdAt"`
}

// toCheck normalises either half of the union into a model.Check.
func (n contextNode) toCheck() model.Check {
	if n.TypeName == "StatusContext" {
		check := model.Check{
			Name:        n.Context,
			URL:         n.TargetURL,
			Description: n.Description,
			Status:      model.ParseStatusContext(n.State),
		}
		if n.CreatedAt != nil {
			check.StartedAt = *n.CreatedAt
			check.CompletedAt = *n.CreatedAt
		}
		return check
	}

	check := model.Check{
		Name:   n.Name,
		URL:    n.DetailsURL,
		Status: model.ParseCheckRun(n.Status, n.Conclusion),
	}
	if n.CheckSuite.WorkflowRun != nil {
		check.Workflow = n.CheckSuite.WorkflowRun.Workflow.Name
	}
	if n.StartedAt != nil {
		check.StartedAt = *n.StartedAt
	}
	if n.CompletedAt != nil {
		check.CompletedAt = *n.CompletedAt
	}
	return check
}

// reviewThreadResponse mirrors the data envelope returned by
// reviewThreadQuery.
type reviewThreadResponse struct {
	Viewer struct {
		Login string `json:"login"`
	} `json:"viewer"`
	Repository struct {
		PullRequest struct {
			ReviewThreads struct {
				TotalCount int                `json:"totalCount"`
				Nodes      []reviewThreadNode `json:"nodes"`
			} `json:"reviewThreads"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// reviewThreadNode is one review conversation as reviewThreadQuery selects it.
type reviewThreadNode struct {
	ID         string `json:"id"`
	IsResolved bool   `json:"isResolved"`
	IsOutdated bool   `json:"isOutdated"`
	Path       string `json:"path"`
	Opener     struct {
		Nodes []reviewCommentNode `json:"nodes"`
	} `json:"opener"`
	Latest struct {
		TotalCount int                 `json:"totalCount"`
		Nodes      []reviewCommentNode `json:"nodes"`
	} `json:"latest"`
}

// reviewCommentNode is one comment inside a review thread. Only the fields
// both aliases can supply are declared; the missing ones decode to zero.
type reviewCommentNode struct {
	ID        string     `json:"id"`
	URL       string     `json:"url"`
	Body      string     `json:"body"`
	CreatedAt *time.Time `json:"createdAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
}

// toReviewThread converts one node, reporting false for a thread GitHub
// returned without any comments in it, which nothing can be said about.
func (n reviewThreadNode) toReviewThread() (model.ReviewThread, bool) {
	if len(n.Opener.Nodes) == 0 || len(n.Latest.Nodes) == 0 {
		return model.ReviewThread{}, false
	}
	opener, latest := n.Opener.Nodes[0], n.Latest.Nodes[0]

	return model.ReviewThread{
		ID:       n.ID,
		Resolved: n.IsResolved,
		Outdated: n.IsOutdated,
		Path:     n.Path,
		URL:      latest.URL,
		Opener:   opener.Author.Login,
		OpenedAt: at(opener.CreatedAt),
		Body:     strings.TrimSpace(opener.Body),
		LatestBy: latest.Author.Login,
		LatestID: latest.ID,
		LatestAt: at(latest.CreatedAt),
		Comments: n.Latest.TotalCount,
	}, true
}

// at dereferences an optional timestamp, since GitHub returns null for a field
// it has no value for and the zero time reads the same way everywhere else in
// prutil.
func at(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// watchResponse mirrors the data envelope returned by watchQuery. The nodes
// come back in the order the ids went out, but a node the token can no longer
// see comes back as null, so the reply is matched on the id it carries rather
// than on its position.
type watchResponse struct {
	Nodes []watchNode `json:"nodes"`
}

// watchNode is one pull request as the tripwire selects it.
type watchNode struct {
	TypeName  string     `json:"__typename"`
	ID        string     `json:"id"`
	UpdatedAt *time.Time `json:"updatedAt"`
	Comments  struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	ReviewThreads struct {
		TotalCount int `json:"totalCount"`
	} `json:"reviewThreads"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID               string `json:"oid"`
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// toSnapshot converts one node, reporting false for anything that is not a
// pull request, which is what a null node decodes to.
func (n watchNode) toSnapshot() (model.Snapshot, bool) {
	if n.TypeName != "PullRequest" || n.ID == "" {
		return model.Snapshot{}, false
	}

	snap := model.Snapshot{
		NodeID:    n.ID,
		UpdatedAt: at(n.UpdatedAt),
		Comments:  n.Comments.TotalCount,
		Threads:   n.ReviewThreads.TotalCount,
		Rollup:    model.StatusUnknown,
	}
	if len(n.Commits.Nodes) > 0 {
		commit := n.Commits.Nodes[0].Commit
		snap.HeadOID = commit.OID
		if commit.StatusCheckRollup != nil {
			snap.Rollup = model.ParseRollupState(commit.StatusCheckRollup.State)
		}
	}
	return snap, true
}
