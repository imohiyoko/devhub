package git

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/pathutil"
)

// parseWorktreePorcelain parses `git worktree list --porcelain` into records.
// Each record has "path" and optionally "head"/"branch"/"detached"/"bare".
// The first record is the main worktree. Pure string parsing (unit-testable).
func parseWorktreePorcelain(text string) []map[string]any {
	var worktrees []map[string]any
	current := map[string]any{}
	flush := func() {
		if len(current) > 0 {
			worktrees = append(worktrees, current)
			current = map[string]any{}
		}
	}
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if key, val, found := strings.Cut(line, " "); found {
			switch key {
			case "worktree":
				flush()
				current = map[string]any{"path": val}
			case "HEAD":
				current["head"] = val
			case "branch":
				current["branch"] = strings.TrimPrefix(val, "refs/heads/")
			}
			continue
		}
		switch strings.TrimSpace(line) {
		case "detached":
			current["detached"] = true
		case "bare":
			current["bare"] = true
		}
	}
	flush()
	return worktrees
}

// ListWorktrees is the exported entry point used by the env-launcher to resolve
// (repo, branch) -> worktree path. Same behavior as the /api/git/worktrees core.
func (c *Controller) ListWorktrees(repoPath string) ([]map[string]any, error) {
	return listWorktrees(repoPath)
}

// listWorktrees returns the worktrees for repoPath annotated with is_main/exists.
func listWorktrees(repoPath string) ([]map[string]any, error) {
	out, stderr, timedOut, err := runCmd(repoPath, 15*time.Second, nil, "git", "worktree", "list", "--porcelain")
	if timedOut {
		return nil, httpx.Errorf(http.StatusGatewayTimeout, "git worktree list timed out")
	}
	if err != nil {
		return nil, httpx.Errorf(http.StatusBadRequest, "%s", stderr)
	}
	worktrees := parseWorktreePorcelain(out)
	for i, wt := range worktrees {
		wt["is_main"] = i == 0
		path, _ := wt["path"].(string)
		wt["exists"] = path != "" && pathutil.IsDir(path)
	}
	return worktrees, nil
}

// defaultWorktreePath derives a sibling path '<repo>-wt-<sanitized-branch>'.
func defaultWorktreePath(repoPath, branch string) string {
	return strings.TrimRight(repoPath, "/") + "-wt-" + nonBranchChar.ReplaceAllString(branch, "-")
}

// addWorktree runs `git worktree add`, validating inputs. Returns git stdout, or
// an *httpx.HTTPError carrying the worktree error status/message.
func addWorktree(repoPath, worktreePath, branch string, newBranch bool, baseCommit string) (string, error) {
	if worktreePath == "" || branch == "" {
		return "", httpx.Errorf(http.StatusBadRequest, "missing worktree_path or branch")
	}
	if !isValidBranchName(branch) {
		return "", httpx.Errorf(http.StatusBadRequest, "invalid branch name")
	}
	if msg := validateWorktreePath(worktreePath); msg != "" {
		return "", httpx.Errorf(http.StatusBadRequest, "%s", msg)
	}
	if baseCommit != "" {
		if strings.HasPrefix(baseCommit, "-") || strings.Contains(baseCommit, "..") || !baseCommitRe.MatchString(baseCommit) {
			return "", httpx.Errorf(http.StatusBadRequest, "invalid base commit/branch")
		}
	}

	// Prune stale worktrees before adding (best-effort).
	_, _, _, _ = runCmd(repoPath, 30*time.Second, nil, "git", "worktree", "prune")

	args := []string{"worktree", "add"}
	if newBranch {
		args = append(args, "-b", branch, worktreePath)
		if baseCommit != "" {
			args = append(args, baseCommit)
		}
	} else {
		args = append(args, worktreePath, branch)
	}

	stdout, stderr, timedOut, err := runCmd(repoPath, 120*time.Second, nil, "git", args...)
	if timedOut {
		return "", httpx.Errorf(http.StatusGatewayTimeout, "git worktree add timed out")
	}
	if err != nil {
		return "", httpx.Errorf(http.StatusBadRequest, "%s", stderr)
	}
	return stdout, nil
}

// ensureWorktreeFromPR fetches a GitHub PR's head and checks it out in a fresh
// worktree. Returns {output, worktree_path, branch, pr_number, used_gh} or an
// *httpx.HTTPError (including 409 when the branch already exists).
func ensureWorktreeFromPR(repoPath, prURL, worktreePath string) (map[string]any, error) {
	owner, repo, number, ok := parseGithubPRURL(prURL)
	if !ok {
		return nil, httpx.Errorf(http.StatusBadRequest, "invalid PR URL")
	}
	remote := remoteForGithubRepo(repoPath, owner, repo)
	if remote == "" {
		return nil, httpx.Errorf(http.StatusBadRequest, "この worktree に %s/%s を指す git remote がありません", owner, repo)
	}
	ghBranch := ghPRHeadBranch(owner, repo, number)
	usedGh := ghBranch != ""
	branch := ghBranch
	if branch == "" {
		branch = fmt.Sprintf("pr-%d", number)
	}
	if !isValidBranchName(branch) {
		return nil, httpx.Errorf(http.StatusBadRequest, "invalid branch name")
	}
	if worktreePath == "" {
		worktreePath = defaultWorktreePath(repoPath, branch)
	}
	if msg := validateWorktreePath(worktreePath); msg != "" {
		return nil, httpx.Errorf(http.StatusBadRequest, "%s", msg)
	}

	env := gitEnvHardenedC()
	prRef := fmt.Sprintf("pull/%d/head", number)
	if _, stderr, timedOut, err := runCmd(repoPath, 60*time.Second, env, "git", "fetch", remote, prRef); timedOut {
		return nil, httpx.Errorf(http.StatusGatewayTimeout, "git fetch timed out")
	} else if err != nil {
		return nil, httpx.Errorf(http.StatusBadRequest, "%s", stderr)
	}

	// Prune stale worktrees before adding (best-effort).
	_, _, _, _ = runCmd(repoPath, 30*time.Second, nil, "git", "worktree", "prune")

	stdout, stderr, timedOut, err := runCmd(repoPath, 120*time.Second, env, "git", "worktree", "add", "-b", branch, worktreePath, "FETCH_HEAD")
	if timedOut {
		return nil, httpx.Errorf(http.StatusGatewayTimeout, "git worktree add timed out")
	}
	if err != nil {
		if strings.Contains(stderr, "already exists") {
			return nil, &httpx.HTTPError{
				Status: http.StatusConflict,
				Msg:    fmt.Sprintf("ブランチ '%s' は既に存在します（この PR は取り込み済みかもしれません）", branch),
				Extra:  map[string]any{"stderr": stderr},
			}
		}
		return nil, httpx.Errorf(http.StatusBadRequest, "%s", stderr)
	}

	return map[string]any{
		"output":        stdout,
		"worktree_path": worktreePath,
		"branch":        branch,
		"pr_number":     number,
		"used_gh":       usedGh,
	}, nil
}
