package git

import (
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Conservative allowlist for branch names passed to git as positional args.
// Intentionally stricter than git's real ref grammar in exchange for a small,
// auditable character set.
var branchNameRe = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

// baseCommitRe is broader than branchNameRe (allows revspec chars ~^@{}#) because
// a base commit is a revspec, not a branch name. The dash/.. guards still apply.
var baseCommitRe = regexp.MustCompile(`^[a-zA-Z0-9_./~^@{}#-]+$`)

// worktreeBadChars rejects NUL and ASCII control chars (incl. newlines) in paths.
var worktreeBadChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// nonBranchChar matches characters not allowed in a sanitized default path slug.
var nonBranchChar = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// prURLRe matches a GitHub PR URL; the left boundary pins the host so look-alikes
// (evilgithub.com) do not match. The number is digits-only.
var prURLRe = regexp.MustCompile(`(?:^|[/@.])github\.com[/:]([^/]+)/([^/]+?)(?:\.git)?/pull/(\d+)(?:\b|/)`)

// remoteRepoRe extracts owner/repo from a github.com remote URL.
var remoteRepoRe = regexp.MustCompile(`github\.com[/:]([^/]+?)/([^/]+?)(?:\.git)?/?$`)

// pathSepRe splits a path on runs of forward/back slashes.
var pathSepRe = regexp.MustCompile(`[\\/]+`)

func hasPathTraversal(p string) bool {
	return slices.Contains(pathSepRe.Split(p, -1), "..")
}

// isValidBranchName reports whether branch is safe as a positional git argument.
func isValidBranchName(branch string) bool {
	return branch != "" &&
		!strings.HasPrefix(branch, "-") &&
		!strings.Contains(branch, "..") &&
		branchNameRe.MatchString(branch)
}

// validateWorktreePath returns an error message if the path is unsafe, else "".
// Requires an absolute path, no '..', no leading dash, no control characters.
func validateWorktreePath(p string) string {
	if p == "" {
		return "missing worktree path"
	}
	// Reject control chars before the absoluteness check so the result does not
	// depend on the OS's notion of "absolute" (on Windows "/a\nb" is not absolute,
	// so without this ordering the newline would be masked by the abs-path error).
	if strings.HasPrefix(p, "-") || hasPathTraversal(p) || worktreeBadChars.MatchString(p) {
		return "invalid worktree path"
	}
	if !filepath.IsAbs(p) {
		return "worktree path must be absolute"
	}
	return ""
}

// parseGithubPRURL returns owner, repo, number from a GitHub PR URL.
func parseGithubPRURL(url string) (owner, repo string, number int, ok bool) {
	m := prURLRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", "", 0, false
	}
	owner, repo = m[1], m[2]
	n, err := strconv.Atoi(m[3])
	if err != nil || owner == "" || repo == "" || strings.HasPrefix(owner, "-") || strings.HasPrefix(repo, "-") {
		return "", "", 0, false
	}
	return owner, repo, n, true
}

// normalizeGithubRemote returns lowercased "owner/repo" for a github.com remote URL.
func normalizeGithubRemote(url string) string {
	m := remoteRepoRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1] + "/" + m[2])
}

// remoteForGithubRepo returns the remote name pointing at github.com/<owner>/<repo>
// (preferring origin), or "" if none. Best-effort.
func remoteForGithubRepo(repoPath, owner, repo string) string {
	target := strings.ToLower(owner + "/" + repo)
	out, _, _, err := runCmd(repoPath, 10*time.Second, nil, "git", "remote", "-v")
	if err != nil {
		return ""
	}
	var matches []string
	for line := range strings.SplitSeq(out, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name, url := parts[0], parts[1]
		if strings.HasPrefix(name, "-") {
			continue
		}
		if normalizeGithubRemote(url) == target && !slices.Contains(matches, name) {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	if slices.Contains(matches, "origin") {
		return "origin"
	}
	return matches[0]
}

// ghPRHeadBranch returns the PR's real head branch via `gh`, or "" on any failure.
func ghPRHeadBranch(owner, repo string, number int) string {
	out, _, _, err := runCmd("", 15*time.Second, nil, "gh", "pr", "view", strconv.Itoa(number),
		"--repo", owner+"/"+repo, "--json", "headRefName", "-q", ".headRefName")
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(out)
	if isValidBranchName(name) {
		return name
	}
	return ""
}

// pollBuckets are the discrete local-status poll intervals (seconds).
var pollBuckets = []int{30, 60, 120, 300, 600}

func bucketizeInterval(seconds int) int {
	for _, b := range pollBuckets {
		if seconds <= b {
			return b
		}
	}
	return pollBuckets[len(pollBuckets)-1]
}

// baseMergeRef returns the ref used as the merge base for "merged" detection:
// origin/HEAD, falling back to local main then master, or "" if none.
func baseMergeRef(repoPath string) string {
	if out, _, _, err := runCmd(repoPath, 10*time.Second, nil, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref
		}
	}
	for _, name := range []string{"main", "master"} {
		if _, _, _, err := runCmd(repoPath, 10*time.Second, nil, "git", "rev-parse", "--verify", "--quiet", name); err == nil {
			return name
		}
	}
	return ""
}

// mergedBranchSet returns local branch short-names merged into baseRef, excluding
// the base branch itself. Best-effort (empty set on failure).
func mergedBranchSet(repoPath, baseRef string) map[string]bool {
	set := map[string]bool{}
	if baseRef == "" {
		return set
	}
	out, _, _, err := runCmd(repoPath, 15*time.Second, nil, "git", "branch", "--merged", baseRef, "--format=%(refname:short)")
	if err != nil {
		return set
	}
	for line := range strings.SplitSeq(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			set[s] = true
		}
	}
	delete(set, baseRef)
	if _, after, found := strings.Cut(baseRef, "/"); found {
		delete(set, after)
	}
	return set
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
