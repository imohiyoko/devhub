// Package workspace implements the directory browser (/api/ls) and editor open
// (/api/open) endpoints.
package workspace

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/pathutil"
	"github.com/imohiyoko/devhub/internal/platform"
)

// darwinApps maps an editor id to its macOS application name for `open -a`.
var darwinApps = map[string]string{"code": "Visual Studio Code", "cursor": "Cursor", "windsurf": "Windsurf"}

// settingsReader is the narrow persistence the workspace controller needs: it
// only reads the shared settings document (for the configured editor). It reads
// the global document rather than owning a keyspace, so it depends on the typed
// LoadSettings helper, not the raw key/value seam. *storage.Store satisfies it.
type settingsReader interface {
	LoadSettings() (map[string]any, error)
}

// Controller serves workspace endpoints; it consults git for the repo list.
type Controller struct {
	store settingsReader
	git   *gitctl.Controller
}

// New returns a workspace controller.
func New(store settingsReader, git *gitctl.Controller) *Controller {
	return &Controller{store: store, git: git}
}

func (c *Controller) editor() string {
	settings, _ := c.store.LoadSettings()
	if e, ok := settings["editor"].(string); ok && e != "" {
		return e
	}
	return "code"
}

// OpenInEditor opens path in the configured editor (exported for the env-launcher).
func (c *Controller) OpenInEditor(path string) { openInEditor(c.editor(), path) }

func openInEditor(editor, path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		resolved := editor
		if p, err := exec.LookPath(editor); err == nil {
			resolved = p
		}
		cmd = exec.Command(resolved, path) //execaudit:workspace-editor
	case "darwin":
		if app, ok := darwinApps[editor]; ok {
			cmd = exec.Command("open", "-a", app, path) //execaudit:workspace-editor
		} else {
			cmd = exec.Command(editor, path) //execaudit:workspace-editor
		}
	default:
		cmd = exec.Command(editor, path) //execaudit:workspace-editor
	}
	_ = cmd.Start() // fire-and-forget, matching subprocess.Popen
}

// HandleOpen serves GET /api/open?path=<dir>: opens the directory in the editor.
// The path is used as-is (the frontend sends absolute paths from /api/ls).
func (c *Controller) HandleOpen(w http.ResponseWriter, r *http.Request) error {
	target := r.URL.Query().Get("path")
	if target == "" || !pathutil.IsDir(target) {
		return httpx.Errorf(http.StatusBadRequest, "invalid path")
	}
	openInEditor(c.editor(), target)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// HandleLs serves GET /api/ls?path=<dir>: lists subdirectories with git/workspace
// annotations. Defaults to ~; on Windows the sentinel __drives__ lists drives.
func (c *Controller) HandleLs(w http.ResponseWriter, r *http.Request) error {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		raw = "~"
	}

	if platform.IsWindows() && raw == "__drives__" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"path": "__drives__", "parent": nil, "is_git": false, "entries": listDrives(),
		})
		return nil
	}

	target := pathutil.AbsExpand(raw)
	if !pathutil.IsDir(target) {
		return httpx.Errorf(http.StatusBadRequest, "not a directory")
	}

	wsPaths := map[string]bool{}
	for _, rp := range c.git.AllRepos() {
		wsPaths[pathutil.NormCase(rp.Path)] = true
	}

	dirents, err := os.ReadDir(target) // sorted by name
	if err != nil {
		if os.IsPermission(err) {
			return httpx.Errorf(http.StatusForbidden, "permission denied")
		}
		return err
	}
	entries := []map[string]any{}
	for _, e := range dirents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(target, name)
		if !pathutil.IsDir(full) { // follows symlinks, like scandir.is_dir()
			continue
		}
		entries = append(entries, map[string]any{
			"name":         name,
			"path":         full,
			"is_git":       pathutil.Exists(filepath.Join(full, ".git")),
			"in_workspace": wsPaths[pathutil.NormCase(full)],
		})
	}

	parentDir := filepath.Dir(target)
	var parent any
	if parentDir != target {
		parent = parentDir
	} else if platform.IsWindows() {
		parent = "__drives__"
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"path":    target,
		"parent":  parent,
		"is_git":  pathutil.Exists(filepath.Join(target, ".git")),
		"entries": entries,
	})
	return nil
}

// listDrives probes A:..Z: (avoids a Windows-only syscall dependency).
func listDrives() []map[string]any {
	entries := []map[string]any{}
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if pathutil.Exists(root) {
			entries = append(entries, map[string]any{
				"name":         string(c) + ":",
				"path":         filepath.Clean(root),
				"is_git":       false,
				"in_workspace": false,
			})
		}
	}
	return entries
}
