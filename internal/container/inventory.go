package container

// The inventory half of this package: what containers actually exist on this
// machine, as opposed to what an environment declares. env-launcher answers
// "are my components running"; this answers "what is on here at all", which is
// the only way to see a container devhub never declared — a leftover from a
// project that was renamed, something holding a port, a compose stack that
// exited for reasons the declaring environment cannot show.
//
// Two properties separate it from the adapter calls in runtime_docker.go and
// runtime_containerd.go, and they are why the spawn here is a different seam
// with its own execaudit Surface rather than a reuse of execRunner:
//
//   - Those are confined to a declared compose project. These are deliberately
//     machine-wide. Sharing one Surface would turn an exact claim ("scoped to
//     the definition's project") into a vague one, which is the opposite of
//     what the audit is for.
//   - Those read and write. These only read. Nothing in this file can start,
//     stop or remove anything.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Compose stamps these on every container it creates. They are how a container
// is traced back to the project and service that own it — the join devhub needs
// to line an inventory row up against an environment's declaration.
const (
	labelComposeProject = "com.docker.compose.project"
	labelComposeService = "com.docker.compose.service"
)

// Source is one place containers can live: the ambient Docker context, or a
// Colima profile's engine. Unavailable sources are reported rather than
// dropped, for the same reason Providers reports an unusable provider with its
// reason — a profile that is merely stopped is a thing the user can act on, and
// silently omitting it looks identical to having no containers.
type Source struct {
	ID    string // "docker", or "colima:<profile>"
	Label string
	// Context is the Docker context these containers are listed through, empty
	// for the ambient one. Profile and Engine are set for Colima sources.
	Context string
	Profile string
	Engine  string
	// Available is false when the source could not be listed at all; Reason
	// says why, in the CLI's own words where there is one.
	Available bool
	Reason    string
	// Status is Colima's own word for the VM's state ("Running", "Stopped", …),
	// empty for a Docker source. It is reported because Available collapses two
	// unlike situations into one bit: a profile that is merely stopped, and one
	// running an engine devhub cannot drive. Only the first is fixed by starting
	// it, so a caller that cannot tell them apart would offer a button that
	// changes nothing.
	Status string
	// AliasOf names the source this one turned out to be a second route to —
	// the ambient Docker context resolving to a Colima profile's VM, almost
	// always. Its containers are reported under that source instead, so they
	// are not counted twice. Empty for a source that stands alone.
	AliasOf string
	// CPUs, MemoryBytes and DiskBytes describe a Colima source's VM. Listing
	// reads them and never sets them: changing a size stops and restarts the
	// VM, so it is a request of its own (ProfileManager) and never something
	// that falls out of looking at the machine.
	CPUs        int
	MemoryBytes int64
	DiskBytes   int64
}

// Container is one container as the panel shows it. Every field is what the CLI
// reported, unparsed except for the compose labels: devhub does not know better
// than Docker what a container's state is called.
type Container struct {
	ID      string
	Name    string
	Image   string
	State   string // running | exited | created | paused | …
	Status  string // the CLI's human phrasing, e.g. "Up 3 hours"
	Ports   string
	Project string // compose project, empty when not compose-managed
	Service string
	Source  string // the Source.ID this was listed from
}

// Running reports whether the row is live. It reads the same token the compose
// adapter does, so the panel and the env-launcher state never disagree about
// what "running" means.
func (c Container) Running() bool { return c.State == "running" }

// DisplayName is what a row is labelled with. Docker reports an empty Names for
// some containers — build intermediates and the like; 3 of the 90 on the
// machine this was written on — and a blank row cannot be identified or acted
// on, so the ID stands in. Name is left as the CLI reported it, because "this
// container has no name" is itself worth being able to tell.
func (c Container) DisplayName() string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

