// Package envs implements the env-launcher endpoints (/api/envs*). It launches
// per-OS terminals, resolves worktree bindings, assigns ports (baton/offset),
// and tracks launches in the SQLite registry.
package envs

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
	workspacectl "github.com/imohiyoko/devhub/internal/controllers/workspace"
	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/pathutil"
)

var (
	envIDRe  = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// launchStore is the persistence the env-launcher needs. It is the one rich
// consumer: besides the envs and settings documents it drives the launch
// registry, whose load->mutate->save must be serialized under a mutex. That
// mutex is a *storage.Store field an interface cannot express, so the two
// mutating registry operations are encapsulated behind AppendLaunch /
// RemoveLaunch — keeping this a pure method interface that a fake can satisfy.
// *storage.Store satisfies it.
type launchStore interface {
	LoadEnvs() (map[string]any, error)
	SaveEnvs(data map[string]any) error
	LoadLaunches() (map[string]any, error)
	AppendLaunch(record map[string]any) error
	RemoveLaunch(launchID string) error
	LoadSettings() (map[string]any, error)
}

// Controller serves env-launcher endpoints, reusing git/ports/workspace.
type Controller struct {
	store     launchStore
	git       *gitctl.Controller
	ports     *portsctl.Controller
	workspace *workspacectl.Controller
}

// New returns an env-launcher controller.
func New(store launchStore, git *gitctl.Controller, ports *portsctl.Controller, workspace *workspacectl.Controller) *Controller {
	return &Controller{store: store, git: git, ports: ports, workspace: workspace}
}

// HandleGet serves GET /api/envs, /api/envs/launches, /api/envs/worktrees.
func (c *Controller) HandleGet(w http.ResponseWriter, r *http.Request) error {
	switch r.URL.Path {
	case "/api/envs":
		data, err := c.store.LoadEnvs()
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, data)
	case "/api/envs/launches":
		data, err := c.enrichLaunches()
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, data)
	case "/api/envs/worktrees":
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"repos": c.worktreeInventory(), "home": pathutil.ExpandUser("~")})
	default:
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	return nil
}

// HandlePost serves POST /api/envs and the launch/registry actions.
func (c *Controller) HandlePost(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	switch r.URL.Path {
	case "/api/envs":
		if err := validateEnvs(data); err != nil {
			return err
		}
		if err := c.store.SaveEnvs(data); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "/api/envs/launch":
		envID := pStr(data, "env_id")
		if envID == "" {
			return httpx.Errorf(http.StatusBadRequest, "env_id is required")
		}
		if err := c.launchEnvironment(envID); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "/api/envs/launch/process":
		if err := c.launchSingleProcess(data); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "/api/envs/launches/remove":
		launchID := pStr(data, "launch_id")
		if launchID == "" {
			return httpx.Errorf(http.StatusBadRequest, "launch_id is required")
		}
		force, ok := data["force"].(bool)
		if _, present := data["force"]; present && !ok {
			return httpx.Errorf(http.StatusBadRequest, "force must be a boolean")
		}
		if err := c.removeLaunch(launchID, force); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "/api/envs/launches/open":
		launchID := pStr(data, "launch_id")
		if launchID == "" {
			return httpx.Errorf(http.StatusBadRequest, "launch_id is required")
		}
		if err := c.openLaunch(launchID, pStr(data, "target")); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	return nil
}

// launchSingleProcess handles POST /api/envs/launch/process.
func (c *Controller) launchSingleProcess(data map[string]any) error {
	envID := pStr(data, "env_id")
	processID := pStr(data, "process_id")
	if envID == "" || processID == "" {
		return httpx.Errorf(http.StatusBadRequest, "env_id and process_id are required")
	}
	envDef, err := c.findEnv(envID)
	if err != nil {
		return err
	}
	var processDef map[string]any
	for _, p := range processes(envDef) {
		if pStr(p, "id") == processID {
			processDef = p
			break
		}
	}
	if processDef == nil {
		return fmt.Errorf("Process '%s' not found", processID)
	}
	envCwd, err := c.setupWorktree(pMap(envDef, "worktree"))
	if err != nil {
		return err
	}
	wrapper := map[string]any{"processes": []any{processDef}}
	cwds, err := c.resolveCwds(wrapper, envCwd)
	if err != nil {
		return err
	}
	var extraEnv map[string]string
	var assignedPort *int
	if isOffset(processDef) {
		assigned := c.assignPorts(wrapper, c.livePortIndex())
		if ap, ok := assigned[processID]; ok {
			extraEnv = map[string]string{pStr(processDef, "port_env_var"): fmt.Sprintf("%d", ap)}
			v := ap
			assignedPort = &v
		}
	} else {
		c.killPortsFor([]map[string]any{processDef})
	}
	c.launchProcess(processDef, cwds[processID], extraEnv, assignedPort)
	return nil
}

// worktreeInventory fans out `git worktree list` across repos (order-preserving).
func (c *Controller) worktreeInventory() []any {
	candidates := c.git.AllRepos()
	if len(candidates) == 0 {
		return []any{}
	}
	results := make([]map[string]any, len(candidates))
	sem := make(chan struct{}, min(16, len(candidates)))
	var wg sync.WaitGroup
	for i, repo := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, repo gitctl.Repo) {
			defer wg.Done()
			defer func() { <-sem }()
			wts, err := c.git.ListWorktrees(repo.Path)
			if err != nil {
				return // skipped, not fatal
			}
			results[i] = map[string]any{"name": repo.Name, "path": repo.Path, "worktrees": wts}
		}(i, repo)
	}
	wg.Wait()
	repos := []any{}
	for _, r := range results {
		if r != nil {
			repos = append(repos, r)
		}
	}
	return repos
}

