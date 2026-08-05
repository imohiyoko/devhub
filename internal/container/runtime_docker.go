package container

// Minimal Docker Compose adapter. Every invocation is a fixed argv scoped to
// the definition's project name, so this adapter only ever reports on and
// operates on the compose project the environment declares, and never on
// unrelated containers (plan §13). The bound is this file's, not devhub's:
// inventory.go lists the machine and control.go acts on a container nothing
// declared, each under its own execaudit Surface. Nothing here changes the
// global Docker context (plan §6.3).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/pathutil"
)

// composeProbeTimeout bounds one compose read — `compose ps`, or the
// availability probe. A status endpoint must not hang when the daemon is slow
// to answer or is coming up.
const composeProbeTimeout = 10 * time.Second

// composeOpTimeout bounds one mutating compose call — `up` or `stop`. It is
// generous because a first `up` may pull images; the operation is synchronous
// by design, since its exit status is what tells the caller the services
// actually came up. `stop` shares it rather than getting a tighter one of its
// own: a stop that is slow for the same reason an up is (a busy or
// still-starting daemon) should not be the one call that gives up early.
//
// Both are unexported because the method that needs one applies it itself: it
// knows which subcommand it is about to run, and no caller has to. A caller
// that wants to give up sooner still can, since the deadline is derived from
// the context it passes and the shorter of the two wins.
const composeOpTimeout = 5 * time.Minute

// ErrDockerMissing is the one failure devhub diagnoses itself; every other
// reason a compose call fails comes from Docker's own output.
var ErrDockerMissing = errors.New("docker コマンドが見つかりません")

// Adapter is what devhub does with a Compose implementation: read the
// state of a project's services, and start or stop the services a component
// declares. One implementation per container engine; Runtime holds them as
// interfaces so tests answer without Docker or Colima.
type Adapter interface {
	// Available reports why the adapter cannot run at all, or nil. It is the
	// "is this engine usable" half of the runtimes API, so it checks what
	// every other method needs.
	//
	// Implementations must bound themselves: Providers sets no deadline of its
	// own, so an implementation that blocks on an unbounded context hangs the
	// runtimes endpoint outright. There is no outer net to catch it.
	Available(ctx context.Context) error
	// The operational methods take the environment's runtime and derive what
	// they need from it — a Docker context, a Colima profile. It is a
	// parameter rather than adapter state because devhub must never select an
	// engine globally (plan §6.3): making every call site name the runtime is
	// what keeps `docker context use` unnecessary, and what stops one
	// environment's profile leaking into another's commands.
	//
	// These bound themselves too, and callers pass a plain background context:
	// the method knows which subcommand it is about to run and therefore which
	// budget applies, and no two callers can disagree about it.
	ServiceStates(ctx context.Context, rt Spec, spec ComposeSpec) (map[string]State, error)
	Up(ctx context.Context, rt Spec, spec ComposeSpec) error
	Stop(ctx context.Context, rt Spec, spec ComposeSpec) error
}

// dockerCompose talks to the local Docker via the `docker compose` CLI.
type dockerCompose struct {
	runner commandRunner
	// lookPath resolves the docker binary — a seam so tests can act as a host
	// that has no Docker installed.
	lookPath func(string) (string, error)
}

func newDockerCompose() *dockerCompose {
	return &dockerCompose{runner: execRunner{}, lookPath: exec.LookPath}
}

// Available probes for both halves of `docker compose`. The plugin check runs
// a command rather than looking for a file because Compose can be installed as
// a CLI plugin in several directories; `docker compose version` is the
// supported way to ask. It does not contact the daemon, so a machine with
// Docker installed but not running still answers promptly.
//
// Only the capability API pays for this: the operational path (run) keeps the
// cheap binary check, because it is about to invoke compose anyway and
// Docker's own "is not a docker command" error says it better than devhub can.
func (d *dockerCompose) Available(ctx context.Context) error {
	if err := d.binaryPresent(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, composeProbeTimeout)
	defer cancel()
	if _, stderr, err := d.runner.Run(ctx, "", "docker", "compose", "version", "--short"); err != nil {
		return fmt.Errorf("docker compose が使えません: %w", cliError(stderr, err))
	}
	return nil
}

func (d *dockerCompose) binaryPresent() error {
	if _, err := d.lookPath("docker"); err != nil {
		return ErrDockerMissing
	}
	return nil
}

