package execaudit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// markerRe extracts the id from an `//execaudit:<id>` marker. A space after //
// (as `// execaudit:<id>`) is tolerated so gofmt/linters can't break the link.
var markerRe = regexp.MustCompile(`//\s*execaudit:\s*([A-Za-z0-9_-]+)`)

// callSite is one discovered exec.Command / exec.CommandContext invocation.
type callSite struct {
	file string
	line int
	id   string // "" when the call carries no //execaudit marker
}

// TestExecCallSitesAreRegistered is the drift guard: every place the codebase
// spawns a process must be tagged with an //execaudit:<id> marker naming a
// Surface in Registry, and every Surface must be referenced by at least one such
// call site. Add an exec call without annotating it, or leave a stale Surface
// behind, and this fails.
func TestExecCallSitesAreRegistered(t *testing.T) {
	root := moduleRoot(t)
	sites := discoverExecCallSites(t, root)

	// A zero count means the scanner stopped matching (e.g. an import-name or AST
	// assumption broke) and would silently pass everything — fail loudly instead.
	if len(sites) == 0 {
		t.Fatal("no exec.Command/CommandContext call sites found; the scanner is broken")
	}

	known := map[string]bool{}
	for _, s := range Registry {
		known[s.ID] = true
	}

	used := map[string]bool{}
	var unannotated []string
	for _, cs := range sites {
		rel := relPath(root, cs.file)
		if cs.id == "" {
			unannotated = append(unannotated, rel+":"+strconv.Itoa(cs.line))
			continue
		}
		used[cs.id] = true
		if !known[cs.id] {
			t.Errorf("%s:%d: marked //execaudit:%s but no such Surface in Registry (registry.go)", rel, cs.line, cs.id)
		}
	}

	if len(unannotated) > 0 {
		sort.Strings(unannotated)
		t.Errorf("exec.Command/CommandContext call site(s) without an //execaudit:<id> marker:\n  %s\n"+
			"Add a trailing //execaudit:<id> naming a Surface in internal/execaudit/registry.go (add the Surface if it is new).",
			strings.Join(unannotated, "\n  "))
	}

	for _, s := range Registry {
		if !used[s.ID] {
			t.Errorf("Registry Surface %q is referenced by no exec call site; remove it or annotate the code that uses it", s.ID)
		}
	}
}

// TestRegistryWellFormed checks the Registry's own invariants so a malformed
// entry can't weaken the audit.
func TestRegistryWellFormed(t *testing.T) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(Registry))
	for _, s := range Registry {
		switch {
		case s.ID == "":
			t.Errorf("Surface with empty ID: %+v", s)
			continue
		case seen[s.ID]:
			t.Errorf("duplicate Surface ID %q", s.ID)
		}
		seen[s.ID] = true
		ids = append(ids, s.ID)

		if len(s.Binaries) == 0 {
			t.Errorf("Surface %q: Binaries must not be empty", s.ID)
		}
		if s.Kind != Fixed && s.Kind != Dynamic {
			t.Errorf("Surface %q: Kind must be Fixed or Dynamic, got %q", s.ID, s.Kind)
		}
		if strings.TrimSpace(s.Trigger) == "" {
			t.Errorf("Surface %q: Trigger must not be empty", s.ID)
		}
		if strings.TrimSpace(s.Gate) == "" {
			t.Errorf("Surface %q: Gate must not be empty", s.ID)
		}
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("Registry must be kept sorted by ID; got %v", ids)
	}
}

// discoverExecCallSites walks the module and returns every stdlib exec.Command /
// exec.CommandContext invocation in non-test .go source.
func discoverExecCallSites(t *testing.T, root string) []callSite {
	var sites []callSite
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			// Skip VCS/tooling and, crucially, .claude/worktrees/* — those are full
			// copies of the repo that would double-count every call site. `scripts`
			// holds standalone build/CI tools (not the server binary) and is out of
			// the audit's scope.
			case ".git", ".claude", "node_modules", "testdata", "vendor", "scripts":
				return filepath.SkipDir
			}
			return nil
		}
		// _test.go is out of scope: the audit covers the production exec surface,
		// not test helpers that shell out.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		sites = append(sites, scanFile(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return sites
}

// scanFile parses one file and returns its exec call sites, each resolved to the
// //execaudit id attached to it (if any). It matches against the file's actual
// local name for "os/exec", so an aliased import is handled and an unrelated
// package-qualified Command() is not mistaken for the stdlib one.
func scanFile(t *testing.T, path string) []callSite {
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	execName := execImportName(f)
	if execName == "" {
		return nil // file does not import os/exec
	}
	lines := strings.Split(string(src), "\n")

	var sites []callSite
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != execName {
			return true
		}
		if sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext" {
			return true
		}
		line := fset.Position(call.Pos()).Line
		sites = append(sites, callSite{file: path, line: line, id: markerFor(lines, line)})
		return true
	})
	return sites
}

// execImportName returns the local identifier a file uses for "os/exec"
// (usually "exec"), or "" if the file does not import it.
func execImportName(f *ast.File) string {
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) != "os/exec" {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "exec"
	}
	return ""
}

// markerFor returns the //execaudit id for the exec call on 1-based line n: a
// trailing marker on that line, or failing that a marker on a comment-only line
// immediately above. The above-line form must be a pure comment so a trailing
// marker belonging to a different statement can never be borrowed.
func markerFor(lines []string, n int) string {
	if m := lineMarker(lines, n); m != "" {
		return m
	}
	if n-1 >= 1 && n-1 <= len(lines) && strings.HasPrefix(strings.TrimSpace(lines[n-2]), "//") {
		return lineMarker(lines, n-1)
	}
	return ""
}

func lineMarker(lines []string, n int) string {
	if n < 1 || n > len(lines) {
		return ""
	}
	if m := markerRe.FindStringSubmatch(lines[n-1]); m != nil {
		return m[1]
	}
	return ""
}

// moduleRoot walks up from the test's working directory to the go.mod dir.
func moduleRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the test directory")
		}
		dir = parent
	}
}

func relPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}
