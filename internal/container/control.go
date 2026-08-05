package container

// The operating half of the inventory: reading one container's logs, stopping
// it, starting it again, restarting it.
//
// This is a third seam rather than more calls under inventory.go or
// runtime_docker.go, and the reason is the same one that split those two — an
// execaudit Surface is only worth what its narrowest claim is:
//
//   - The adapter calls in runtime_docker.go are confined to a declared compose
//     project. These are not: the whole point of the panel is the container
//     nothing declared, and that is exactly the one a user needs to stop.
//   - The listings in inventory.go only ever read. These do not.
//
// What replaces the project bound is a different one, and it is the reason this
// file asks for a listing before it acts: an operation may only name a
// container the source is reporting right now. A caller cannot hand devhub an
// arbitrary string and have it reach a command line, cannot reach a source
// devhub does not know, and cannot act on something the panel never showed.
//
// The cost is one sweep of the machine per operation — the same listing the
// panel itself does, `colima list` plus a `ps` per source, run concurrently.
// That is the trade profile.go makes when it looks a VM up before touching it,
// and it is why control endpoints appear in the container-runtime and
// containers-list Surfaces as well as their own.
//
// Deliberately absent: anything that removes. No `rm`, no `prune`, no
// `system prune` — those destroy state that cannot be recovered by pressing the
// other button, and a panel that lists containers machine-wide is the worst
// possible place to put them.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// logsTimeout bounds a log read. It is short: `logs --tail N` on a running
// container returns what is already on disk and exits.
const logsTimeout = 20 * time.Second

// controlOpTimeout bounds a stop, a start or a restart. Long enough to cover a
// container that uses its full stop grace period and then some, short enough
// that a wedged daemon does not hold the request open indefinitely. A restart
// is one command, so this covers the whole of it.
const controlOpTimeout = 3 * time.Minute

// maxLogTail caps how much history one request may ask for. Logs go into a JSON
// response and then into a browser, and an unbounded tail on a chatty container
// is a way to make devhub the thing that falls over.
const maxLogTail = 2000

// defaultLogTail is what a request that does not say gets.
const defaultLogTail = 200

var (
	// ErrContainerMissing is an operation aimed at a container the source is
	// not reporting. It is a refusal, not a failure: nothing was attempted.
	ErrContainerMissing = errors.New("そのコンテナは見つかりません（一覧を再読込してください）")
	// ErrSourceMissing is an operation aimed at an engine devhub cannot see.
	ErrSourceMissing = errors.New("その実行基盤が見つかりません")
)

// containerIDRe is what may pass for a container ID. Docker and nerdctl both
// use hex, short (12) or full (64); this accepts any hex run in that range.
//
// The lookup below is the real bound — an ID that is not in the listing is
// refused whatever it looks like — but this runs first, so a malformed value
// never reaches a listing, a command line, or an engine. Anything that could
// pass for a flag fails it by construction: hex has no leading hyphen.
//
// A rejected value is quoted back to the caller that sent it, which is what
// makes the 404 diagnosable. It reaches no interpreter on the way: %q escapes
// it into the JSON body, and the panel renders errors with textContent.
var containerIDRe = regexp.MustCompile(`^[0-9a-fA-F]{12,64}$`)

// ValidContainerID reports whether s can name a container.
func ValidContainerID(s string) bool { return containerIDRe.MatchString(s) }

// Operator runs the operations the panel offers on one container.
// Implementations bound themselves, as everything else in this package does:
// the caller sets no deadline.
type Operator interface {
	// Logs returns the last tail lines. tail is clamped, not rejected: a
	// caller asking for more history than devhub will carry gets the most it
	// will, which is more useful than an error about a number.
	Logs(ctx context.Context, src Source, id string, tail int) (string, error)
	Stop(ctx context.Context, src Source, id string) error
	// Start is the way back from Stop, and the reason Stop can be offered at
	// all: a panel that only takes things down leaves the user in a terminal to
	// undo it. It cannot create anything — the resolve ahead of it only names a
	// container the engine is already reporting, which for a stopped one means
	// a container that still exists on disk.
	Start(ctx context.Context, src Source, id string) error
	Restart(ctx context.Context, src Source, id string) error
}

// controlRunner spawns the operations. Its own seam, so the execaudit Surface
// covering "devhub stopped a container it did not declare" holds exactly the
// calls that can do that, and nothing filed under containers-list ever can.
type controlRunner struct{}

