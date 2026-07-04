# internal/core — tool plugin contract (PoC)

A modular-monolith seam for devhub: each **tool** (git, ports, env-launcher, …)
is a self-contained module behind one contract. The gateway, API routing and the
dashboard's `/api/tools` nav are all **derived from registration**, so adding a
tool never edits core. And because each tool's HTTP contract is identical
in-process and over the wire, a tool can be **extracted to its own service** by a
config entry — single binary by default, microservices-ready when needed.

## The four pieces

| Piece | File | Role |
|---|---|---|
| `Tool` / `Meta` / `Route` / `Handler` | `tool.go` | the contract every tool implements |
| `Registry` | `registry.go` | the one explicit list you grow per tool |
| `Store` + `Namespace` | `store.go` | per-tool data ownership (keys auto-prefixed) |
| `Gateway` + `Upstreams` | `gateway.go` | dispatch in-proc **or** reverse-proxy; serves `/api/tools` |

## Adding a tool (the whole checklist)

1. New package `internal/tools/<id>` with `func New(d core.Deps) core.Tool`.
2. Implement `Meta()` (id/title/page) and `Routes()` (its `/api/<id>/…` endpoints).
3. Drop the frontend at `tools/<id>/index.html` (auto-embedded by `assets.go`).
4. Add **one line** to the registry in the composition root.

No edits to the gateway, router, or dashboard nav.

```go
func New(d core.Deps) core.Tool {
    st := core.Namespace(d.Store, "hello") // this tool owns the "hello:" keyspace
    return helloTool{store: st}
}

func (t helloTool) Meta() core.Meta {
    return core.Meta{ID: "hello", Title: "Hello", Page: "tools/hello/index.html"}
}
func (t helloTool) Routes() []core.Route {
    return []core.Route{
        {Method: http.MethodGet, Pattern: "/api/hello/ping", Handle: t.ping},
    }
}
```

## Extracting a tool to its own service (no code change)

Run the *same* `Tool` behind its own binary (`cmd/devhub-<id>`) and point the
gateway at it:

```go
core.NewGateway(reg, core.Upstreams{
    "db-table": "http://127.0.0.1:9002", // db-table now runs out-of-process
}, pageFn)
```

The gateway reverse-proxies `/db-table` and `/api/db-table/*` to that URL; every
other tool stays in-process. The default (`nil`/empty `Upstreams`) is the
single static binary.

## How richness scales (DDD where it earns its keep)

`tool = bounded context`. The contract is uniform; internal depth is per tool:

- **Thin** (diff-kun, diagram): `Meta{Page}` only, ~no backend.
- **Medium** (ports, workspace, db-table): controller + typed model.
- **Rich** (env-launcher, git): domain types with invariants (`Launch`, `Offset`,
  `Worktree`), a repository, domain services — aggregates/value objects belong
  *here*, not everywhere.

## Status

Live: the `server` wires this contract in production. `tools.Registry` builds
the tool set and `core.NewGateway(reg, nil, pageFn)` fronts every request
(`internal/server/server.go`). Every tool runs in-process — `Upstreams` is
`nil`, so the whole thing is the single static binary; pointing an entry at a
URL extracts that tool to its own service with no code change. Requests no tool
claims fall through to the gateway's `Next` handler (the server's system
routes).
