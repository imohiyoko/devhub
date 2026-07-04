// Command devhub is the single-binary local dev dashboard. It serves the
// embedded frontend and API on 127.0.0.1, backed by a SQLite store under
// $DEVHUB_HOME (default ~/.devhub).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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
	// Subcommands are dispatched before flag parsing: they are short-lived CLI
	// actions against local state (see cli.go), not a server start, so the
	// server flags don't apply to them. Any non-flag first argument goes
	// through the dispatcher, so an unknown word errors with the usage instead
	// of being silently swallowed by flag.Parse and starting the server.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit(runSubcommand(os.Args[1], os.Args[2:]))
	}

	noBrowser := flag.Bool("no-browser", false, "do not open a browser on start")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() { fmt.Fprint(os.Stderr, rootUsage) }
	flag.Parse()

	if *showVersion {
		printVersion()
		return
	}

	// A Windows self-update leaves the previous binary as "<exe>.old" (a running
	// .exe can't be deleted); remove it now that the old process has exited.
	updater.CleanupOld()

	home := platform.DevhubHome()
	store, err := storage.Open(home, devhub.Assets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: storage:", err)
		os.Exit(1)
	}
	defer store.Close()

	settings, err := store.LoadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: settings:", err)
		os.Exit(1)
	}

	srv, err := server.New(store, devhub.Assets, settings, *noBrowser, version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		os.Exit(1)
	}
	if err := srv.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		os.Exit(1)
	}
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
