package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseProvenance(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"binary":   {provBinary, true},
		"release":  {provBinary, true}, // alias
		"BINARY":   {provBinary, true}, // case-insensitive
		"homebrew": {provHomebrew, true},
		"brew":     {provHomebrew, true}, // alias
		"code":     {provCode, true},
		"Source":   {provCode, true}, // alias, case-insensitive
		"nope":     {"", false},
		"":         {"", false},
	}
	for token, want := range cases {
		got, ok := parseProvenance(token)
		if got != want.want || ok != want.ok {
			t.Errorf("parseProvenance(%q) = (%q, %v), want (%q, %v)", token, got, ok, want.want, want.ok)
		}
	}
}

func TestProvenanceArg(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		token   string
		rest    []string
		present bool
	}{
		{"none", nil, "", nil, false},
		{"provenance only", []string{"code"}, "code", []string{}, true},
		{"provenance + flag", []string{"code", "-no-browser"}, "code", []string{"-no-browser"}, true},
		{"leading flag", []string{"-no-browser"}, "", []string{"-no-browser"}, false},
		{"flag then word", []string{"-no-browser", "code"}, "", []string{"-no-browser", "code"}, false},
	}
	for _, c := range cases {
		token, rest, present := provenanceArg(c.args)
		if token != c.token || present != c.present || !reflect.DeepEqual(rest, c.rest) {
			t.Errorf("%s: provenanceArg(%v) = (%q, %v, %v), want (%q, %v, %v)",
				c.name, c.args, token, rest, present, c.token, c.rest, c.present)
		}
	}
}

// existsSet returns an exists-func true only for the given paths.
func existsSet(paths ...string) func(string) bool {
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	return func(p string) bool { return set[p] }
}

// baseDeps is a launchDeps where every lookup fails; tests enable just the
// pieces the case under test needs.
func baseDeps() launchDeps {
	return launchDeps{
		isWin:      false,
		exists:     func(string) bool { return false },
		execExists: func(string) bool { return false },
		lookPath:   func(string) (string, error) { return "", errors.New("not found") },
		readFile:   func(string) ([]byte, error) { return nil, errors.New("no file") },
		evalSyml:   func(p string) (string, error) { return p, nil },
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return "", errors.New("no cwd") },
		devhubHome: filepath.Join("/home", "u", ".devhub"),
		pathDirs:   nil,
		slot:       filepath.Join("/home", "u", ".local", "bin", "devhub"),
	}
}

func TestResolveLaunch_Binary(t *testing.T) {
	d := baseDeps()
	bin := filepath.Join(d.devhubHome, "bin", "devhub")
	d.exists = existsSet(bin)

	argv, dir, err := resolveLaunch(provBinary, []string{"-no-browser"}, d)
	if err != nil {
		t.Fatalf("resolveLaunch binary: %v", err)
	}
	want := []string{bin, "start", "-no-browser"}
	if !reflect.DeepEqual(argv, want) || dir != "" {
		t.Errorf("binary = (%v, %q), want (%v, %q)", argv, dir, want, "")
	}

	// Windows uses devhub.exe.
	dw := baseDeps()
	dw.isWin = true
	winBin := filepath.Join(dw.devhubHome, "bin", "devhub.exe")
	dw.exists = existsSet(winBin)
	argv, _, err = resolveLaunch(provBinary, nil, dw)
	if err != nil || argv[0] != winBin {
		t.Errorf("windows binary = (%v, %v), want argv[0]=%q", argv, err, winBin)
	}

	// Missing binary is an error.
	if _, _, err := resolveLaunch(provBinary, nil, baseDeps()); err == nil {
		t.Error("missing release binary should error")
	}
}

