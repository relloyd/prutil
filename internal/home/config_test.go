package home_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

func TestTheSelfTestMarkerDefaultsToTheBuiltInOne(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  base_interval: 5m\n"))

	require.NoError(t, err)
	assert.Equal(t, model.DefaultSelfTestMarker, cfg.Watch.Marker(),
		"a configuration that does not mention it keeps the built-in marker")
}

func TestTheSelfTestMarkerCanBeReplacedOrTurnedOff(t *testing.T) {
	// The empty string has to mean off rather than unset, which is why the
	// field is a pointer.
	replaced, err := home.ParseConfig([]byte("watch:\n  self_test_marker: \"<!-- mine -->\"\n"))
	require.NoError(t, err)
	assert.Equal(t, "<!-- mine -->", replaced.Watch.Marker())

	off, err := home.ParseConfig([]byte("watch:\n  self_test_marker: \"\"\n"))
	require.NoError(t, err)
	assert.Empty(t, off.Watch.Marker(), "an explicit empty marker turns the exception off")
}

func TestAZeroWatchConfigStillGetsTheBuiltInMarker(t *testing.T) {
	assert.Equal(t, model.DefaultSelfTestMarker, home.WatchConfig{}.Marker())
}

func TestTheWrittenTemplateNamesTheSelfTestMarker(t *testing.T) {
	assert.Contains(t, string(home.DefaultConfigTemplate()), "self_test_marker:")
}

func TestOneConfigurationCannotChangeAnothersDefaultMarker(t *testing.T) {
	// The YAML decoder writes through a non-nil pointer it finds in the
	// target, so a default pointing at shared memory would let one file
	// rewrite the built-in marker for every configuration parsed after it,
	// and two parsed at once would race over it.
	replaced, err := home.ParseConfig([]byte("watch:\n  self_test_marker: \"<!-- mine -->\"\n"))
	require.NoError(t, err)
	require.Equal(t, "<!-- mine -->", replaced.Watch.Marker())

	after, err := home.ParseConfig([]byte("watch:\n  base_interval: 5m\n"))
	require.NoError(t, err)
	assert.Equal(t, model.DefaultSelfTestMarker, after.Watch.Marker())
	assert.Equal(t, model.DefaultSelfTestMarker, home.DefaultConfig().Watch.Marker())
}

func TestReviewCommentDefaultsToBuiltIn(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  base_interval: 5m\n"))
	require.NoError(t, err)
	assert.Equal(t, home.DefaultReviewComment, cfg.Review.CommentFor("relloyd/prutil"))
}

func TestReviewCommentCanBeOverridden(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("review:\n  comment: \"/gemini review --full\"\n"))
	require.NoError(t, err)
	assert.Equal(t, "/gemini review --full", cfg.Review.CommentFor("relloyd/prutil"))
}

func TestReviewCommentEmptyStringDisablesFeature(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("review:\n  comment: \"\"\n"))
	require.NoError(t, err)
	assert.Empty(t, cfg.Review.CommentFor("relloyd/prutil"))
}

func TestReviewCommentPerRepoOverrides(t *testing.T) {
	cfg, err := home.ParseConfig([]byte(`review:
  comment: "/gemini review"
  repos:
    org/coderabbit-repo: "@coderabbitai review"
    org/disabled-repo: ""
`))
	require.NoError(t, err)
	assert.Equal(t, "@coderabbitai review", cfg.Review.CommentFor("org/coderabbit-repo"))
	assert.Empty(t, cfg.Review.CommentFor("org/disabled-repo"))
	assert.Equal(t, "/gemini review", cfg.Review.CommentFor("org/other-repo"))
}

func TestTheWrittenTemplateNamesReviewComment(t *testing.T) {
	tmpl := string(home.DefaultConfigTemplate())
	assert.Contains(t, tmpl, "review:")
	assert.Contains(t, tmpl, "comment: \"/gemini review\"")
}

func TestFallbackDefaults(t *testing.T) {
	cfg := home.DefaultConfig()
	assert.Equal(t, home.FallbackNew, cfg.Herdr.Fallback)
}