func (c *Controller) findEnv(envID string) (map[string]any, error) {
	data, err := c.store.LoadEnvs()
	if err != nil {
		return nil, err
	}
	for _, e := range toAnySlice(data["environments"]) {
		if m, ok := e.(map[string]any); ok && pStr(m, "id") == envID {
			return m, nil
		}
	}
	return nil, fmt.Errorf("Environment '%s' not found", envID)
}

// validateEnvs mirrors the save-time validation in handle_post('/api/envs').
func validateEnvs(data map[string]any) error {
	envIDs := map[string]bool{}
	for _, envAny := range toAnySlice(data["environments"]) {
		env, _ := envAny.(map[string]any)
		eid := pStr(env, "id")
		if eid == "" || !envIDRe.MatchString(eid) {
			return errors.New("invalid environment id")
		}
		if envIDs[eid] {
			return fmt.Errorf("Duplicate environment ID '%s'", eid)
		}
		envIDs[eid] = true

		// repos/ips declare the environment's allowed scope. When repos is
		// non-empty, every worktree repo (env-level and per-process binding)
		// must be one of them — selecting outside the declared set is rejected
		// so a process can't accidentally point at an unrelated repository.
		allowedRepos, err := scopeList(env, "repos", eid)
		if err != nil {
			return err
		}
		if _, err := scopeList(env, "ips", eid); err != nil {
			return err
		}
		var repoScope map[string]bool
		if len(allowedRepos) > 0 {
			repoScope = make(map[string]bool, len(allowedRepos))
			for _, r := range allowedRepos {
				repoScope[pathutil.ExpandUser(r)] = true
			}
			if rp := pStr(pMap(env, "worktree"), "repo_path"); rp != "" && !repoScope[pathutil.ExpandUser(rp)] {
				return fmt.Errorf("Environment '%s' worktree repo_path '%s' is not in the environment's repos", eid, rp)
			}
		}

		procIDs := map[string]bool{}
		procs := processes(env)
		for _, proc := range procs {
			pid, ok := proc["id"].(string)
			if !ok || pid == "" {
				return fmt.Errorf("Process ID is required and must be a string in environment '%s'", eid)
			}
			if procIDs[pid] {
				return fmt.Errorf("Duplicate process ID '%s' in environment '%s'", pid, eid)
			}
			procIDs[pid] = true

			if _, err := parsePortSpec(proc["port"]); err != nil {
				return fmt.Errorf("Process '%s' port must be a port (3000) or range (3000-3010) within 1-65535 in environment '%s'", pid, eid)
			}

			if binding, present := proc["binding"]; present && binding != nil {
				bm, ok := binding.(map[string]any)
				if !ok {
					return fmt.Errorf("Process '%s' binding must be an object in environment '%s'", pid, eid)
				}
				brepo, okR := bindingStr(bm, "repo_path")
				bbranch, okB := bindingStr(bm, "branch")
				if !okR || !okB {
					return fmt.Errorf("Process '%s' binding repo_path/branch must be strings in environment '%s'", pid, eid)
				}
				if (brepo != "") != (bbranch != "") {
					return fmt.Errorf("Process '%s' binding needs both repo_path and branch in environment '%s'", pid, eid)
				}
				if repoScope != nil && brepo != "" && !repoScope[pathutil.ExpandUser(brepo)] {
					return fmt.Errorf("Process '%s' binding repo_path '%s' is not in environment '%s' repos", pid, brepo, eid)
				}
			}

			if strategy, present := proc["port_strategy"]; present && strategy != nil {
				s, _ := strategy.(string)
				if s != "baton" && s != "offset" {
					return fmt.Errorf("Process '%s' port_strategy must be 'baton' or 'offset' in environment '%s'", pid, eid)
				}
				if s == "offset" {
					envVar := pStr(proc, "port_env_var")
					if envVar == "" || !envVarRe.MatchString(envVar) {
						return fmt.Errorf("Process '%s' offset strategy needs a valid port_env_var (e.g. PORT) in environment '%s'", pid, eid)
					}
					if ports, _ := parsePortSpec(proc["port"]); len(ports) == 0 {
						return fmt.Errorf("Process '%s' offset strategy needs a base port in environment '%s'", pid, eid)
					}
				}
			}
		}

		if err := validateDeps(procs, eid); err != nil {
			return err
		}
	}
	return nil
}

