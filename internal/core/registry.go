package core

import "sort"

// Registry is the single list of tools the gateway serves. Build it once in the
// composition root — this is the only place that grows when you add a tool:
//
//	reg := core.NewRegistry(
//	    git.New(d), ports.New(d), workspace.New(d),
//	    envs.New(d), database.New(d), settings.New(d),
//	    // ← new tools: one line here, nothing else in core changes.
//	)
//
// Registration is an explicit list (not init()-based self-registration) on
// purpose: greppable, deterministic order, trivially testable, no import-for-
// side-effect magic.
type Registry struct {
	tools []Tool
}

// NewRegistry builds a registry from the given tools, in order.
func NewRegistry(tools ...Tool) *Registry {
	return &Registry{tools: tools}
}

// Tools returns the registered tools in registration order.
func (r *Registry) Tools() []Tool { return r.tools }

// Metas returns every tool's Meta, sorted by ID, for the dashboard nav served
// at GET /api/tools. The dashboard already renders cards from a data array, so
// it only needs to switch its data source to this endpoint.
func (r *Registry) Metas() []Meta {
	out := make([]Meta, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Meta())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