func TestResolveLaunch_Code(t *testing.T) {
	goBin := filepath.Join("/usr", "bin", "go")
	root := filepath.Join("/home", "u", "devhub")
	goMod := filepath.Join(root, "go.mod")
	mainGo := filepath.Join(root, "cmd", "devhub", "main.go")
	wantArgv := []string{goBin, "run", "./cmd/devhub", "start", "-no-browser"}

	// (1) cwd is the checkout root.
	d := baseDeps()
	d.lookPath = func(string) (string, error) { return goBin, nil }
	d.getwd = func() (string, error) { return root, nil }
	d.exists = existsSet(goMod, mainGo)
	argv, dir, err := resolveLaunch(provCode, []string{"-no-browser"}, d)
	if err != nil || !reflect.DeepEqual(argv, wantArgv) || dir != root {
		t.Errorf("code via cwd = (%v, %q, %v), want (%v, %q)", argv, dir, err, wantArgv, root)
	}

	// (2) cwd is a subdirectory — ascend to the checkout root.
	d2 := baseDeps()
	d2.lookPath = func(string) (string, error) { return goBin, nil }
	d2.getwd = func() (string, error) { return filepath.Join(root, "internal", "server"), nil }
	d2.exists = existsSet(goMod, mainGo)
	if _, dir, err := resolveLaunch(provCode, nil, d2); err != nil || dir != root {
		t.Errorf("code via ascend = (%q, %v), want dir %q", dir, err, root)
	}

	// (3) cwd is not a checkout, but the dev shim records one.
	shim := "#!/usr/bin/env bash\n# devhub dev shim\ncd \"" + root + "\" || exit 1\nexec go run ./cmd/devhub \"$@\"\n"
	d3 := baseDeps()
	d3.lookPath = func(string) (string, error) { return goBin, nil }
	d3.getwd = func() (string, error) { return filepath.Join("/tmp", "elsewhere"), nil }
	d3.exists = existsSet(goMod, mainGo)
	d3.readFile = func(string) ([]byte, error) { return []byte(shim), nil }
	if _, dir, err := resolveLaunch(provCode, nil, d3); err != nil || dir != root {
		t.Errorf("code via shim = (%q, %v), want dir %q", dir, err, root)
	}

	// (4) DEVHUB_SRC override.
	d4 := baseDeps()
	d4.lookPath = func(string) (string, error) { return goBin, nil }
	d4.getwd = func() (string, error) { return filepath.Join("/tmp", "elsewhere"), nil }
	d4.exists = existsSet(goMod, mainGo)
	d4.getenv = func(k string) string {
		if k == "DEVHUB_SRC" {
			return root
		}
		return ""
	}
	if _, dir, err := resolveLaunch(provCode, nil, d4); err != nil || dir != root {
		t.Errorf("code via DEVHUB_SRC = (%q, %v), want dir %q", dir, err, root)
	}

	// (5) No checkout anywhere → error.
	dNone := baseDeps()
	dNone.lookPath = func(string) (string, error) { return goBin, nil }
	if _, _, err := resolveLaunch(provCode, nil, dNone); err == nil {
		t.Error("code with no checkout should error")
	}

	// (6) go missing → error even if a checkout exists.
	dNoGo := baseDeps()
	dNoGo.getwd = func() (string, error) { return root, nil }
	dNoGo.exists = existsSet(goMod, mainGo)
	if _, _, err := resolveLaunch(provCode, nil, dNoGo); err == nil {
		t.Error("code without go toolchain should error")
	}
}

func TestResolveLaunch_Homebrew(t *testing.T) {
	brewDir := filepath.Join("/opt", "homebrew", "bin")
	brewDevhub := filepath.Join(brewDir, "devhub")

	// Found: a PATH devhub whose resolved path is under a Homebrew prefix.
	d := baseDeps()
	d.pathDirs = []string{brewDir}
	d.execExists = existsSet(brewDevhub)
	d.evalSyml = func(string) (string, error) {
		return "/opt/homebrew/Cellar/devhub/1.0/bin/devhub", nil
	}
	argv, dir, err := resolveLaunch(provHomebrew, []string{"-no-browser"}, d)
	want := []string{brewDevhub, "start", "-no-browser"}
	if err != nil || !reflect.DeepEqual(argv, want) || dir != "" {
		t.Errorf("homebrew found = (%v, %q, %v), want %v", argv, dir, err, want)
	}

	// A non-Homebrew devhub on PATH is not accepted.
	dMiss := baseDeps()
	localDir := filepath.Join("/usr", "local", "bin")
	dMiss.pathDirs = []string{localDir}
	dMiss.execExists = existsSet(filepath.Join(localDir, "devhub"))
	dMiss.evalSyml = func(p string) (string, error) { return p, nil }
	if _, _, err := resolveLaunch(provHomebrew, nil, dMiss); err == nil {
		t.Error("non-homebrew PATH devhub should not resolve as homebrew")
	}

	// Windows: homebrew is unavailable. Wire up inputs that WOULD resolve on a
	// non-Windows host (a Homebrew devhub.exe on PATH), so the failure can only
	// come from the isWin guard — then assert the error says so, not a generic
	// "not found". (Note: the PATH name must be the .exe form, because
	// scanPathForDevhub only looks for PATHEXT names when isWin is true.)
	dWin := baseDeps()
	dWin.isWin = true
	dWin.pathDirs = []string{brewDir}
	dWin.execExists = existsSet(filepath.Join(brewDir, "devhub.exe"))
	dWin.evalSyml = func(string) (string, error) {
		return "/opt/homebrew/Cellar/devhub/1.0/bin/devhub", nil
	}
	if _, _, err := resolveLaunch(provHomebrew, nil, dWin); err == nil || !strings.Contains(err.Error(), "Windows") {
		t.Errorf("homebrew on Windows: err = %v, want a Windows-specific error", err)
	}
}
