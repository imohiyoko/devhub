package envs

// The env-launcher side of the runtime story. The capability model and every
// container CLI call live in internal/container; what is left here is the part
// that is specific to this tool — rendering that model for its own API.

import (
	"github.com/imohiyoko/devhub/internal/container"
)

// runtimeProvidersJSON renders the capability report for GET /api/envs/runtimes
// (plan §9). Slices are materialised as empty arrays rather than null so the UI
// can iterate without null checks.
func runtimeProvidersJSON(providers []container.Provider) map[string]any {
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
			"id": p.ID, "label": p.Label, "available": p.Available,
			"supported": p.Supported, "reason": p.Reason,
			"engines": engines, "profiles": profiles,
		})
	}
	return map[string]any{"providers": out}
}
