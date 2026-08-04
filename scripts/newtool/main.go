// Command newtool scaffolds a new devhub tool: a core.Tool adapter under
// internal/tools, a page stub under tools/<id>, and the one-line registration in
// internal/tools/registry.go — so no manual wiring step is left.
//
//	go run ./scripts/newtool <id> [--page-only] [--go-name <Name>]
//
// <id> is the route namespace: the page is served at /<id>. Unlike the old bash
// generator, dash-containing ids are accepted (the shipped diff-kun / db-table /
// env-launcher use them); the Go type name is derived by dropping dashes and
// CamelCasing, or set explicitly with --go-name (id db-table -> --go-name Database).
//
// --page-only scaffolds a frontend-only tool that reuses pageTool (like diff-kun
// and diagram) instead of a struct with a ping route.
//
// It is pure Go so it runs the same on Windows PowerShell as on a Unix shell; the
// Makefile target and the mise task are thin wrappers around it.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// registryMarker is the anchor in registry.go before which each new tool's
// constructor is inserted. Keep it in sync with internal/tools/registry.go.
const registryMarker = "// ← new tools:"

var (
	// idPattern: lowercase alphanumeric segments joined by single dashes, no
	// leading/trailing/double dash. Permits notes, my-tool, db-table; rejects
	// -x, x-, x--y, Upper.
	idPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	// goNamePattern validates an explicit --go-name (a bare Go identifier).
	goNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "newtool:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	id, goNameFlag, pageOnly, err := parseArgs(args)
	if err != nil {
		return err
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid id %q: must match %s (lowercase, digits, single dashes)", id, idPattern)
	}

	goName := goNameFlag
	if goName == "" {
		goName = deriveGoName(id)
	} else if !goNamePattern.MatchString(goName) {
		return fmt.Errorf("invalid --go-name %q: must be a Go identifier", goName)
	}
	ctor := "new" + goName                  // e.g. newMyTool
	typeName := lowerFirst(goName) + "Tool" // e.g. myToolTool (unused for --page-only)

	root, err := moduleRoot()
	if err != nil {
		return err
	}
	goFile := filepath.Join(root, "internal", "tools", id+".go")
	pageDir := filepath.Join(root, "tools", id)
	pageFile := filepath.Join(pageDir, "index.html")
	registryFile := filepath.Join(root, "internal", "tools", "registry.go")

	if exists(goFile) {
		return fmt.Errorf("%s already exists", rel(root, goFile))
	}
	if exists(pageFile) {
		return fmt.Errorf("%s already exists", rel(root, pageFile))
	}

	data := tmplData{ID: id, Type: typeName, Ctor: ctor}
	goSrc, err := render(goTemplate(pageOnly), data)
	if err != nil {
		return err
	}
	pageSrc, err := render(pageTemplate(pageOnly), data)
	if err != nil {
		return err
	}

	// Wire the registry first: if that fails (marker missing), no files are left
	// half-created for the caller to clean up.
	registrySrc, err := os.ReadFile(registryFile)
	if err != nil {
		return err
	}
	wired, err := insertConstructor(string(registrySrc), ctor)
	if err != nil {
		return err
	}

	if err := os.WriteFile(goFile, []byte(goSrc), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(pageFile, []byte(pageSrc), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(registryFile, []byte(wired), 0o644); err != nil {
		return err
	}

	// Best-effort: keep the generated + edited Go gofmt-clean. gofmt ships with
	// the toolchain, so it is normally present; a miss is not fatal.
	gofmt(goFile, registryFile)

	fmt.Println("created:")
	fmt.Println("  " + rel(root, goFile))
	fmt.Println("  " + rel(root, pageFile))
	fmt.Println("wired:")
	fmt.Printf("  %s  (added %s() to the registry)\n", rel(root, registryFile), ctor)
	fmt.Println()
	fmt.Println("next: go build ./... && go run ./cmd/devhub   # the card appears automatically")
	// A new route is a new answer owed to the exec ledger, and the guard will
	// say so on the next test run. Say it here first, so the failure reads as a
	// step that was skipped rather than as a broken scaffold.
	fmt.Println("then:  classify the new route in internal/execaudit — a Surface's Callers if it can spawn a process,")
	fmt.Println("       execFreeEndpoints in callers_test.go if it cannot. go test ./internal/execaudit will insist.")
	return nil
}

// parseArgs pulls the positional id and the --page-only / --go-name flags out of
// args in any order, so `newtool notes --page-only` and `newtool --page-only
// notes` both work (flag.Parse stops at the first positional).
func parseArgs(args []string) (id, goName string, pageOnly bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--page-only":
			pageOnly = true
		case a == "--go-name":
			i++
			if i >= len(args) {
				return "", "", false, errors.New("--go-name needs a value")
			}
			goName = args[i]
		case strings.HasPrefix(a, "--go-name="):
			goName = strings.TrimPrefix(a, "--go-name=")
			if goName == "" {
				return "", "", false, errors.New("--go-name needs a value")
			}
		case strings.HasPrefix(a, "-"):
			return "", "", false, fmt.Errorf("unknown flag %q", a)
		default:
			if id != "" {
				return "", "", false, fmt.Errorf("unexpected extra argument %q", a)
			}
			id = a
		}
	}
	if id == "" {
		return "", "", false, errors.New("usage: newtool <id> [--page-only] [--go-name <Name>]")
	}
	return id, goName, pageOnly, nil
}

