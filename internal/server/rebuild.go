package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/imohiyoko/devhub/internal/httpx"
)

type rebuildState struct {
	mu      sync.Mutex
	running bool
	done    bool
	errMsg  string
	output  string
}

var rebuildSt rebuildState

// handleRebuild serves POST /api/rebuild.
// It verifies compilation via a unique temp binary (discarded after the check),
// then restarts via `go run ./cmd/devhub` so no binary is left in the repository.
// Returns 409 when repoRoot is empty (distributed binary with no source tree)
// or when a build is already in progress.
func (s *Server) handleRebuild(w http.ResponseWriter, _ *http.Request) error {
	if s.repoRoot == "" {
		return httpx.Errorf(http.StatusConflict, "ソースツリーが見つかりません（配布バイナリでは使用できません）")
	}

	rebuildSt.mu.Lock()
	if rebuildSt.running {
		rebuildSt.mu.Unlock()
		return httpx.Errorf(http.StatusConflict, "ビルドが既に実行中です")
	}
	rebuildSt.running = true
	rebuildSt.done = false
	rebuildSt.errMsg = ""
	rebuildSt.output = ""
	rebuildSt.mu.Unlock()

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	token := s.token
	repoRoot := s.repoRoot
	go func() {
		// Use a unique temp file to avoid collisions across concurrent instances.
		tmp, err := os.CreateTemp("", "devhub_build_check-*")
		if err != nil {
			setRebuildDone("一時ファイル作成に失敗: "+err.Error(), "")
			return
		}
		tmpExe := tmp.Name()
		_ = tmp.Close()
		defer os.Remove(tmpExe)

		checkCmd := exec.Command("go", "build", "-o", tmpExe, "./cmd/devhub")
		checkCmd.Dir = repoRoot
		out, buildErr := checkCmd.CombinedOutput()
		if buildErr != nil {
			setRebuildDone(buildErr.Error(), string(out))
			return
		}

		// Start `go run ./cmd/devhub` as the new process.
		// Mark done only after Start succeeds so the frontend never sees
		// done=true,error="" while the restart has already silently failed.
		runCmd := exec.Command("go", "run", "./cmd/devhub")
		runCmd.Dir = repoRoot
		runCmd.Args = append(runCmd.Args, os.Args[1:]...)
		runCmd.Env = append(os.Environ(), "DEVHUB_API_TOKEN="+token)
		runCmd.Stdout = os.Stdout
		runCmd.Stderr = os.Stderr
		runCmd.Stdin = os.Stdin
		if err := runCmd.Start(); err != nil {
			setRebuildDone("起動に失敗: "+err.Error(), string(out))
			return
		}

		rebuildSt.mu.Lock()
		rebuildSt.running = false
		rebuildSt.done = true
		rebuildSt.output = string(out)
		rebuildSt.mu.Unlock()

		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}()
	return nil
}

// handleRebuildStatus serves GET /api/rebuild/status.
// The available field indicates whether rebuild is supported (source tree present).
func (s *Server) handleRebuildStatus(w http.ResponseWriter, _ *http.Request) error {
	rebuildSt.mu.Lock()
	defer rebuildSt.mu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"running":   rebuildSt.running,
		"done":      rebuildSt.done,
		"error":     rebuildSt.errMsg,
		"output":    rebuildSt.output,
		"available": s.repoRoot != "",
	})
	return nil
}

func setRebuildDone(errMsg, output string) {
	rebuildSt.mu.Lock()
	rebuildSt.running = false
	rebuildSt.done = true
	rebuildSt.errMsg = errMsg
	rebuildSt.output = output
	rebuildSt.mu.Unlock()
}

// findRepoRoot walks up from the working directory looking for go.mod.
// Returns an empty string if not found (e.g. distributed binary without source).
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return findRepoRootFrom(dir)
}

// findRepoRootFrom walks up from dir looking for go.mod. Separated from
// findRepoRoot so tests can pass an arbitrary start directory without chdir.
func findRepoRootFrom(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
