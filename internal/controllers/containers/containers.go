// Package containers implements the container inventory endpoint
// (/api/containers): what is actually on this machine, across every Docker
// context and Colima profile devhub can reach.
//
// It is the counterpart to env-launcher, not a part of it. env-launcher answers
// a question about a *declaration* — are the components this environment
// declares running — and by construction cannot show anything the declaration
// does not name. This tool answers a question about the *machine*, which is the
// only way a leftover from a renamed project, or a stray container holding a
// port, becomes visible at all. The precedent is ports: a machine-wide panel
// that env-launcher consumes rather than contains.
//
// Everything here is read-only. The panel's operations (logs, stop, restart)
// are deliberately a later, separate change with its own execaudit Surface.
package containers

import (
	"context"
	"net/http"
	"sort"

	"github.com/imohiyoko/devhub/internal/container"
	"github.com/imohiyoko/devhub/internal/httpx"
)

// inventory is the narrow view this controller needs of the container package:
// one call, no arguments it could get wrong. Declared here, in the consumer, so
// the tests below answer without a daemon.
type inventory interface {
	Containers(ctx context.Context) ([]container.Source, []container.Container)
}

// Controller serves the container inventory.
type Controller struct{ runtime inventory }

// New returns a containers controller wired to the real CLIs. Nothing is probed
// at construction — that only happens when a request arrives.
func New() *Controller { return &Controller{runtime: container.New()} }

// HandleGet serves GET /api/containers. The request's context is passed
// through, so a user who closes the tab stops the listings; the deadline itself
// belongs to the container package, which applies one per source.
func (c *Controller) HandleGet(w http.ResponseWriter, r *http.Request) error {
	sources, list := c.runtime.Containers(r.Context())
	httpx.WriteJSON(w, http.StatusOK, inventoryJSON(sources, list))
	return nil
}

// inventoryJSON renders the payload. Slices are materialised as empty arrays
// rather than null so the UI can iterate without null checks, the same
// convention the runtimes endpoint follows.
//
// Sources are returned even when they could not be listed: the panel shows a
// stopped Colima profile with the command that would start it, which is
// information a caller cannot reconstruct from an absence.
func inventoryJSON(sources []container.Source, list []container.Container) map[string]any {
	srcOut := make([]any, 0, len(sources))
	for _, s := range sources {
		entry := map[string]any{
			"id": s.ID, "label": s.Label, "context": s.Context,
			"profile": s.Profile, "engine": s.Engine,
			"available": s.Available, "reason": s.Reason,
			"alias_of": s.AliasOf,
		}
		// Only Colima sources have a VM behind them, and only a VM has a size.
		// Absent keys rather than zeroes: "6 CPUs" and "unknown" must not look
		// the same, and 0 would render as a real answer.
		if s.CPUs > 0 {
			entry["cpus"] = s.CPUs
		}
		if s.MemoryBytes > 0 {
			entry["memory_bytes"] = s.MemoryBytes
		}
		if s.DiskBytes > 0 {
			entry["disk_bytes"] = s.DiskBytes
		}
		srcOut = append(srcOut, entry)
	}

	sorted := make([]container.Container, len(list))
	copy(sorted, list)
	sortContainers(sorted)

	out := make([]any, 0, len(sorted))
	for _, c := range sorted {
		out = append(out, map[string]any{
			"id": c.ID, "name": c.Name, "display_name": c.DisplayName(),
			"image": c.Image, "state": c.State, "running": c.Running(),
			"status": c.Status, "ports": c.Ports,
			"project": c.Project, "service": c.Service, "source": c.Source,
		})
	}
	return map[string]any{"sources": srcOut, "containers": out}
}

// sortContainers puts running containers first, then groups by compose project
// so a stack reads as one block, and finally orders by name. Containers with no
// project sort after the ones that have one: they are the rows that need a
// human decision, and burying them among a stack's services hides them.
func sortContainers(list []container.Container) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Running() != b.Running() {
			return a.Running()
		}
		if (a.Project == "") != (b.Project == "") {
			return a.Project != ""
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.DisplayName() < b.DisplayName()
	})
}
