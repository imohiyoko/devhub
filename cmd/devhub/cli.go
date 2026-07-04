package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	devhub "github.com/imohiyoko/devhub"
	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
	"github.com/imohiyoko/devhub/internal/platform"
	"github.com/imohiyoko/devhub/internal/storage"
)

const rootUsage = `devhub — local dev dashboard (single binary)

Usage:
  devhub [flags]            start the server (default action)
  devhub status             show the instance on the configured port (exit 1 if none)
  devhub stop               stop that instance after verifying it is devhub
  devhub doctor             diagnose command slot / PATH / running instance (exit 1 on warnings)
  devhub env list           list env-launcher environments and their live ports
  devhub env start <env-id> launch an environment (baton ports are taken over)
  devhub env stop <env-id>  kill the live processes of an environment
  devhub version            print version info (same as -version)
  devhub help               show this help

Flags (server):
  -no-browser               do not open a browser on start
  -version                  print version and exit

Environment:
  DEVHUB_PORT               listen/target port (overrides the settings value; default 8765)
  DEVHUB_HOME               data root (default %LOCALAPPDATA%\devhub / ~/.devhub)
  DEVHUB_BIN_DIR            command-slot directory checked by doctor
`

// runSubcommand dispatches `devhub <name> …` and returns the process exit
// code. Called for any non-flag first argument, so an unknown word is an error
// here instead of being silently swallowed by flag.Parse and starting the
// server (the pre-subcommand behavior, and a surprise when you typo `stpo`).
func runSubcommand(name string, args []string) int {
	switch name {
	case "env":
		return runEnv(args)
	case "doctor":
		return runDoctor()
	case "status":
		return runStatus()
	case "stop":
		return runStop()
	case "version":
		printVersion()
		return 0
	case "help":
		fmt.Print(rootUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "devhub: unknown subcommand %q\n\n%s", name, rootUsage)
		return 2
	}
}

// openStoreQuiet opens the shared store, or returns nil after reporting to
// stderr. CLI subcommands degrade instead of aborting: status/stop/doctor can
// still probe the default port when the DB is unreadable.
func openStoreQuiet() *storage.Store {
	store, err := storage.Open(platform.DevhubHome(), devhub.Assets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: storage:", err, "(continuing without settings)")
		return nil
	}
	return store
}

// resolvePort returns the port the main devhub instance binds, tolerating a
// nil store (settings unreadable → default 8765, DEVHUB_PORT still honored).
func resolvePort(store *storage.Store) int {
	if store == nil {
		port := 8765
		if env := os.Getenv("DEVHUB_PORT"); env != "" {
			if p, err := parsePortEnv(env); err == nil {
				port = p
			}
		}
		return port
	}
	return devhubPort(store)
}

func parsePortEnv(s string) (int, error) {
	var p int
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &p)
	return p, err
}

// listenersOn returns the pids listening on port (usually one).
func listenersOn(port int) []int {
	entries, err := portsctl.ListListening()
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: listing ports:", err)
		return nil
	}
	var pids []int
	seen := map[int]bool{}
	for _, e := range entries {
		if e.Port == port && !seen[e.PID] {
			seen[e.PID] = true
			pids = append(pids, e.PID)
		}
	}
	return pids
}

// serverInfo is the subset of GET /api/info the CLI consumes.
type serverInfo struct {
	Version  string `json:"version"`
	Edition  string `json:"edition"`
	Base     string `json:"base"`
	Port     int    `json:"port"`
	Instance string `json:"instance"`
}

// probeInfo asks the listener on port to identify itself via the token-less
// /ai-api/info read (loopback-only, no approval needed for plain GETs). This
// is the CLI's only sanctioned window into a running server: the /api/ token
// lives in server memory alone. Redirects are not followed — a devhub too old
// to serve /ai-api answers 302-to-dashboard, which must read as "cannot
// identify", not as success.
func probeInfo(port int) (*serverInfo, error) {
	client := &http.Client{
		Timeout: 1500 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/ai-api/info", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response %s (devhub too old for /ai-api, or another app)", resp.Status)
	}
	var info serverInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return nil, fmt.Errorf("not devhub: %v", err)
	}
	if info.Instance == "" && info.Version == "" {
		return nil, fmt.Errorf("response lacks devhub info fields")
	}
	return &info, nil
}
