// Package envs implements the env-launcher endpoints (/api/envs*). It launches
// per-OS terminals, resolves worktree bindings, assigns ports (baton/offset),
// and tracks launches in the SQLite registry.
package envs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/pathutil"
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

// gitService is the slice of the git controller the env-launcher uses: repo
// discovery and worktree listing for binding resolution. *gitctl.Controller
// satisfies it; tests substitute a fake.
type gitService interface {
	AllRepos() []gitctl.Repo
	ListWorktrees(repoPath string) ([]map[string]any, error)
}

// portsService is the slice of the ports controller the env-launcher uses:
// listener discovery (live status, offset assignment) and the safety-checked
// port kill (baton take-over, env stop). *portsctl.Controller satisfies it.
type portsService interface {
	ListOpen() ([]portsctl.PortEntry, error)
	KillPortProcess(port, pid int) error
}

// workspaceService is the slice of the workspace controller the env-launcher
// uses: opening a launch's worktree in the editor. *workspacectl.Controller
// satisfies it.
type workspaceService interface {
	OpenInEditor(path string)
}

// Controller serves env-launcher endpoints, reusing git/ports/workspace.
type Controller struct {
	store     launchStore
	git       gitService
	ports     portsService
	workspace workspaceService

	// compose reads and operates on compose_service components; colima reports
	// the profiles this host offers. Per-instance like the seams below so
	// tests answer without Docker or Colima installed.
	compose composeAdapter
	colima  colimaProvider

	// spawn starts a prepared command and reports whether it started; settle is
	// the pause after baton kills before processes start. Both are per-instance
	// so tests can capture spawns and skip the wait without mutating package
	// state — the HTTP launch path spawns on a goroutine, which a swapped
	// package var would race.
	spawn  func(*exec.Cmd) error
	settle time.Duration
}

// New returns an env-launcher controller.
func New(store launchStore, git gitService, ports portsService, workspace workspaceService) *Controller {
	return &Controller{
		store: store, git: git, ports: ports, workspace: workspace,
		compose: newDockerCompose(),
		colima:  newColimaCLI(),
		spawn:   func(cmd *exec.Cmd) error { return cmd.Start() },
		settle:  500 * time.Millisecond,
	}
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
	case "/api/envs/state":
		data, err := c.environmentStates()
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, data)
	case "/api/envs/runtimes":
		ctx, cancel := context.WithTimeout(r.Context(), runtimeProbeTimeout)
		defer cancel()
		httpx.WriteJSON(w, http.StatusOK, runtimeProvidersJSON(c.RuntimeProviders(ctx)))
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
	case "/api/envs/switch/plan":
		plan, err := c.switchPlan(data)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, plan)
	case "/api/envs/switch/apply":
		results, err := c.applySwitch(data)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, results)
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
	env, err := c.findEnv(envID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(env.Processes, func(p process) bool { return p.ID == processID })
	if i < 0 {
		return fmt.Errorf("Process '%s' not found", processID)
	}
	proc := env.Processes[i]
	envCwd, err := c.setupWorktree(env.Worktree)
	if err != nil {
		return err
	}
	single := []process{proc}
	cwds, err := c.resolveCwds(single, envCwd)
	if err != nil {
		return err
	}
	var assigned map[string]int
	if proc.isOffset() {
		assigned = assignPorts(single, c.livePortIndex())
	} else {
		c.killPorts(batonKillTargets(single, c.livePortIndex()))
	}
	// Inline (not async): this path can report a launch failure, and does.
	return spawnErr(c.runSpawns([]spawnStep{spawnStepFor(proc, cwds[processID], assigned)}, false))
}

// environmentStates serves GET /api/envs/state: every environment's components
// with their observed state, plus the scenarios they can be switched to. The
// port scan and the launch registry are read once for all environments.
func (c *Controller) environmentStates() (map[string]any, error) {
	envsDoc, err := c.store.LoadEnvs()
	if err != nil {
		return nil, err
	}
	launchDoc, err := c.store.LoadLaunches()
	if err != nil {
		return nil, err
	}
	launches := toAnySlice(launchDoc["launches"])
	live := c.livePortIndex()
	envs := []any{}
	for _, env := range decodeEnvironments(envsDoc) {
		states := componentStates(env, launches, live, c.composeStates(env))
		components := []any{}
		for _, r := range componentReports(env, states) {
			components = append(components, map[string]any{
				"id": r.ID, "label": r.Label, "kind": r.Kind,
				"shared": r.Shared, "state": r.State, "reason": r.Reason,
			})
		}
		scenarios := []any{}
		for _, s := range scenarioInfos(env) {
			scenarios = append(scenarios, map[string]any{"id": s.ID, "name": s.Name, "components": s.Components})
		}
		envs = append(envs, map[string]any{
			"id": env.ID, "name": env.Name, "components": components, "scenarios": scenarios,
			// The declared execution base, so the environment card can show it
			// next to what /api/envs/runtimes says this host can actually
			// offer (plan §10).
			"runtime": map[string]any{
				"provider": env.Runtime.Provider,
				"profile":  env.Runtime.Profile,
				"engine":   env.Runtime.Engine,
			},
		})
	}
	return map[string]any{"environments": envs}, nil
}

// EnvComponents reports one environment's components with their observed state
// and the scenarios it can be switched to — the read side `devhub env status`
// prints.
func (c *Controller) EnvComponents(envID string) ([]ComponentReport, []ScenarioInfo, error) {
	env, states, err := c.observeEnv(envID)
	if err != nil {
		return nil, nil, err
	}
	return componentReports(env, states), scenarioInfos(env), nil
}

