package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRepoRootFrom(t *testing.T) {
	// Build a temp tree: root/sub/deep, with go.mod only at root.
	root := t.TempDir()
	deep := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("from root itself", func(t *testing.T) {
		got := findRepoRootFrom(root)
		if got != root {
			t.Errorf("got %q, want %q", got, root)
		}
	})

	t.Run("from nested dir", func(t *testing.T) {
		got := findRepoRootFrom(deep)
		if got != root {
			t.Errorf("got %q, want %q", got, root)
		}
	})

	t.Run("no go.mod in subtree", func(t *testing.T) {
		// Create an isolated tree with no go.mod anywhere in it.
		// Use filepath.VolumeName to build a path under the volume root that
		// we fully control, ensuring no ancestor within our tree has go.mod.
		nomod := t.TempDir()
		child := filepath.Join(nomod, "child")
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatal(err)
		}
		// Walk only within nomod — stop before reaching real ancestors by
		// passing child and verifying the result is NOT inside nomod.
		// Since TempDir's ancestors may have go.mod (e.g. home dir), we only
		// assert that the result is not the child dir itself nor nomod, meaning
		// findRepoRootFrom did not find a false positive inside our controlled tree.
		got := findRepoRootFrom(child)
		if got == child || got == nomod {
			t.Errorf("found unexpected go.mod inside controlled tree: %q", got)
		}
	})
}
