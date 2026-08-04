package envs

// Save-time validation for POST /api/envs. This operates on the raw document
// (map[string]any) — the boundary where shape errors must be caught before the
// full-document replace hits the store — and is strict where the decode layer
// (model.go) is deliberately lenient. Version 1 documents keep the historical
// checks unchanged; version 2 documents are validated over components and
// scenarios.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/imohiyoko/devhub/internal/pathutil"
)

var (
	envIDRe  = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// validateEnvs mirrors the save-time validation in handle_post('/api/envs').
func validateEnvs(data map[string]any) error {
	version, err := validateDocVersion(data)
	if err != nil {
		return err
	}
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

		if version >= 2 {
			if err := validateComponents(env, eid, repoScope); err != nil {
				return err
			}
		} else if err := validateProcesses(env, eid, repoScope); err != nil {
			return err
		}
	}
	return nil
}

// validateDocVersion is the strict version gate: an absent version means v1;
// anything present must be exactly 1 or 2.
func validateDocVersion(data map[string]any) (int, error) {
	raw, present := data["version"]
	if !present || raw == nil {
		return 1, nil
	}
	// Compare the number as-is: truncating first would let a fractional
	// version (1.5) pass as a supported one.
	switch n := raw.(type) {
	case float64:
		if n == 1 || n == 2 {
			return int(n), nil
		}
	case int:
		if n == 1 || n == 2 {
			return n, nil
		}
	}
	return 0, errors.New("Config version must be 1 or 2")
}

// validateProcesses checks a v1 environment's processes array.
func validateProcesses(env map[string]any, eid string, repoScope map[string]bool) error {
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
		if err := validateProcessFields(proc, pid, eid, repoScope); err != nil {
			return err
		}
	}
	return validateDeps(procs, eid)
}

// validateProcessFields checks the process-shaped fields (port, binding,
// port_strategy) shared by v1 processes and v2 host_process components.
func validateProcessFields(proc map[string]any, pid, eid string, repoScope map[string]bool) error {
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
	return nil
}

// validateComponents checks a v2 environment's components and scenarios. The
// v2 schema has no pre-versioning documents to tolerate, so shapes are
// checked strictly (non-object entries error instead of being skipped).
func validateComponents(env map[string]any, eid string, repoScope map[string]bool) error {
	if _, present := env["processes"]; present {
		return fmt.Errorf("Environment '%s' must not have processes in a version 2 config (use components)", eid)
	}
	provider, err := validateRuntime(env, eid)
	if err != nil {
		return err
	}
	if raw, present := env["components"]; present && raw != nil {
		if _, ok := raw.([]any); !ok {
			return fmt.Errorf("Environment '%s' components must be an array", eid)
		}
	}

	compIDs := map[string]bool{}
	var comps []component
	for _, item := range toAnySlice(env["components"]) {
		cm, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("Environment '%s' components must be objects", eid)
		}
		cid, ok := cm["id"].(string)
		if !ok || cid == "" || !envIDRe.MatchString(cid) {
			return fmt.Errorf("Component ID is required and must be alphanumeric/_/- in environment '%s'", eid)
		}
		if compIDs[cid] {
			return fmt.Errorf("Duplicate component ID '%s' in environment '%s'", cid, eid)
		}
		compIDs[cid] = true

		kind := kindHostProcess
		if raw, present := cm["kind"]; present && raw != nil {
			kind, _ = raw.(string)
		}
		switch kind {
		case kindHostProcess:
			if err := validateProcessFields(cm, cid, eid, repoScope); err != nil {
				return err
			}
		case kindComposeService:
			if err := validateCompose(cm, cid, eid); err != nil {
				return err
			}
			// A host-only runtime has nothing to run containers with, so
			// saying both is a contradiction rather than a preference devhub
			// could quietly resolve.
			if provider == providerHost {
				return fmt.Errorf("Component '%s' is a compose_service but environment '%s' runtime provider is 'host'", cid, eid)
			}
		default:
			return fmt.Errorf("Component '%s' kind must be 'host_process' or 'compose_service' in environment '%s'", cid, eid)
		}

		if raw, present := cm["lifecycle"]; present && raw != nil {
			if s, _ := raw.(string); s != lifecycleShared && s != lifecycleScenario {
				return fmt.Errorf("Component '%s' lifecycle must be 'shared' or 'scenario' in environment '%s'", cid, eid)
			}
		}
		// depends_on must be checked before decoding: the decode drops
		// non-string entries, so a malformed list would otherwise be saved
		// with some or all of its edges silently missing, changing start order.
		if _, ok := stringList(cm["depends_on"]); !ok {
			return fmt.Errorf("Component '%s' depends_on must be an array of component ids in environment '%s'", cid, eid)
		}
		comps = append(comps, decodeComponent(cm))
	}

	if err := validateComponentDeps(comps, eid); err != nil {
		return err
	}
	return validateScenarios(env, comps, eid)
}

