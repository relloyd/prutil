package gh

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
