package envs

// Scenario switching, pure half: componentStates approximates what is running
// now, planSwitch diffs that against a scenario's target state, and the
// resulting stop/keep/start difference is what the plan API returns and a later
// apply step will execute. Like planner.go this file touches no store, no
// network and no processes — everything it needs is passed in.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/imohiyoko/devhub/internal/container"
)

// A component's observed state. The vocabulary is the container package's,
// reused verbatim for host processes: a process is running, stopped, or
// unobservable in exactly the same sense a container is, and having two
// spellings of it would only invite a conversion that can drift.
type componentState = container.State

const (
	stateRunning = container.StateRunning
	stateStopped = container.StateStopped
	stateUnknown = container.StateUnknown
)

// componentStatus pairs the observed state with the reason it could not be
// observed — an "unknown" the user cannot explain is not actionable. Reason is
// empty for running/stopped.
type componentStatus struct {
	State  componentState
	Reason string
}

// componentStates observes every component of env. A host_process's state is
// approximated by port LISTEN (plan §5): a component whose identifying ports
// have a listener counts as running, one with identifying ports but no
// listener as stopped, and one with no port to look for as unknown. States for
// kinds this pure function cannot observe are passed in through adapted (the
// Compose adapter fills in compose_service); a kind with no adapter at all is
// unknown. Unknown components are never stopped automatically.
func componentStates(env environment, launches []any, live map[int]int, adapted map[string]componentStatus) map[string]componentStatus {
	ports := portsByProcess(env, launches)
	out := make(map[string]componentStatus, len(env.Components))
	for _, comp := range env.Components {
		if comp.Kind != kindHostProcess {
			status, ok := adapted[comp.ID]
			if !ok {
				status = componentStatus{stateUnknown, fmt.Sprintf("kind '%s' の状態取得は未対応です", comp.Kind)}
			}
			out[comp.ID] = status
			continue
		}
		observable := ports[comp.ID]
		if len(observable) == 0 {
			out[comp.ID] = componentStatus{stateUnknown, "監視できるポートが宣言されていません"}
			continue
		}
		status := componentStatus{State: stateStopped}
		for _, p := range observable {
			if _, listening := live[p]; listening {
				status.State = stateRunning
				break
			}
		}
		out[comp.ID] = status
	}
	return out
}

// ComponentReport is one component's identity and observed state — what the
// state API renders and what `devhub env status` prints.
type ComponentReport struct {
	ID     string
	Label  string
	Kind   string
	Shared bool
	State  string
	Reason string // why State is unknown; empty otherwise
}

// ScenarioInfo names a scenario an environment can be switched to.
type ScenarioInfo struct {
	ID         string
	Name       string
	Components []string
}

// componentReports pairs each component with its observed state, in definition
// order.
func componentReports(env environment, states map[string]componentStatus) []ComponentReport {
	out := make([]ComponentReport, 0, len(env.Components))
	for _, comp := range env.Components {
		status := states[comp.ID]
		out = append(out, ComponentReport{
			ID: comp.ID, Label: comp.displayLabel(), Kind: comp.Kind, Shared: comp.Shared,
			State: string(status.State), Reason: status.Reason,
		})
	}
	return out
}

func scenarioInfos(env environment) []ScenarioInfo {
	out := make([]ScenarioInfo, 0, len(env.Scenarios))
	for _, s := range env.Scenarios {
		members := s.Components
		if members == nil {
			members = []string{}
		}
		out = append(out, ScenarioInfo{ID: s.ID, Name: s.Name, Components: members})
	}
	return out
}

// SwitchTarget is the target state to switch to: a scenario id, or an
// explicit component selection (plan §9). Shared components join the target
// either way, so an empty selection means "only the shared components".
type SwitchTarget struct {
	ScenarioID string
	Components []string
}

// PlanStep is one component in a switch plan, carrying enough for a
// confirmation screen to render without re-reading the definition.
type PlanStep struct {
	ID     string
	Label  string
	Kind   string
	Shared bool
	State  componentState
}

// SwitchPlan is the difference between the observed state and the target
// state. Stop is in reverse dependency order (dependents first), Keep and
// Start in dependency order. Warnings carry what the user should see before
// applying: components devhub could not observe, and the ones it will
// therefore neither stop nor deduplicate.
type SwitchPlan struct {
	EnvID      string
	ScenarioID string
	Stop       []PlanStep
	Keep       []PlanStep
	Start      []PlanStep
	Warnings   []string
	// Fingerprint identifies the observed state this plan was computed from,
	// so apply can refuse a plan the user approved against a different reality.
	Fingerprint string
}

