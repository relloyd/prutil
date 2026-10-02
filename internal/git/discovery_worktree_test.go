package git_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/git"
	"github.com/relloyd/prutil/internal/home"
)

func TestDiscoveryFindsAWorktreeUnderARepositoryDirectory(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "shared-acquiring-services", "migrate-fep-to-dcp-1")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere"), 0o600))
	id := &fakeIdentifier{checkouts: map[string]git.Checkout{
		worktree: {Root: worktree, Repo: "dojo-engineering/shared-acquiring-services", Branch: "migrate-fep-to-dcp-1"},
	}}
	cfg := home.DefaultConfig()
	cfg.Discovery.Roots = []string{root}

	got, err := git.NewResolver(id, cfg, home.OpenIn(t.TempDir())).
		Resolve(context.Background(), "dojo-engineering/shared-acquiring-services")

	require.NoError(t, err)
	assert.Equal(t, worktree, got.Root)
}
