package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/imohiyoko/devhub/internal/platform"
)

// A provenance names one of devhub's own distribution channels — which physical
// `devhub` a `devhub start <provenance>` should hand off to. The canonical names
// mirror the user-facing vocabulary (binary / homebrew / code); aliases fold in
// devhub's internal edition words (release / brew / source).
//
// This is a per-invocation, stateless selector: it changes no command slot and
// no PATH resolution, it just launches a chosen implementation once. That is the
// distinction from the persistent `slot use` switcher ADR 0002 rejected as
// YAGNI — see ADR 0004.
const (
	provBinary   = "binary"
	provHomebrew = "homebrew"
	provCode     = "code"
)

// provenanceArg splits a leading provenance token off the `start` args. A
// provenance, when present, is the first positional (a non-flag token); Go's
// flag package stops at the first non-flag anyway, so it must lead. This only
// detects the shape — parseProvenance validates the name. present=false means an
// ordinary in-process start (no args, or a leading flag).
func provenanceArg(args []string) (token string, rest []string, present bool) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:], true
	}
	return "", args, false
}

// parseProvenance normalises a `start` positional into a canonical provenance,
// or reports ok=false for anything unrecognised. Matching is case-insensitive
// and folds the internal-vocabulary aliases onto the canonical names.
func parseProvenance(token string) (string, bool) {
	switch strings.ToLower(token) {
	case provBinary, "release":
		return provBinary, true
	case provHomebrew, "brew":
		return provHomebrew, true
	case provCode, "source":
		return provCode, true
	default:
		return "", false
	}
}

// launchDeps injects the environment lookups resolveLaunch needs, so provenance
// resolution is a pure function testable without touching the real filesystem,
// PATH, cwd, or os.Executable — the same dependency-injection style as
// scanPathForDevhub's exists func.
type launchDeps struct {
	isWin      bool
	exists     func(string) bool            // regular-file existence (fileExists)
	execExists func(string) bool            // PATH-scan existence (executableFile)
	lookPath   func(string) (string, error) // exec.LookPath
	readFile   func(string) ([]byte, error) // os.ReadFile
	evalSyml   func(string) (string, error) // filepath.EvalSymlinks
	getenv     func(string) string
	getwd      func() (string, error)
	devhubHome string   // platform.DevhubHome()
	pathDirs   []string // filepath.SplitList($PATH)
	slot       string   // slotFile()
}

// liveLaunchDeps wires launchDeps to the real environment.
func liveLaunchDeps() launchDeps {
	return launchDeps{
		isWin:      platform.IsWindows(),
		exists:     fileExists,
		execExists: executableFile,
		lookPath:   exec.LookPath,
		readFile:   os.ReadFile,
		evalSyml:   filepath.EvalSymlinks,
		getenv:     os.Getenv,
		getwd:      os.Getwd,
		devhubHome: platform.DevhubHome(),
		pathDirs:   filepath.SplitList(os.Getenv("PATH")),
		slot:       slotFile(),
	}
}

// resolveLaunch turns a canonical provenance + the pass-through args into the
// target command (argv) and working directory to hand off to. dir is empty for a
// plain binary hand-off and the source checkout for the `go run` (code) path.
func resolveLaunch(prov string, rest []string, d launchDeps) (argv []string, dir string, err error) {
	switch prov {
	case provBinary:
		return resolveBinary(rest, d)
	case provHomebrew:
		return resolveHomebrew(rest, d)
	case provCode:
		return resolveCode(rest, d)
	default:
		return nil, "", fmt.Errorf("unknown provenance %q", prov)
	}
}

// resolveBinary points at the release binary the installers place under
// <DevhubHome>/bin — the canonical install location doctor already reports.
func resolveBinary(rest []string, d launchDeps) ([]string, string, error) {
	name := "devhub"
	if d.isWin {
		name = "devhub.exe"
	}
	bin := filepath.Join(d.devhubHome, "bin", name)
	if !d.exists(bin) {
		return nil, "", fmt.Errorf("release binary not installed at %s — run install.sh / install.ps1", bin)
	}
	return append([]string{bin, "start"}, rest...), "", nil
}

