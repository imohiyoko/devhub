package git

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/pathutil"
)

// validatedPath resolves a user-supplied repo path to its canonical form, but
// only if it matches a configured repo (mirrors _get_validated_path).
func (c *Controller) validatedPath(raw string) string {
	if raw == "" {
		return ""
	}
	norm := pathutil.NormCase(pathutil.AbsExpand(raw))
	for _, r := range c.AllRepos() {
		if pathutil.NormCase(r.Path) == norm {
			return r.Path
		}
	}
	return ""
}

// writeRun maps a git command result to a JSON response: {output} (or {ok,output}
// when withOK), a 504 on timeout, or a 400 carrying stderr.
func writeRun(w http.ResponseWriter, withOK bool, stdout, stderr string, timedOut bool, err error, timeoutMsg string) error {
	if timedOut {
		return httpx.Errorf(http.StatusGatewayTimeout, "%s", timeoutMsg)
	}
	if err != nil {
		return httpx.Errorf(http.StatusBadRequest, "%s", stderr)
	}
	resp := map[string]any{"output": stdout}
	if withOK {
		resp["ok"] = true
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
	return nil
}

// HandleGet dispatches GET /api/git/* (and /api/repos is handled by the server).
func (c *Controller) HandleGet(w http.ResponseWriter, r *http.Request) error {
	repoPath := c.validatedPath(r.URL.Query().Get("path"))
	if repoPath == "" {
		return httpx.Errorf(http.StatusBadRequest, "invalid or missing repository path")
	}
	switch r.URL.Path {
	case "/api/git/status":
		return c.handleStatus(w, r, repoPath)
	case "/api/git/log":
		n := 100
		if v := r.URL.Query().Get("n"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil {
				n = parsed
			}
		}
		n = max(1, min(n, 1000))
		stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", "log", "--oneline", "--decorate", "--graph", fmt.Sprintf("-n%d", n))
		return writeRun(w, false, stdout, stderr, false, err, "")
	case "/api/git/branches":
		fmtArg := "%(refname)\t%(refname:short)\t%(HEAD)\t%(committername)\t%(committerdate:relative)\t%(committerdate:iso)"
		stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", "branch", "-a", "--sort=-committerdate", "--format="+fmtArg)
		return writeRun(w, false, stdout, stderr, false, err, "")
	case "/api/git/diff":
		file := r.URL.Query().Get("file")
		if file == "" {
			return httpx.Errorf(http.StatusBadRequest, "empty file path")
		}
		args := []string{"diff"}
		if r.URL.Query().Get("staged") == "1" {
			args = append(args, "--cached")
		}
		args = append(args, "--", file)
		stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", args...)
		return writeRun(w, false, stdout, stderr, false, err, "")
	case "/api/git/stash/list":
		stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", "stash", "list")
		return writeRun(w, false, stdout, stderr, false, err, "")
	case "/api/git/worktrees":
		return c.handleWorktrees(w, repoPath)
	}
	return httpx.Errorf(http.StatusNotFound, "not found")
}

func (c *Controller) handleStatus(w http.ResponseWriter, r *http.Request, repoPath string) error {
	stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", "status", "--porcelain=v1", "-u")
	if err != nil {
		return httpx.Errorf(http.StatusBadRequest, "%s", stderr)
	}
	payload := map[string]any{"output": stdout}

	if r.URL.Query().Get("suggest") != "" {
		dynamic := 600 // slow ceiling: used with <2 commits/hour or on git failure
		if logOut, _, _, lerr := runCmd(repoPath, 10*time.Second, nil, "git", "log", "--since=1 hour ago", "--format=%ct"); lerr == nil {
			var ts []int64
			for line := range strings.SplitSeq(logOut, "\n") {
				s := strings.TrimSpace(line)
				if isDigits(s) {
					if v, e := strconv.ParseInt(s, 10, 64); e == nil {
						ts = append(ts, v)
					}
				}
			}
			if len(ts) >= 2 {
				var sum int64
				for i := 0; i+1 < len(ts); i++ {
					d := ts[i] - ts[i+1]
					if d < 0 {
						d = -d
					}
					sum += d
				}
				avg := float64(sum) / float64(len(ts)-1)
				dynamic = bucketizeInterval(int(avg / 4))
			}
		}
		payload["suggested_local_interval"] = dynamic
		payload["suggested_remote_interval"] = dynamic * 3
		if remoteOut, _, _, rerr := runCmd(repoPath, 10*time.Second, nil, "git", "remote"); rerr == nil {
			payload["has_remote"] = strings.TrimSpace(remoteOut) != ""
		}
	}
	httpx.WriteJSON(w, http.StatusOK, payload)
	return nil
}

func (c *Controller) handleWorktrees(w http.ResponseWriter, repoPath string) error {
	worktrees, err := listWorktrees(repoPath)
	if err != nil {
		return err
	}
	baseRef := baseMergeRef(repoPath)
	merged := mergedBranchSet(repoPath, baseRef)
	for _, wt := range worktrees {
		branch, _ := wt["branch"].(string)
		wt["merged"] = branch != "" && merged[branch]
	}
	mergedList := make([]string, 0, len(merged))
	for b := range merged {
		mergedList = append(mergedList, b)
	}
	sort.Strings(mergedList)
	var baseBranch any
	if baseRef != "" {
		baseBranch = baseRef
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"worktrees":       worktrees,
		"base_branch":     baseBranch,
		"merged_branches": mergedList,
	})
	return nil
}

