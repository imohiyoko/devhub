package tools

import (
	databasectl "github.com/imohiyoko/devhub/internal/controllers/database"
	envsctl "github.com/imohiyoko/devhub/internal/controllers/envs"
	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
	settingsctl "github.com/imohiyoko/devhub/internal/controllers/settings"
	workspacectl "github.com/imohiyoko/devhub/internal/controllers/workspace"
	"github.com/imohiyoko/devhub/internal/core"
	"github.com/imohiyoko/devhub/internal/storage"
)

// The concrete store is the default core.Store backing every tool's namespaced
// view. Assert the seam here, in the one package that wires them together.
var _ core.Store = (*storage.Store)(nil)

// Registry constructs every devhub tool and returns the registry served by the
// core gateway. This is the single composition root: adding a tool means adding
// one constructor to the list below — server, router, and the dashboard nav are
// all derived from it.
//
// Controllers are constructed once here and shared. The env-launcher genuinely
// orchestrates git/ports/workspace, so it receives those controllers directly;
// keeping that wiring in this layer (not in core or server) is what lets a tool
// later be extracted behind the gateway without disturbing the others.
//
// This is also the single place core.Deps is assembled: the concrete
// *storage.Store satisfies core.Store (see internal/storage/kv.go), so a tool
// whose state is a plain key/value document is handed a core.Namespace view
// instead of the concrete store — making per-tool data ownership structural.
// The settings tool is the first mover: its per-tool "tool:<id>" document flows
// through that seam.
//
// No controller takes the concrete *storage.Store anymore: each defines a narrow
// interface capturing exactly the store methods it uses (defined in the consumer
// package, the Go idiom), and *storage.Store satisfies them all — so these calls
// are unchanged while controllers can now be built against a fake in tests. The
// controllers that read shared global documents (git config, the settings
// allowlist) keep typed helpers behind those interfaces rather than the raw
// key/value seam, because forcing them onto a per-tool Namespace would change
// their data semantics. envs is the one rich consumer (launch registry).
func Registry(store *storage.Store) *core.Registry {
	deps := core.Deps{Store: store}

	git := gitctl.New(store)
	ports := portsctl.New(store)
	workspace := workspacectl.New(store, git)
	database := databasectl.New(store)
	envs := envsctl.New(store, git, ports, workspace)
	settings := settingsctl.New(store, core.Namespace(deps.Store, "tool"))

	return core.NewRegistry(
		newGit(git),
		newPorts(ports),
		newWorkspace(workspace),
		newDatabase(database),
		newEnvs(envs),
		newSettings(settings),
		newDiffKun(),
		newDiagram(),
		// ← new tools: add one constructor here. Nothing else in core/server changes.
	)
}
