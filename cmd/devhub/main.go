// Command devhub is the single-binary local dev dashboard. It serves the
// embedded frontend and API on 127.0.0.1, backed by a SQLite store under
// $DEVHUB_HOME (default ~/.devhub).
package main

import (
	"flag"
	"fmt"
	"os"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/platform"
	"github.com/imohiyoko/devhub/internal/server"
	"github.com/imohiyoko/devhub/internal/storage"
	"github.com/imohiyoko/devhub/internal/updater"
)

// version/commit/date are stamped at build time via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	// Every action is an explicit subcommand, including starting the server
	// (`devhub start`). A bare `devhub` no longer launches a server — it prints
	// help. This extends ADR 0002's decision 5 (an unknown word must not fall
	// through to a server start): the reflexive `devhub` should never bind a
	// port by surprise, so the stateful action is named. Only the pure-info
	// -version / -h flags are still honored at the top level.
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(rootUsage)
		return
	}
	switch args[0] {
	case "-version", "--version":
		printVersion()
		return
	case "-h", "--help":
		fmt.Print(rootUsage)
		return
	}
	os.Exit(runSubcommand(args[0], args[1:]))
}

// runServer handles `devhub start [<provenance>] [flags]`. When the first
// positional is a provenance (binary / homebrew / code) it hands the server off
// to that specific devhub implementation (see start.go); otherwise it starts the
// server in-process via startServer. The provenance is extracted here, before
// flag parsing, because Go's flag package stops at the first non-flag token
// anyway — so a provenance must lead, and everything after it is passed through
// to the target's own `start`.
func runServer(args []string) int {
	if token, rest, present := provenanceArg(args); present {
		prov, ok := parseProvenance(token)
		if !ok {
			fmt.Fprintf(os.Stderr, "devhub start: unknown provenance %q (want: binary, homebrew, or code)\n\n%s", token, startUsage)
			return 2
		}
		return runLaunch(prov, rest)
	}
	return startServer(args)
}

// startServer starts the dashboard server in-process and returns the process
// exit code. It parses the start-only flags, then blocks in srv.Run until the
// process is stopped or re-execs itself. A self-restart (update / rebuild /
// restart) carries os.Args forward, so the "start" subcommand is preserved
// across the restart — the new process starts a server, not help.
func startServer(args []string) int {
	fs := flag.NewFlagSet("devhub start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	noBrowser := fs.Bool("no-browser", false, "do not open a browser on start")
	fs.Usage = func() { fmt.Fprint(os.Stderr, startUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// A Windows self-update leaves the previous binary as "<exe>.old" (a running
	// .exe can't be deleted); remove it now that the old process has exited.
	updater.CleanupOld()

	home := platform.DevhubHome()
	store, err := storage.Open(home, devhub.Assets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: storage:", err)
		return 1
	}
	defer store.Close()

	settings, err := store.LoadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: settings:", err)
		return 1
	}

	srv, err := server.New(store, devhub.Assets, settings, *noBrowser, version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 1
	}
	if err := srv.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 1
	}
	return 0
}

// printVersion prints the stamped version/edition, shared by the -version
// flag and the `version` subcommand.
func printVersion() {
	fmt.Println("devhub", version)
	fmt.Println("  edition:", platform.Edition(version))
	if commit != "" {
		fmt.Println("  commit:", commit)
	}
	if date != "" {
		fmt.Println("  built: ", date)
	}
}
