# Contributing to devhub

devhub is a single static binary that serves a set of local dev tools under
`localhost:8765`. Tools are modular: each is a `core.Tool` registered in one
place, and the gateway routing plus the dashboard nav are derived from that
registration. Adding a tool does not touch the server, the router, or the
dashboard.

## Toolchain

`go` is pinned in `.mise.toml`. Run `mise install` to provision it, then use
either `make` (POSIX shell) or `mise run` (cross-platform, incl. PowerShell-only
Windows). Both mirror the CI gate:

```bash
make build     # go build ./...          mise run build      # → ./devhub binary
make test      # go test ./...           mise run test
make vet       # go vet ./...            mise run vet
make fmt       # gofmt -w .              mise run fmt
make fmt-check # the CI format gate      mise run fmt-check
#                                        mise run check   # fmt-check + vet + build + test
```

Prefer `mise run …` on Windows: it needs neither `make` nor Git Bash, so the
same CI gate is reachable from PowerShell. `mise run check` runs the whole Go
job (format, vet, build, test) in one shot before you push.

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

### Rebuilding from the UI

When running from source, the dashboard's restart button (↻) has a **「リビルド + 再起動」** option in its dropdown. It compiles the project in the background and restarts via `go run` — no terminal needed. Build errors are shown inline; on success the page reloads automatically.

This option is only available when devhub detects a `go.mod` alongside itself (i.e. source tree present). It does nothing when running a distributed binary.

> **Note on subsequent restarts:** after a rebuild the process is `terminal → go run → <temp binary>`. The plain "再起動のみ" option re-execs that same temp binary (Go's build cache makes this effectively instant), so it stays consistent for the rest of the session. The next explicit rebuild will recompile from source again.

> **Note on the terminal:** the rebuilt process is started in its own session (`setsid`), detached from the terminal that launched it. This is required so it survives the moment the old `go run` parent exits and the terminal tears down its pty (otherwise the replacement is killed by SIGHUP and the port never comes back). The practical consequence: **after a rebuild, `Ctrl+C` in the original terminal no longer stops the instance** — use `scripts/dev.sh stop` (see below).

### Stopping

Foreground: `Ctrl+C` (but note: a UI rebuild detaches the process, so `Ctrl+C`
stops only an instance that has *not* been rebuilt this session). For a
backgrounded / rebuilt / other-terminal instance:

```bash
DEVHUB_PORT=9000 scripts/dev.sh stop   # stop the instance on :9000
scripts/dev.sh status                  # show what's listening
```

`stop` kills whatever process listens on `DEVHUB_PORT` (default 8765), so point
it at the instance you mean to stop. Note the in-app **ports tool deliberately
refuses to kill devhub's own PID** (`internal/controllers/ports/ports.go`) as a
safety measure — that's why this dedicated `stop` exists.

The `devhub` binary itself also has `devhub status` / `devhub stop` (works for
release-binary users with no checkout; `stop` verifies the listener is devhub
via `/ai-api/info` before killing). When you're unsure **which devhub the
command slot runs** (release shim vs source shim vs a stray `devhub.exe` in
the cwd shadowing it under cmd.exe), run `devhub doctor` — it prints the slot
kind, the full PATH resolution order and the instance on the configured port.
See docs/root/0002.

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

### Updating the `devhub` command from source

`install.sh` installs a **pinned, checksum-verified release** binary — that's for
end users, and it does **not** pick up changes you merge to `main` until a new
release is cut. When you work on devhub itself, the global `devhub` command
should track *your* code instead. Two ways:

- **Active development:** just use `scripts/dev.sh run` (runs `go run` from the
  current checkout — always reflects edits, no install step).
- **Update the installed command:** `make install` (or `scripts/dev.sh install`;
  `scripts\dev.ps1 install` on Windows) writes a small shim onto your PATH that
  runs *this checkout* from source via `go run`. It does **not** compile a fixed
  binary, so edits take effect on the next `devhub` launch with no rebuild — no
  more stale-binary surprises. The shim lands at `~/.local/bin/devhub` (Unix) or
  `%USERPROFILE%\bin\devhub.cmd` (Windows); override the directory with
  `DEVHUB_BIN_DIR` (same var as the release installer). `devhub --version`
  reports `dev` in this mode.
- **Run source once, from anywhere:** `devhub start code` launches the current
  checkout via `go run` no matter which `devhub` is on your PATH — a one-off that
  touches no command slot (see [docs/root/0004](docs/root/0004-devhub-start-provenance.md)).
  It finds the checkout from your cwd, the dev shim's recorded checkout, or
  `$DEVHUB_SRC`. (`devhub start binary` / `homebrew` pick the other provenances.)

A `go run` launch compiles to a temp binary, so a running instance keeps serving
its old code until you restart it: `scripts/dev.sh stop` (`scripts\dev.ps1 stop`
on Windows) then `devhub start` (or the dashboard's ↻ rebuild).

## Add a tool

```bash
go run ./scripts/newtool notes              # or: make new-tool NAME=notes
go run ./scripts/newtool my-tool --page-only
```

`newtool` is a pure-Go generator (runs the same on PowerShell-only Windows). It
scaffolds:

- `internal/tools/<id>.go` — a `core.Tool` adapter (Meta + Routes)
- `tools/<id>/index.html` — a page stub (auto-embedded via `assets.go`)

and **wires it into `internal/tools/registry.go` for you** — no manual step. Build
and run; the dashboard card appears automatically from `GET /api/tools`.

Flags: `--page-only` scaffolds a frontend-only tool that reuses `pageTool` (no API
route), like diff-kun / diagram. `--go-name <Name>` overrides the derived Go type
name when the auto-derivation isn't what you want.

Notes:

- `Meta.ID` is the route namespace: the page is served at `/<id>`. Dash-in-id is
  fine (e.g. `db-table`, `env-launcher`); the generator derives a valid Go type
  name by dropping dashes and CamelCasing (`my-tool` → `newMyTool`), so the id can
  match the shipped tools' conventions. API routes are declared explicitly in
  `Routes()`, so they may differ from the id (e.g. db-table serves `/api/db/*`).
- An API-only tool (no `Page`) is valid — it simply has no dashboard card.
- Every API route has to be classified against the exec ledger: list it in the
  `Callers` of each `internal/execaudit` Surface it can reach, or in
  `execFreeEndpoints` (`internal/execaudit/callers_test.go`) with the reason it
  spawns nothing. `go test ./internal/execaudit` fails until one of the two is
  true — that is the point. `Surface.Trigger` used to carry this as prose and
  went stale twice; the field and the guard exist so the decision is recorded
  rather than assumed.
- Give each tool its own storage namespace with `core.Namespace(store, id)` so
  tools never collide on keys.

Both `make new-tool NAME=<id> [ARGS=--page-only]` and `mise run new-tool -- <id>
[--page-only]` are thin wrappers around `go run ./scripts/newtool`.

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
