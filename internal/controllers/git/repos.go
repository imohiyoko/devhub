// Package git implements the /api/repos and /api/git/* endpoints. This file
// holds repo discovery and listing; the git command endpoints live in the
// other files of this package.
package git

import (
	"log"
	"os"
	"path/filepath"

	"github.com/imohiyoko/devhub/internal/pathutil"
)

// configStore is the narrow persistence the git controller needs: it only reads
// the git-tool config document (scan_roots / pinned_repos / excludes …) to
// discover repos. It reads a shared document rather than owning a keyspace, so
// it depends on the typed LoadConfig helper, not the raw key/value seam.
// *storage.Store satisfies it.
type configStore interface {
	LoadConfig() (map[string]any, error)
}

// Controller serves repo discovery and git operations backed by the store.
type Controller struct{ store configStore }

// New returns a git controller.
func New(store configStore) *Controller { return &Controller{store: store} }

// Repo is a discovered git repository (name + absolute path).
type Repo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// findRepos scans a single root: each immediate child that is a git repo is
// returned; non-repo children are scanned one level deeper (group/name).
func findRepos(root string) []Repo {
	root = pathutil.ExpandUser(root)
	var repos []Repo
	entries, err := os.ReadDir(root) // sorted by name; perm/missing -> empty
	if err != nil {
		return repos
	}
	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		if !pathutil.IsDir(full) {
			continue
		}
		if pathutil.Exists(filepath.Join(full, ".git")) {
			repos = append(repos, Repo{Name: e.Name(), Path: full})
			continue
		}
		subs, err := os.ReadDir(full)
		if err != nil {
			continue
		}
		for _, s := range subs {
			sfull := filepath.Join(full, s.Name())
			if pathutil.IsDir(sfull) && pathutil.Exists(filepath.Join(sfull, ".git")) {
				repos = append(repos, Repo{Name: e.Name() + "/" + s.Name(), Path: sfull})
			}
		}
	}
	return repos
}

// AllRepos returns the deduplicated repos from scan_roots and pinned_repos,
// honoring excludes.
func (c *Controller) AllRepos() []Repo {
	cfg, err := c.store.LoadConfig()
	if err != nil {
		// Don't mask storage failures as "no repos": surface them in the log so
		// an empty repo list is debuggable rather than silently wrong.
		log.Printf("git: AllRepos LoadConfig failed: %v", err)
	}
	excludes := map[string]bool{}
	for _, p := range asStringSlice(cfg["excludes"]) {
		excludes[pathutil.NormCase(pathutil.AbsExpand(p))] = true
	}
	seen := map[string]bool{}
	repos := []Repo{}

	add := func(name, path string) {
		c := pathutil.NormCase(path)
		if seen[c] || excludes[c] {
			return
		}
		seen[c] = true
		repos = append(repos, Repo{Name: name, Path: path})
	}

	for _, root := range asStringSlice(cfg["scan_roots"]) {
		for _, r := range findRepos(root) {
			add(r.Name, pathutil.AbsClean(r.Path))
		}
	}

	for _, p := range asStringSlice(cfg["pinned_repos"]) {
		expanded := pathutil.AbsExpand(p)
		if excludes[pathutil.NormCase(expanded)] || !pathutil.IsDir(expanded) {
			continue
		}
		if pathutil.Exists(filepath.Join(expanded, ".git")) {
			add(filepath.Base(expanded), expanded)
			continue
		}
		sub := findRepos(expanded)
		if len(sub) > 0 {
			for _, r := range sub {
				add(r.Name, pathutil.AbsClean(r.Path))
			}
		} else {
			add(filepath.Base(expanded), expanded)
		}
	}
	return repos
}

// asStringSlice coerces a JSON array value into a []string, skipping non-strings.
func asStringSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