// validateCompose checks a compose_service component's compose payload: cwd
// (where docker compose runs), project (the ownership marker devhub restricts
// its operations to) and at least one service are required.
func validateCompose(cm map[string]any, cid, eid string) error {
	compose, ok := cm["compose"].(map[string]any)
	if !ok {
		return fmt.Errorf("Component '%s' needs a compose object (cwd/project/services) in environment '%s'", cid, eid)
	}
	if pStr(compose, "cwd") == "" {
		return fmt.Errorf("Component '%s' compose needs a cwd in environment '%s'", cid, eid)
	}
	if pStr(compose, "project") == "" {
		return fmt.Errorf("Component '%s' compose needs a project name in environment '%s'", cid, eid)
	}
	services, ok := stringList(compose["services"])
	if !ok {
		return fmt.Errorf("Component '%s' compose services must be non-empty strings in environment '%s'", cid, eid)
	}
	if len(services) == 0 {
		return fmt.Errorf("Component '%s' compose needs at least one service in environment '%s'", cid, eid)
	}
	if _, ok := stringList(compose["files"]); !ok {
		return fmt.Errorf("Component '%s' compose files must be an array of strings in environment '%s'", cid, eid)
	}
	return nil
}

// validateRuntime checks the optional runtime block and returns the provider
// the environment declares (providerDocker when it says nothing). Fields that
// do not apply to the chosen provider are rejected rather than ignored: a
// profile silently dropped because the provider is not colima would leave the
// user believing devhub talks to a VM it never touches.
func validateRuntime(env map[string]any, eid string) (string, error) {
	raw, present := env["runtime"]
	if !present || raw == nil {
		return providerDocker, nil
	}
	rt, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("Environment '%s' runtime must be an object", eid)
	}
	provider := providerDocker
	if v, present := rt["provider"]; present && v != nil {
		s, _ := v.(string)
		switch s {
		case providerHost, providerDocker, providerColima:
			provider = s
		default:
			return "", fmt.Errorf("Environment '%s' runtime provider must be 'host', 'docker' or 'colima'", eid)
		}
	}

	if v, present := rt["profile"]; present && v != nil {
		s, _ := v.(string)
		if provider != providerColima {
			return "", fmt.Errorf("Environment '%s' runtime profile is only valid for the colima provider", eid)
		}
		if !envIDRe.MatchString(s) {
			return "", fmt.Errorf("Environment '%s' runtime profile must be alphanumeric/_/-", eid)
		}
	}

	if v, present := rt["engine"]; present && v != nil {
		s, _ := v.(string)
		// The engine is a property of a Colima profile. Under the docker
		// provider nothing reads it — not even `engine: docker`, which is
		// merely true rather than honoured — and under host there are no
		// containers at all. Storing a field devhub does not consult is how a
		// config starts meaning something it does not.
		if provider != providerColima {
			return "", fmt.Errorf("Environment '%s' runtime engine is only valid for the colima provider", eid)
		}
		if s != engineDocker && s != engineContainerd {
			return "", fmt.Errorf("Environment '%s' runtime engine must be 'docker' or 'containerd'", eid)
		}
	}
	return provider, nil
}

