// Package devhub holds the embedded frontend and example settings used by the
// devhub server. The embed directive lives at the module root because go:embed
// can only reference files at or below its own package directory.
package devhub

import "embed"

// Assets bundles the served frontend (dashboard/, tools/, shared/) and the
// committed example settings used to seed first-run config. Runtime files
// (devhub.db, *.json under settings/) are intentionally NOT embedded; they live
// on disk.
//
//go:embed dashboard tools shared settings/config.example.json settings/server.example.json settings/envs.example.json settings/tools/git.example.json
var Assets embed.FS
