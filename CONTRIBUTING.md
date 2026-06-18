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

## Running from source

The shipped `devhub` is a single binary, but during development you usually run
straight from source — handy when binaries can't be installed (e.g. a policy
forbids running binary software), and so you exercise the code in *this* working
tree. Assets are embedded from the module root (`assets.go`), so `go run`
reflects your current checkout rather than a previously built binary.

```bash
mise run dev               # run from source on :8765
scripts/dev.sh run         # same (no exec bit yet? `bash scripts/dev.sh run`)
```

On Windows use `scripts\dev.ps1 run`. The `dev.sh` / `dev.ps1` helpers `cd` to
their own worktree root first, so launching the script from a given worktree
always runs that worktree's code.

### Multiple instances on different ports

`DEVHUB_PORT` overrides the listen port (default 8765), so a verification
instance can run alongside your main one without a clash:

| instance | command | URL |
|---|---|---|
| main | `scripts/dev.sh run` | http://localhost:8765 |
| verify | `DEVHUB_PORT=9000 scripts/dev.sh run` | http://localhost:9000 |

`DEVHUB_PORT=9000 mise run dev` works too.

### Stopping

Foreground: `Ctrl+C`. For a backgrounded / other-terminal instance:

```bash
DEVHUB_PORT=9000 scripts/dev.sh stop   # stop the instance on :9000
scripts/dev.sh status                  # show what's listening
```

`stop` kills whatever process listens on `DEVHUB_PORT` (default 8765), so point
it at the instance you mean to stop. Note the in-app **ports tool deliberately
refuses to kill devhub's own PID** (`internal/controllers/ports/ports.go`) as a
safety measure — that's why this dedicated `stop` exists.

### Data isolation (optional)

State lives under `DEVHUB_HOME` (default `~/.devhub`; `%LOCALAPPDATA%\devhub` on
Windows). By default instances share it and only the port differs. To fully
isolate a verification instance's DB/settings, give it its own home:

```bash
DEVHUB_PORT=9000 DEVHUB_HOME="$HOME/.devhub-verify" scripts/dev.sh run
```

### Building a local binary

`make build` compile-checks every package. To produce a runnable binary in the
repo root, use `mise run build` or `scripts/dev.sh build` (→ `./devhub`;
`scripts\dev.ps1 build` → `devhub.exe`). Both outputs are gitignored.

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
