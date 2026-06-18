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

// Registry constructs every devhub tool and returns the registry served by the
// core gateway. This is the single composition root: adding a tool means adding
// one constructor to the list below — server, router, and the dashboard nav are
// all derived from it.
//
// Controllers are constructed once here and shared. The env-launcher genuinely
// orchestrates git/ports/workspace, so it receives those controllers directly;
// keeping that wiring in this layer (not in core or server) is what lets a tool
// later be extracted behind the gateway without disturbing the others.
func Registry(store *storage.Store) *core.Registry {
	git := gitctl.New(store)
	ports := portsctl.New(store)
	workspace := workspacectl.New(store, git)
	database := databasectl.New(store)
	envs := envsctl.New(store, git, ports, workspace)
	settings := settingsctl.New(store)

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
