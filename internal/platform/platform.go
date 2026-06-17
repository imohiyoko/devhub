// Package platform centralizes OS-specific naming and path resolution so the
// rest of the codebase never branches on runtime.GOOS directly.
package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

// PyName maps runtime.GOOS to the value Python's platform.system() returns
// ("Darwin"/"Windows"/"Linux"). Terminal settings are keyed by this name, so
// using runtime.GOOS (lowercase) directly would silently miss the config and
// fall back to a raw shell launch.
func PyName() string {
	switch runtime.GOOS {
	case "darwin":
		return "Darwin"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		if runtime.GOOS == "" {
			return ""
		}
		// Title-case fallback (e.g. "freebsd" -> "Freebsd").
		return string(runtime.GOOS[0]-'a'+'A') + runtime.GOOS[1:]
	}
}

// IsWindows reports whether the current OS is Windows.
func IsWindows() bool { return runtime.GOOS == "windows" }

// Home returns the user's home directory (os.path.expanduser("~") equivalent).
func Home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// DevhubHome resolves the devhub data root. Honors DEVHUB_HOME; otherwise uses
// the same per-user location the managed install used so an upgrade from the
// Python app reuses its existing settings/ and devhub.db: %LOCALAPPDATA%\devhub
// on Windows, ~/.devhub elsewhere.
func DevhubHome() string {
	if v := os.Getenv("DEVHUB_HOME"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "devhub")
		}
	}
	return filepath.Join(Home(), ".devhub")
}
