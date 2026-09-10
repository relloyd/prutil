package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/relloyd/prutil/internal/home"
)

// Identifier reports what repository and branch a local directory holds.
type Identifier interface {
	Identify(ctx context.Context, dir string) Checkout
}

// CacheStore is the slice of home.Store the resolver needs.
type CacheStore interface {
	LoadRepoCache() (home.RepoCache, error)
	SaveRepoCache(cache home.RepoCache) error
	MergeRepoCache(cache home.RepoCache) error
}

// ErrCheckoutNotFound means there is no local checkout for the repository in
// either config, cache or discovery roots.
var ErrCheckoutNotFound = errors.New("no local checkout found")

// Resolver maps owner/name repositories to local checkout roots.
type Resolver struct {
	id    Identifier
	cfg   home.Config
	store CacheStore
}

// NewResolver builds a repository resolver over a git identifier, runtime
// config and cache store.
func NewResolver(id Identifier, cfg home.Config, store CacheStore) *Resolver {
	return &Resolver{id: id, cfg: cfg, store: store}
}

// Resolve returns a validated checkout for repo in owner/name form, consulting
// explicit config mappings first, then cache, then discovery roots.
func (r *Resolver) Resolve(ctx context.Context, repo string) (Checkout, error) {
	if r.id == nil {
		return Checkout{}, fmt.Errorf("repository resolver needs a git identifier")
	}

	want, err := normalizeRepo(repo)
	if err != nil {
		return Checkout{}, err
	}

	if configuredPath, ok := explicitPathFor(want, r.cfg.Repos); ok {
		return r.resolveConfigured(ctx, want, configuredPath)
	}

	cache := home.NewRepoCache()
	if r.store != nil {
		loaded, err := r.store.LoadRepoCache()
		if err != nil {
			return Checkout{}, err
		}
		cache = loaded
	}
	if entry, ok := cache.Lookup(want); ok {
		if checkout, ok := r.matchCheckout(ctx, want, entry.Path); ok {
			return checkout, nil
		}
		cache.Delete(want)
		if err := r.saveCache(cache); err != nil {
			return Checkout{}, err
		}
	}

	if len(r.cfg.Discovery.Roots) == 0 {
		return Checkout{}, fmt.Errorf("%w for %s", ErrCheckoutNotFound, want)
	}

	for _, root := range r.cfg.Discovery.Roots {
		checkout, found := r.discover(ctx, want, root)
		if !found {
			continue
		}
		cache.Set(want, checkout.Root)
		if err := r.saveCache(cache); err != nil {
			return Checkout{}, err
		}
		return checkout, nil
	}

	return Checkout{}, fmt.Errorf("%w for %s", ErrCheckoutNotFound, want)
}

func (r *Resolver) resolveConfigured(ctx context.Context, repo, configuredPath string) (Checkout, error) {
	path, err := resolvePath(configuredPath)
	if err != nil {
		return Checkout{}, fmt.Errorf("config repos.%s has an invalid path %q: %w", repo, configuredPath, err)
	}

	checkout := r.id.Identify(ctx, path)
	if checkout.Root == "" {
		return Checkout{}, fmt.Errorf("config repos.%s points to %q, which is not a git checkout", repo, configuredPath)
	}
	mapped, ok := normalizeRepoLoose(checkout.Repo)
	if !ok {
		return Checkout{}, fmt.Errorf("config repos.%s points to %q, but its origin remote is not owner/name", repo, configuredPath)
	}
	if mapped != repo {
		return Checkout{}, fmt.Errorf("config repos.%s points to %q, which is %s", repo, configuredPath, checkout.Repo)
	}
	return checkout, nil
}

func (r *Resolver) discover(ctx context.Context, want, configuredRoot string) (Checkout, bool) {
	root, err := resolvePath(configuredRoot)
	if err != nil {
		return Checkout{}, false
	}

	var found Checkout
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Name() != ".git" {
			return nil
		}

		candidate := filepath.Dir(path)
		checkout, ok := r.matchCheckout(ctx, want, candidate)
		if d.IsDir() {
			if ok {
				found = checkout
				return errStopWalk
			}
			return fs.SkipDir
		}
		if ok {
			found = checkout
			return errStopWalk
		}
		return nil
	})

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Checkout{}, false
	case errors.Is(err, errStopWalk):
		return found, true
	case err != nil:
		return Checkout{}, false
	default:
		return Checkout{}, false
	}
}

var errStopWalk = errors.New("checkout found")

func (r *Resolver) matchCheckout(ctx context.Context, want, path string) (Checkout, bool) {
	checkout := r.id.Identify(ctx, path)
	if checkout.Root == "" {
		return Checkout{}, false
	}
	got, ok := normalizeRepoLoose(checkout.Repo)
	if !ok || got != want {
		return Checkout{}, false
	}
	return checkout, true
}

func (r *Resolver) saveCache(cache home.RepoCache) error {
	if r.store == nil {
		return nil
	}
	return r.store.MergeRepoCache(cache)
}

func explicitPathFor(repo string, repos map[string]string) (string, bool) {
	if len(repos) == 0 {
		return "", false
	}
	if path, ok := repos[repo]; ok {
		return path, true
	}
	for key, path := range repos {
		norm, ok := normalizeRepoLoose(key)
		if ok && norm == repo {
			return path, true
		}
	}
	return "", false
}

func resolvePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not resolve ~: %w", err)
		}
		path = filepath.Join(homeDir, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Clean(path), nil
}

func normalizeRepo(repo string) (string, error) {
	norm, ok := normalizeRepoLoose(repo)
	if !ok {
		return "", fmt.Errorf("repository %q must be owner/name", repo)
	}
	return norm, nil
}

func normalizeRepoLoose(repo string) (string, bool) {
	repo = strings.TrimSpace(strings.TrimSuffix(repo, ".git"))
	repo = strings.Trim(repo, "/")
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return strings.ToLower(parts[0]) + "/" + strings.ToLower(parts[1]), true
}
