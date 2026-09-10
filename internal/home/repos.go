package home

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
)

// repoCacheMu protects cache read-modify-write updates across Store instances
// in one prutil process. The cache is an optimization, but dropping a parallel
// discovery result makes a configured discovery root unexpectedly expensive.
var repoCacheMu sync.Mutex

// RepoCache remembers discovered local checkout roots by repository.
type RepoCache struct {
	repos map[string]RepoCacheEntry
}

// RepoCacheEntry is one cached checkout mapping.
type RepoCacheEntry struct {
	// Path is the local checkout root.
	Path string `json:"path"`
}

type repoCacheDisk struct {
	Repos map[string]RepoCacheEntry `json:"repos"`
}

// NewRepoCache returns an empty repository cache.
func NewRepoCache() RepoCache {
	return RepoCache{repos: map[string]RepoCacheEntry{}}
}

// Lookup returns the cached entry for one repository.
func (c RepoCache) Lookup(repo string) (RepoCacheEntry, bool) {
	entry, ok := c.repos[repo]
	return entry, ok
}

// Set remembers one repository mapping.
func (c *RepoCache) Set(repo, path string) {
	c.ensure()
	repo = strings.TrimSpace(repo)
	path = strings.TrimSpace(path)
	if repo == "" || path == "" {
		return
	}
	c.repos[repo] = RepoCacheEntry{Path: path}
}

// Delete removes one cached repository mapping.
func (c *RepoCache) Delete(repo string) {
	if c == nil || c.repos == nil {
		return
	}
	delete(c.repos, repo)
}

func (c *RepoCache) ensure() {
	if c.repos == nil {
		c.repos = map[string]RepoCacheEntry{}
	}
}

func (c *RepoCache) normalize() {
	c.ensure()
	for rawRepo, entry := range c.repos {
		repo := strings.TrimSpace(rawRepo)
		entry.Path = strings.TrimSpace(entry.Path)
		if rawRepo != repo {
			delete(c.repos, rawRepo)
		}
		if repo == "" || entry.Path == "" {
			delete(c.repos, repo)
			continue
		}
		c.repos[repo] = entry
	}
}

// LoadRepoCache reads repos.json. A missing file is an empty cache.
func (s *Store) LoadRepoCache() (RepoCache, error) {
	data, err := os.ReadFile(s.Path(RepoCacheFile))
	if errors.Is(err, fs.ErrNotExist) {
		return NewRepoCache(), nil
	}
	if err != nil {
		return NewRepoCache(), fmt.Errorf("could not read %s: %w", s.Path(RepoCacheFile), err)
	}

	var disk repoCacheDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return NewRepoCache(), fmt.Errorf("could not read %s: %w", s.Path(RepoCacheFile), err)
	}
	cache := RepoCache{repos: disk.Repos}
	cache.normalize()
	return cache, nil
}

// SaveRepoCache writes repos.json atomically.
func (s *Store) SaveRepoCache(cache RepoCache) error {
	cache.normalize()
	data, err := json.MarshalIndent(repoCacheDisk{Repos: cache.repos}, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode the repository cache: %w", err)
	}
	return s.writeAtomic(RepoCacheFile, append(data, '\n'))
}

// MergeRepoCache merges cache entries with the latest on-disk cache before
// saving. It keeps simultaneous handoffs from replacing one another's newly
// discovered repository mappings.
func (s *Store) MergeRepoCache(cache RepoCache) error {
	repoCacheMu.Lock()
	defer repoCacheMu.Unlock()

	current, err := s.LoadRepoCache()
	if err != nil {
		return err
	}
	cache.normalize()
	for repo, entry := range cache.repos {
		current.Set(repo, entry.Path)
	}
	return s.SaveRepoCache(current)
}
