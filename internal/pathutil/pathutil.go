// Package pathutil provides the filesystem path helpers shared by the workspace
// and git controllers, mirroring the os.path.* operations used in the Python code.
package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExpandUser replaces a leading ~ with the user's home directory (os.path.expanduser).
func ExpandUser(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(p, `~\`)) {
		return filepath.Join(home(), p[2:])
	}
	return p
}

// NormCase mirrors os.path.normcase: case-fold on Windows, identity elsewhere.
// (filepath.Clean already normalizes separators on Windows.)
func NormCase(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// AbsClean returns the cleaned absolute form of p (os.path.normpath(os.path.abspath(p))).
func AbsClean(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs // filepath.Abs already calls Clean
	}
	return filepath.Clean(p)
}

// AbsExpand expands ~ then returns the cleaned absolute path.
func AbsExpand(p string) string {
	return AbsClean(ExpandUser(p))
}

// IsDir reports whether p is a directory, following symlinks (like scandir.is_dir()).
func IsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Exists reports whether p exists, following symlinks (os.path.exists).
func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
