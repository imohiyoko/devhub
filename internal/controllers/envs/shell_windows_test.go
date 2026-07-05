//go:build windows

package envs

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestShellCmdRawCmdLine pins the raw `cmd /S /C "..."` form: the command
// string must reach cmd.exe verbatim, without Go's default argument encoding.
func TestShellCmdRawCmdLine(t *testing.T) {
	cmd := shellCmd(`echo "a b"`)
	want := `cmd /S /C "echo "a b""`
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != want {
		t.Fatalf("SysProcAttr = %+v, want CmdLine %q", cmd.SysProcAttr, want)
	}
}

// TestShellCmdPreservesQuotes reproduces issue #114 end-to-end: Go's default
// encoding turns inner double quotes into \", which cmd.exe does not
// interpret, so PowerShell used to receive its -Command argument with literal
// quotes and echo the expression back as a string ("Write-Output (1+1)")
// instead of evaluating it. With the raw command line it prints 2.
func TestShellCmdPreservesQuotes(t *testing.T) {
	shell := "powershell"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "pwsh"
		if _, err := exec.LookPath(shell); err != nil {
			t.Skip("neither powershell nor pwsh on PATH")
		}
	}
	cmd := shellCmd(shell + ` -NoProfile -Command "Write-Output (1+1)"`)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "2" {
		t.Fatalf("output = %q, want \"2\"", got)
	}
}
