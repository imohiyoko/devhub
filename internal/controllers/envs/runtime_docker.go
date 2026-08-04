package envs

// Minimal Docker Compose adapter — the read half (service state). Every
// invocation is a fixed argv scoped to the definition's project name, so
// devhub only ever reports on (and later, only ever operates on) the compose
// project the environment declares and never on unrelated containers
// (plan §13). Nothing here changes the global Docker context (plan §6.3).

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

// composeProbeTimeout bounds one `docker compose ps`. A status endpoint must
// not hang when the daemon is slow to answer or is coming up.
const composeProbeTimeout = 10 * time.Second

// composeUpTimeout bounds one `docker compose up`. It is generous because a
// first run may pull images; the operation is synchronous by design, since its
// exit status is what tells the caller the services actually came up.
const composeUpTimeout = 5 * time.Minute

// composeAdapter is what devhub does with Docker Compose: read the state of a
// project's services, and start or stop the services a component declares. The
// Controller holds it as an interface so tests answer without Docker.
type composeAdapter interface {
	ServiceStates(ctx context.Context, spec composeSpec) (map[string]componentState, error)
	Up(ctx context.Context, spec composeSpec) error
	Stop(ctx context.Context, spec composeSpec) error
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

// ServiceStates returns the state of each service of spec's project that has a
// container. Services with no container at all are absent from the result, and
// the caller reads that as stopped. The error is what the user is shown as the
// reason a component's state is unknown, so it carries Docker's own wording:
// "docker is missing" and "the daemon is unreachable" are different problems
// with different fixes, and only Docker can tell them apart reliably.
func (d *dockerCompose) ServiceStates(ctx context.Context, spec composeSpec) (map[string]componentState, error) {
	stdout, err := d.run(ctx, spec, "ps", "--format", "json", "--all")
	if err != nil {
		return nil, err
	}
	return parseComposePS(stdout)
}

// Up starts the component's services and waits for them to be up. Unlike a
// host process handed to a terminal, this reports whether the start actually
// succeeded — that is what `--wait` buys, and why apply can tell the user
// which components really came up.
func (d *dockerCompose) Up(ctx context.Context, spec composeSpec) error {
	_, err := d.run(ctx, spec, append([]string{"up", "--detach", "--wait"}, spec.Services...)...)
	return err
}

// Stop stops the component's services, leaving the rest of the project (and
// any other project) alone.
func (d *dockerCompose) Stop(ctx context.Context, spec composeSpec) error {
	_, err := d.run(ctx, spec, append([]string{"stop"}, spec.Services...)...)
	return err
}

// run executes one `docker compose` subcommand for spec's project. Building
// the scoping flags in one place is what guarantees every operation devhub
// performs — read or write — is confined to the declared project.
func (d *dockerCompose) run(ctx context.Context, spec composeSpec, sub ...string) (string, error) {
	if _, err := d.lookPath("docker"); err != nil {
		return "", errors.New("docker コマンドが見つかりません")
	}
	args := []string{"compose", "--project-name", spec.Project}
	for _, file := range spec.Files {
		args = append(args, "--file", pathutil.ExpandUser(file))
	}
	args = append(args, sub...)

	stdout, stderr, err := d.runner.Run(ctx, pathutil.ExpandUser(spec.Cwd), "docker", args...)
	if err != nil {
		return stdout, composeError(stderr, err)
	}
	return stdout, nil
}

// composeError turns a failed invocation into the reason a user sees: Docker's
// own first stderr line when it wrote one, the exec error otherwise (a missing
// working directory or a timeout writes nothing to stderr).
func composeError(stderr string, err error) error {
	if line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n"); line != "" {
		return errors.New(line)
	}
	return err
}

// composePSEntry is the subset of `docker compose ps --format json` devhub reads.
type composePSEntry struct {
	Service string `json:"Service"`
	State   string `json:"State"`
}

// parseComposePS reads `docker compose ps --format json`, which prints either a
// JSON array or one JSON object per line depending on the Compose version —
// both shapes are accepted so devhub does not pin a Compose release.
func parseComposePS(out string) (map[string]componentState, error) {
	out = strings.TrimSpace(out)
	states := map[string]componentState{}
	if out == "" {
		return states, nil
	}
	var entries []composePSEntry
	if strings.HasPrefix(out, "[") {
		if err := json.Unmarshal([]byte(out), &entries); err != nil {
			return nil, fmt.Errorf("docker compose ps の出力を解釈できません: %w", err)
		}
	} else {
		for line := range strings.SplitSeq(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var entry composePSEntry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				return nil, fmt.Errorf("docker compose ps の出力を解釈できません: %w", err)
			}
			entries = append(entries, entry)
		}
	}
	for _, entry := range entries {
		if entry.Service == "" {
			continue
		}
		state := stateRunning
		if entry.State != "running" {
			state = stateStopped
		}
		// With replicas one container that is not running makes the whole
		// service not-running, so a later `up -d` can bring the missing one
		// back: once a service is stopped it stays stopped.
		if prev, seen := states[entry.Service]; !seen || prev == stateRunning {
			states[entry.Service] = state
		}
	}
	return states, nil
}

// composeComponentState folds a project's service states into one component
// state: every service the component declares must be running for it to count
// as running, so a half-up project is still a start candidate.
func composeComponentState(spec composeSpec, services map[string]componentState) componentState {
	if len(spec.Services) == 0 {
		return stateUnknown
	}
	for _, name := range spec.Services {
		if services[name] != stateRunning {
			return stateStopped
		}
	}
	return stateRunning
}