// Lister enumerates containers from one source. It is an interface so the
// controller's tests never reach a daemon, and so the two engines can answer
// differently without the caller knowing which is which.
type Lister interface {
	// List returns everything on src, stopped containers included. The panel
	// filters; this does not, because a filter applied here would be invisible
	// to the caller and would make "nothing is running" and "nothing exists"
	// look the same.
	//
	// Implementations bound themselves, as everything else in this package
	// does: the caller sets no deadline.
	List(ctx context.Context, src Source) ([]Container, error)
}

// inventoryProbeTimeout bounds one listing. The panel is a page load, so it
// must fail rather than hang; it is applied per source, and the sources run
// concurrently, so one unreachable engine cannot spend another's budget.
const inventoryProbeTimeout = 10 * time.Second

// inventoryRunner spawns the read-only listings. It is deliberately not
// execRunner: see the file comment — different bounds, different Surface. It
// satisfies commandRunner (ignoring cwd, which no listing needs) so the tests
// here drive it with the same double as every other command in this package.
type inventoryRunner struct{}

func (inventoryRunner) Run(ctx context.Context, _, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //execaudit:containers-list
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// cliInventory lists through whichever CLI the source's engine needs. One type
// rather than two adapters: unlike the compose operations, the two engines
// differ here only in the argv — the output shape and everything devhub does
// with it are identical.
type cliInventory struct{ runner commandRunner }

func newCLIInventory() *cliInventory { return &cliInventory{runner: inventoryRunner{}} }

// List returns everything on src. The argv is fixed per engine and carries
// nothing from a request; --all/-a is what makes stopped containers visible,
// which is the panel's reason to exist.
func (i *cliInventory) List(ctx context.Context, src Source) ([]Container, error) {
	ctx, cancel := context.WithTimeout(ctx, inventoryProbeTimeout)
	defer cancel()

	name, args := "docker", []string(nil)
	if src.Engine == EngineContainerd {
		// containerd lives inside the profile's VM, reached the same way the
		// compose adapter reaches it: through colima's nerdctl passthrough,
		// with `--` keeping colima's own flags apart from nerdctl's.
		name = "colima"
		args = []string{"nerdctl", "--profile", src.Profile, "--", "ps", "-a", "--format", "json"}
	} else {
		if src.Context != "" {
			args = append(args, "--context", src.Context)
		}
		args = append(args, "ps", "--all", "--format", "json")
	}

	stdout, stderr, err := i.runner.Run(ctx, "", name, args...)
	if err != nil {
		return nil, cliError(stderr, err)
	}
	return parsePS(stdout, src.ID)
}

// Containers reports every container on this host, grouped by where it lives.
// Both return values matter: a source that could not be listed is returned
// with its reason rather than dropped, because "this profile is stopped" and
// "this profile has no containers" call for different things from the user and
// look identical once a source disappears.
//
// Sources are listed concurrently. Each bounds itself, so the whole sweep costs
// about one listing rather than the sum — and, as in Providers, an engine that
// is slow to answer cannot spend the budget of one that is fine.
func (r *Runtime) Containers(ctx context.Context) ([]Source, []Container) {
	sources := r.inventorySources(ctx)

	var wg sync.WaitGroup
	found := make([][]Container, len(sources))
	for i := range sources {
		if !sources[i].Available {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			list, err := r.Inventory.List(ctx, sources[i])
			if err != nil {
				sources[i].Available, sources[i].Reason = false, err.Error()
				return
			}
			found[i] = list
		}(i)
	}
	wg.Wait()
	collapseAliases(sources, found)

	var all []Container
	for _, list := range found {
		all = append(all, list...)
	}
	return sources, all
}

// collapseAliases folds sources that turned out to be the same daemon reached
// two ways.
//
// They routinely are. `colima start` creates a docker context for the profile
// and makes it current, so the ambient context — the one `docker ps` uses with
// no --context — is that same VM. A DOCKER_HOST pointing at the profile's
// socket does it too, and that one survives `colima stop`, which removes the
// context. Left alone, every container would be listed twice: once under
// Docker, once under the profile. For a panel whose whole claim is an accurate
// answer to "what is on this machine", twelve containers shown as twenty-four
// is wrong in the same way as showing none.
//
// The test is the container IDs, not the context name. Name comparison is what
// suggests itself and it does not hold: with DOCKER_HOST set, `docker context
// show` reports "default" while the ambient socket is the profile's. Two
// sources that returned a container with the same ID are the same daemon,
// whatever route each took to it.
//
// The profile keeps the rows, because it is the more specific answer — it can
// say which VM, and how big. The ambient source is marked as pointing there
// rather than dropped: "Docker and this profile are the same thing right now"
// is worth seeing, and a source that silently vanished would look like a bug.
func collapseAliases(sources []Source, found [][]Container) {
	seen := map[string]int{} // container ID -> index of the source that keeps it
	// Colima sources go first so they win the rows; the ambient source is the
	// one that gets folded.
	order := make([]int, 0, len(sources))
	for i, s := range sources {
		if s.ID != ProviderDocker {
			order = append(order, i)
		}
	}
	for i, s := range sources {
		if s.ID == ProviderDocker {
			order = append(order, i)
		}
	}

	for _, i := range order {
		if len(found[i]) == 0 {
			continue
		}
		// Any overlap, not just the first row. The listings run concurrently,
		// and `ps` prints newest first, so a container created between the two
		// calls shifts one list's head — matching on that alone would drop the
		// collapse and put every container on screen twice.
		owner := -1
		for _, c := range found[i] {
			if c.ID == "" {
				continue // parsePS rejects these; never match on one anyway
			}
			if o, dup := seen[c.ID]; dup {
				owner = o
				break
			}
		}
		if owner >= 0 {
			sources[i].AliasOf = sources[owner].ID
			found[i] = nil
			continue
		}
		for _, c := range found[i] {
			if c.ID != "" {
				seen[c.ID] = i
			}
		}
	}
}

// inventorySources derives what to list from the capability report, so the
// panel and the runtime picker can never disagree about which engines exist.
// Nothing here is user input: every context and profile name came from Colima's
// own output.
func (r *Runtime) inventorySources(ctx context.Context) []Source {
	var sources []Source
	for _, p := range r.Providers(ctx) {
		switch p.ID {
		case ProviderDocker:
			sources = append(sources, Source{
				ID: ProviderDocker, Label: "Docker", Available: p.Available, Reason: p.Reason,
			})
		case ProviderColima:
			if !p.Available {
				sources = append(sources, Source{
					ID: ProviderColima, Label: "Colima", Available: false, Reason: p.Reason,
				})
				continue
			}
			for _, profile := range p.Profiles {
				sources = append(sources, colimaSource(profile))
			}
		}
	}
	return sources
}

// colimaSource turns one profile into a listable source, or into an entry that
// explains why it is not one. A stopped profile is the common case and the one
// worth naming: its containers still exist on disk, devhub simply cannot see
// them until the VM is started, which a listing never does on its own (plan §13).
func colimaSource(p Profile) Source {
	src := Source{
		ID: ProviderColima + ":" + p.Name, Label: "Colima: " + p.Name,
		Profile: p.Name, Engine: p.Engine, Context: p.Context, Status: p.Status,
		CPUs: p.CPUs, MemoryBytes: p.MemoryBytes, DiskBytes: p.DiskBytes,
	}
	switch {
	// Checked before the status: if devhub knows it cannot drive this engine,
	// "start the VM" is wrong advice, because starting it changes nothing. A
	// stopped profile usually reports no engine at all, so this mostly matters
	// for a broken one — and for whatever colima reports next.
	case p.Engine != "" && !p.Supported:
		src.Reason = p.Reason
	case !strings.EqualFold(p.Status, "Running"):
		src.Reason = fmt.Sprintf("profile '%s' は %s です。`colima start -p %s` で起動してください。", p.Name, p.Status, p.Name)
	case !p.Supported:
		src.Reason = p.Reason
	default:
		src.Available = true
	}
	return src
}

// psEntry is the subset of a `ps --format json` row devhub reads. The field
// names were read off real output from Docker 29.4.0, which prints one JSON
// object per line and carries ten more keys (Command, CreatedAt, Mounts,
// Networks, Platform, …) this deliberately ignores.
//
// parsePS still makes a mismatch loud rather than silent: a row that yields
// neither an ID nor a name is an error, not a skip. A future release renaming a
// key would otherwise surface as "this host has no containers", which is the
// one wrong answer a panel about finding stray containers must never give.
type psEntry struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Ports  string `json:"Ports"`
	Labels string `json:"Labels"`
}

