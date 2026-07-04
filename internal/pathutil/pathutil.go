// Package pathutil provides the filesystem path helpers shared by the workspace
// and git controllers.
package pathutil

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExpandUser replaces a leading ~ with the user's home directory.
func ExpandUser(p string) string {
	// If home can't be resolved, leave ~ untouched rather than letting
	// filepath.Join silently collapse it to a CWD-relative path.
	h := home()
	if p == "~" {
		if h == "" {
			return p
		}
		return h
	}
	if strings.HasPrefix(p, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(p, `~\`)) {
		if h == "" {
			return p
		}
		return filepath.Join(h, p[2:])
	}
	return p
}

// NormCase case-folds on Windows, identity elsewhere.
// (filepath.Clean already normalizes separators on Windows.)
func NormCase(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// AbsClean returns the cleaned absolute form of p.
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

// FileURI renders an (absolute) filesystem path as a file: URI with reserved
// characters percent-encoded (spaces, '#', '%', ...). The SQLite driver hands a
// "file:" DSN to SQLite's URI parser, which otherwise treats '#' as a fragment
// (silently opening a different, empty database) and '%XX' as an escape — so a
// raw path containing those characters would open the wrong file. Callers append
// their own "?..." query (e.g. mode, _pragma) to the returned string.
func FileURI(path string) string {
	p := filepath.ToSlash(path)
	if p == "" || p[0] != '/' {
		p = "/" + p // file:///C:/... on Windows, file:///abs/... already starts with /
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// IsDir reports whether p is a directory, following symlinks.
func IsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Exists reports whether p exists, following symlinks.
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