// switchPlan serves POST /api/envs/switch/plan: the stop/keep/start difference
// for a target scenario (or an explicit component selection), computed without
// touching anything. Applying a plan is a separate call.
func (c *Controller) switchPlan(data map[string]any) (map[string]any, error) {
	envID, req, err := parseSwitchRequest(data)
	if err != nil {
		return nil, err
	}
	// Through PlanSwitch, not planSwitch: the exported one is what the CLI
	// calls, and re-deriving the plan here instead is how the two surfaces
	// silently drift apart (plan §6.5). It cost the UI its runtime warnings
	// once already.
	plan, err := c.PlanSwitch(envID, req)
	if err != nil {
		return nil, err
	}
	return switchPlanJSON(plan), nil
}

// parseSwitchRequest reads the target of a plan/apply request: an environment
// and exactly one of a scenario or an explicit component selection.
func parseSwitchRequest(data map[string]any) (string, SwitchTarget, error) {
	envID := pStr(data, "env_id")
	if envID == "" {
		return "", SwitchTarget{}, httpx.Errorf(http.StatusBadRequest, "env_id is required")
	}
	req := SwitchTarget{ScenarioID: pStr(data, "scenario_id")}
	selection, selected := data["components"]
	if selected {
		ids, ok := stringList(selection)
		if !ok {
			return "", SwitchTarget{}, httpx.Errorf(http.StatusBadRequest, "components must be an array of component ids")
		}
		req.Components = ids
	}
	// An empty components array is a valid target (the shared components
	// alone), so the request is read by key presence, not by emptiness.
	if byScenario := req.ScenarioID != ""; byScenario == selected {
		return "", SwitchTarget{}, httpx.Errorf(http.StatusBadRequest, "specify exactly one of scenario_id or components")
	}
	return envID, req, nil
}

// observeEnv decodes an environment and collects the state its switch planning
// runs on: the port scan, the launch registry and the compose probes.
func (c *Controller) observeEnv(envID string) (environment, map[string]componentStatus, error) {
	env, err := c.findEnv(envID)
	if err != nil {
		return environment{}, nil, err
	}
	launchDoc, err := c.store.LoadLaunches()
	if err != nil {
		return environment{}, nil, err
	}
	states := componentStates(env, toAnySlice(launchDoc["launches"]), c.livePortIndex(), c.composeStates(env))
	return env, states, nil
}

// composeStates probes the compose projects of env's compose_service
// components. Components sharing one project are answered by a single
// `docker compose ps`. A failed probe is not fatal: those components report
// unknown carrying Docker's own message, so an environment of host processes
// still works on a machine without Docker — and an unknown component is never
// stopped automatically.
func (c *Controller) composeStates(env environment) map[string]componentStatus {
	type group struct {
		spec  composeSpec
		comps []component
	}
	groups := map[string]*group{}
	var order []string
	for _, comp := range env.Components {
		if comp.Kind != kindComposeService {
			continue
		}
		key := strings.Join(append([]string{comp.Compose.Project, comp.Compose.Cwd}, comp.Compose.Files...), "\x00")
		if groups[key] == nil {
			groups[key] = &group{spec: comp.Compose}
			order = append(order, key)
		}
		groups[key].comps = append(groups[key].comps, comp)
	}

	dockerContext := dockerContextFor(env.Runtime)
	out := make(map[string]componentStatus, len(env.Components))
	for _, key := range order {
		g := groups[key]
		ctx, cancel := context.WithTimeout(context.Background(), composeProbeTimeout)
		services, err := c.compose.ServiceStates(ctx, dockerContext, g.spec)
		cancel()
		for _, comp := range g.comps {
			if err != nil {
				out[comp.ID] = componentStatus{stateUnknown, err.Error()}
				continue
			}
			out[comp.ID] = componentStatus{State: composeComponentState(comp.Compose, services)}
		}
	}
	return out
}

// switchPlanJSON renders a plan for the wire — the boundary where the typed
// model turns back into maps.
func switchPlanJSON(plan SwitchPlan) map[string]any {
	steps := func(list []PlanStep) []any {
		out := []any{}
		for _, s := range list {
			out = append(out, map[string]any{
				"id": s.ID, "label": s.Label, "kind": s.Kind, "shared": s.Shared, "state": string(s.State),
			})
		}
		return out
	}
	warnings := plan.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{
		"env_id":      plan.EnvID,
		"scenario_id": plan.ScenarioID,
		"stop":        steps(plan.Stop),
		"keep":        steps(plan.Keep),
		"start":       steps(plan.Start),
		"warnings":    warnings,
		// Echoed back on apply so a plan approved against one state is not
		// applied to another.
		"fingerprint": plan.Fingerprint,
	}
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

// findEnv looks the environment up in the stored document and decodes it into
// the typed model — the point where maps end and types begin on the launch paths.
func (c *Controller) findEnv(envID string) (environment, error) {
	data, err := c.store.LoadEnvs()
	if err != nil {
		return environment{}, err
	}
	version := docVersion(data)
	for _, e := range toAnySlice(data["environments"]) {
		if m, ok := e.(map[string]any); ok && pStr(m, "id") == envID {
			return decodeEnvironment(m, version), nil
		}
	}
	return environment{}, fmt.Errorf("Environment '%s' not found", envID)
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

func processes(envDef map[string]any) []map[string]any { return objSlice(envDef["processes"]) }

// objSlice returns the object entries of a JSON array value, skipping others.
func objSlice(v any) []map[string]any {
	var out []map[string]any
	for _, item := range toAnySlice(v) {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func toAnySlice(v any) []any {
	s, _ := v.([]any)
	return s
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
