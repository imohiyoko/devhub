package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	devhub "github.com/imohiyoko/devhub"
	envsctl "github.com/imohiyoko/devhub/internal/controllers/envs"
	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
	workspacectl "github.com/imohiyoko/devhub/internal/controllers/workspace"
	"github.com/imohiyoko/devhub/internal/platform"
	"github.com/imohiyoko/devhub/internal/storage"
)

const envUsage = `Usage:
  devhub env list             list environments and their live ports
  devhub env start <env-id>   launch an environment (dependency order, worktree/port resolution)
  devhub env stop <env-id>    kill the live processes on an environment's ports

Works directly against the local devhub state, whether or not the devhub
server is running. Protected ports (ports tool) are never killed. start keeps
baton semantics: a process whose declared port is held by something else takes
the port over (the kill is printed).
`

// runEnv executes `devhub env <subcommand>` and returns the process exit code.
// It opens the shared store itself (read-only usage; WAL makes a second reader
// process safe) and wires the same controllers the server's registry does, so
// CLI stop behaves exactly like the env-launcher UI's kill buttons.
func runEnv(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(envUsage)
		return 0
	}
	store, err := storage.Open(platform.DevhubHome(), devhub.Assets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: storage:", err)
		return 1
	}
	defer store.Close()
	git := gitctl.New(store)
	envs := envsctl.New(store, git, portsctl.New(store), workspacectl.New(store, git))

	switch args[0] {
	case "list":
		return envList(envs)
	case "start":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "devhub: env start needs an environment id (see `devhub env list`)")
			return 2
		}
		return envStart(envs, args[1])
	case "stop":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "devhub: env stop needs an environment id (see `devhub env list`)")
			return 2
		}
		return envStop(envs, store, args[1])
	default:
		fmt.Fprintf(os.Stderr, "devhub: unknown env subcommand %q\n\n%s", args[0], envUsage)
		return 2
	}
}

// envStart launches the environment synchronously and reports every baton
// take-over — a port changing hands must be visible, not silent.
func envStart(envs *envsctl.Controller, envID string) int {
	killed, err := envs.StartEnvironment(envID)
	for _, k := range killed {
		fmt.Printf("baton  :%d (pid %d) killed to take the port over\n", k.Port, k.PID)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 1
	}
	fmt.Printf("started %s (check with `devhub env list`)\n", envID)
	return 0
}

func envList(envs *envsctl.Controller) int {
	statuses, err := envs.EnvStatuses()
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 1
	}
	if len(statuses) == 0 {
		fmt.Println("no environments defined (configure them in the env-launcher tool)")
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPROCESSES\tLIVE")
	for _, st := range statuses {
		live := "-"
		if len(st.LivePorts) > 0 {
			ports := make([]string, len(st.LivePorts))
			for i, p := range st.LivePorts {
				ports[i] = ":" + strconv.Itoa(p)
			}
			live = strings.Join(ports, " ")
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", st.ID, st.Name, st.Processes, live)
	}
	_ = w.Flush()
	return 0
}

func envStop(envs *envsctl.Controller, store *storage.Store, envID string) int {
	outcomes, err := envs.StopEnvironment(envID, devhubPort(store))
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 1
	}
	if len(outcomes) == 0 {
		fmt.Printf("%s: no live processes on its ports\n", envID)
		return 0
	}
	code := 0
	for _, o := range outcomes {
		switch {
		case o.Avoided:
			fmt.Printf("skip   :%d (pid %d) — devhub's own port\n", o.Port, o.PID)
		case o.Err != nil:
			fmt.Fprintf(os.Stderr, "failed :%d (pid %d): %v\n", o.Port, o.PID, o.Err)
			code = 1
		default:
			fmt.Printf("killed :%d (pid %d)\n", o.Port, o.PID)
		}
	}
	return code
}

// devhubPort resolves the port the main devhub instance binds, mirroring
// server.New: settings["port"] (default 8765) overridden by DEVHUB_PORT. Stop
// passes it as an avoid port so an env that declares devhub's base port (like
// the devhub-verify example, whose offset instance is the one meant to die)
// can never take down the main instance.
func devhubPort(store *storage.Store) int {
	port := 8765
	if settings, err := store.LoadSettings(); err == nil {
		switch p := settings["port"].(type) {
		case float64:
			port = int(p)
		case int:
			port = p
		case string:
			if i, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
				port = i
			}
		}
	}
	if env := os.Getenv("DEVHUB_PORT"); env != "" {
		if p, err := strconv.Atoi(env); err == nil {
			port = p
		}
	}
	return port
}