// validateComponentDeps rejects unknown/circular dependencies between
// components and the shared→scenario dependency direction: a shared component
// depending on a scenario-scoped one would break as soon as that scenario's
// components are stopped by a switch.
func validateComponentDeps(comps []component, eid string) error {
	byID := map[string]component{}
	for _, c := range comps {
		byID[c.ID] = c
	}
	for _, c := range comps {
		if !c.Shared {
			continue
		}
		for _, dep := range c.DependsOn {
			if d, ok := byID[dep]; ok && !d.Shared {
				return fmt.Errorf("Shared component '%s' cannot depend on scenario component '%s' in environment '%s'", c.ID, dep, eid)
			}
		}
	}
	_, unknownDep, badComp, cyclic := topoOrder(componentNodes(comps))
	if unknownDep != "" {
		return fmt.Errorf("Dependency '%s' for component '%s' not found in environment '%s'", unknownDep, badComp, eid)
	}
	if cyclic {
		return fmt.Errorf("Circular dependency detected in environment '%s'", eid)
	}
	return nil
}

// validateScenarios checks scenario ids and that every referenced component
// exists. Listing a shared component is allowed (it is implicit anyway).
func validateScenarios(env map[string]any, comps []component, eid string) error {
	known := map[string]bool{}
	for _, c := range comps {
		known[c.ID] = true
	}
	if raw, present := env["scenarios"]; present && raw != nil {
		if _, ok := raw.([]any); !ok {
			return fmt.Errorf("Environment '%s' scenarios must be an array", eid)
		}
	}
	scenIDs := map[string]bool{}
	for _, item := range toAnySlice(env["scenarios"]) {
		sm, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("Environment '%s' scenarios must be objects", eid)
		}
		sid, ok := sm["id"].(string)
		if !ok || sid == "" || !envIDRe.MatchString(sid) {
			return fmt.Errorf("Scenario ID is required and must be alphanumeric/_/- in environment '%s'", eid)
		}
		if scenIDs[sid] {
			return fmt.Errorf("Duplicate scenario ID '%s' in environment '%s'", sid, eid)
		}
		scenIDs[sid] = true
		refs, ok := stringList(sm["components"])
		if !ok {
			return fmt.Errorf("Scenario '%s' components must be an array of component ids in environment '%s'", sid, eid)
		}
		for _, cid := range refs {
			if !known[cid] {
				return fmt.Errorf("Scenario '%s' references unknown component '%s' in environment '%s'", sid, cid, eid)
			}
		}
	}
	return nil
}

// validateDeps checks for unknown/circular dependencies with env-scoped messages.
// It shares the dependency-sort core with topoSort (planner.go) so the two can't
// drift; only the error wording differs (scoped to the environment id here).
func validateDeps(procs []map[string]any, eid string) error {
	_, unknownDep, badProc, cyclic := topoOrder(procNodes(decodeProcesses(procs)))
	if unknownDep != "" {
		return fmt.Errorf("Dependency '%s' for process '%s' not found in environment '%s'", unknownDep, badProc, eid)
	}
	if cyclic {
		return fmt.Errorf("Circular dependency detected in environment '%s'", eid)
	}
	return nil
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

// stringList reads a JSON array of non-empty strings — the shape v2 uses for
// id lists (depends_on, scenario components) and compose files/services. An
// absent value yields no entries and ok=true; a present value that is not such
// an array yields ok=false, leaving the field-specific message to the caller.
func stringList(raw any) ([]string, bool) {
	if raw == nil {
		return nil, true
	}
	arr, isArray := raw.([]any)
	if !isArray {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, isString := item.(string)
		if !isString || strings.TrimSpace(s) == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
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
