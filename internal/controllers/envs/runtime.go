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
)

// RuntimeProfile is one Colima profile as the UI sees it.
type RuntimeProfile struct {
	Name   string
	Status string
	// Engine is empty when it cannot be observed (a stopped profile does not
	// report one). The UI shows that as unknown; devhub does not infer it.
	Engine string
	Arch   string
	// Context is the Docker context this profile's docker engine listens on.
	// It is what devhub would pass per command, and never something it sets
	// globally (plan §6.3).
	Context string
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
	if err := c.compose.Available(); err != nil {
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
			colima.Profiles = append(colima.Profiles, RuntimeProfile{
				Name: p.Name, Status: p.Status, Engine: p.Engine, Arch: p.Arch,
				Context: colimaDockerContext(p.Name),
			})
		}
	}

	return []RuntimeProvider{host, docker, colima}
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
