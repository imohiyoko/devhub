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
)

// version/commit/date are stamped at build time via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	noBrowser := flag.Bool("no-browser", false, "do not open a browser on start")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("devhub", version)
		fmt.Println("  edition:", platform.Edition(version))
		if commit != "" {
			fmt.Println("  commit:", commit)
		}
		if date != "" {
			fmt.Println("  built: ", date)
		}
		return
	}

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
