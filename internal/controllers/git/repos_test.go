package git

import (
	"os"
	"path/filepath"
	"testing"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/storage"
)

func TestAllReposScanAndExclude(t *testing.T) {
	tree := t.TempDir()
	mustMkdir(t, filepath.Join(tree, "repoA", ".git"))         // direct repo
	mustMkdir(t, filepath.Join(tree, "group", "sub", ".git"))  // group/sub repo
	mustMkdir(t, filepath.Join(tree, "group", "sub2", ".git")) // group/sub2 repo
	mustMkdir(t, filepath.Join(tree, "plain"))                 // not a repo

	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveConfig(map[string]any{
		"scan_roots":   []any{tree},
		"excludes":     []any{filepath.Join(tree, "group", "sub2")},
		"pinned_repos": []any{},
	}); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, r := range New(st).AllRepos() {
		got[r.Name] = true
	}
	if !got["repoA"] {
		t.Error("missing repoA")
	}
	if !got["group/sub"] {
		t.Error("missing group/sub")
	}
	if got["group/sub2"] {
		t.Error("group/sub2 should be excluded")
	}
	if len(got) != 2 {
		t.Errorf("want 2 repos, got %d: %v", len(got), got)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