func (controlRunner) Run(ctx context.Context, _, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //execaudit:containers-control
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// cliControl drives whichever CLI the source's engine needs. One type rather
// than two adapters, for the reason cliInventory is one: the engines differ
// here only in the argv.
type cliControl struct{ runner commandRunner }

func newCLIControl() *cliControl { return &cliControl{runner: controlRunner{}} }

// engineArgv builds the command for one source, given the subcommand and its
// arguments. Every operation in this file goes through it, so the containerd
// passthrough and the docker context selection are decided once.
func (c *cliControl) engineArgv(src Source, sub ...string) (string, []string) {
	if src.Engine == EngineContainerd {
		// containerd lives inside the profile's VM, reached the way the compose
		// adapter and the listing reach it: colima's nerdctl passthrough, with
		// `--` keeping colima's own flags apart from nerdctl's.
		return "colima", append([]string{"nerdctl", "--profile", src.Profile, "--"}, sub...)
	}
	var args []string
	if src.Context != "" {
		args = append(args, "--context", src.Context)
	}
	return "docker", append(args, sub...)
}

func (c *cliControl) Logs(ctx context.Context, src Source, id string, tail int) (string, error) {
	if tail <= 0 {
		tail = defaultLogTail
	}
	tail = min(tail, maxLogTail)

	ctx, cancel := context.WithTimeout(ctx, logsTimeout)
	defer cancel()

	name, args := c.engineArgv(src, "logs", "--tail", strconv.Itoa(tail), id)
	stdout, stderr, err := c.runner.Run(ctx, "", name, args...)
	if err != nil {
		return "", cliError(stderr, err)
	}
	// Docker writes a container's stderr to its own stderr, so a container that
	// only logs to stderr would otherwise come back empty. Both streams are the
	// container's output; devhub does not know which one the user wants.
	if stderr != "" {
		if stdout != "" {
			return stdout + "\n" + stderr, nil
		}
		return stderr, nil
	}
	return stdout, nil
}

func (c *cliControl) Stop(ctx context.Context, src Source, id string) error {
	return c.act(ctx, src, "stop", id)
}

func (c *cliControl) Start(ctx context.Context, src Source, id string) error {
	return c.act(ctx, src, "start", id)
}

func (c *cliControl) Restart(ctx context.Context, src Source, id string) error {
	return c.act(ctx, src, "restart", id)
}

func (c *cliControl) act(ctx context.Context, src Source, verb, id string) error {
	ctx, cancel := context.WithTimeout(ctx, controlOpTimeout)
	defer cancel()

	name, args := c.engineArgv(src, verb, id)
	if _, stderr, err := c.runner.Run(ctx, "", name, args...); err != nil {
		return cliError(stderr, err)
	}
	return nil
}

// ContainerTarget is one container an operation may address, resolved against
// what the machine is reporting rather than taken from the request.
type ContainerTarget struct {
	Source    Source
	Container Container
}

// ResolveContainer finds the container an operation names, or says why it will
// not run. It is the bound this file's Surface rests on: past here, the ID in
// the argv is one the engine itself just reported.
//
// One sweep, and the answer comes out of it. Containers already lists every
// source concurrently, folds a source that failed into its own Available and
// Reason, and collapses aliases so each container appears once under the source
// that owns it. Listing the target again would pay a second time for a result
// no more current than the one already in hand.
//
// Nothing here is cached. A container that exited a second ago is one the user
// should be told about rather than have devhub act on a stale row — and the
// panel's own view is a page load old by definition.
func (r *Runtime) ResolveContainer(ctx context.Context, sourceID, id string) (ContainerTarget, error) {
	if !ValidContainerID(id) {
		return ContainerTarget{}, fmt.Errorf("%w: %q", ErrContainerMissing, id)
	}
	sources, all := r.Containers(ctx)

	src, ok := findSource(sources, sourceID)
	if !ok {
		return ContainerTarget{}, ErrSourceMissing
	}
	// An alias's rows were folded onto the source it aliases, so that is where
	// they are. A bounded walk rather than recursion: nothing builds a cycle
	// today, and a lookup that spins because something started to is worse than
	// one that stops and reports.
	for hops := 0; src.AliasOf != "" && hops <= len(sources); hops++ {
		next, ok := findSource(sources, src.AliasOf)
		if !ok {
			break
		}
		src = next
	}
	if !src.Available {
		return ContainerTarget{}, fmt.Errorf("%w: %s", ErrSourceMissing, src.Reason)
	}

	var match *Container
	for i := range all {
		c := &all[i]
		if c.Source != src.ID || !sameContainerID(c.ID, id) {
			continue
		}
		// An ambiguous prefix is refused rather than resolved to whichever came
		// first. The point of resolving is that the operation acts on the
		// container the caller meant, and here devhub cannot tell which.
		if match != nil {
			return ContainerTarget{}, fmt.Errorf("%w: %q は複数のコンテナに一致します", ErrContainerMissing, id)
		}
		match = c
	}
	if match == nil {
		return ContainerTarget{}, ErrContainerMissing
	}
	return ContainerTarget{Source: src, Container: *match}, nil
}

func findSource(sources []Source, id string) (Source, bool) {
	for _, s := range sources {
		if s.ID == id {
			return s, true
		}
	}
	return Source{}, false
}

// sameContainerID reports whether two IDs name one container. `docker ps`
// prints twelve hex digits and `--no-trunc` prints sixty-four, so which form a
// caller holds depends on where they read it; the short one is a prefix of the
// long one. Comparing for equality alone would mean the panel is the only
// client that can drive this API, which is the opposite of the point.
func sameContainerID(listed, asked string) bool {
	a, b := strings.ToLower(listed), strings.ToLower(asked)
	if len(a) > len(b) {
		a, b = b, a
	}
	return a != "" && strings.HasPrefix(b, a)
}
