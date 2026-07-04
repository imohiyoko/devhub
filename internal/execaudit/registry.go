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
		Binaries: []string{"sh -c (unix)", "cmd /c (windows)"},
		Kind:     Fixed,
		Trigger:  "POST /api/envs/launch and /api/envs/launch/process (fallback path when no terminal emulator is usable)",
		Input:    "Runs the env definition's `command` string verbatim via the shell. The command is NOT taken from the request — it is read from the saved env definition (store), launched by env_id/process_id.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api). Writing env definitions goes through POST /api/envs (same gates).",
		Notes:    "This is the widest surface: an arbitrary shell command runs. It is acceptable because the command is user-authored config launched by id, not a per-request payload — mirroring the id-indexed design the whole exec layer already follows.",
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
		ID:       "workspace-editor",
		Binaries: []string{"settings.editor (default: code)", "open -a (darwin, for mapped editors)"},
		Kind:     Dynamic,
		Trigger:  "GET /api/open and the env-launcher's editor fallback",
		Input:    "Target path must be an existing directory. The editor program name is settings.editor and is passed as argv[0] (no shell), so it is executed directly, not interpreted.",
		Gate:     "host allowlist + API token (/api); loopback + Sec-Fetch + manual approval (/ai-api). Writing settings.editor goes through the settings API, whose /ai-api writes require approval — this is exactly the 'setting editor to a shell command' case called out in server/router.go.",
		Notes:    "Editor name is user config; the settings-write path is part of this surface's trust boundary.",
	},
}