// resolveHomebrew finds a Homebrew-provided devhub on PATH. There is no brew
// formula shipped by this repo; this only resolves an already-installed one, by
// walking PATH (resolution order) and picking the first candidate whose
// symlink-resolved path sits under a Homebrew prefix. Homebrew is a Unix package
// manager, so this is unavailable on Windows.
func resolveHomebrew(rest []string, d launchDeps) ([]string, string, error) {
	if d.isWin {
		return nil, "", fmt.Errorf("the homebrew provenance is not available on Windows")
	}
	for _, cand := range scanPathForDevhub(d.pathDirs, d.isWin, d.execExists) {
		resolved := cand
		if r, err := d.evalSyml(cand); err == nil {
			resolved = r
		}
		if platform.IsHomebrewPath(resolved) {
			return append([]string{cand, "start"}, rest...), "", nil
		}
	}
	return nil, "", fmt.Errorf("no Homebrew devhub found on PATH — install it with brew first, or use `devhub start binary`")
}

// resolveCode runs the current source via `go run ./cmd/devhub start` from a
// devhub checkout. It needs the go toolchain on PATH and a checkout to run from.
func resolveCode(rest []string, d launchDeps) ([]string, string, error) {
	goBin, err := d.lookPath("go")
	if err != nil {
		return nil, "", fmt.Errorf("the code provenance needs the `go` toolchain on PATH: %w", err)
	}
	checkout, err := findCheckout(d)
	if err != nil {
		return nil, "", err
	}
	argv := append([]string{goBin, "run", "./cmd/devhub", "start"}, rest...)
	return argv, checkout, nil
}

// findCheckout locates a devhub source tree, in priority order:
//  1. the current directory or an ancestor is a devhub checkout;
//  2. the dev shim in the command slot records the checkout it was installed
//     from (classifyShimContent already extracts it);
//  3. the DEVHUB_SRC environment override.
func findCheckout(d launchDeps) (string, error) {
	if wd, err := d.getwd(); err == nil {
		if root := ascendToCheckout(wd, d.exists); root != "" {
			return root, nil
		}
	}
	if b, err := d.readFile(d.slot); err == nil {
		if kind, detail := classifyShimContent(string(b)); kind == shimSource && detail != "" && isDevhubCheckout(detail, d.exists) {
			return detail, nil
		}
	}
	if src := strings.TrimSpace(d.getenv("DEVHUB_SRC")); src != "" {
		if isDevhubCheckout(src, d.exists) {
			return src, nil
		}
		return "", fmt.Errorf("DEVHUB_SRC=%s is not a devhub checkout (need go.mod + cmd/devhub)", src)
	}
	return "", fmt.Errorf("no devhub source checkout found — cd into one, set DEVHUB_SRC, or run `scripts/dev install`")
}

// isDevhubCheckout reports whether dir is the root of a devhub source tree. It
// checks both go.mod and cmd/devhub/main.go so `go run ./cmd/devhub` can't be
// aimed at an unrelated Go module that merely has a go.mod.
func isDevhubCheckout(dir string, exists func(string) bool) bool {
	return exists(filepath.Join(dir, "go.mod")) && exists(filepath.Join(dir, "cmd", "devhub", "main.go"))
}

// ascendToCheckout walks up from dir and returns the first ancestor that is a
// devhub checkout, or "" if none up to the filesystem root.
func ascendToCheckout(dir string, exists func(string) bool) string {
	for {
		if isDevhubCheckout(dir, exists) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// runLaunch resolves a provenance and hands the process off to the chosen devhub
// implementation. On success it never returns (execLaunch replaces the image on
// unix, or exits with the child's status on windows); it returns a non-zero exit
// code only when resolution or the hand-off itself fails.
func runLaunch(prov string, rest []string) int {
	argv, dir, err := resolveLaunch(prov, rest, liveLaunchDeps())
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub start:", err)
		return 1
	}
	// Self short-circuit: if the resolved target is the binary already running
	// (e.g. `devhub start binary` from the release binary itself), don't spawn a
	// second process — run the server in-process. `code` hands off to `go run`, a
	// different image, so it never matches here.
	if self, err := os.Executable(); err == nil {
		if sameFilePath(resolvePathOr(argv[0]), resolvePathOr(self)) {
			return startServer(rest)
		}
	}
	if err := execLaunch(argv, dir); err != nil {
		fmt.Fprintln(os.Stderr, "devhub start:", err)
		return 1
	}
	return 0 // unreachable on unix; on windows execLaunch exits with the child status
}

// resolvePathOr returns p with symlinks resolved, or p unchanged if it can't be.
func resolvePathOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