// stateFingerprint identifies an observed state (plan §9). It covers exactly
// what the stop/keep/start difference is derived from, so an unchanged
// fingerprint means re-deriving the plan would produce the same operations —
// and in particular the same set of things to stop.
func stateFingerprint(states map[string]componentStatus) string {
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	h := sha256.New()
	for _, id := range ids {
		fmt.Fprintf(h, "%s=%s\n", id, states[id].State)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// planSwitch computes the plan for switching env to req's target state.
// Dependency problems are reported as errors rather than partial plans: the
// document is validated on save, but decode is lenient, so a hand-edited file
// can still reach here with a cycle or a dangling dependency.
func planSwitch(env environment, req SwitchTarget, states map[string]componentStatus) (SwitchPlan, error) {
	order, unknownDep, badNode, cyclic := topoOrder(componentNodes(env.Components))
	if unknownDep != "" {
		return SwitchPlan{}, fmt.Errorf("Dependency '%s' for component '%s' not found in environment '%s'", unknownDep, badNode, env.ID)
	}
	if cyclic {
		return SwitchPlan{}, fmt.Errorf("Circular dependency detected in environment '%s'", env.ID)
	}
	target, err := targetComponents(env, req)
	if err != nil {
		return SwitchPlan{}, err
	}

	byID := make(map[string]component, len(env.Components))
	for _, comp := range env.Components {
		byID[comp.ID] = comp
	}
	plan := SwitchPlan{EnvID: env.ID, ScenarioID: req.ScenarioID, Fingerprint: stateFingerprint(states)}
	for _, id := range order {
		comp := byID[id]
		status, observed := states[id]
		if !observed {
			// A component the collector did not cover must not be mistaken for
			// a stopped one, which would make it a silent stop candidate.
			status = componentStatus{stateUnknown, "状態が取得されていません"}
		}
		step := PlanStep{ID: id, Label: comp.displayLabel(), Kind: comp.Kind, Shared: comp.Shared, State: status.State}
		switch {
		case target[id] && status.State == stateRunning:
			plan.Keep = append(plan.Keep, step)
		case target[id]:
			// An unobservable component is started rather than assumed
			// running: not starting it would leave the target state unmet.
			plan.Start = append(plan.Start, step)
			if status.State == stateUnknown {
				plan.Warnings = append(plan.Warnings,
					fmt.Sprintf("%s: %s。起動対象に含めるため、既に起動している場合は重複します。", step.Label, status.Reason))
			}
		case status.State == stateRunning:
			plan.Stop = append(plan.Stop, step)
		case status.State == stateUnknown:
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("%s: %s。自動停止の対象にしません。", step.Label, status.Reason))
		}
	}
	// Dependents must go down before what they depend on.
	slices.Reverse(plan.Stop)
	return plan, nil
}

// targetComponents resolves what must be running after the switch: every
// shared component (implicitly part of every scenario, plan §5), the requested
// components, and — transitively — everything they depend on, so a scenario
// only has to name its entry points.
func targetComponents(env environment, req SwitchTarget) (map[string]bool, error) {
	byID := make(map[string]component, len(env.Components))
	for _, comp := range env.Components {
		byID[comp.ID] = comp
	}
	requested := req.Components
	if req.ScenarioID != "" {
		i := slices.IndexFunc(env.Scenarios, func(s scenario) bool { return s.ID == req.ScenarioID })
		if i < 0 {
			return nil, fmt.Errorf("Scenario '%s' not found in environment '%s'", req.ScenarioID, env.ID)
		}
		requested = env.Scenarios[i].Components
	}

	target := map[string]bool{}
	// Cycles are rejected by the caller, and the visited check makes this
	// terminate regardless.
	var add func(id string) error
	add = func(id string) error {
		comp, ok := byID[id]
		if !ok {
			return fmt.Errorf("Component '%s' not found in environment '%s'", id, env.ID)
		}
		if target[id] {
			return nil
		}
		target[id] = true
		for _, dep := range comp.DependsOn {
			if err := add(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, comp := range env.Components {
		if comp.Shared {
			if err := add(comp.ID); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range requested {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	return target, nil
}
