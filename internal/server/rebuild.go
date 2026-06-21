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
// It verifies compilation, then restarts via `go run ./cmd/devhub` so no
// binary is left in the repository (development use only).
// Returns 409 if repoRoot is empty (distributed binary with no source tree).
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
		// Build to a temp file outside the repository just to catch errors.
		// The binary is discarded; the actual restart uses `go run`.
		tmpExe := filepath.Join(os.TempDir(), "devhub_build_check.exe")
		_ = os.Remove(tmpExe)

		checkCmd := exec.Command("go", "build", "-o", tmpExe, "./cmd/devhub")
		checkCmd.Dir = repoRoot
		out, buildErr := checkCmd.CombinedOutput()
		_ = os.Remove(tmpExe)

		if buildErr != nil {
			setRebuildDone(buildErr.Error(), string(out))
			return
		}

		rebuildSt.mu.Lock()
		rebuildSt.running = false
		rebuildSt.done = true
		rebuildSt.output = string(out)
		rebuildSt.mu.Unlock()

		// Start `go run ./cmd/devhub` as the new process, then exit.
		runCmd := exec.Command("go", "run", "./cmd/devhub")
		runCmd.Dir = repoRoot
		runCmd.Args = append(runCmd.Args, os.Args[1:]...)
		runCmd.Env = append(os.Environ(), "DEVHUB_API_TOKEN="+token)
		runCmd.Stdout = os.Stdout
		runCmd.Stderr = os.Stderr
		runCmd.Stdin = os.Stdin
		if err := runCmd.Start(); err != nil {
			rebuildSt.mu.Lock()
			rebuildSt.errMsg = "起動に失敗: " + err.Error()
			rebuildSt.mu.Unlock()
			return
		}
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}()
	return nil
}

// handleRebuildStatus serves GET /api/rebuild/status.
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
// Returns empty string if not found (e.g. distributed binary).
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
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
