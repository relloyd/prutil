package gh

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRepoBatchQueryAliasesEveryRepository(t *testing.T) {
	doc, vars, covered := buildRepoBatchQuery("is:pr is:closed", []string{"a/b", "c/d"}, 3)

	require.Equal(t, []string{"a/b", "c/d"}, covered)
	assert.Contains(t, doc, "query($first: Int!, $q0: String!, $q1: String!) {")
	assert.Contains(t, doc, "r0: search(query: $q0, type: ISSUE, first: $first)")
	assert.Contains(t, doc, "r1: search(query: $q1, type: ISSUE, first: $first)")
	assert.Contains(t, doc, "fragment prFields on PullRequest", "the field set is shared, not repeated per repository")

	assert.Equal(t, map[string]any{
		"first": 3,
		"q0":    "is:pr is:closed repo:a/b",
		"q1":    "is:pr is:closed repo:c/d",
	}, vars)
}

func TestBuildRepoBatchQueryDropsMalformedRepositoryNames(t *testing.T) {
	hostile := []string{
		`a/b" ) { x } #`,
		"no-slash",
		"too/many/slashes",
		"has space/name",
		"{braces}/name",
		"",
	}

	for _, name := range hostile {
		t.Run(name, func(t *testing.T) {
			doc, vars, covered := buildRepoBatchQuery("is:pr", []string{name}, 3)
			assert.Empty(t, covered)
			assert.Empty(t, doc, "a document with no repositories is not worth sending")
			assert.Nil(t, vars)
		})
	}
}

func TestBuildRepoBatchQueryKeepsTheGoodNamesAndRenumbers(t *testing.T) {
	doc, vars, covered := buildRepoBatchQuery("is:pr", []string{"a/b", "no-slash", "c/d"}, 2)

	require.Equal(t, []string{"a/b", "c/d"}, covered)
	assert.Contains(t, doc, "$q1: String!")
	assert.NotContains(t, doc, "$q2", "aliases are numbered by position in the output, not the input")
	assert.Equal(t, "is:pr repo:c/d", vars["q1"])
}

func TestRepoNamesQueryStaysCheapPerNode(t *testing.T) {
	// Discovery reads names off the same search the view already runs. Adding
	// fields here would make it as expensive as the sweep and defeat the point.
	assert.Contains(t, repoNamesQuery, "repository { nameWithOwner }")
	for _, field := range []string{"title", "additions", "statusCheckRollup", "comments", "closedAt"} {
		assert.NotContains(t, repoNamesQuery, field, "discovery selects names and nothing else")
	}
}

func TestClosedListQueryCarriesItsFragment(t *testing.T) {
	// The fragment is spread into the search connection, so it has to travel
	// with the query rather than being defined only in the batched document.
	assert.Contains(t, closedListQuery, "...prFields")
	assert.Contains(t, closedListQuery, "fragment prFields on PullRequest")
	assert.Equal(t, 1, strings.Count(closedListQuery, "fragment prFields"))
}