func TestFallbackStrategyCanBeConfigured(t *testing.T) {
	cases := []struct {
		input    string
		expected home.FallbackStrategy
	}{
		{"herdr:\n  fallback: new\n", home.FallbackNew},
		{"herdr:\n  fallback: provision\n", home.FallbackNew},
		{"herdr:\n  fallback: none\n", home.FallbackNone},
		{"herdr:\n  fallback: strict\n", home.FallbackNone},
		{"herdr:\n  fallback: repo\n", home.FallbackRepo},
		{"herdr:\n  fallback: repository\n", home.FallbackRepo},
	}
	for _, tc := range cases {
		cfg, err := home.ParseConfig([]byte(tc.input))
		require.NoError(t, err)
		assert.Equal(t, tc.expected, cfg.Herdr.Fallback)
	}
}

func TestInvalidFallbackReturnsError(t *testing.T) {
	_, err := home.ParseConfig([]byte("herdr:\n  fallback: invalid\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fallback strategy")
}

func TestTheWrittenTemplateNamesFallback(t *testing.T) {
	tmpl := string(home.DefaultConfigTemplate())
	assert.Contains(t, tmpl, "fallback: new")
}

func TestSelfReviewDefaultsToFalse(t *testing.T) {
	cfg := home.DefaultConfig()
	assert.False(t, cfg.Watch.SelfReview)
	assert.False(t, cfg.Watch.ReviewFilter().SelfReview)
}

func TestSelfReviewCanBeEnabled(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  self_review: true\n"))
	require.NoError(t, err)
	assert.True(t, cfg.Watch.SelfReview)
	assert.True(t, cfg.Watch.ReviewFilter().SelfReview)
}

func TestTheWrittenTemplateNamesSelfReview(t *testing.T) {
	tmpl := string(home.DefaultConfigTemplate())
	assert.Contains(t, tmpl, "self_review: false")
}

func TestThePromptTellsTheAgentToWriteTheMarkerTheWatcherReads(t *testing.T) {
	// The prompt is the only thing that makes an agent's reply recognisable
	// later, so the string it asks for has to be the string model looks for.
	assert.Contains(t, home.DefaultPrompt, model.AgentCommentMarker)
	assert.Contains(t, string(home.DefaultConfigTemplate()), model.AgentCommentMarker)
}

func TestEveryRenderedPromptAsksForTheMarker(t *testing.T) {
	// Whatever the prompt says, the agent has to be told to sign its replies:
	// an unsigned reply is read as fresh feedback and handed back to an agent,
	// which is a loop that never settles.
	data := home.PromptData{Repo: "relloyd/prutil", Number: 8, URL: "https://github.com/relloyd/prutil/pull/8"}
	cases := []struct {
		name  string
		herdr home.HerdrConfig
	}{
		{
			name:  "the default prompt, which asks for the marker in its own words",
			herdr: home.HerdrConfig{Prompt: home.DefaultPrompt},
		},
		{
			name:  "a skill prompt, which is a slash command and says nothing about replies",
			herdr: home.HerdrConfig{Prompt: home.DefaultPrompt, Skill: "pr-triage"},
		},
		{
			name:  "a prompt written before the marker existed",
			herdr: home.HerdrConfig{Prompt: "Have a look at {{.URL}} please."},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.herdr.RenderPrompt(data)
			require.NoError(t, err)
			assert.Contains(t, out, model.AgentCommentMarker)
		})
	}
}

func TestAPromptThatAlreadyAsksForTheMarkerIsLeftAlone(t *testing.T) {
	// Appending to a prompt that has already asked would have the agent read
	// the same instruction twice, once in somebody else's wording.
	herdr := home.HerdrConfig{Prompt: home.DefaultPrompt}
	out, err := herdr.RenderPrompt(home.PromptData{URL: "https://github.com/relloyd/prutil/pull/8"})
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(out, model.AgentCommentMarker))
	assert.Equal(t, 1, strings.Count(out, home.MarkerInstruction))
}

