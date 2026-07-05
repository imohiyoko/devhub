package envs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imohiyoko/devhub/internal/platform"
)

// On the Windows Terminal (wt) launch path the env prefix must be joined to the
// command with a newline, never ';': wt splits its command line on ';' with no
// working escape (microsoft/terminal#11314), which used to tear the composed
// "$env:… ; <cmd>" across tabs and fail the launch with 0x80070002. PowerShell
// accepts a newline as the statement separator.
func TestBuildCmdWithEnvPowerShellUsesNewline(t *testing.T) {
	got := buildCmdWithEnv("go run ./cmd/devhub start", map[string]string{"DEVHUB_PORT": "8766"}, true, true)
	want := "$env:DEVHUB_PORT='8766'\ngo run ./cmd/devhub start"
	if got != want {
		t.Errorf("buildCmdWithEnv() = %q, want %q", got, want)
	}
	if strings.Contains(got, ";") {
		t.Errorf("PowerShell/wt command must not contain ';' (wt would split it): %q", got)
	}
}

// The non-PowerShell Windows path (cmd) keeps its ' & ' separator, and no env
// means the command passes through untouched.
func TestBuildCmdWithEnvOtherShells(t *testing.T) {
	if got := buildCmdWithEnv("go build", nil, true, false); got != "go build" {
		t.Errorf("no-env command should be unchanged, got %q", got)
	}
	cmd := buildCmdWithEnv("run", map[string]string{"K": "v"}, true, false)
	if !strings.Contains(cmd, `set "K=v"`) || !strings.Contains(cmd, " & run") {
		t.Errorf("cmd path should set-and-'&', got %q", cmd)
	}
}

// The wt/PowerShell path hands wt a script file (never an inline -Command with a
// ';' or newline it would mangle), so writeLaunchScript must produce a .ps1 that
// contains the composed env-prefixed command verbatim.
func TestWriteLaunchScript(t *testing.T) {
	p, err := writeLaunchScript("$env:DEVHUB_PORT='8766'\ngo run ./cmd/devhub start")
	if err != nil {
		t.Fatalf("writeLaunchScript: %v", err)
	}
	defer os.Remove(p)
	if !strings.HasSuffix(p, ".ps1") {
		t.Errorf("script path %q should end in .ps1", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if got := string(b); !strings.Contains(got, "$env:DEVHUB_PORT='8766'") || !strings.Contains(got, "go run ./cmd/devhub start") {
		t.Errorf("script content missing the command: %q", got)
	}
}

// sanitizePath must repair a PATH with CR/LF spliced in (the real Windows
// registry corruption that severed powershell.exe resolution) while leaving a
// clean PATH byte-for-byte unchanged.
func TestSanitizePath(t *testing.T) {
	sep := string(os.PathListSeparator)
	clean := "C:\\a" + sep + "C:\\b"
	if got := sanitizePath(clean); got != clean {
		t.Errorf("clean PATH must be unchanged, got %q", got)
	}
	// A newline spliced right after a separator (';\n' — the exact corruption).
	if got := sanitizePath("C:\\a" + sep + "\nC:\\b"); got != clean {
		t.Errorf("';<LF>' should collapse: got %q, want %q", got, clean)
	}
	// A bare newline standing in for the separator.
	if got := sanitizePath("C:\\a\nC:\\b"); got != clean {
		t.Errorf("newline-only separator: got %q, want %q", got, clean)
	}
	// CRLF plus a doubled separator both normalize to a single break.
	if got := sanitizePath("C:\\a" + sep + sep + "\r\nC:\\b"); got != clean {
		t.Errorf("CRLF/doubled sep: got %q, want %q", got, clean)
	}
}

// mergedEnv must hand children a PATH free of CR/LF, whatever this process
// inherited.
func TestMergedEnvSanitizesPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", "C:\\a"+sep+"\nC:\\b")
	var got string
	found := false
	for _, e := range mergedEnv(nil) {
		if k, v, ok := strings.Cut(e, "="); ok && strings.EqualFold(k, "PATH") {
			got, found = v, true
		}
	}
	if !found {
		t.Fatal("PATH missing from mergedEnv output")
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("mergedEnv PATH still contains CR/LF: %q", got)
	}
	if want := "C:\\a" + sep + "C:\\b"; got != want {
		t.Errorf("mergedEnv PATH = %q, want %q", got, want)
	}
}

// lookPathIn resolves against an explicit PATH (honoring PATHEXT on Windows) and
// reports absence rather than falling back to the live PATH.
func TestLookPathIn(t *testing.T) {
	dir := t.TempDir()
	name := "devhubfakeshell"
	file := name
	if platform.IsWindows() {
		file = name + ".exe"
	}
	full := filepath.Join(dir, file)
	if err := os.WriteFile(full, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Compare case-insensitively: on Windows the extension comes from PATHEXT
	// (".EXE") while the file was created ".exe"; the FS is case-insensitive, and
	// this mirrors what Go's own exec.LookPath returns.
	if got, ok := lookPathIn(name, dir); !ok || !strings.EqualFold(got, full) {
		t.Errorf("lookPathIn(%q) = %q, %v; want %q (any case), true", name, got, ok, full)
	}
	if _, ok := lookPathIn("devhub-nonexistent-xyz", dir); ok {
		t.Error("lookPathIn found a nonexistent executable")
	}
}

// resolveShell resolves from a (sanitized) PATH, returns explicit paths as-is,
// and falls back to the bare name when nothing matches (never empty).
func TestResolveShell(t *testing.T) {
	dir := t.TempDir()
	name := "devhubfakeshell"
	file := name
	if platform.IsWindows() {
		file = name + ".exe"
	}
	full := filepath.Join(dir, file)
	if err := os.WriteFile(full, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := resolveShell(name); !strings.EqualFold(got, full) {
		t.Errorf("resolveShell(%q) = %q, want %q (any case)", name, got, full)
	}
	if got := resolveShell("devhub-nonexistent-xyz"); got != "devhub-nonexistent-xyz" {
		t.Errorf("resolveShell fallback = %q, want the bare name", got)
	}
	// An explicit path is returned unchanged (contains a separator, not re-looked-up).
	explicit := filepath.Join(dir, "somewhere", "sh")
	if got := resolveShell(explicit); got != explicit {
		t.Errorf("resolveShell(path) = %q, want %q", got, explicit)
	}
}
