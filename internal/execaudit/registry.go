// Package execaudit is the single source of truth for every place devhub spawns
// an external process.
//
// The backend never accepts a raw shell command from the frontend and runs it;
// instead each exec.Command / exec.CommandContext call site in the codebase is a
// fixed capability, tagged with a trailing `//execaudit:<id>` marker that names
// one Surface in Registry below. A guard test (audit_test.go) walks the whole
// module and fails the build if any call site is unannotated, names an unknown
// Surface, or if a Surface is left with no call sites. That keeps this file from
// drifting out of sync with the code, so the complete set of programs the
// backend can launch — and how each is gated — is auditable in one place.
//
// Adding an exec surface is therefore a closed operation: annotate the call site
// and register the Surface here, or `go test ./internal/execaudit` goes red.
//
// Scope: the server binary (cmd/ + internal/). Standalone build/CI tools under
// scripts/ are excluded by the guard (see audit_test.go).
package execaudit

// BinaryKind classifies the *program name* a surface spawns.
//
// It is deliberately about the binary, not the arguments: the risky-argument
// story (e.g. envs-run-shell passes a user-authored command string to sh -c)
// lives in Input/Notes. The distinction that BinaryKind draws is whether an
// attacker can influence *which executable runs* by writing configuration —
// because auditing a Dynamic surface means auditing who can set that value, not
// just reading the call site.
type BinaryKind string

const (
	// Fixed: the program name is a compile-time literal or an internal constant
	// (git, mysql, lsof, the `go` toolchain, devhub's own executable). Untrusted
	// input, if any, only reaches the arguments.
	Fixed BinaryKind = "fixed"
	// Dynamic: the program name comes from user-writable settings
	// (settings.editor, settings.terminal.<os>.emulator). The relevant gate is
	// therefore on the settings-write path as much as on the trigger itself.
	Dynamic BinaryKind = "dynamic"
)

// Surface is one logically-distinct external-process capability. Several
// physical call sites (typically per-OS branches) may share a single Surface id.
type Surface struct {
	// ID matches the //execaudit:<id> markers in the code.
	ID string
	// Binaries names the program(s) this surface can spawn. For Dynamic surfaces
	// it names the settings key that supplies the value.
	Binaries []string
	// Kind is Fixed or Dynamic (see BinaryKind).
	Kind BinaryKind
	// Trigger is what causes the exec — an HTTP route, or a process-lifecycle
	// event for the surfaces that are not reachable over HTTP.
	Trigger string
	// Input describes how the untrusted parts of the invocation are constrained.
	Input string
	// Gate is the authorization boundary in front of the trigger.
	Gate string
	// Notes captures anything else an auditor needs.
	Notes string
}

