package envs

// Launch orchestration: findEnv decodes the stored document into the typed
// model, worktree resolution and the live port index collect current state,
// planner.go computes what to do, and executor.go does it.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/imohiyoko/devhub/internal/pathutil"
)

// livePortIndex maps declared port -> listening pid (first listener wins).
func (c *Controller) livePortIndex() map[int]int {
	index := map[int]int{}
	list, err := c.ports.ListOpen()
	if err != nil {
		return index
	}
	for _, p := range list {
		if _, ok := index[p.Port]; !ok {
			index[p.Port] = p.PID
		}
	}
	return index
}

// resolveWorktree resolves (repo, branch) to an existing worktree path, or "".
func (c *Controller) resolveWorktree(repoPath, branch string) string {
	repo := pathutil.ExpandUser(repoPath)
	if repo == "" || branch == "" {
		return ""
	}
	worktrees, err := c.git.ListWorktrees(repo)
	if err != nil {
		return ""
	}
	for _, wt := range worktrees {
		b, _ := wt["branch"].(string)
		exists, _ := wt["exists"].(bool)
		if b == branch && exists {
			p, _ := wt["path"].(string)
			return p
		}
	}
	return ""
}

// setupWorktree resolves the env-level worktree binding to an existing path.
// Returns "" when none is configured; errors when one is configured but missing.
func (c *Controller) setupWorktree(w worktree) (string, error) {
	if !w.Enabled || w.RepoPath == "" || w.Branch == "" {
		return "", nil
	}
	wt := c.resolveWorktree(w.RepoPath, w.Branch)
	if wt == "" {
		return "", fmt.Errorf("branch '%s' の worktree が見つかりません（%s）。git tool で作成してください。", w.Branch, w.RepoPath)
	}
	return wt, nil
}

// resolveCwds builds {process_id: cwd}; a bound process must have an existing
// worktree (error otherwise), an unbound one inherits envCwdOverride.
func (c *Controller) resolveCwds(procs []process, envCwdOverride string) (map[string]string, error) {
	cwds := map[string]string{}
	for _, p := range procs {
		if p.Binding.RepoPath != "" && p.Binding.Branch != "" {
			wt := c.resolveWorktree(p.Binding.RepoPath, p.Binding.Branch)
			if wt == "" {
				return nil, fmt.Errorf("process '%s': branch '%s' の worktree が見つかりません（%s）。git tool で作成してください。", p.ID, p.Binding.Branch, p.Binding.RepoPath)
			}
			cwds[p.ID] = wt
		} else {
			cwds[p.ID] = envCwdOverride
		}
	}
	return cwds, nil
}

// recordLaunch appends a launch record to the registry. The record is built as
// the raw map the registry stores and the UI reads back — the persistence
// boundary where the typed model ends.
func (c *Controller) recordLaunch(env environment, worktreePath string, cwds map[string]string, assigned map[string]int) error {
	repoPath := ""
	branch := ""
	if env.Worktree.Enabled {
		repoPath = pathutil.ExpandUser(env.Worktree.RepoPath)
		branch = env.Worktree.Branch
	}
	procRecords := []any{}
	for _, p := range env.Processes {
		label := p.Label
		if label == "" {
			label = p.ID
		}
		var assignedPort any
		if ap, ok := assigned[p.ID]; ok {
			assignedPort = ap
		}
		procRecords = append(procRecords, map[string]any{
			"id":            p.ID,
			"label":         label,
			"command":       p.Command,
			"port":          p.Port,
			"worktree_path": orNil(cwds[p.ID]),
			"repo_path":     p.Binding.RepoPath,
			"branch":        p.Binding.Branch,
			"assigned_port": assignedPort,
		})
	}
	name := env.Name
	if name == "" {
		name = env.ID
	}
	record := map[string]any{
		"launch_id":     time.Now().Format("20060102-150405-") + tokenHex(3),
		"env_id":        env.ID,
		"env_name":      name,
		"worktree_path": orNil(worktreePath),
		"repo_path":     repoPath,
		"branch":        branch,
		"launched_at":   time.Now().Format(time.RFC3339),
		"processes":     procRecords,
	}
	return c.store.AppendLaunch(record)
}

// launchEnvironment resolves worktrees/ports and launches an environment
// asynchronously (the HTTP path).
func (c *Controller) launchEnvironment(envID string) error {
	_, err := c.startEnvironment(envID, true)
	return err
}

// StartEnvironment launches envID synchronously — the CLI path (`devhub env
// start`). Registry writes are safe from this second process because
// AppendLaunch is a single-row INSERT (see internal/storage/launches.go).
// The returned BatonKills are what was killed to free declared ports; baton
// from the CLI deliberately keeps full take-over semantics (killing even a
// devhub holding the port is the point — 上書き起動), so the caller must
// print them.
func (c *Controller) StartEnvironment(envID string) ([]BatonKill, error) {
	return c.startEnvironment(envID, false)
}

func (c *Controller) startEnvironment(envID string, async bool) ([]BatonKill, error) {
	env, err := c.findEnv(envID)
	if err != nil {
		return nil, err
	}
	envCwd, err := c.setupWorktree(env.Worktree)
	if err != nil {
		return nil, err
	}
	cwds, err := c.resolveCwds(env.Processes, envCwd)
	if err != nil {
		return nil, err
	}
	killed := c.killPorts(batonKillTargets(env.Processes, c.livePortIndex()))
	// Offset assignment observes the port index again, after the baton kills,
	// so ports just freed count as available.
	assigned := assignPorts(env.Processes, c.livePortIndex())
	// The record is written before the spawn plan is computed: a document with
	// a dependency cycle still leaves a launch record behind (as it always has).
	if err := c.recordLaunch(env, envCwd, cwds, assigned); err != nil {
		return killed, err
	}
	steps, err := planSpawns(env.Processes, cwds, assigned)
	if err != nil {
		return killed, err
	}
	c.runSpawns(steps, async)
	return killed, nil
}

func tokenHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
