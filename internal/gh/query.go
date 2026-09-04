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
// check runs are fetched afterwards by detailQuery.
const listQuery = `
query($q: String!, $first: Int!, $after: String) {
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      __typename
      ... on PullRequest {
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
        commits(last: 1) {
          nodes { commit { statusCheckRollup { state } } }
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
// it once per repository, turning a sixty-repo request into a document tens of
// kilobytes long.
const closedPRFields = `
fragment prFields on PullRequest {
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
  commits(last: 1) {
    nodes { commit { statusCheckRollup { state } } }
  }
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

// repoDiscoveryQuery names the repositories worth asking about individually
// when the global sweep did not reach far enough back. Enumerating every
// repository in a large organisation is not feasible, so this asks for two
// bounded sets instead: the repositories the user has actually sent pull
// requests to, and the most recently pushed repositories in each of their
// organisations.
const repoDiscoveryQuery = `
query($repos: Int!, $orgs: Int!, $orgRepos: Int!) {
  viewer {
    repositoriesContributedTo(
      first: $repos
      contributionTypes: [PULL_REQUEST]
      includeUserRepositories: true
      orderBy: {field: PUSHED_AT, direction: DESC}
    ) {
      nodes { nameWithOwner isArchived }
    }
    organizations(first: $orgs) {
      nodes {
        repositories(first: $orgRepos, orderBy: {field: PUSHED_AT, direction: DESC}) {
          nodes { nameWithOwner isArchived }
        }
      }
    }
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