func TestTheTrustBoundaryShipsStrict(t *testing.T) {
	cfg := home.DefaultConfig()

	assert.Equal(t, []string{"OWNER", "COLLABORATOR"}, cfg.Security.TrustedAssociations,
		"MEMBER is not trusted: in a large organisation it implies no write access")
	assert.Equal(t, []string{"gemini-code-assist[bot]"}, cfg.Security.TrustedAuthors,
		"prutil's own review.comment default summons this bot")
}

func TestAnAbsentSecurityBlockKeepsTheDefaultTrustBoundary(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  base_interval: 5m\n"))
	require.NoError(t, err)

	assert.Equal(t, home.DefaultConfig().Security, cfg.Security,
		"a file that says nothing about trust gets prutil's boundary, not an empty one")
}

func TestAnEmptyTrustListIsADeliberateChoiceAndIsHonoured(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("security:\n  trusted_associations: []\n"))
	require.NoError(t, err)

	assert.Empty(t, cfg.Security.TrustedAssociations,
		"writing the key empty trusts nobody by association, which is stricter than the default")
	assert.Equal(t, []string{"gemini-code-assist[bot]"}, cfg.Security.TrustedAuthors,
		"and says nothing about the other key")
}

func TestTheTrustPolicyLeavesTheViewerToWhoeverReadThePullRequest(t *testing.T) {
	policy := home.DefaultConfig().TrustPolicy()

	assert.Equal(t, model.TrustPolicy{
		Associations: []string{"OWNER", "COLLABORATOR"},
		Authors:      []string{"gemini-code-assist[bot]"},
		Marker:       model.DefaultSelfTestMarker,
	}, policy)
	assert.Empty(t, policy.Viewer,
		"the login comes from the credentials that read the threads, not from the file")
}

func TestTheTrustPolicyCarriesTheSelfTestMarkerFromTheWatchBlock(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  self_test_marker: \"<!-- mine -->\"\n"))
	require.NoError(t, err)

	assert.Equal(t, "<!-- mine -->", cfg.TrustPolicy().Marker,
		"the detector has to know the reader's own marker or it flags them for using it")
}

func TestTheWrittenTemplateNamesTheTrustBoundary(t *testing.T) {
	template := string(home.DefaultConfigTemplate())

	assert.Contains(t, template, "security:")
	assert.Contains(t, template, "trusted_associations:")
	assert.Contains(t, template, "gemini-code-assist[bot]")
	assert.NotContains(t, template, "MEMBER\n", "MEMBER is described, never shipped as trusted")

	cfg, err := home.ParseConfig(home.DefaultConfigTemplate())
	require.NoError(t, err)
	assert.Equal(t, home.DefaultConfig().Security, cfg.Security,
		"what the template writes has to read back as the defaults it was built from")
}

func TestCloneSharesNothing(t *testing.T) {
	// Every map, slice and pointer is given a value, so that a nil one cannot
	// pass for an independent one.
	marker, comment := "<!-- m -->", "/review"
	cfg := home.DefaultConfig()
	cfg.Repos = map[string]string{"a/b": "/x"}
	cfg.Review.Repos = map[string]string{"a/b": "/review"}
	cfg.Review.Comment = &comment
	cfg.Watch.SelfTestMarker = &marker
	cfg.Discovery.Roots = []string{"/src"}

	clone := cfg.Clone()
	assertIndependent(t, reflect.ValueOf(cfg), reflect.ValueOf(clone), "Config")
	assert.Equal(t, cfg, clone, "and equal in every value")
}

// assertIndependent fails for any map, slice or pointer the two values share,
// however deep in the struct it is.
func assertIndependent(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	switch a.Kind() {
	case reflect.Struct:
		for i := range a.NumField() {
			assertIndependent(t, a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name)
		}
	case reflect.Map, reflect.Slice, reflect.Pointer:
		if a.IsNil() {
			t.Errorf("%s is nil in the fixture, so the test cannot tell whether Clone copies it", path)
			return
		}
		assert.NotEqual(t, a.Pointer(), b.Pointer(), "%s is shared with the clone", path)
	}
}