// deriveGoName turns a tool id into a CamelCase Go name: my-tool -> MyTool,
// notes -> Notes, db-table -> DbTable.
func deriveGoName(id string) string {
	var b strings.Builder
	for seg := range strings.SplitSeq(id, "-") {
		if seg == "" {
			continue
		}
		b.WriteString(strings.ToUpper(seg[:1]))
		b.WriteString(seg[1:])
	}
	return b.String()
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// insertConstructor adds "<ctor>()," on its own line just before the registry
// marker, matching the marker line's indentation. It errors if the marker is
// absent or the constructor is already registered.
func insertConstructor(src, ctor string) (string, error) {
	call := ctor + "(),"
	if strings.Contains(src, "\n\t\t"+call) || strings.Contains(src, "\t"+call+"\n") {
		return "", fmt.Errorf("%s is already registered in registry.go", ctor)
	}
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.Contains(line, registryMarker) {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		inserted := append([]string{}, lines[:i]...)
		inserted = append(inserted, indent+call)
		inserted = append(inserted, lines[i:]...)
		return strings.Join(inserted, "\n"), nil
	}
	return "", fmt.Errorf("registry marker %q not found in registry.go", registryMarker)
}

type tmplData struct {
	ID   string // route namespace / page dir, e.g. my-tool
	Type string // Go struct type, e.g. myToolTool
	Ctor string // constructor func, e.g. newMyTool
}

func render(tmpl string, data tmplData) (string, error) {
	t, err := template.New("t").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func goTemplate(pageOnly bool) string {
	if pageOnly {
		return pageOnlyGoTmpl
	}
	return apiGoTmpl
}

func pageTemplate(pageOnly bool) string {
	if pageOnly {
		return pageOnlyHTMLTmpl
	}
	return apiHTMLTmpl
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if exists(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found — run from within the devhub repo")
		}
		dir = parent
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}

// gofmt formats the given Go files in place, best-effort.
func gofmt(files ...string) {
	args := append([]string{"-w"}, files...)
	if err := exec.Command("gofmt", args...).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "newtool: gofmt skipped (%v); files are still valid\n", err)
	}
}

const apiGoTmpl = `package tools

import (
	"net/http"

	"github.com/imohiyoko/devhub/internal/core"
	"github.com/imohiyoko/devhub/internal/httpx"
)

// {{.Type}} is the {{.ID}} tool. Replace this stub with real behavior.
type {{.Type}} struct{}

func {{.Ctor}}() core.Tool { return {{.Type}}{} }

func (t {{.Type}}) Meta() core.Meta {
	return core.Meta{
		ID:    "{{.ID}}",
		Title: "{{.ID}}",
		Icon:  "🔧",
		Desc:  "TODO: describe {{.ID}}",
		Page:  "tools/{{.ID}}/index.html",
	}
}

func (t {{.Type}}) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/{{.ID}}/ping", Handle: func(w http.ResponseWriter, _ *http.Request) error {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
			return nil
		}},
	}
}
`

const pageOnlyGoTmpl = `package tools

import "github.com/imohiyoko/devhub/internal/core"

// {{.Ctor}} is the frontend-only {{.ID}} tool (no API); it serves an embedded page.
func {{.Ctor}}() core.Tool {
	return pageTool{meta: core.Meta{
		ID:    "{{.ID}}",
		Title: "{{.ID}}",
		Icon:  "🔧",
		Desc:  "TODO: describe {{.ID}}",
		Page:  "tools/{{.ID}}/index.html",
	}}
}
`

const apiHTMLTmpl = `<!doctype html>
<html lang="ja">
<head><meta charset="utf-8"><title>{{.ID}}</title></head>
<body>
  <h1>{{.ID}}</h1>
  <p>TODO: build the {{.ID}} UI. The API token shim is injected automatically.</p>
  <pre id="out">loading…</pre>
  <script>
    fetch('/api/{{.ID}}/ping').then(r => r.json()).then(d => {
      document.getElementById('out').textContent = JSON.stringify(d);
    });
  </script>
</body>
</html>
`

const pageOnlyHTMLTmpl = `<!doctype html>
<html lang="ja">
<head><meta charset="utf-8"><title>{{.ID}}</title></head>
<body>
  <h1>{{.ID}}</h1>
  <p>TODO: build the {{.ID}} UI (frontend-only tool).</p>
</body>
</html>
`
