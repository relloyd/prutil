package handoff_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/git"
	"github.com/relloyd/prutil/internal/herdr"
	"github.com/relloyd/prutil/internal/home"
)

func TestManualInvestigationReusesTheMatchingHeadWorktree(t *testing.T) {
	const root = "/work/shared-acquiring-services"
	const path = root + "/migrate-fep-to-dcp-1"
	const head = "b9f8cd14c66d996e89ffc53e745a14208c70f28d"

	tests := []struct {
		name   string
		manual bool
		branch string
		head   string
		repo   string
		reuse  bool
		noOID  bool
	}{
		{"manual handoff on matching branch and commit", true, "migrate-fep-to-dcp-1", head, "acme/widgets", true, false},
		{"automatic handoff leaves a reader's checkout alone", false, "migrate-fep-to-dcp-1", head, "acme/widgets", false, false},
		{"different commit does not reuse a checkout", true, "migrate-fep-to-dcp-1", "other", "acme/widgets", false, false},
		{"different repository does not reuse a checkout", true, "migrate-fep-to-dcp-1", head, "acme/other", false, false},
		{"different branch does not reuse a checkout", true, "other", head, "acme/widgets", false, false},
		{"unknown head does not reuse a checkout", true, "migrate-fep-to-dcp-1", head, "acme/widgets", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			control := &fakeHerdr{
				worktrees: [][]herdr.Worktree{{{Path: path, Branch: "migrate-fep-to-dcp-1"}}},
				open:      herdr.WorktreeSession{WorkspaceID: "w8", TabID: "w8:t1", RootPaneID: "w8:p1"},
				create:    herdr.WorktreeSession{WorkspaceID: "w9", TabID: "w9:t1", RootPaneID: "w9:p1"},
				start:     herdr.Agent{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w8:p1"},
			}
			checkouts := fakeGit{path: {Root: path, Repo: tt.repo, Branch: tt.branch, Head: tt.head}}
			repos := &fakeResolver{checkout: git.Checkout{Root: root, Repo: "acme/widgets"}}
			fetch := &fakeFetcher{}
			dispatcher, _ := dispatcherWithProvision(t, control, checkouts, repos, fetch, func(cfg *home.Config) {
				cfg.Herdr.AgentKind = "claude"
			})
			req := request()
			req.PR.Repo = "acme/widgets"
			req.PR.HeadRef = "migrate-fep-to-dcp-1"
			req.PR.HeadOID = head
			if tt.noOID {
				req.PR.HeadOID = ""
			}
			req.Manual = tt.manual
			req.AllowProvision = true

			_, err := dispatcher.Dispatch(context.Background(), req)
			require.NoError(t, err)
			if tt.reuse {
				assert.Equal(t, []string{root + "|" + path + "|migrate-fep-to-dcp-1|acme/widgets#42"}, control.opened)
				assert.Empty(t, control.created)
				assert.Empty(t, fetch.calls)
			} else {
				assert.Empty(t, control.opened)
				assert.Equal(t, []string{root + "|prutil/acme-widgets-42|acme/widgets#42"}, control.created)
			}
		})
	}
}

func TestManualInvestigationPrefersTheHeadWorktreeOverAnOldPrutilWorkspace(t *testing.T) {
	const root = "/work/shared-acquiring-services"
	const path = root + "/migrate-fep-to-dcp-1"
	const head = "b9f8cd14c66d996e89ffc53e745a14208c70f28d"
	control := &fakeHerdr{
		worktrees: [][]herdr.Worktree{{
			{Path: root + "/prutil-42", Branch: "prutil/acme-widgets-42"},
			{Path: path, Branch: "migrate-fep-to-dcp-1"},
		}},
		open:  herdr.WorktreeSession{WorkspaceID: "w8", TabID: "w8:t1", RootPaneID: "w8:p1"},
		start: herdr.Agent{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w8:p1"},
	}
	repos := &fakeResolver{checkout: git.Checkout{Root: root, Repo: "acme/widgets"}}
	dispatcher, _ := dispatcherWithProvision(t, control, fakeGit{
		path: {Root: path, Repo: "acme/widgets", Branch: "migrate-fep-to-dcp-1", Head: head},
	}, repos, &fakeFetcher{}, func(cfg *home.Config) {
		cfg.Herdr.AgentKind = "claude"
	})
	req := request()
	req.PR.Repo = "acme/widgets"
	req.PR.HeadRef = "migrate-fep-to-dcp-1"
	req.PR.HeadOID = head
	req.Manual, req.AllowProvision = true, true

	_, err := dispatcher.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, []string{root + "|" + path + "|migrate-fep-to-dcp-1|acme/widgets#42"}, control.opened)
}
