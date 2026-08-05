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

// Docs bundles docs/ so `devhub docs` and GET /api/docs can serve it from the
// binary. It is a separate FS from Assets because nothing that consumes Assets
// (the page/asset cache, the settings seeder) should see documentation.
//
// Embedding means a doc travels with the binary that it describes — a released
// devhub can never quote a doc from a different version — at the cost of needing
// a release to publish a doc edit. Files beginning with "." or "_" are excluded
// by go:embed itself, so the .gitkeep placeholders in the empty tool
// directories are not bundled.
//
//go:embed docs
var Docs embed.FS
