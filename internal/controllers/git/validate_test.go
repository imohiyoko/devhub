package git

import "testing"

func TestIsValidBranchName(t *testing.T) {
	valid := []string{"main", "feat/x", "a_b.c-d", "release/1.2.3", "worktree-feat+go-migration"}
	for _, b := range valid {
		if !isValidBranchName(b) {
			t.Errorf("isValidBranchName(%q) = false, want true", b)
		}
	}
	// Only a *leading* dash is rejected (argument-injection guard); a mid-string
	// "--" like "feat/--evil" is a valid branch name, matching the allowlist.
	if !isValidBranchName("feat/--evil") {
		t.Error("feat/--evil should be valid (only leading dash is rejected)")
	}
	invalid := []string{"", "-x", "a..b", "a b", "a~b", "br$"}
	for _, b := range invalid {
		if isValidBranchName(b) {
			t.Errorf("isValidBranchName(%q) = true, want false", b)
		}
	}
}

func TestParseGithubPRURL(t *testing.T) {
	cases := []struct {
		url         string
		wantOK      bool
		owner, repo string
		number      int
	}{
		{"https://github.com/owner/repo/pull/123", true, "owner", "repo", 123},
		{"github.com/owner/repo/pull/123", true, "owner", "repo", 123},
		{"https://www.github.com/owner/repo/pull/123", true, "owner", "repo", 123},
		{"git@github.com:owner/repo.git/pull/5", true, "owner", "repo", 5},
		{"https://github.com/o/r/pull/9/files", true, "o", "r", 9},
		{"https://evilgithub.com/o/r/pull/1", false, "", "", 0},
		{"https://evil.test/github.com/o/r/pull/1", false, "", "", 0},
		{"https://github.com/-bad/r/pull/1", false, "", "", 0},
		{"not a url", false, "", "", 0},
	}
	for _, c := range cases {
		owner, repo, number, ok := parseGithubPRURL(c.url)
		if ok != c.wantOK {
			t.Errorf("parseGithubPRURL(%q) ok=%v want %v", c.url, ok, c.wantOK)
			continue
		}
		if ok && (owner != c.owner || repo != c.repo || number != c.number) {
			t.Errorf("parseGithubPRURL(%q) = (%q,%q,%d) want (%q,%q,%d)", c.url, owner, repo, number, c.owner, c.repo, c.number)
		}
	}
}

func TestValidateWorktreePath(t *testing.T) {
	// Absolute-path cases use a unix-style path; this test runs on the dev OS.
	cases := []struct{ in, want string }{
		{"", "missing worktree path"},
		{"-x", "invalid worktree path"},
		{"/a/../b", "invalid worktree path"},
		{"rel/path", "worktree path must be absolute"},
		{"/a\nb", "invalid worktree path"},
	}
	for _, c := range cases {
		if got := validateWorktreePath(c.in); got != c.want {
			t.Errorf("validateWorktreePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeGithubRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/Owner/Repo.git":  "owner/repo",
		"git@github.com:Owner/Repo":          "owner/repo",
		"org-1234@github.com:Owner/Repo.git": "owner/repo",
		"git@ssh.github.com:Owner/Repo.git":  "owner/repo",
		"git@github.com:owner/repo?ref=x":    "",
		"git@github.com:owner/repo#frag":     "",
		"git@github.com:owner/repo.git?x":    "",
		"ssh://git@github.com/o/r.git":       "o/r",
		"https://GitHub.com/Owner/Repo/":     "owner/repo",
		"https://www.github.com/Owner/Repo":  "owner/repo",
		"https://evilgithub.com/o/r":         "",
		"org-1234@evilgithub.com:Owner/Repo": "",
		"https://evil.github.com/o/r":        "",
		"https://github.com.evil.test/o/r":   "",
		"https://github.com@evil.test/o/r":   "",
		"https://github.com/o/r/extra":       "",
		"https://github.com/o/r?ref=x":       "",
		"https://gitlab.com/o/r":             "",
	}
	for in, want := range cases {
		if got := normalizeGithubRemote(in); got != want {
			t.Errorf("normalizeGithubRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBucketizeInterval(t *testing.T) {
	cases := map[int]int{0: 30, 10: 30, 45: 60, 119: 120, 200: 300, 301: 600, 5000: 600}
	for in, want := range cases {
		if got := bucketizeInterval(in); got != want {
			t.Errorf("bucketizeInterval(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestParseWorktreePorcelain(t *testing.T) {
	out := "worktree /repo/main\nHEAD abc123\nbranch refs/heads/main\n\n" +
		"worktree /repo/wt-feature\nHEAD def456\nbranch refs/heads/feature\n\n" +
		"worktree /repo/detached\nHEAD aaa111\ndetached\n\n" +
		"worktree /repo/bare\nbare\n"
	got := parseWorktreePorcelain(out)
	if len(got) != 4 {
		t.Fatalf("parseWorktreePorcelain returned %d records, want 4: %+v", len(got), got)
	}
	if got[0]["path"] != "/repo/main" || got[0]["branch"] != "main" || got[0]["head"] != "abc123" {
		t.Errorf("record0 = %+v", got[0])
	}
	if got[1]["branch"] != "feature" {
		t.Errorf("record1 branch = %v, want feature", got[1]["branch"])
	}
	if got[2]["detached"] != true {
		t.Errorf("record2 should be detached: %+v", got[2])
	}
	if got[3]["bare"] != true {
		t.Errorf("record3 should be bare: %+v", got[3])
	}
}