// HandlePost dispatches POST /api/git/*.
func (c *Controller) HandlePost(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	repoPath := c.validatedPath(strData(data, "path"))
	if repoPath == "" {
		return httpx.Errorf(http.StatusBadRequest, "invalid or missing repository path")
	}
	switch r.URL.Path {
	case "/api/git/stage":
		files, ok := filesArg(data)
		if !ok {
			return httpx.Errorf(http.StatusBadRequest, "invalid files list")
		}
		stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", append([]string{"add", "--"}, files...)...)
		return writeRun(w, true, stdout, stderr, false, err, "")
	case "/api/git/unstage":
		files, ok := filesArg(data)
		if !ok {
			return httpx.Errorf(http.StatusBadRequest, "invalid files list")
		}
		stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", append([]string{"restore", "--staged", "--"}, files...)...)
		return writeRun(w, true, stdout, stderr, false, err, "")
	case "/api/git/commit":
		msg := strData(data, "message")
		if msg == "" {
			return httpx.Errorf(http.StatusBadRequest, "no message specified")
		}
		stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", "commit", "-m", msg)
		return writeRun(w, true, stdout, stderr, false, err, "")
	case "/api/git/push":
		stdout, stderr, timedOut, err := runCmd(repoPath, 60*time.Second, nil, "git", "push")
		return writeRun(w, true, stdout, stderr, timedOut, err, "push timed out")
	case "/api/git/pull":
		stdout, stderr, timedOut, err := runCmd(repoPath, 60*time.Second, nil, "git", "pull")
		return writeRun(w, true, stdout, stderr, timedOut, err, "pull timed out")
	case "/api/git/checkout":
		branch := strData(data, "branch")
		if !isValidBranchName(branch) {
			return httpx.Errorf(http.StatusBadRequest, "invalid branch name")
		}
		stdout, stderr, timedOut, err := runCmd(repoPath, 30*time.Second, nil, "git", "checkout", branch)
		return writeRun(w, true, stdout, stderr, timedOut, err, "git checkout timed out")
	case "/api/git/branch/create":
		branch := strData(data, "branch")
		if !isValidBranchName(branch) {
			return httpx.Errorf(http.StatusBadRequest, "invalid branch name")
		}
		stdout, stderr, timedOut, err := runCmd(repoPath, 30*time.Second, nil, "git", "checkout", "-b", branch)
		return writeRun(w, true, stdout, stderr, timedOut, err, "git branch create timed out")
	case "/api/git/stash":
		return c.handleStash(w, repoPath, data)
	case "/api/git/worktree/add":
		output, err := addWorktree(repoPath, strData(data, "worktree_path"), strData(data, "branch"), boolData(data, "new_branch"), strData(data, "base_commit"))
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "output": output})
		return nil
	case "/api/git/worktree/remove":
		return c.handleWorktreeRemove(w, repoPath, data)
	case "/api/git/worktree/pull":
		wt := strData(data, "worktree_path")
		if wt == "" {
			return httpx.Errorf(http.StatusBadRequest, "missing worktree_path")
		}
		if msg := validateWorktreePath(wt); msg != "" {
			return httpx.Errorf(http.StatusBadRequest, "%s", msg)
		}
		stdout, stderr, timedOut, err := runCmd(wt, 60*time.Second, gitEnvHardened(), "git", "pull")
		return writeRun(w, true, stdout, stderr, timedOut, err, "pull timed out")
	case "/api/git/worktree/prune":
		stdout, stderr, timedOut, err := runCmd(repoPath, 30*time.Second, nil, "git", "worktree", "prune", "-v")
		return writeRun(w, true, stdout, stderr, timedOut, err, "git worktree prune timed out")
	case "/api/git/branch/delete":
		branch := strData(data, "branch")
		if branch == "" {
			return httpx.Errorf(http.StatusBadRequest, "missing branch name")
		}
		if !isValidBranchName(branch) {
			return httpx.Errorf(http.StatusBadRequest, "invalid branch name")
		}
		flag := "-d"
		if boolData(data, "force") {
			flag = "-D"
		}
		stdout, stderr, timedOut, err := runCmd(repoPath, 30*time.Second, nil, "git", "branch", flag, branch)
		return writeRun(w, true, stdout, stderr, timedOut, err, "git branch delete timed out")
	case "/api/git/fetch":
		stdout, stderr, timedOut, err := runCmd(repoPath, 30*time.Second, gitEnvHardened(), "git", "fetch", "--prune")
		return writeRun(w, true, stdout, stderr, timedOut, err, "git fetch timed out")
	case "/api/git/worktree/from-pr":
		result, err := ensureWorktreeFromPR(repoPath, strData(data, "pr_url"), strData(data, "worktree_path"))
		if err != nil {
			return err
		}
		result["ok"] = true
		httpx.WriteJSON(w, http.StatusOK, result)
		return nil
	}
	return httpx.Errorf(http.StatusNotFound, "not found")
}