// parsePS reads `ps --format json` from either engine. Both shapes are
// accepted — a JSON array, or one object per line — for the same reason
// parseComposePS accepts both: the release decides which one it prints, and
// devhub should not pin that.
func parsePS(out, source string) ([]Container, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var entries []psEntry
	if strings.HasPrefix(out, "[") {
		if err := json.Unmarshal([]byte(out), &entries); err != nil {
			return nil, fmt.Errorf("ps の出力を解釈できません: %w", err)
		}
	} else {
		for line := range strings.SplitSeq(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var entry psEntry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				return nil, fmt.Errorf("ps の出力を解釈できません: %w", err)
			}
			entries = append(entries, entry)
		}
	}

	out2 := make([]Container, 0, len(entries))
	for _, e := range entries {
		// The loudness rule from psEntry's comment, keyed on the one field that
		// is always there. Names is genuinely optional — real output has rows
		// without it — but every container has an ID, so an empty one means the
		// keys are not the ones assumed here.
		//
		// Requiring it matters beyond the error message: collapseAliases treats
		// equal IDs as the same daemon, so a schema change that renamed only ID
		// would give every row the same empty one, and every source after the
		// first would be folded away as a duplicate. Rows would vanish silently,
		// which is worse than the empty list this rule was written to prevent.
		if e.ID == "" {
			return nil, fmt.Errorf("ps の出力に想定した項目がありません（ID）。docker/nerdctl の出力形式が変わった可能性があります")
		}
		labels := parseLabels(e.Labels)
		out2 = append(out2, Container{
			ID:      e.ID,
			Name:    firstName(e.Names),
			Image:   e.Image,
			State:   strings.ToLower(e.State),
			Status:  e.Status,
			Ports:   e.Ports,
			Project: labels[labelComposeProject],
			Service: labels[labelComposeService],
			Source:  source,
		})
	}
	return out2, nil
}

