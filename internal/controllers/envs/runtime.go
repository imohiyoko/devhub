package envs

// The runtime capability surface (plan §9 `GET /api/envs/runtimes`): what
// execution bases this host can offer, which container engines each one can
// run, and — for Colima — which profiles exist and what state they are in.
//
// The UI renders whatever this returns instead of hardcoding provider or
// engine names (plan §6.4), so a host with no Docker and no Colima still gets
// a coherent answer: the providers are listed as unavailable with the reason
// devhub actually observed.

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// runtimeProbeTimeout bounds the whole capability report. The runtimes
// endpoint is on the UI's load path, so it must fail rather than hang.
const runtimeProbeTimeout = 10 * time.Second

// RuntimeProfile is one Colima profile as the UI sees it.
type RuntimeProfile struct {
	Name   string
	Status string
	// Engine is Colima's own value, verbatim. It is empty when it cannot be
	// observed (a stopped profile does not report one); the UI shows that as
	// unknown, and devhub does not infer it.
	Engine string
	Arch   string
	// Context is the Docker context this profile's docker engine listens on.
	// It is what devhub would pass per command, and never something it sets
	// globally (plan §6.3).
	Context string
	// Supported is false when the profile runs an engine devhub has no adapter
	// for — Colima can also host incus, which this tool cannot drive. Reason
	// says which engine that is, so such a profile is listed with an
	// explanation rather than hidden, or worse, offered as a valid choice that
	// save-time validation would then reject.
	Supported bool
	Reason    string
}

// RuntimeProvider is one execution base and what it can currently do.
type RuntimeProvider struct {
	ID        string
	Label     string
	Available bool
	// Reason explains an unavailable provider. It is empty when Available.
	Reason string
	// Engines are the container engines this provider can run. Empty for the
	// host provider, which runs processes rather than containers.
	Engines  []string
	Profiles []RuntimeProfile
}

// RuntimeProviders reports the execution bases available on this host. It is
// read-only: it looks for CLIs and lists Colima profiles, and starts nothing.
func (c *Controller) RuntimeProviders(ctx context.Context) []RuntimeProvider {
	host := RuntimeProvider{ID: providerHost, Label: "ホスト", Available: true, Engines: []string{}}

	docker := RuntimeProvider{ID: providerDocker, Label: "Docker", Available: true, Engines: []string{engineDocker}}
	if err := c.compose.Available(ctx); err != nil {
		docker.Available, docker.Reason = false, err.Error()
	}

	// Colima can host either engine, so both are offered even on a host where
	// no profile currently runs containerd: the engine list describes the
	// provider's capability, while a profile's own Engine describes reality.
	colima := RuntimeProvider{ID: providerColima, Label: "Colima", Engines: []string{engineDocker, engineContainerd}}
	profiles, err := c.colima.Profiles(ctx)
	if err != nil {
		colima.Reason = err.Error()
	} else {
		colima.Available = true
		for _, p := range profiles {
			supported, reason := engineSupport(p.Engine, colima.Engines)
			colima.Profiles = append(colima.Profiles, RuntimeProfile{
				Name: p.Name, Status: p.Status, Engine: p.Engine, Arch: p.Arch,
				Context: colimaDockerContext(p.Name), Supported: supported, Reason: reason,
			})
		}
	}

	return []RuntimeProvider{host, docker, colima}
}

// engineSupport judges whether devhub can drive a profile. Colima also hosts
// engines this tool has no adapter for (incus), and such a profile must be
// reported as unusable up front rather than offered and then rejected at save
// time. An engine devhub cannot observe — a stopped profile reports none — is
// not called unsupported: nothing is known about it yet.
func engineSupport(engine string, supported []string) (bool, string) {
	if engine == "" || slices.Contains(supported, engine) {
		return true, ""
	}
	return false, fmt.Sprintf("engine '%s' に対応するアダプタがありません", engine)
}

// runtimeProvidersJSON renders the capability report for the API. Slices are
// materialised as empty arrays rather than null so the UI can iterate without
// null checks.
func runtimeProvidersJSON(providers []RuntimeProvider) map[string]any {
	out := make([]any, 0, len(providers))
	for _, p := range providers {
		profiles := make([]any, 0, len(p.Profiles))
		for _, pr := range p.Profiles {
			profiles = append(profiles, map[string]any{
				"name": pr.Name, "status": pr.Status, "engine": pr.Engine,
				"arch": pr.Arch, "context": pr.Context,
				"supported": pr.Supported, "reason": pr.Reason,
			})
		}
		engines := make([]any, 0, len(p.Engines))
		for _, e := range p.Engines {
			engines = append(engines, e)
		}
		out = append(out, map[string]any{
			"id": p.ID, "label": p.Label, "available": p.Available, "reason": p.Reason,
			"engines": engines, "profiles": profiles,
		})
	}
	return map[string]any{"providers": out}
}
