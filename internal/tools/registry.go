package tools

import (
	"github.com/imohiyoko/devhub/internal/core"
	"github.com/imohiyoko/devhub/internal/storage"
)

// Registry builds the set of tools served through the core gateway. This is the
// one place that grows when a tool is migrated onto the core contract: add its
// constructor to the list. Tools still on the legacy router are reached via the
// gateway's Next fallthrough until they move here.
//
// Migration status: git. (settings, ports, workspace, database, envs follow.)
func Registry(store *storage.Store) *core.Registry {
	return core.NewRegistry(
		NewGit(store),
	)
}
