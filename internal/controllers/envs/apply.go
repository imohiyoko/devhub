package envs

// Applying a switch plan: the side-effecting counterpart of planSwitch. Stops
// run first, in the plan's order (dependents before what they depend on), then
// starts in dependency order. A failing operation does not abort the rest —
// the caller gets one result per operation, because a half-applied switch the
// user can see is better than one they have to guess at.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/httpx"
)

// applyResult is one executed operation.
type applyResult struct {
	Step   PlanStep
	Action string // "stop" | "start"
	Err    error
}

// applySwitch serves POST /api/envs/switch/apply. It recomputes the plan from
// the current state rather than trusting one the caller passed: the target is
// declarative, so re-deriving is both safe and the only way to act on reality.
// An optional fingerprint (from the plan the user approved) makes that safety
// visible — if the observed state moved since, the request is refused instead
// of stopping something the user never saw listed.
func (c *Controller) applySwitch(data map[string]any) (map[string]any, error) {
	envID, req, err := parseSwitchRequest(data)
	if err != nil {
		return nil, err
	}
	env, states, err := c.observeEnv(envID)
	if err != nil {
		return nil, err
	}
	plan, err := planSwitch(env, req, states)
	if err != nil {
		return nil, err
	}
	if want := pStr(data, "fingerprint"); want != "" && want != plan.Fingerprint {
		return nil, httpx.Errorf(http.StatusConflict,
			"環境の状態が変化しています。もう一度確認してから適用してください。")
	}

	byID := make(map[string]component, len(env.Components))
	for _, comp := range env.Components {
		byID[comp.ID] = comp
	}
	results := c.applyStops(plan, byID, portsByProcess(env, nil), c.livePortIndex())
	results = append(results, c.applyStarts(env, plan, byID)...)
	return applyResultsJSON(plan, results), nil
}

// applyStops stops each planned component: a compose service through the
// adapter, a host process by killing the listeners that made it look running
// in the first place.
func (c *Controller) applyStops(plan SwitchPlan, byID map[string]component, ports map[string][]int, live map[int]int) []applyResult {
	results := make([]applyResult, 0, len(plan.Stop))
	for _, step := range plan.Stop {
		comp := byID[step.ID]
		var err error
		if comp.Kind == kindComposeService {
			ctx, cancel := context.WithTimeout(context.Background(), composeUpTimeout)
			err = c.compose.Stop(ctx, comp.Compose)
			cancel()
		} else {
			err = c.stopHostComponent(ports[comp.ID], live)
		}
		results = append(results, applyResult{Step: step, Action: "stop", Err: err})
	}
	return results
}

// stopHostComponent kills whatever is listening on the component's identifying
// ports. A port that is already free is not an error: the component is in the
// state the caller asked for. Refusals (protected ports, devhub itself) come
// from the ports controller and are reported as they are.
func (c *Controller) stopHostComponent(ports []int, live map[int]int) error {
	var failed []string
	for _, port := range ports {
		pid, listening := live[port]
		if !listening {
			continue
		}
		if err := c.ports.KillPortProcess(port, pid); err != nil {
			failed = append(failed, fmt.Sprintf(":%d (%v)", port, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("停止できませんでした: %s", strings.Join(failed, ", "))
	}
	return nil
}

// applyStarts starts the planned components in the plan's order, which is
// dependency order across both kinds — a host process that depends on a
// compose service waits for that service's `up --wait` to return.
func (c *Controller) applyStarts(env environment, plan SwitchPlan, byID map[string]component) []applyResult {
	procs := make([]process, 0, len(plan.Start))
	for _, step := range plan.Start {
		if comp := byID[step.ID]; comp.Kind == kindHostProcess {
			procs = append(procs, comp.Proc)
		}
	}
	// Worktrees, baton take-overs, offset assignment and the launch record all
	// apply to the host processes only, and only when there are any: a
	// compose-only switch must not fail on an unrelated missing worktree.
	var cwds map[string]string
	var assigned map[string]int
	var hostErr error
	if len(procs) > 0 {
		cwds, assigned, hostErr = c.prepareHostStarts(env, procs)
	}

	results := make([]applyResult, 0, len(plan.Start))
	for i, step := range plan.Start {
		comp := byID[step.ID]
		var err error
		switch {
		case comp.Kind == kindComposeService:
			ctx, cancel := context.WithTimeout(context.Background(), composeUpTimeout)
			err = c.compose.Up(ctx, comp.Compose)
			cancel()
		case hostErr != nil:
			err = hostErr
		default:
			err = spawnErr(c.runSpawns([]spawnStep{spawnStepFor(comp.Proc, cwds[comp.ID], assigned)}, false))
			if i < len(plan.Start)-1 {
				time.Sleep(comp.Proc.Delay)
			}
		}
		results = append(results, applyResult{Step: step, Action: "start", Err: err})
	}
	return results
}

// prepareHostStarts resolves worktrees, frees baton ports, assigns offset
// ports and records the launch for the host processes about to start. The
// record matters beyond the UI list: it is what lets a later stop find an
// offset process, whose port the definition alone cannot name.
func (c *Controller) prepareHostStarts(env environment, procs []process) (map[string]string, map[string]int, error) {
	envCwd, err := c.setupWorktree(env.Worktree)
	if err != nil {
		return nil, nil, err
	}
	cwds, err := c.resolveCwds(procs, envCwd)
	if err != nil {
		return nil, nil, err
	}
	c.killPorts(batonKillTargets(procs, c.livePortIndex()))
	// Assignment re-observes ports after the baton kills, so ports just freed
	// count as available — the same sequence the launch path uses.
	assigned := assignPorts(procs, c.livePortIndex())

	started := env
	started.Processes = procs
	return cwds, assigned, c.recordLaunch(started, envCwd, cwds, assigned)
}

// applyResultsJSON renders the outcome. ok is false when any operation failed,
// so a caller that only checks a flag still learns the switch was partial.
func applyResultsJSON(plan SwitchPlan, results []applyResult) map[string]any {
	ok := true
	out := []any{}
	for _, r := range results {
		entry := map[string]any{
			"id": r.Step.ID, "label": r.Step.Label, "kind": r.Step.Kind,
			"action": r.Action, "ok": r.Err == nil,
		}
		if r.Err != nil {
			ok = false
			entry["error"] = r.Err.Error()
		}
		out = append(out, entry)
	}
	warnings := plan.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{
		"env_id": plan.EnvID, "scenario_id": plan.ScenarioID,
		"ok": ok, "results": out, "warnings": warnings,
	}
}
