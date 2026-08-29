package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/imohiyoko/devhub/internal/platform"
)

// shimKind classifies what occupies the command slot.
type shimKind int

const (
	shimUnknown shimKind = iota
	shimRelease          // pinned binary from install.ps1 / install.sh
	shimSource           // `go run` dev shim from scripts/dev.{ps1,sh} install
)

var (
	// The dev shims carry a "devhub dev shim" marker; the Windows release shim
	// gained a "devhub release shim" marker at the same time this command was
	// added. The exec-line fallbacks keep classification working for slots
	// written by older installers that predate the markers.
	reShimPushd   = regexp.MustCompile(`(?m)^pushd (.+?)\r?$`)
	reShimCd      = regexp.MustCompile(`(?m)^cd "([^"]+)"`)
	reShimExecWin = regexp.MustCompile(`(?m)^"(.+?devhub\.exe)" %\*`)
	reShimRootB64 = regexp.MustCompile(`(?mi)^(?:#|rem) devhub-source-root-b64: ([a-z0-9+/=]+)\r?$`)
)

// classifyShimContent inspects a command-slot file's content. detail is the
// source checkout root (shimSource) or the target binary path (shimRelease,
// when the exec line reveals it).
func classifyShimContent(content string) (kind shimKind, detail string) {
	if strings.Contains(content, "devhub dev shim") || strings.Contains(content, "go run ./cmd/devhub") {
		if m := reShimRootB64.FindStringSubmatch(content); m != nil {
			if raw, err := base64.StdEncoding.DecodeString(m[1]); err == nil && len(raw) > 0 {
				return shimSource, string(raw)
			}
		}
		if m := reShimPushd.FindStringSubmatch(content); m != nil {
			return shimSource, strings.TrimSpace(m[1])
		}
		if m := reShimCd.FindStringSubmatch(content); m != nil {
			return shimSource, m[1]
		}
		return shimSource, ""
	}
	if m := reShimExecWin.FindStringSubmatch(content); m != nil {
		return shimRelease, m[1]
	}
	if strings.Contains(content, "devhub release shim") {
		return shimRelease, ""
	}
	return shimUnknown, ""
}

// slotFile returns the command-slot path the installers write: DEVHUB_BIN_DIR
// (or its per-OS default) + devhub.cmd / devhub.
func slotFile() string {
	binDir := os.Getenv("DEVHUB_BIN_DIR")
	if binDir == "" {
		if platform.IsWindows() {
			binDir = filepath.Join(platform.Home(), "bin")
		} else {
			binDir = filepath.Join(platform.Home(), ".local", "bin")
		}
	}
	name := "devhub"
	if platform.IsWindows() {
		name = "devhub.cmd"
	}
	return filepath.Join(binDir, name)
}

