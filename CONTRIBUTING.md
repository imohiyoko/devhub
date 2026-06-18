# Contributing to devhub

devhub is a single static binary that serves a set of local dev tools under
`localhost:8765`. Tools are modular: each is a `core.Tool` registered in one
place, and the gateway routing plus the dashboard nav are derived from that
registration. Adding a tool does not touch the server, the router, or the
dashboard.

## Toolchain

`go` is pinned in `.mise.toml`. Run `mise install` to provision it, then:

```bash
make build    # go build ./...
make test     # go test ./...
make vet      # go vet ./...
make fmt      # gofmt -w .
make fmt-check # the CI format gate
```

CI runs `gofmt` (must be clean), `go vet`, `go build`, and `go test` on
Linux/macOS/Windows.

## Add a tool

```bash
make new-tool NAME=notes
```

This scaffolds:

- `internal/tools/notes.go` — a `core.Tool` adapter (Meta + Routes)
- `tools/notes/index.html` — a page stub (auto-embedded via `assets.go`)

Then do the one wiring step it prints: add `newNotes()` to the `NewRegistry(...)`
list in `internal/tools/registry.go`. Build and run — the dashboard card appears
automatically from `GET /api/tools`.

Notes:

- `Meta.ID` is the route namespace: the page is served at `/<id>`. Use a
  Go-identifier-safe id (lowercase, no dashes) so the generated type compiles;
  put a nicer label in `Title`. API routes are declared explicitly in `Routes()`,
  so they may differ from the id (e.g. db-table serves `/api/db/*`).
- An API-only tool (no `Page`) is valid — it simply has no dashboard card.
- Give each tool its own storage namespace with `core.Namespace(store, id)` so
  tools never collide on keys.

## How rich should a tool be?

Treat a tool as a bounded context and scale its internals to its needs:

- **Thin** (diff-kun, diagram): a page, no backend.
- **Medium** (ports, workspace, db-table): a controller and typed models.
- **Rich** (env-launcher, git): domain types with invariants, where richer
  patterns (aggregates, value objects, domain services) earn their keep.

Don't impose heavy structure on a thin tool.

## Extracting a tool to its own process (advanced)

A tool can run out-of-process without code changes: point a gateway `Upstreams`
entry at its URL and the gateway reverse-proxies that tool's page and API while
the rest stay in-process. The single binary is the default; do this only when a
tool needs independent scaling, isolation, or a different runtime. See
`internal/core/README.md`.
