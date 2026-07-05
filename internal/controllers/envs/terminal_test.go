package envs

import (
	"os"
	"strings"
	"testing"
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
