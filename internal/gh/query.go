package gh

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultSearchQuery finds every open pull request the authenticated user has
// opened, in every repository their token can see, newest first. Archived
// repositories are excluded because nothing can be done about those PRs.
const DefaultSearchQuery = "is:open is:pr author:@me archived:false sort:created-desc"

// listQuery is the headline request. It deliberately stops at the check rollup
// state so that the first paint needs exactly one round trip; the individual
// check runs are fetched afterwards by detailQuery. The head OID is included
// so the watcher can persist failed-check deduplication by commit.
const listQuery = `
query($q: String!, $first: Int!, $after: String) {
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      __typename
      ... on PullRequest {
        id
        number
        title
        url
        isDraft
        createdAt
        updatedAt
        mergeable
        reviewDecision
        additions
        deletions
        changedFiles
        headRefName
        baseRefName
        repository { nameWithOwner }
        comments { totalCount }
        reviewThreads { totalCount }
        commits(last: 1) {
          nodes { commit { oid statusCheckRollup { state } } }
        }
      }
    }
  }
}`

// detailQuery fetches the individual checks attached to a pull request's head
// commit, covering both GitHub Actions check runs and legacy commit statuses.
const detailQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup {
              state
              contexts(first: 100) {
                nodes {
                  __typename
                  ... on CheckRun {
                    name
                    status
                    conclusion
                    detailsUrl
                    startedAt
                    completedAt
                    checkSuite { workflowRun { workflow { name } } }
                  }
                  ... on StatusContext {
                    context
                    state
                    targetUrl
                    description
                    createdAt
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`

// DefaultClosedSearchQuery finds every pull request the authenticated user has
// closed or merged, most recently touched first. GitHub search cannot sort by
// close date, so updated-desc is the closest proxy; the results are re-sorted
// by closedAt once they arrive.
const DefaultClosedSearchQuery = "is:pr author:@me is:closed archived:false sort:updated-desc"

// closedPRFields is the field set shared by every closed pull request query. It
// lives in a fragment because the batched per-repo query would otherwise repeat
// it once per repository, turning a thirty-repo request into a document tens of
// kilobytes long.
//
// It deliberately omits the check rollup that listQuery selects. Resolving the
// head commit's rollup costs about a quarter of the request, and GitHub gives a
// document roughly ten seconds before returning 502, which is budget this view
// cannot spare. A closed pull request's CI is history anyway: the list dot
// starts hollow and fills in when the check prefetch lands, and detailQuery
// still fetches the individual checks when a row is selected.
const closedPRFields = `
fragment prFields on PullRequest {
  id
  number
  title
  url
  isDraft
  state
  createdAt
  updatedAt
  closedAt
  mergedAt
  reviewDecision
  additions
  deletions
  changedFiles
  headRefName
  baseRefName
  repository { nameWithOwner }
  comments { totalCount }
}`

// closedListQuery is the global sweep: one search across every repository the
// token can see. It mirrors listQuery but selects the close and merge
// timestamps and drops mergeable, which means nothing once a pull request has
// been closed.
const closedListQuery = `
query($q: String!, $first: Int!, $after: String) {
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes { __typename ...prFields }
  }
}` + closedPRFields

// repoNamesQuery reads repository names off the closed search, and nothing
// else. It is how the per-repo fill learns which repositories to ask about.
//
// The obvious alternative, enumerating the viewer's organisations and their
// repositories, does not work: it is a guess that both misses repositories the
// user has worked in and returns hundreds they have never opened a pull request
// against, each of which would then cost an alias to learn nothing. Reading the
// names off the search the view is already running is exact by construction,
// costs one small field per node, and does not care how large the organisation
// is.
//
// GitHub's repositoriesContributedTo is not a substitute either. It is
// recency-biased: on a real account it returned six repositories while omitting
// one holding 125 of that user's closed pull requests.
const repoNamesQuery = `
query($q: String!, $first: Int!, $after: String) {
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest { repository { nameWithOwner } } }
  }
}`

// repoNamePattern is the shape of an owner/name repository identifier. A name
// that does not match is dropped rather than queried, so that a surprising
// value from repository discovery cannot produce a nonsense search.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// buildRepoBatchQuery builds one document that searches several repositories at
// once, each under a generated alias. GitHub charges the whole document a
// single rate limit point, and a repository the token cannot see comes back
// empty rather than failing, so one unreachable repository cannot spoil the
// batch.
//
// Each repository's search string is passed as a GraphQL variable rather than
// being interpolated into the document, so no caller-supplied text is ever
// spliced into the query text. It returns the document, its variables, and the
// repositories actually covered, in alias order.
func buildRepoBatchQuery(base string, repos []string, perRepo int) (doc string, vars map[string]any, covered []string) {
	var decls, body strings.Builder
	vars = map[string]any{"first": perRepo}
	covered = make([]string, 0, len(repos))

	for _, repo := range repos {
		if !repoNamePattern.MatchString(repo) {
			continue
		}
		i := len(covered)
		fmt.Fprintf(&decls, ", $q%d: String!", i)
		fmt.Fprintf(&body, "\n  r%d: search(query: $q%d, type: ISSUE, first: $first) { nodes { __typename ...prFields } }", i, i)
		vars[fmt.Sprintf("q%d", i)] = base + " repo:" + repo
		covered = append(covered, repo)
	}

	if len(covered) == 0 {
		return "", nil, nil
	}
	return "query($first: Int!" + decls.String() + ") {" + body.String() + "\n}" + closedPRFields, vars, covered
}

// reviewThreadQuery fetches the review conversations on one pull request.
//
// It reads the first and the last comment of every thread under separate
// aliases, because the two answer different questions: the first is the
// feedback itself, and the last is what says whether the thread has been
// replied to since prutil last looked. A thread's own totalCount cannot tell
// those apart.
//
// The viewer's login rides along in the same document. It costs nothing, and
// without it prutil cannot tell a reviewer's comment from the pull request
// author answering their own thread.
const reviewThreadQuery = `
query($owner: String!, $name: String!, $number: Int!, $first: Int!) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: $first) {
        totalCount
        nodes {
          id
          isResolved
          isOutdated
          path
          opener: comments(first: 1) {
            nodes { author { login } createdAt body }
          }
          latest: comments(last: 1) {
            totalCount
            nodes { id url author { login } createdAt body }
          }
        }
      }
    }
  }
}`

// watchQuery is the tripwire the watcher polls with: one document covering
// armed pull requests by node id rather than by repository, so a reader
// watching ten pull requests across ten organisations still costs one request
// and one rate limit point.
//
// GitHub caps nodes(ids:) at a hundred and refuses the whole document past it,
// so WatchSnapshot sends a document per hundred. Anything added to the
// selection here is resolved once per pull request in the document, which is
// what the point cost is derived from: keep it to fields that cost nothing.
//
// It selects nothing that has to be paged and no review thread bodies. The
// point is only to notice that something moved; the precise question of what
// moved is reviewThreadQuery's, and it is asked of the few pull requests this
// one flagged.
//
// updatedAt is the field doing most of the work. A reply inside an existing
// review thread changes neither comment count, and a resolved thread changes
// the thread count in the wrong direction, so the counts alone would miss
// both.
const watchQuery = `
query($ids: [ID!]!) {
  nodes(ids: $ids) {
    __typename
    ... on PullRequest {
      id
      updatedAt
      comments { totalCount }
      reviewThreads { totalCount }
      commits(last: 1) {
        nodes { commit { oid statusCheckRollup { state } } }
      }
    }
  }
}`