// Registry is the canonical list of exec surfaces, kept sorted by ID. The guard
// test enforces that this set is exactly the set referenced by the code's
// //execaudit markers.
var Registry = []Surface{
	{
		ID:       "browser-open",
		Binaries: []string{"open (darwin)", "rundll32 (windows)", "xdg-open (linux)"},
		Kind:     Fixed,
		Trigger:  "process startup — opens devhub's own dashboard URL in the default browser",
		Input:    "URL is devhub's own loopback dashboard address, built server-side; no request data reaches it.",
		Gate:     "Not reachable over HTTP; runs in-process at boot only.",
	},
	{
		ID:       "colima-profile",
		Binaries: []string{"colima"},
		Kind:     Fixed,
		Trigger:  "POST /api/containers/profiles (create) and POST /api/containers/profiles/{name}/resize. Nothing else reaches it: no switch, status read or page load starts, stops or reconfigures a VM.",
		Input:    "Fixed argv: `start --profile <name> [--cpus N] [--memory N] [--disk N] [--runtime docker|containerd]`, and for a resize `stop --profile <name>` first. The name must match [A-Za-z0-9_][A-Za-z0-9_-]* — the leading character cannot be a hyphen, so a name can never pass for a flag — and the engine must be one devhub has an adapter for, both checked before anything is spawned; the numbers are rendered from parsed integers, never passed through as text. --force is never passed to stop, so the guest shuts down gracefully.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api). Both routes are POSTs, so the /ai-api path always waits for approval — an agent can ask for a VM, it cannot have one without the user saying yes.",
		Notes:    "This is the one place devhub moves a Colima VM rather than reading it, which is why it is its own Surface rather than more calls under container-runtime: that one is bounded to a declared compose project, this one starts and stops whole machines. Create refuses a name that already exists rather than silently resizing it, because a resize stops every container in the VM — including containers belonging to other environments that merely share the profile, which the caller is expected to list to the user first. Shrinking a disk is refused outright: colima cannot do it in place, so it would mean recreating the VM and losing every image on it, and unlike a stop that cannot be undone by starting the profile again. Spawns through adminRunner in internal/container/profile.go.",
	},
	{
		ID:       "container-runtime",
		Binaries: []string{"docker", "colima"},
		Kind:     Fixed,
		Trigger:  "docker/colima nerdctl: GET /api/envs/state and POST /api/envs/switch/plan (read: `ps`); POST /api/envs/switch/apply (write: `up`/`stop`); GET /api/envs/runtimes (read: docker `compose version`). colima `list --json` reaches further than the env endpoints, because every caller that needs to know which VMs exist asks the same probe: GET /api/envs/runtimes and POST /api/envs/switch/{plan,apply}, plus GET /api/containers (the panel derives its sources from the profile list), POST /api/containers/profiles (checks the name is free) and POST /api/containers/profiles/{name}/resize (checks the profile exists, how big it is now, and what a restart would stop). Those last three have their own Surfaces for what they spawn directly — containers-list and colima-profile — but the `list` they cause is filed here, because it is execRunner that spawns it.",
		Input:    "Fixed argv per operation. docker: `[--context colima-<profile>] compose --project-name <project> [--file <f>…]` followed by `ps --format json --all`, `up --detach --wait <services>` or `stop <services>`. containerd goes through the same colima binary: `nerdctl --profile <profile> -- compose --project-name <project> [--file <f>…]` followed by the same three subcommands (no --wait, which nerdctl does not have). project/files/cwd/services/profile come from the saved env definition, not from the request, and the profile name is checked at save time by container.ValidProfileName, the same rule the colima-profile Surface names. The capability probe is a bare `compose version --short`. colima: `list --json`, which takes no input at all. No command string is accepted from the caller on any path.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api). Writing env definitions goes through POST /api/envs (same gates).",
		Notes:    "Every invocation that touches containers is scoped to the definition's compose project name and to the services that definition declares, which is how devhub bounds itself to the containers the environment owns: it can start and stop those, and nothing else. The engine is selected per command — a --context for docker, a --profile for colima nerdctl — and `docker context use` is never run, so the user's other terminals keep whatever context they had (plan §6.3). An environment that declares no Colima profile passes no --context and uses the ambient one. Note that `colima nerdctl` runs inside the profile's VM, so the compose files and cwd are resolved against the VM's mounts. The colima binary appears here in two unrelated roles, and only one of them is VM management: as a VM manager it is asked for `list` and nothing else, while `colima nerdctl` is simply how the containerd engine is reached inside an already-running VM — that one does change container state, bounded to the declared project like every other call here. So nothing under this Surface starts, stops or reconfigures a VM, and nothing devhub does on its own does either: a switch, a status read and a page load all leave a stopped profile stopped. Creating or resizing a profile is possible only through an explicit request, and that is the separate colima-profile Surface. All calls funnel through execRunner in internal/container/command.go, which is why one Surface covers every runtime adapter — and why a second consumer of that package inherits the same bounds.",
	},
	{
		ID:       "containers-control",
		Binaries: []string{"docker", "colima"},
		Kind:     Fixed,
		Trigger:  "POST /api/containers/logs (read: `logs --tail N`), POST /api/containers/stop and POST /api/containers/restart (write). One container per request, named in the body; nothing here is reachable by a page load or by any env-launcher endpoint.",
		Input:    "Fixed argv: docker `[--context colima-<profile>] {logs --tail <n>|stop|restart} <id>`, or for a containerd profile `colima nerdctl --profile <profile> -- {…} <id>`. The container ID is the only value from the request that reaches a command line, and it is not trusted: it must match [0-9a-fA-F]{12,64} and then must appear in a listing taken from that engine in the same request (Runtime.ResolveContainer). The source and context names are not request data either — they come from `colima list --json`, so the only values passed are ones Colima itself reported. The tail count is clamped, not passed through as text.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api). All three are POSTs, so the /ai-api path always waits for approval — which matters more here than anywhere else in devhub, because this panel deliberately lists containers no environment declared, so an agent reading it can name anything on the machine.",
		Notes:    "The third seam in internal/container, and separate for what each one can claim: the adapters under container-runtime are confined to a declared compose project, containers-list only ever reads, and this one is neither. What bounds it instead is the resolve — an operation may only name a container the engine is reporting right now, so an arbitrary string cannot reach argv and a caller cannot act on something the panel never showed. Nothing here removes anything: no `rm`, no `prune`. Those destroy state that pressing the other button does not bring back, and a machine-wide panel is the worst place to offer them. Spawns through controlRunner in internal/container/control.go.",
	},
	{
		ID:       "containers-list",
		Binaries: []string{"docker", "colima"},
		Kind:     Fixed,
		Trigger:  "GET /api/containers (the panel's read), and once per POST /api/containers/{logs,stop,restart} — an operation resolves the container it names against a fresh listing before it runs, so the containers-control Surface causes a listing here every time it acts.",
		Input:    "Fixed argv, read-only: docker `[--context colima-<profile>] ps --all --format json`, or for a containerd profile `colima nerdctl --profile <profile> -- ps -a --format json`. Nothing from the request reaches the argv. The context and profile names are not user input either: they come from `colima list --json` via the capability probe, so the only values ever passed are ones Colima itself reported.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api).",
		Notes:    "Deliberately NOT scoped to a compose project, which is the whole point of the panel and the reason this is a separate Surface from container-runtime rather than more calls under it: seeing a container devhub never declared — a leftover from a renamed project, something holding a port — requires listing past the declaration. The bound that remains is that it only ever reads: no subcommand here creates, starts, stops or removes anything, and the panel's write operations are a different Surface. It spawns through inventoryRunner in internal/container/inventory.go, kept separate from execRunner in the same package precisely so the project-scoped claim on container-runtime stays exact.",
	},
	{
		ID:       "envs-open-terminal",
		Binaries: []string{"settings.terminal.<os>.emulator: ghostty|wt|gnome-terminal|xterm", "osascript (Terminal.app/iTerm)"},
		Kind:     Dynamic,
		Trigger:  "POST /api/envs/launches/open (target=terminal) and the worktree 'open terminal' action",
		Input:    "cwd must be an existing directory; no command is injected — this opens an interactive shell only.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api).",
		Notes:    "Emulator name is user config, so the settings-write path is part of this surface's trust boundary. inPath() must resolve it or devhub falls back to the editor.",
	},
	{
		ID:       "envs-run-shell",
		Binaries: []string{"sh -c (unix)", `cmd /S /C (windows)`},
		Kind:     Fixed,
		Trigger:  "POST /api/envs/launch and /api/envs/launch/process (fallback path when no terminal emulator is usable)",
		Input:    "Runs the env definition's `command` string verbatim via the shell. The command is NOT taken from the request — it is read from the saved env definition (store), launched by env_id/process_id.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api). Writing env definitions goes through POST /api/envs (same gates).",
		Notes:    "This is the widest surface: an arbitrary shell command runs. It is acceptable because the command is user-authored config launched by id, not a per-request payload — mirroring the id-indexed design the whole exec layer already follows. On Windows the raw command line is passed via SysProcAttr.CmdLine because cmd.exe does not interpret Go's default \\\" argument escaping (issue #114).",
	},
	{
		ID:       "envs-run-terminal",
		Binaries: []string{"settings.terminal.<os>.emulator: ghostty|wt|gnome-terminal|xterm", "osascript (Terminal.app/iTerm)"},
		Kind:     Dynamic,
		Trigger:  "POST /api/envs/launch and /api/envs/launch/process (when a terminal emulator is configured and present)",
		Input:    "Emulator runs the env definition's `command` (from the store, not the request) with env exports. Same command source as envs-run-shell.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api).",
		Notes:    "Both the emulator (user config) and the command (saved def) are user-controlled; see envs-run-shell for why launch-by-id is the trust model.",
	},
	{
		ID:       "git",
		Binaries: []string{"git"},
		Kind:     Fixed,
		Trigger:  "GET/POST /api/git/* — the exact subcommand set is the switch in internal/controllers/git/handlers.go (status, log, diff, commit, push, pull, checkout, branch, worktree add/remove/pull/push/prune, fetch, stash, from-pr)",
		Input:    "Fixed argv per route; repo path must match a configured repo (validatedPath); branch/worktree/base-commit validated; `--` terminators. No command string is accepted from the caller.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval for writes (/ai-api).",
		Notes:    "All git operations funnel through the single runCmd() call site in internal/controllers/git/exec.go.",
	},
	{
		ID:       "portreclaim",
		Binaries: []string{"ps (unix)", "tasklist (windows)"},
		Kind:     Fixed,
		Trigger:  "process startup — reclaims devhub's own TCP port from an orphaned or previous instance ('newest launch wins')",
		Input:    "port is devhub's own configured port (int); a candidate PID is killed only when its executable basename is exactly \"devhub\" (devhub.exe on Windows). Listener discovery goes through the ports-list surface.",
		Gate:     "Not reachable over HTTP; runs in-process at boot only (no-op where ps/tasklist are absent).",
	},
	{
		ID:       "ports-kill",
		Binaries: []string{"taskkill (windows)"},
		Kind:     Fixed,
		Trigger:  "POST /api/ports/kill and the env-launcher's baton port cleanup",
		Input:    "pid is an int; fixed argv. On unix the kill uses syscall.Kill (no exec) — this surface is Windows-only.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api).",
	},
	{
		ID:       "ports-list",
		Binaries: []string{"lsof (unix)", "netstat (windows)"},
		Kind:     Fixed,
		Trigger:  "GET /api/ports, the env-launcher's live-port index, the CLI (status/stop/doctor/env), and the startup port reclaim's listener lookup",
		Input:    "Fixed argv; read-only; no request data reaches the arguments.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch (/ai-api). Read-only, so no approval.",
	},
	{
		ID:       "rebuild",
		Binaries: []string{"go build", "go run"},
		Kind:     Fixed,
		Trigger:  "POST /api/rebuild",
		Input:    "Fixed argv targeting ./cmd/devhub; the `go` path is resolved via PATH then GOROOT (goBin). Requires a source tree (repoRoot); 409 otherwise.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api).",
	},
	{
		ID:       "restart",
		Binaries: []string{"os.Executable() (devhub's own image, windows)"},
		Kind:     Fixed,
		Trigger:  "POST /api/restart, and the self-update re-exec after a successful update",
		Input:    "Re-execs devhub's own resolved executable with the current argv. On unix this uses syscall.Exec (no exec.Command) — this surface is Windows-only.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api).",
	},
	{
		ID:       "self-update-verify",
		Binaries: []string{"cosign"},
		Kind:     Fixed,
		Trigger:  "POST /api/update/apply -> updater.SelfUpdate -> verifyCosign, and only when DEVHUB_VERIFY_SIGNATURE=1",
		Input:    "Fixed argv: `cosign version` (to pick the verification format), then `cosign verify-blob` over temp files downloaded during the update (checksums.txt + .sigstore.json bundle on cosign v3+, or .sig/.pem on v2); certificate-identity-regexp pins the release workflow identity. No request data reaches argv.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api). Installer edition only (409 otherwise).",
		Notes:    "Opt-in signature check; requires cosign on PATH. The self-update then re-execs via the `restart` surface.",
	},
	{
		ID:       "start-launch",
		Binaries: []string{"devhub (release/homebrew binary)", "go run ./cmd/devhub (code)"},
		Kind:     Fixed,
		Trigger:  "process startup — `devhub start <provenance>` hands the server off to a chosen devhub implementation (binary / homebrew / code)",
		Input:    "provenance is a fixed enum (binary|homebrew|code, plus aliases); the target is derived from it: the release binary under <DevhubHome>/bin, a Homebrew devhub discovered on PATH, or `go run` in a discovered source checkout (cwd/dev-shim/DEVHUB_SRC). The pass-through flags after the provenance are handed to the target's own `start` verbatim (the target re-parses them).",
		Gate:     "Not reachable over HTTP; runs in-process only when a user explicitly types `devhub start <provenance>`.",
		Notes:    "Fixed, not Dynamic: the target is a bounded set — devhub's own release binary or the `go` toolchain — selected by a fixed CLI enum, not by user-writable settings, so there is no settings-write trust boundary (unlike workspace-editor). Unix hands off with syscall.Exec (no exec.Command — not scanned by the guard); Windows uses exec.Command + Run and propagates the child's exit code. The provenance token is stripped before hand-off, so the target's argv is `start <flags>` and a subsequent self-restart stays on that provenance.",
	},
	{
		ID:       "workspace-editor",
		Binaries: []string{"settings.editor (default: code)", "open -a (darwin, for mapped editors)"},
		Kind:     Dynamic,
		Trigger:  "GET /api/open and the env-launcher's editor fallback",
		Input:    "Target path must be an existing directory. The editor program name is settings.editor and is passed as argv[0] (no shell), so it is executed directly, not interpreted.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api). Writing settings.editor goes through the settings API, whose /ai-api writes require approval — this is exactly the 'setting editor to a shell command' case called out in server/router.go.",
		Notes:    "Editor name is user config; the settings-write path is part of this surface's trust boundary.",
	},
}
