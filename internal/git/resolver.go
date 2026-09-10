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
// config and cache store. A nil store means prutil has no application
// directory to remember discovered checkouts in, which is not a reason to
// refuse to look for one: discovery still runs, it is simply repeated next
// time.
func NewResolver(id Identifier, cfg home.Config, store CacheStore) *Resolver {
	if noStore(store) {
		store = noopCache{}
	}
	return &Resolver{id: id, cfg: cfg, store: store}
}

// noStore reports a cache there is no point calling, in either of the two
// shapes a caller can produce one. A plain nil interface is the obvious one.
// The other is a nil *home.Store put into the interface, which is not a nil
// interface, passes any ordinary nil check, and then dereferences a nil
// receiver on the first method call. Absorbing it here is what keeps the check
// in one place rather than at every use of the field.
func noStore(store CacheStore) bool {
	if store == nil {
		return true
	}
	s, ok := store.(*home.Store)
	return ok && s == nil
}

// noopCache stands in for the on-disk cache when there is nowhere to write it.
// It exists so that Resolve does not have to ask, on every path, whether it
// has a store; a nil check inside the method is also the check a nil pointer
// stored in a non-nil interface walks straight past.
type noopCache struct{}

// LoadRepoCache implements CacheStore.
func (noopCache) LoadRepoCache() (home.RepoCache, error) { return home.NewRepoCache(), nil }

// SaveRepoCache implements CacheStore.
func (noopCache) SaveRepoCache(home.RepoCache) error { return nil }

// MergeRepoCache implements CacheStore.
func (noopCache) MergeRepoCache(home.RepoCache) error { return nil }

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

	cache, err := r.store.LoadRepoCache()
	if err != nil {
		return Checkout{}, err
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

// discoverDepth is how far below a configured root a checkout is looked for.
// Code lives a directory or three down from the root somebody points at, and
// the cost of guessing high is every node_modules and vendor tree underneath.
const discoverDepth = 4

func (r *Resolver) discover(ctx context.Context, want, configuredRoot string) (Checkout, bool) {
	root, err := resolvePath(configuredRoot)
	if err != nil {
		return Checkout{}, false
	}
	rootDepth := len(strings.Split(filepath.Clean(root), string(filepath.Separator)))

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
		if d.IsDir() && d.Name() != ".git" &&
			len(strings.Split(path, string(filepath.Separator)))-rootDepth >= discoverDepth {
			return fs.SkipDir
		}
		if d.Name() != ".git" {
			return nil
		}

		candidate := filepath.Dir(path)
		// The origin remote is read out of the checkout's own config before
		// git is asked anything. A directory of two hundred checkouts is two
		// hundred small file reads rather than six hundred forked processes,
		// and only the one that matches costs a process at all.
		if !originMatches(path, want) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

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

// originMatches reports whether the checkout at gitPath names want as its
// origin, read from the repository's own config file.
//
// It is a filter, not an answer: a checkout it accepts is still confirmed by
// asking git, which is the only thing that knows about includes, conditional
// config and worktrees. A checkout it cannot read is passed through for git to
// judge, so a shape this does not parse costs a process rather than a miss.
func originMatches(gitPath, want string) bool {
	config := filepath.Join(gitPath, "config")
	if info, err := os.Stat(gitPath); err == nil && !info.IsDir() {
		// A linked worktree's .git is a file pointing at the real directory.
		// Following it is more trouble than the process it would save.
		return true
	}
	data, err := os.ReadFile(config)
	if err != nil {
		return true
	}

	inOrigin := false
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.HasPrefix(line, `[remote "origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != "url" {
			continue
		}
		got, ok := normalizeRepoLoose(ParseRemote(strings.TrimSpace(value)))
		return !ok || got == want
	}
	// No origin url found where one was expected: let git have the last word.
	return true
}

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
