package workspace

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	devhub "github.com/imohiyoko/devhub"
	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/storage"
)

// newController wires a workspace controller over a fresh on-disk store, the
// hermetic style used across the controller tests (temp dir + real SQLite).
func newController(t *testing.T) (*Controller, *storage.Store) {
	t.Helper()
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, gitctl.New(st)), st
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// wantStatus asserts err is an *httpx.HTTPError carrying the given status.
func wantStatus(t *testing.T, err error, code int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with status %d, got nil", code)
	}
	var he *httpx.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("expected *httpx.HTTPError, got %T: %v", err, err)
	}
	if he.Status != code {
		t.Errorf("status = %d, want %d", he.Status, code)
	}
}

type lsEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsGit       bool   `json:"is_git"`
	InWorkspace bool   `json:"in_workspace"`
}

type lsResponse struct {
	Path    string    `json:"path"`
	Parent  any       `json:"parent"`
	IsGit   bool      `json:"is_git"`
	Entries []lsEntry `json:"entries"`
}

func lsGet(t *testing.T, c *Controller, path string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/ls?path="+url.QueryEscape(path), nil)
	return rec, c.HandleLs(rec, req)
}

// TestHandleLs lists a directory: only visible subdirectories are returned
// (dotfiles and plain files are skipped), each is annotated is_git, and repos
// known to the git controller are annotated in_workspace.
func TestHandleLs(t *testing.T) {
	tree := t.TempDir()
	mustMkdir(t, filepath.Join(tree, "projectA", ".git")) // a git repo
	mustMkdir(t, filepath.Join(tree, "plain"))            // dir, not a repo
	mustMkdir(t, filepath.Join(tree, ".hidden"))          // dotfile dir -> skipped
	if err := os.WriteFile(filepath.Join(tree, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err) // regular file -> skipped (only dirs are listed)
	}

	c, st := newController(t)
	// Pin projectA so the git controller reports it; keep scans hermetic.
	if err := st.SaveConfig(map[string]any{
		"scan_roots":   []any{},
		"pinned_repos": []any{filepath.Join(tree, "projectA")},
		"excludes":     []any{},
	}); err != nil {
		t.Fatal(err)
	}

	rec, err := lsGet(t, c, tree)
	if err != nil {
		t.Fatalf("HandleLs: %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp lsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	byName := map[string]lsEntry{}
	for _, e := range resp.Entries {
		byName[e.Name] = e
	}
	if len(byName) != 2 {
		t.Fatalf("entries = %v, want exactly projectA and plain", resp.Entries)
	}
	if _, ok := byName[".hidden"]; ok {
		t.Error("dotfile dir .hidden should be filtered out")
	}
	if _, ok := byName["notes.txt"]; ok {
		t.Error("plain file notes.txt should be filtered out (dirs only)")
	}

	if a := byName["projectA"]; !a.IsGit {
		t.Error("projectA should be is_git")
	} else if !a.InWorkspace {
		t.Error("projectA is a pinned repo, should be in_workspace")
	}
	if p := byName["plain"]; p.IsGit {
		t.Error("plain should not be is_git")
	} else if p.InWorkspace {
		t.Error("plain should not be in_workspace")
	}

	// The listed directory itself is not a repo, and it has a parent above it.
	if resp.IsGit {
		t.Error("the listed tree is not a git repo")
	}
	if parent, ok := resp.Parent.(string); !ok || parent == "" {
		t.Errorf("parent = %v, want a non-empty path", resp.Parent)
	}
}

// TestHandleLsRejectsNonDirectory: a path that is a file (not a dir) is a 400.
func TestHandleLsRejectsNonDirectory(t *testing.T) {
	c, _ := newController(t)
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := lsGet(t, c, file)
	wantStatus(t, err, 400)
}

// TestHandleOpenRejectsInvalidPath: a missing or non-directory path is a 400,
// before any editor is launched.
func TestHandleOpenRejectsInvalidPath(t *testing.T) {
	c, _ := newController(t)

	for _, tc := range []struct {
		name string
		path string
	}{
		{"missing", ""},
		{"nonexistent", filepath.Join(t.TempDir(), "nope")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/open?path="+url.QueryEscape(tc.path), nil)
			wantStatus(t, c.HandleOpen(rec, req), 400)
		})
	}
}

// TestHandleOpenValidDir: a valid directory returns {"ok":true}. The editor is
// set to a command that does not exist, so the fire-and-forget launch fails
// silently and no real editor is spawned by the test.
func TestHandleOpenValidDir(t *testing.T) {
	c, st := newController(t)
	if err := st.SaveSettings(map[string]any{"editor": "devhub-nonexistent-editor-xyz"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/open?path="+url.QueryEscape(dir), nil)
	if err := c.HandleOpen(rec, req); err != nil {
		t.Fatalf("HandleOpen: %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if out["ok"] != true {
		t.Errorf("body = %v, want ok:true", out)
	}
}

// TestEditorReflectsSettingChange documents that the editor is read live from
// the store on every open — there is no per-process settings snapshot to go
// stale — so a POST /api/settings change takes effect on the next launch with
// no server restart (issue #84, settings hot-reload).
func TestEditorReflectsSettingChange(t *testing.T) {
	c, st := newController(t)
	if err := st.SaveSettings(map[string]any{"editor": "code"}); err != nil {
		t.Fatal(err)
	}
	if got := c.editor(); got != "code" {
		t.Fatalf("editor = %q, want code", got)
	}
	// Change the setting on the same controller instance: no restart, no rebuild.
	if err := st.SaveSettings(map[string]any{"editor": "cursor"}); err != nil {
		t.Fatal(err)
	}
	if got := c.editor(); got != "cursor" {
		t.Errorf("editor = %q after settings change, want cursor (hot reload)", got)
	}
}
