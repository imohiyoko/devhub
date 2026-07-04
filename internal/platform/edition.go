package platform

import (
	"os"
	"path/filepath"
	"strings"
)

// Edition constants describe how a running devhub binary was distributed. They
// surface in /api/info and the dashboard footer so a user can tell at a glance
// which build they launched.
const (
	EditionCode      = "code"      // built/run from a source checkout (go run / local build)
	EditionHomebrew  = "homebrew"  // installed via `brew install --cask`
	EditionInstaller = "installer" // placed by install.sh / install.ps1, a manual download, or `go install`
)

// Edition reports how this binary was distributed, given the build-stamped
// version string (main.version, "dev" when unstamped).
//
// It must be derived at runtime, not stamped at build time: the Homebrew cask
// and the install-script binary are byte-identical goreleaser artifacts, so
// ldflags cannot tell them apart — only their on-disk location can.
//
// Precedence:
//  1. DEVHUB_EDITION env override — for packagers, or to force a value in tests.
//  2. "code"      — version is empty or "dev"; goreleaser always stamps a real
//     version, so an unstamped build is a from-source run.
//  3. "homebrew"  — the executable resolves under a Homebrew Caskroom/Cellar or
//     brew prefix.
//  4. "installer" — anything else with a stamped version (release archive).
func Edition(version string) string {
	if e := strings.TrimSpace(os.Getenv("DEVHUB_EDITION")); e != "" {
		return e
	}
	if version == "" || version == "dev" {
		return EditionCode
	}
	if isHomebrewPath(executablePath()) {
		return EditionHomebrew
	}
	return EditionInstaller
}

// executablePath returns the fully symlink-resolved path of the running binary,
// or "" if it can't be determined. Symlink resolution matters for Homebrew:
// `brew` links bin/devhub into its prefix, but the real file lives under
// Caskroom/Cellar — which is where the marker below is found.
func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// isHomebrewPath reports whether p sits within a Homebrew installation. It
// matches the cask store (Caskroom) and formula store (Cellar) — which cover
// the `/usr/local` and `/opt/homebrew` prefixes on macOS — plus the generic
// prefixes for Apple-Silicon and Linuxbrew. Matching is case-insensitive and
// slash-normalised so it works regardless of OS path separator.
func isHomebrewPath(p string) bool {
	if p == "" {
		return false
	}
	s := strings.ToLower(filepath.ToSlash(p))
	for _, marker := range []string{"/caskroom/", "/cellar/", "/homebrew/", "/linuxbrew/"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
