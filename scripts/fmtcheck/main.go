// Command fmtcheck fails when any Go source under the repo is not gofmt-clean.
//
// It exists so the CI format gate can be run identically everywhere via
// `mise run fmt-check`. The Makefile gate uses `[ -n "$(gofmt -l .)" ]`, which
// is POSIX-shell only; mise runs tasks under `cmd /c` on Windows, so a shell
// idiom would not be portable. Shelling out to gofmt from Go keeps the check
// shell-independent (PowerShell-only Windows contributors included).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	out, err := exec.Command("gofmt", "-l", ".").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fmtcheck: running gofmt:", err)
		os.Exit(2)
	}
	if files := strings.TrimSpace(string(out)); files != "" {
		fmt.Fprintln(os.Stderr, "these files need gofmt:")
		fmt.Fprintln(os.Stderr, files)
		os.Exit(1)
	}
}