// parseLabels splits the CLI's comma-joined "k=v,k=v" label string.
//
// The encoding is lossy and `ps --format json` offers no alternative: a value
// that itself contains a comma is indistinguishable from a label boundary.
// That is not hypothetical — compose sets com.docker.compose.project.config_files
// to a comma-separated list of paths, so any stack with two compose files
// produces one. Leftover fragments carry no "=" and are dropped, which is why
// the two labels devhub reads survive it: project and service names contain no
// commas.
//
// First occurrence wins, which matters for the one case the split can be
// abused. Docker emits labels sorted by the joined "k=v" string, so a container
// that set `description=x,com.docker.compose.project=someone-elses` would have
// its forged pair land *after* the genuine com.docker.compose.project= — and
// last-wins would take the forgery. This is not airtight (a key sorting before
// "com.docker.compose.project=" could still inject) but it covers the ordinary
// label namespaces, which is where anything set by hand tends to live.
//
// Checked against Docker's own extraction, `ps --format '{{.Label "…"}}'`,
// which does not go through this encoding: identical project and service for
// all 90 containers on the machine this was written on.
func parseLabels(s string) map[string]string {
	labels := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if _, seen := labels[k]; !seen {
			labels[k] = v
		}
	}
	return labels
}

// firstName takes the first of the comma-separated names a container may carry.
// Docker reports every alias; the panel shows one, and the first is the one the
// CLI itself displays.
func firstName(names string) string {
	name, _, _ := strings.Cut(names, ",")
	return strings.TrimSpace(name)
}