// ServiceStates returns the state of each service of spec's project that has a
// container. Services with no container at all are absent from the result, and
// the caller reads that as stopped. The error is what the user is shown as the
// reason a component's state is unknown, so it carries Docker's own wording:
// "docker is missing" and "the daemon is unreachable" are different problems
// with different fixes, and only Docker can tell them apart reliably.
func (d *dockerCompose) ServiceStates(ctx context.Context, rt Spec, spec ComposeSpec) (map[string]State, error) {
	ctx, cancel := context.WithTimeout(ctx, composeProbeTimeout)
	defer cancel()
	stdout, err := d.run(ctx, rt, spec, "ps", "--format", "json", "--all")
	if err != nil {
		return nil, err
	}
	return parseComposePS(stdout)
}

// Up starts the component's services and waits for them to be up. Unlike a
// host process handed to a terminal, this reports whether the start actually
// succeeded — that is what `--wait` buys, and why apply can tell the user
// which components really came up.
func (d *dockerCompose) Up(ctx context.Context, rt Spec, spec ComposeSpec) error {
	ctx, cancel := context.WithTimeout(ctx, composeOpTimeout)
	defer cancel()
	_, err := d.run(ctx, rt, spec, append([]string{"up", "--detach", "--wait"}, spec.Services...)...)
	return err
}

// Stop stops the component's services, leaving the rest of the project (and
// any other project) alone.
func (d *dockerCompose) Stop(ctx context.Context, rt Spec, spec ComposeSpec) error {
	ctx, cancel := context.WithTimeout(ctx, composeOpTimeout)
	defer cancel()
	_, err := d.run(ctx, rt, spec, append([]string{"stop"}, spec.Services...)...)
	return err
}

// run executes one `docker compose` subcommand for spec's project in the given
// Docker context. Building the scoping flags in one place is what guarantees
// every operation devhub performs — read or write — is confined to the declared
// project on the intended engine.
//
// --context is a flag of `docker` itself, so it goes before the `compose`
// subcommand; `docker compose --context …` is rejected as an unknown flag.
func (d *dockerCompose) run(ctx context.Context, rt Spec, spec ComposeSpec, sub ...string) (string, error) {
	if err := d.binaryPresent(); err != nil {
		return "", err
	}
	var args []string
	if dockerContext := DockerContextFor(rt); dockerContext != "" {
		args = append(args, "--context", dockerContext)
	}
	args = append(args, "compose", "--project-name", spec.Project)
	for _, file := range spec.Files {
		args = append(args, "--file", pathutil.ExpandUser(file))
	}
	args = append(args, sub...)

	stdout, stderr, err := d.runner.Run(ctx, pathutil.ExpandUser(spec.Cwd), "docker", args...)
	if err != nil {
		return stdout, cliError(stderr, err)
	}
	return stdout, nil
}

// composePSEntry is the subset of `compose ps --format json` devhub reads.
// nerdctl names these fields the same way (its PortPublisher comment says the
// intent is to "match the json output with docker compose"), so one shape
// serves both engines.
type composePSEntry struct {
	Service string `json:"Service"`
	State   string `json:"State"`
}

// parseComposePS reads `compose ps --format json` from either engine, which
// prints a JSON array or one JSON object per line depending on the release —
// both shapes are accepted so devhub does not pin one. "running" is the token
// both emit for a live container; nerdctl's other values are raw containerd
// statuses ("exited", "created", "paused"), which fall through to stopped
// exactly as Docker's do.
func parseComposePS(out string) (map[string]State, error) {
	out = strings.TrimSpace(out)
	states := map[string]State{}
	if out == "" {
		return states, nil
	}
	var entries []composePSEntry
	if strings.HasPrefix(out, "[") {
		if err := json.Unmarshal([]byte(out), &entries); err != nil {
			return nil, fmt.Errorf("compose ps の出力を解釈できません: %w", err)
		}
	} else {
		for line := range strings.SplitSeq(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var entry composePSEntry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				return nil, fmt.Errorf("compose ps の出力を解釈できません: %w", err)
			}
			entries = append(entries, entry)
		}
	}
	for _, entry := range entries {
		if entry.Service == "" {
			continue
		}
		state := StateRunning
		if entry.State != "running" {
			state = StateStopped
		}
		// With replicas one container that is not running makes the whole
		// service not-running, so a later `up -d` can bring the missing one
		// back: once a service is stopped it stays stopped.
		if prev, seen := states[entry.Service]; !seen || prev == StateRunning {
			states[entry.Service] = state
		}
	}
	return states, nil
}

// ComposeState folds a project's service states into one component
// state: every service the component declares must be running for it to count
// as running, so a half-up project is still a start candidate.
func ComposeState(spec ComposeSpec, services map[string]State) State {
	if len(spec.Services) == 0 {
		return StateUnknown
	}
	for _, name := range spec.Services {
		if services[name] != StateRunning {
			return StateStopped
		}
	}
	return StateRunning
}
