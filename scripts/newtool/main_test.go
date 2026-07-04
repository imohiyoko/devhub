package main

import (
	"strings"
	"testing"
)

func TestDeriveGoName(t *testing.T) {
	cases := map[string]string{
		"notes":    "Notes",
		"my-tool":  "MyTool",
		"db-table": "DbTable",
		"diff-kun": "DiffKun",
		"a-b-c":    "ABC",
		"env2":     "Env2",
	}
	for id, want := range cases {
		if got := deriveGoName(id); got != want {
			t.Errorf("deriveGoName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestIDPattern(t *testing.T) {
	valid := []string{"notes", "my-tool", "db-table", "diff-kun", "env2", "a1-b2"}
	invalid := []string{"", "-x", "x-", "x--y", "My-Tool", "with_underscore", "1lead", "Upper"}
	for _, id := range valid {
		if !idPattern.MatchString(id) {
			t.Errorf("id %q should be valid", id)
		}
	}
	for _, id := range invalid {
		if idPattern.MatchString(id) {
			t.Errorf("id %q should be invalid", id)
		}
	}
}

func TestParseArgs(t *testing.T) {
	t.Run("id only", func(t *testing.T) {
		id, gn, po, err := parseArgs([]string{"notes"})
		if err != nil || id != "notes" || gn != "" || po {
			t.Fatalf("got id=%q gn=%q po=%v err=%v", id, gn, po, err)
		}
	})
	t.Run("flags after id", func(t *testing.T) {
		id, gn, po, err := parseArgs([]string{"my-tool", "--page-only", "--go-name", "Widget"})
		if err != nil || id != "my-tool" || gn != "Widget" || !po {
			t.Fatalf("got id=%q gn=%q po=%v err=%v", id, gn, po, err)
		}
	})
	t.Run("flags before id and --go-name=", func(t *testing.T) {
		id, gn, po, err := parseArgs([]string{"--page-only", "--go-name=Widget", "notes"})
		if err != nil || id != "notes" || gn != "Widget" || !po {
			t.Fatalf("got id=%q gn=%q po=%v err=%v", id, gn, po, err)
		}
	})
	t.Run("missing id", func(t *testing.T) {
		if _, _, _, err := parseArgs([]string{"--page-only"}); err == nil {
			t.Fatal("expected error for missing id")
		}
	})
	t.Run("unknown flag", func(t *testing.T) {
		if _, _, _, err := parseArgs([]string{"notes", "--nope"}); err == nil {
			t.Fatal("expected error for unknown flag")
		}
	})
	t.Run("extra positional", func(t *testing.T) {
		if _, _, _, err := parseArgs([]string{"a", "b"}); err == nil {
			t.Fatal("expected error for extra positional")
		}
	})
	t.Run("dangling --go-name", func(t *testing.T) {
		if _, _, _, err := parseArgs([]string{"notes", "--go-name"}); err == nil {
			t.Fatal("expected error for --go-name without value")
		}
	})
}

const sampleRegistry = "package tools\n\nfunc Registry() *core.Registry {\n\treturn core.NewRegistry(\n" +
	"\t\tnewGit(git),\n" +
	"\t\tnewDiagram(),\n" +
	"\t\t// ← new tools: add one constructor here.\n" +
	"\t)\n}\n"

func TestInsertConstructor(t *testing.T) {
	out, err := insertConstructor(sampleRegistry, "newNotes")
	if err != nil {
		t.Fatalf("insertConstructor: %v", err)
	}
	// The new entry sits on its own line, tab-indented, right before the marker.
	if !strings.Contains(out, "\t\tnewNotes(),\n\t\t"+registryMarker) {
		t.Errorf("constructor not inserted before marker:\n%s", out)
	}
	// Existing entries and the marker are preserved.
	for _, want := range []string{"newGit(git),", "newDiagram(),", registryMarker} {
		if !strings.Contains(out, want) {
			t.Errorf("output lost %q", want)
		}
	}
}

func TestInsertConstructorRejectsDuplicate(t *testing.T) {
	out, err := insertConstructor(sampleRegistry, "newNotes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertConstructor(out, "newNotes"); err == nil {
		t.Fatal("expected error when constructor already registered")
	}
}

func TestInsertConstructorMissingMarker(t *testing.T) {
	if _, err := insertConstructor("package tools\n", "newNotes"); err == nil {
		t.Fatal("expected error when marker is absent")
	}
}

func TestRenderGoSource(t *testing.T) {
	data := tmplData{ID: "my-tool", Type: "myToolTool", Ctor: "newMyTool"}

	api, err := render(goTemplate(false), data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func newMyTool() core.Tool { return myToolTool{} }",
		`ID:    "my-tool"`,
		`Pattern: "/api/my-tool/ping"`,
	} {
		if !strings.Contains(api, want) {
			t.Errorf("api template missing %q", want)
		}
	}

	page, err := render(goTemplate(true), data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, "return pageTool{meta: core.Meta{") {
		t.Errorf("page-only template should use pageTool:\n%s", page)
	}
	if strings.Contains(page, "/api/") {
		t.Errorf("page-only template must not declare an API route:\n%s", page)
	}
}