// validateDeps checks for unknown/circular dependencies with env-scoped messages.
// It shares the dependency-sort core with topoSort (launch.go) so the two can't
// drift; only the error wording differs (scoped to the environment id here).
func validateDeps(procs []map[string]any, eid string) error {
	_, unknownDep, badProc, cyclic := topoOrder(procs)
	if unknownDep != "" {
		return fmt.Errorf("Dependency '%s' for process '%s' not found in environment '%s'", unknownDep, badProc, eid)
	}
	if cyclic {
		return fmt.Errorf("Circular dependency detected in environment '%s'", eid)
	}
	return nil
}

// --- small helpers shared across the package ---

func pStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func pMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func processes(envDef map[string]any) []map[string]any {
	var out []map[string]any
	for _, p := range toAnySlice(envDef["processes"]) {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func toAnySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// scopeList validates that env[key], if present, is an array of strings and
// returns its trimmed, non-empty entries. A present non-array (or a non-string
// element) is an error, so a malformed scope can't silently disable the
// repo-scope constraint.
func scopeList(env map[string]any, key, eid string) ([]string, error) {
	v, present := env[key]
	if !present || v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("Environment '%s' %s must be an array of strings", eid, key)
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("Environment '%s' %s must be an array of strings", eid, key)
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func toStringSlice(v any) []string {
	var out []string
	for _, item := range toAnySlice(v) {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// bindingStr returns (value, isStringOrAbsent). A present non-string fails the check.
func bindingStr(m map[string]any, key string) (string, bool) {
	v, present := m[key]
	if !present || v == nil {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}

func toIntVal(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func isDir(p string) bool { return pathutil.IsDir(p) }

func errMsg(s string) error { return errors.New(s) }
