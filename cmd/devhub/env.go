package main

import (
	"bufio"
	"errors"
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
  devhub env list                          list environments and their live ports
  devhub env start <env-id>                launch an environment (dependency order, worktree/port resolution)
  devhub env stop <env-id>                 kill the live processes on an environment's ports
  devhub env status <env-id>               show each component's state and the scenarios available
  devhub env switch <env-id> <scenario>    switch to a scenario (prints the plan, then asks)
  devhub env switch <env-id> --stop        stop the scenario-scoped components, keeping shared ones

Works directly against the local devhub state, whether or not the devhub
server is running. Protected ports (ports tool) are never killed. start keeps
baton semantics: a process whose declared port is held by something else takes
the port over (the kill is printed).

switch prints what it would stop, keep and start and asks before doing it;
-y (or --yes) skips the question. status probes compose_service components,
which runs 'docker compose ps'.
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
	case "status":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "devhub: env status needs an environment id (see `devhub env list`)")
			return 2
		}
		return envComponentStatus(envs, args[1])
	case "switch":
		return envSwitch(envs, args[1:])
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

// envComponentStatus prints one environment's components with the state devhub
// observes, and the scenarios it can be switched to.
func envComponentStatus(envs *envsctl.Controller, envID string) int {
	components, scenarios, err := envs.EnvComponents(envID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 1
	}
	if len(components) == 0 {
		fmt.Printf("%s: no components defined\n", envID)
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(w, "STATE\tCOMPONENT\tKIND\tSCOPE\tDETAIL")
	for _, c := range components {
		scope := "scenario"
		if c.Shared {
			scope = "shared"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.State, c.Label, c.Kind, scope, c.Reason)
	}
	_ = w.Flush()
	if len(scenarios) > 0 {
		names := make([]string, len(scenarios))
		for i, s := range scenarios {
			names[i] = s.ID
		}
		fmt.Printf("\nscenarios: %s\n", strings.Join(names, ", "))
	}
	return 0
}

// parseSwitchArgs reads `<env-id> <scenario-id|--stop> [-y]`. The target is
// positional so the common case reads like the other env subcommands.
func parseSwitchArgs(args []string) (envID string, target envsctl.SwitchTarget, assumeYes bool, err error) {
	var positional []string
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			assumeYes = true
		case "--stop":
			// An empty (non-nil) selection is "only the shared components".
			target.Components = []string{}
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		return "", target, false, errors.New("env switch needs an environment id (see `devhub env list`)")
	}
	envID = positional[0]
	if len(positional) > 1 {
		target.ScenarioID = positional[1]
	}
	if (target.ScenarioID != "") == (target.Components != nil) {
		return "", target, false, errors.New("env switch needs exactly one of a scenario id or --stop (see `devhub env status <env-id>`)")
	}
	return envID, target, assumeYes, nil
}

// envSwitch shows the plan before acting: the CLI has no confirmation screen,
// so the plan print plus the prompt is that screen. The approved plan's
// fingerprint is passed to the apply, so what runs is what was shown.
func envSwitch(envs *envsctl.Controller, args []string) int {
	envID, target, assumeYes, err := parseSwitchArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 2
	}
	plan, err := envs.PlanSwitch(envID, target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 1
	}
	printSwitchPlan(plan)
	if len(plan.Stop) == 0 && len(plan.Start) == 0 {
		fmt.Println("\nnothing to do")
		return 0
	}
	if !assumeYes && !confirmSwitch(len(plan.Stop)) {
		fmt.Println("aborted")
		return 1
	}
	_, results, err := envs.ApplySwitch(envID, target, plan.Fingerprint)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub:", err)
		return 1
	}
	code := 0
	fmt.Println()
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "failed %-5s %s: %v\n", r.Action, r.Step.Label, r.Err)
			code = 1
			continue
		}
		fmt.Printf("ok     %-5s %s\n", r.Action, r.Step.Label)
	}
	return code
}

// printSwitchPlan mirrors the confirmation screen's wording (plan §10).
func printSwitchPlan(plan envsctl.SwitchPlan) {
	line := func(label string, steps []envsctl.PlanStep) {
		names := make([]string, len(steps))
		for i, s := range steps {
			names[i] = s.Label
		}
		if len(names) == 0 {
			names = []string{"なし"}
		}
		fmt.Printf("%s: %s\n", label, strings.Join(names, ", "))
	}
	line("停止", plan.Stop)
	line("維持", plan.Keep)
	line("起動", plan.Start)
	if len(plan.Warnings) == 0 {
		fmt.Println("警告: なし")
		return
	}
	fmt.Println("警告:")
	for _, w := range plan.Warnings {
		fmt.Printf("  - %s\n", w)
	}
}

// confirmSwitch asks before anything is stopped or started. A closed stdin
// (piped, no answer) counts as "no": an unattended run must not stop things by
// default — that is what -y is for.
func confirmSwitch(stops int) bool {
	if stops > 0 {
		fmt.Printf("\n%d件を停止します。続行しますか? [y/N]: ", stops)
	} else {
		fmt.Print("\n適用しますか? [y/N]: ")
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
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
