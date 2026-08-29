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
	"github.com/imohiyoko/devhub/internal/probeauth"
	"github.com/imohiyoko/devhub/internal/storage"
)

const rootUsage = `devhub — local dev dashboard (single binary)

Usage:
  devhub start              start the server (serves the dashboard on 127.0.0.1)
  devhub status             show the instance on the configured port (exit 1 if none)
  devhub stop               stop that instance after verifying it is devhub
  devhub doctor             diagnose command slot / PATH / running instance (exit 1 on warnings)
  devhub env list           list env-launcher environments and their live ports
  devhub env start <env-id> launch an environment (baton ports are taken over)
  devhub env stop <env-id>  kill the live processes of an environment
  devhub docs list          list the embedded documentation (JSON)
  devhub docs show <name>   print one document
  devhub version            print version info (same as -version)
  devhub help               show this help

A bare 'devhub' (no arguments) prints this help. Starting the server is the
explicit 'devhub start', so a reflexive 'devhub' never binds a port by surprise.

Driving devhub from a coding agent? Read 'devhub docs show agent/ai-api' for
the HTTP surface, 'agent/cli' for this command line, and
'agent/troubleshooting' when a call fails.

'devhub start' can take an optional provenance — 'binary', 'homebrew', or
'code' — to launch the server from a specific devhub instead of the current
one (a one-off choice that touches no command slot; see 'devhub start -h').

Flags (devhub start):
  -no-browser               do not open a browser on start

Global:
  -version                  print version and exit

Environment:
  DEVHUB_PORT               listen/target port (overrides the settings value; default 8765)
  DEVHUB_HOME               data root (default %LOCALAPPDATA%\devhub / ~/.devhub)
  DEVHUB_BIN_DIR            command-slot directory checked by doctor
`

// startUsage is printed for `devhub start -h`, a flag parse error, or an
// unknown provenance.
const startUsage = `Usage: devhub start [<provenance>] [flags]

Start the devhub server in the foreground: it serves the dashboard on
127.0.0.1 (Ctrl+C to quit) and, unless -no-browser, opens a browser.

With no <provenance> the server runs from the current devhub. A <provenance>
hands off to a specific devhub implementation instead — a one-off choice that
changes no command slot or PATH:

  binary      the release binary installed under <DevhubHome>/bin
  homebrew    a Homebrew-installed devhub found on PATH (not on Windows)
  code        the current source, via 'go run' in a devhub checkout
              (the current directory, the dev-shim's checkout, or $DEVHUB_SRC)

Aliases: release=binary, brew=homebrew, source=code.

Flags:
  -no-browser   do not open a browser on start

Examples:
  devhub start                      run from the current devhub
  devhub start code                 run this checkout from source
  devhub start binary -no-browser   run the release binary, no browser
`

// runSubcommand dispatches `devhub <name> …` and returns the process exit
// code. Every action is a named subcommand, including `start` (the server);
// an unknown word is an error here rather than falling through to a server
// start, so a typo like `stpo` can't launch a server by surprise.
func runSubcommand(name string, args []string) int {
	switch name {
	case "start":
		return runServer(args)
	case "env":
		return runEnv(args)
	case "docs":
		return runDocs(args)
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
		fmt.Fprintf(os.Stderr, "devhub: unknown subcommand %q\n\n%s\n%s\n", name, rootUsage, docsHint)
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
	return resolvePortCandidates(store)[0]
}

// resolvePortCandidates returns the live runtime port first and the newly
// configured port second. Saving a port does not move an already-bound server;
// stop must still find that old listener before a restart starts the new one.
func resolvePortCandidates(store *storage.Store) []int {
	configured := 8765
	if store != nil {
		configured = devhubPort(store)
	} else if env := os.Getenv("DEVHUB_PORT"); env != "" {
		if port, err := parsePortEnv(env); err == nil {
			configured = port
		}
	}
	if os.Getenv("DEVHUB_PORT") != "" || store == nil {
		return []int{configured}
	}
	ports := []int{}
	if active, err := store.LoadActiveInstance(); err == nil && active.Port >= 1 && active.Port <= 65535 {
		ports = append(ports, active.Port)
	}
	if len(ports) == 0 || ports[0] != configured {
		ports = append(ports, configured)
	}
	return ports
}

func parsePortEnv(s string) (int, error) {
	var p int
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &p)
	return p, err
}

// listenersOn returns the pids listening on port (usually one). Callers must
// distinguish an empty result from a failed system-level listener query.
func listenersOn(port int) ([]int, error) {
	entries, err := portsctl.ListListening()
	if err != nil {
		return nil, err
	}
	var pids []int
	seen := map[int]bool{}
	for _, e := range entries {
		if e.Port == port && !seen[e.PID] {
			seen[e.PID] = true
			pids = append(pids, e.PID)
		}
	}
	return pids, nil
}

// serverInfo is the subset of GET /api/info the CLI consumes.
type serverInfo struct {
	Version string `json:"version"`
	Edition string `json:"edition"`
	PID     int    `json:"pid"`
	Proof   string `json:"proof"`
	Base    string `json:"-"`
}

// probeInfo asks the listener to sign a fresh challenge with the same-user
// secret. The secret is used locally to verify the response and is never sent
// to the unverified TCP peer. Redirects are not followed: an old devhub or
// another app must read as "cannot identify", not as success.
func probeInfo(port int) (*serverInfo, error) {
	agentToken, err := storage.ReadAgentToken(platform.DevhubHome())
	if err != nil {
		return nil, fmt.Errorf("read ai-api token: %w", err)
	}
	nonce, err := probeauth.NewNonce()
	if err != nil {
		return nil, fmt.Errorf("generate probe nonce: %w", err)
	}
	client := &http.Client{
		Timeout: 1500 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/ai-api/probe", port), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Devhub-Probe-Nonce", nonce)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response %s (devhub too old for signed probe, or another app)", resp.Status)
	}
	var info serverInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return nil, fmt.Errorf("not devhub: %v", err)
	}
	if info.PID <= 0 || info.Version == "" || !probeauth.Verify(agentToken, nonce, port, probeauth.Info{
		Version: info.Version, Edition: info.Edition, PID: info.PID, Proof: info.Proof,
	}) {
		return nil, fmt.Errorf("response lacks a valid devhub probe proof")
	}
	info.Base = platform.DevhubHome()
	return &info, nil
}
