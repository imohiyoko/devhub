// Package container is devhub's view of the container runtimes on this
// machine: which execution bases exist, what they can drive, and how to run a
// command against a chosen one.
//
// It owns the vocabulary (provider, engine, Colima profile, Docker context) and
// every spawn that talks to a container CLI. Consumers declare *what* they want
// operated on — env-launcher declares a compose project per component — and
// this package decides what argv that becomes and which engine answers.
//
// Two rules hold throughout and are the reason the package exists as a seam.
// Nothing changes the global Docker context — it is passed per command instead
// (plan §6.3). And nothing starts, stops or reconfigures a Colima profile as a
// side effect of something else: a switch, a status read and a page load all
// leave a stopped VM stopped, and hand the user the command (plan §13). The one
// exception is deliberate and narrow — see profile.go, where a request whose
// entire purpose is to create or resize a profile does exactly that and nothing
// reaches it by accident.
package container

// Runtime providers and container engines (plan §5). A provider is where
// commands run; an engine is what runs containers inside a provider.
const (
	ProviderHost     = "host"
	ProviderDocker   = "docker"
	ProviderColima   = "colima"
	EngineDocker     = "docker"
	EngineContainerd = "containerd"

	// DefaultColimaProfile is the profile colima itself uses when none is
	// given (`colima start` without -p).
	DefaultColimaProfile = "default"
)

// Spec is the execution base a consumer declares for its container work.
// Profile and Engine are meaningful for the colima provider only.
//
// The zero-ish default is provider "docker": a definition that says nothing
// keeps using whatever Docker context the user's shell resolves to, which is
// what devhub did before runtimes existed.
type Spec struct {
	Provider string // ProviderHost | ProviderDocker | ProviderColima
	Profile  string // colima profile name
	Engine   string // EngineDocker | EngineContainerd; "" means "whatever the profile runs"
}

// ComposeSpec locates a set of Compose services: where to run compose, which
// files, under which project name (the ownership marker every operation is
// confined to), and which services.
type ComposeSpec struct {
	Cwd      string
	Files    []string
	Project  string
	Services []string
}

// State is an observed state. Unknown is not a failure: it means devhub could
// not look (the engine is unreachable, the directory is gone), and a caller
// must not act on it as though it meant stopped.
type State string

const (
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateUnknown State = "unknown"
)

// Runtime is the set of engines devhub can drive on this host, plus the Colima
// prober that says which profiles exist. The fields are exported so a consumer
// can substitute fakes in its own tests and never reach a real daemon.
//
// All five are required. Build one with New, or — in a test — set every field;
// the methods dereference them without a nil check on purpose, so a half-wired
// Runtime fails at the line that wired it rather than reporting a host that
// merely looks like it has nothing installed.
type Runtime struct {
	Docker     Adapter
	Containerd Adapter
	Colima     ProfileLister
	// Inventory answers "what is on this machine", as opposed to the adapters'
	// "is what this environment declared running". It is a separate seam
	// because it is read-only and deliberately not scoped to any compose
	// project — see internal/container/inventory.go.
	Inventory Lister
	// Admin is the only seam that moves a VM rather than reading one. It is
	// reached solely by requests that exist to create or resize a profile;
	// nothing devhub does on its own touches it (see profile.go).
	Admin ProfileManager
}

// New wires the real implementations. Nothing is probed here: construction is
// cheap and spawns nothing, so a host with neither Docker nor Colima installed
// pays nothing until something is actually asked of it.
func New() *Runtime {
	colima := newColimaCLI()
	return &Runtime{
		Docker:     newDockerCompose(),
		Containerd: newNerdctlCompose(),
		Colima:     colima,
		Inventory:  newCLIInventory(),
		Admin:      newColimaAdmin(colima),
	}
}