func (c *Controller) handleStash(w http.ResponseWriter, repoPath string, data map[string]any) error {
	action := strData(data, "action")
	args := []string{"stash"}
	switch action {
	case "push":
		args = append(args, action)
	case "pop", "drop":
		args = append(args, action)
		idx, ok := wholeInt(data["index"])
		if !ok {
			return httpx.Errorf(http.StatusBadRequest, "invalid index")
		}
		args = append(args, fmt.Sprintf("stash@{%d}", idx))
	default:
		return httpx.Errorf(http.StatusBadRequest, "invalid action")
	}
	stdout, stderr, _, err := runCmd(repoPath, 0, nil, "git", args...)
	return writeRun(w, true, stdout, stderr, false, err, "")
}

func (c *Controller) handleWorktreeRemove(w http.ResponseWriter, repoPath string, data map[string]any) error {
	wt := strData(data, "worktree_path")
	if wt == "" {
		return httpx.Errorf(http.StatusBadRequest, "missing worktree_path")
	}
	if msg := validateWorktreePath(wt); msg != "" {
		return httpx.Errorf(http.StatusBadRequest, "%s", msg)
	}
	args := []string{"worktree", "remove"}
	if boolData(data, "force") {
		args = append(args, "--force")
	}
	args = append(args, wt)
	stdout, stderr, timedOut, err := runCmd(repoPath, 60*time.Second, nil, "git", args...)
	if timedOut {
		return httpx.Errorf(http.StatusGatewayTimeout, "git worktree remove timed out")
	}
	if err != nil {
		return httpx.Errorf(http.StatusBadRequest, "%s", stderr)
	}
	_, _, _, _ = runCmd(repoPath, 30*time.Second, nil, "git", "worktree", "prune") // best-effort
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "output": stdout})
	return nil
}

// --- small JSON-value helpers ---

func strData(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

func boolData(data map[string]any, key string) bool {
	b, _ := data[key].(bool)
	return b
}

// wholeInt accepts a JSON number that is integer-valued (index for stash pop/drop).
func wholeInt(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || float64(int(f)) != f {
		return 0, false
	}
	return int(f), true
}

// filesArg validates data["files"] as a list of non-empty strings (absent -> empty).
func filesArg(data map[string]any) ([]string, bool) {
	v, ok := data["files"]
	if !ok {
		return []string{}, true
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