// scanPathForDevhub walks the PATH directories in order and returns every
// existing devhub command, resolution-ordered: the first entry is what a bare
// `devhub` runs. On Windows each directory is checked in default PATHEXT
// order (.com/.exe/.bat/.cmd) — a devhub.exe beats a devhub.cmd in the same
// directory. exists is injected so the walk is testable without a filesystem;
// dirs comes pre-split (filepath.SplitList is OS-dependent) so the Windows
// case is testable on any CI platform.
func scanPathForDevhub(dirs []string, isWin bool, exists func(string) bool) []string {
	names := []string{"devhub"}
	if isWin {
		names = []string{"devhub.com", "devhub.exe", "devhub.bat", "devhub.cmd"}
	}
	var out []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, n := range names {
			p := filepath.Join(dir, n)
			key := p
			if isWin {
				key = strings.ToLower(p)
			}
			if seen[key] || !exists(p) {
				continue
			}
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// executableFile reports whether p is a file the shell would run: any regular
// file on Windows (PATHEXT already filtered the names), an exec-bit file
// elsewhere.
func executableFile(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || fi.Mode().Perm()&0o111 != 0
}

// runDoctor prints a diagnosis of "which devhub runs, and which is running":
// the current process, the command slot and full PATH resolution, and the
// instance on the configured port. Exit 1 when any warning fired, 0 clean.
func runDoctor() int {
	var warns []string

	// --- this process ---
	fmt.Println("[process]")
	exe, _ := os.Executable()
	fmt.Printf("  executable : %s\n", exe)
	if strings.Contains(exe, "go-build") {
		cwd, _ := os.Getwd()
		fmt.Printf("  running as : source (go run temp binary, checkout: %s)\n", cwd)
	} else {
		fmt.Println("  running as : binary")
	}
	fmt.Printf("  version    : %s (edition %s)\n", version, platform.Edition(version))

	// --- command slot ---
	fmt.Println("[command slot]")
	slot := slotFile()
	slotDesc := "missing"
	slotExists := false
	if fi, err := os.Lstat(slot); err == nil {
		slotExists = true
		if fi.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(slot)
			slotDesc = fmt.Sprintf("release link → %s", target)
			if !fileExists(target) {
				warns = append(warns, fmt.Sprintf("slot links to a missing binary: %s", target))
			}
		} else if b, err := os.ReadFile(slot); err == nil {
			switch kind, detail := classifyShimContent(string(b)); kind {
			case shimSource:
				slotDesc = fmt.Sprintf("dev shim → go run from %s", detail)
				if detail == "" || !fileExists(filepath.Join(detail, "go.mod")) {
					warns = append(warns, fmt.Sprintf("dev shim points at a directory without go.mod: %q (reinstall with scripts/dev install)", detail))
				}
			case shimRelease:
				slotDesc = "release shim"
				if detail != "" {
					slotDesc += " → " + detail
					if !fileExists(detail) {
						warns = append(warns, fmt.Sprintf("release shim points at a missing binary: %s (re-run the installer)", detail))
					}
				}
			default:
				slotDesc = "unrecognized content"
				warns = append(warns, fmt.Sprintf("slot %s has unrecognized content — not written by a devhub installer?", slot))
			}
		}
	}
	fmt.Printf("  slot       : %s — %s\n", slot, slotDesc)

	hits := scanPathForDevhub(filepath.SplitList(os.Getenv("PATH")), platform.IsWindows(), executableFile)
	if len(hits) == 0 {
		fmt.Println("  PATH       : no devhub found on PATH")
	}
	for i, h := range hits {
		mark := "   (shadowed)"
		if i == 0 {
			mark = "   <- a bare `devhub` runs this"
		}
		fmt.Printf("  PATH %d     : %s%s\n", i+1, h, mark)
	}
	switch {
	case slotExists && len(hits) > 0 && !sameFilePath(hits[0], slot):
		warns = append(warns, fmt.Sprintf("PATH resolves %s before the slot %s", hits[0], slot))
	case !slotExists && len(hits) > 0:
		// An empty installer slot is fine when `devhub` comes from somewhere
		// legitimate the installers don't manage — Homebrew on macOS, a distro
		// package, a hand-rolled setup. The PATH listing above already names
		// it, so this is information, not a warning.
		fmt.Printf("  note       : the installer slot is unused; `devhub` comes from %s\n", hits[0])
	case !slotExists && len(hits) == 0:
		warns = append(warns, fmt.Sprintf("no devhub on PATH and none in the expected slot %s — run install.ps1/install.sh or scripts/dev install", slot))
	}

	relBin := filepath.Join(platform.DevhubHome(), "bin", exeName())
	if fileExists(relBin) {
		fmt.Printf("  release bin: %s (installed)\n", relBin)
	} else {
		fmt.Printf("  release bin: not installed (%s)\n", relBin)
	}

	// cmd.exe resolves the current directory before PATH, so a build artifact
	// in a repo root silently shadows the slot there — PowerShell is immune.
	if platform.IsWindows() {
		if cwd, err := os.Getwd(); err == nil {
			for _, n := range []string{"devhub.com", "devhub.exe", "devhub.bat", "devhub.cmd"} {
				if fileExists(filepath.Join(cwd, n)) {
					warns = append(warns, fmt.Sprintf("%s exists in the current directory — in cmd.exe a bare `devhub` runs it instead of the slot", n))
				}
			}
		}
	}

	// --- running instance ---
	fmt.Println("[instance]")
	store := openStoreQuiet()
	if store != nil {
		defer store.Close()
	}
	port := resolvePort(store)
	fmt.Printf("  port       : %d\n", port)
	pids, listenersErr := listenersOn(port)
	if listenersErr != nil {
		warns = append(warns, fmt.Sprintf("could not inspect listeners on port %d: %v", port, listenersErr))
	} else if len(pids) == 0 {
		fmt.Println("  server     : not running")
	} else {
		fmt.Printf("  listener   : pid %s\n", joinInts(pids))
		if info, err := probeInfo(port); err == nil {
			fmt.Printf("  server     : devhub %s (edition %s), home %s\n", info.Version, info.Edition, info.Base)
			if info.Version != version {
				warns = append(warns, fmt.Sprintf("running instance is devhub %s but this command is %s — `devhub stop` && `devhub start` to align", info.Version, version))
			}
		} else {
			warns = append(warns, fmt.Sprintf("port %d is taken by pid %s which did not identify as devhub (%v)", port, joinInts(pids), err))
		}
	}

	// --- warnings ---
	fmt.Println("[warnings]")
	if len(warns) == 0 {
		fmt.Println("  none")
		return 0
	}
	for _, w := range warns {
		fmt.Printf("  - %s\n", w)
	}
	return 1
}

func exeName() string {
	if platform.IsWindows() {
		return "devhub.exe"
	}
	return "devhub"
}

// sameFilePath compares paths the way the resolving shell would (Windows is
// case-insensitive).
func sameFilePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if platform.IsWindows() {
		return strings.EqualFold(a, b)
	}
	return a == b
}
