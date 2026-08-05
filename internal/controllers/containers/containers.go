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
// Reading the machine is what this file does, and nothing on the path a page
// load takes changes anything. The exceptions live in profiles.go: creating and
// resizing a Colima VM, each one a request whose whole purpose is that, and
// neither reachable as a side effect of looking at the panel. The container
// operations proper — logs, stop, restart — are still a later, separate change
// with their own execaudit Surface.
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

// settingsReader is the narrow persistence this controller needs: the shared
// settings document, where vm_reserve lives. Declared in the consumer, and read
// live on every decision rather than snapshotted at boot — the same shape the
// ports controller uses for protected_ports, and for the same reason: a limit
// the user just changed should be the limit in force.
type settingsReader interface {
	LoadSettings() (map[string]any, error)
}

// Controller serves the container inventory and the profile operations.
type Controller struct {
	runtime inventory
	admin   admin
	control operator
}

// New returns a containers controller wired to the real CLIs. Nothing is probed
// at construction — that only happens when a request arrives.
//
// The store is here only to answer "how much of this machine is off limits".
// It is handed to the container package as a func, so the reserve is read at
// the moment a size is judged rather than fixed when devhub started.
func New(store settingsReader) *Controller {
	rt := container.New(container.WithReserve(func() container.Reserve {
		return reserveFrom(store)
	}))
	return &Controller{runtime: rt, admin: profileAdmin{rt}, control: containerOps{rt}}
}

// reserveFrom reads the configured reserve, falling back to the default.
//
// A store that cannot be read, or a value that no longer parses, yields the
// default rather than an error: this is consulted on the path that decides
// whether a VM may be created, and failing that decision over an unreadable
// preference would take the panel down over a setting. The settings endpoint
// refuses a malformed value at save time, which is where a person is watching.
func reserveFrom(store settingsReader) container.Reserve {
	if store == nil {
		return container.DefaultReserve()
	}
	settings, err := store.LoadSettings()
	if err != nil {
		return container.DefaultReserve()
	}
	res, err := container.NormalizeReserve(settings["vm_reserve"])
	if err != nil {
		return container.DefaultReserve()
	}
	return res
}

// containerOps joins the two halves the operator interface needs, the same way
// profileAdmin does: the operations live on Runtime.Control, the resolve — the
// lookup that decides whether an operation may run at all — on Runtime itself,
// because it needs the inventory as well.
type containerOps struct{ rt *container.Runtime }

func (o containerOps) Resolve(ctx context.Context, sourceID, id string) (container.ContainerTarget, error) {
	return o.rt.ResolveContainer(ctx, sourceID, id)
}

func (o containerOps) Logs(ctx context.Context, src container.Source, id string, tail int) (string, error) {
	return o.rt.Control.Logs(ctx, src, id, tail)
}

func (o containerOps) Stop(ctx context.Context, src container.Source, id string) error {
	return o.rt.Control.Stop(ctx, src, id)
}

func (o containerOps) Start(ctx context.Context, src container.Source, id string) error {
	return o.rt.Control.Start(ctx, src, id)
}

func (o containerOps) Restart(ctx context.Context, src container.Source, id string) error {
	return o.rt.Control.Restart(ctx, src, id)
}

// profileAdmin joins the two halves the admin interface needs: the VM
// operations live on Runtime.Admin, the "what would this stop" read on Runtime
// itself. Kept here rather than widening either of those, so the container
// package does not grow a type that exists only for this controller's shape.
type profileAdmin struct{ rt *container.Runtime }

func (p profileAdmin) Create(ctx context.Context, spec container.ProfileSpec) error {
	return p.rt.Admin.Create(ctx, spec)
}

func (p profileAdmin) Resize(ctx context.Context, spec container.ProfileSpec) error {
	return p.rt.Admin.Resize(ctx, spec)
}

func (p profileAdmin) CheckResize(ctx context.Context, spec container.ProfileSpec) error {
	return p.rt.Admin.CheckResize(ctx, spec)
}

func (p profileAdmin) Start(ctx context.Context, name string) error {
	return p.rt.Admin.Start(ctx, name)
}

func (p profileAdmin) Stop(ctx context.Context, name string) error {
	return p.rt.Admin.Stop(ctx, name)
}

func (p profileAdmin) ProfileTargets(ctx context.Context, name string) ([]container.Container, error) {
	return p.rt.ProfileTargets(ctx, name)
}

func (p profileAdmin) Budget(ctx context.Context) (container.Budget, error) {
	return p.rt.Admin.Budget(ctx)
}

// HandleGet serves GET /api/containers. The request's context is passed
// through, so a user who closes the tab stops the listings; the deadline itself
// belongs to the container package, which applies one per source.
func (c *Controller) HandleGet(w http.ResponseWriter, r *http.Request) error {
	sources, list := c.runtime.Containers(r.Context())
	out := inventoryJSON(sources, list)
	// The budget rides along with the listing rather than living behind a
	// second endpoint. The panel needs it on every load — a size field with no
	// limit on screen is a limit the user meets by being refused — and a new
	// route would be a new line in the execaudit ledger for a payload that
	// spawns nothing.
	if c.admin != nil {
		if b, err := c.admin.Budget(r.Context()); err == nil && b.Detected {
			out["host"] = budgetJSON(b)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// budgetJSON renders the host and its caps. Absent entirely when devhub cannot
// measure the machine — a panel showing "0 CPU" would be stating a limit that
// does not exist, and the caps are not applied in that case either.
func budgetJSON(b container.Budget) map[string]any {
	return map[string]any{
		"cpus": b.HostCPUs, "memory_bytes": b.HostMemBytes,
		// Reported so the profile form can show it. Never a limit: Lima's disk
		// images are sparse, so declaring more than this is legitimate.
		"free_disk_bytes": b.FreeDiskBytes,
		"cpu_cap":         b.CPUCap,
		"memory_cap_gib":  b.MemCapGiB,
		"reserve":         b.Reserve.JSON(),
		"running_cpus":    b.RunningCPUs,
		"running_mem_gib": b.RunningMemGiB,
	}
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
		// Only Colima sources have a VM behind them, and only a VM has a size or
		// a status. Absent keys rather than zeroes: "6 CPUs" and "unknown" must
		// not look the same, and 0 would render as a real answer.
		//
		// The status is what lets a caller tell a profile that is merely stopped
		// from one devhub cannot drive — available is false for both, and only
		// the first is fixed by starting it.
		if s.Status != "" {
			entry["status"] = s.Status
		}
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
