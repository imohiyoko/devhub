package container

// containerd Compose adapter, driven through nerdctl. It is a separate
// adapter from the Docker one — not a flag on it — because the two engines
// differ in ways that matter to the caller: how a command is addressed to a
// profile, and (see Up) whether a start can wait for readiness at all.
//
// devhub reaches nerdctl through `colima nerdctl`, since a Colima profile's
// containerd lives inside the VM. That keeps the profile explicit per command
// (plan §11 PR 3), needs no host-side nerdctl installation, and adds no binary
// beyond the colima CLI devhub already runs.
//
// Note that the compose files and working directory are resolved *inside* the
// VM. Colima mounts the user's home directory by default, so a definition
// under ~ works unchanged; one outside every mount will not be visible to
// nerdctl, and nerdctl's own error says so.

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/imohiyoko/devhub/internal/pathutil"
	"github.com/imohiyoko/devhub/internal/platform"
)

// nerdctlCompose runs `colima nerdctl -p <profile> -- compose …`.
type nerdctlCompose struct {
	runner   commandRunner
	lookPath func(string) (string, error)
	darwin   bool
}

func newNerdctlCompose() *nerdctlCompose {
	return &nerdctlCompose{runner: execRunner{}, lookPath: exec.LookPath, darwin: platform.IsDarwin()}
}

// Available reports whether devhub can reach a containerd engine at all. The
// gate is Colima itself: nerdctl ships inside a containerd profile's VM, so
// there is no separate host-side CLI to look for, and asking colima about it
// would mean starting or entering a VM just to answer a capability question.
func (n *nerdctlCompose) Available(context.Context) error {
	if !n.darwin {
		return ErrColimaUnsupportedOS
	}
	if _, err := n.lookPath("colima"); err != nil {
		return ErrColimaMissing
	}
	return nil
}

func (n *nerdctlCompose) ServiceStates(ctx context.Context, rt Spec, spec ComposeSpec) (map[string]State, error) {
	ctx, cancel := context.WithTimeout(ctx, composeProbeTimeout)
	defer cancel()
	stdout, err := n.run(ctx, rt, spec, "ps", "--format", "json", "--all")
	if err != nil {
		return nil, err
	}
	return parseComposePS(stdout)
}

// Up starts the component's services. Unlike the Docker adapter this cannot
// wait for them to become healthy: `nerdctl compose up` has no --wait, so a
// successful return means "the containers were created", not "the services are
// ready". A caller that starts components in dependency order still gets that
// order, but not the readiness guarantee behind it: a dependent may start
// before what it depends on is actually serving. The user is told so by
// Warnings rather than left to discover it from a flaky start.
func (n *nerdctlCompose) Up(ctx context.Context, rt Spec, spec ComposeSpec) error {
	ctx, cancel := context.WithTimeout(ctx, composeOpTimeout)
	defer cancel()
	_, err := n.run(ctx, rt, spec, append([]string{"up", "--detach"}, spec.Services...)...)
	return err
}

func (n *nerdctlCompose) Stop(ctx context.Context, rt Spec, spec ComposeSpec) error {
	ctx, cancel := context.WithTimeout(ctx, composeOpTimeout)
	defer cancel()
	_, err := n.run(ctx, rt, spec, append([]string{"stop"}, spec.Services...)...)
	return err
}

// run executes one `compose` subcommand through colima's nerdctl passthrough.
// The `--` separator is what keeps the two -p flags apart: colima's own -p
// selects the VM profile, and the -p after the separator is compose's project
// name. Without it colima would swallow the project name as a profile.
func (n *nerdctlCompose) run(ctx context.Context, rt Spec, spec ComposeSpec, sub ...string) (string, error) {
	if err := n.Available(ctx); err != nil {
		return "", err
	}
	args := []string{"nerdctl", "--profile", colimaProfileFor(rt), "--", "compose", "--project-name", spec.Project}
	for _, file := range spec.Files {
		args = append(args, "--file", pathutil.ExpandUser(file))
	}
	args = append(args, sub...)

	stdout, stderr, err := n.runner.Run(ctx, pathutil.ExpandUser(spec.Cwd), "colima", args...)
	if err != nil {
		return stdout, cliError(stderr, err)
	}
	return stdout, nil
}

// errContainerdUnsupported is returned when a Spec asks for containerd
// somewhere it cannot exist.
//
// It is a defensive path, not a user-facing one, and that is the thing worth
// knowing about it. validateRuntime already refuses an engine under any
// provider but colima, so no definition saved through devhub can produce it;
// reaching it means a definition arrived some other way, or a writer stopped
// validating. It stays a typed error rather than a panic for that reason — one
// environment is misconfigured, and the rest of the machine still works.
//
// It stays unexported for the same reason. An exported error is a promise that
// callers will want to match on it, and no consumer can reach this one to try:
// the exported method that returns it, ComposeFor, only produces it for a Spec
// the schema will not accept. Export it when something outside this package has
// a reason to tell it apart from "the engine is not answering" — at that point
// the distinction will be real, and so will the caller.
var errContainerdUnsupported = errors.New("containerd engine は Colima profile でのみ利用できます")

// colimaProfileFor is the profile a runtime addresses, defaulting to colima's
// own default so an environment that names the provider but no profile still
// resolves to a real VM.
func colimaProfileFor(rt Spec) string {
	if strings.TrimSpace(rt.Profile) != "" {
		return rt.Profile
	}
	return DefaultColimaProfile
}
