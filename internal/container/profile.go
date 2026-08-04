package container

// Profile lifecycle: the one place in devhub that acts on a Colima VM rather
// than only reading it.
//
// The rule everywhere else in this package is that devhub never starts, stops
// or reconfigures a profile (plan §13). That rule still holds where it was
// aimed: nothing devhub does *on its own* moves a VM. A switch does not, a
// status read does not, a page load does not — those all continue to report a
// stopped profile and hand the user the command. What is new is an explicit
// door, a request whose entire purpose is to create or resize a profile, and
// nothing walks through it as a side effect of something else.
//
// The distinction matters because the two operations here are not equally
// dangerous:
//
//   - Create makes a VM that did not exist. There is nothing on it to lose, so
//     the blast radius is zero.
//   - Resize cannot be done to a running VM — Colima applies sizes at start —
//     so it means stop and start, and every container in that VM goes down,
//     including any belonging to environments that merely share the profile.
//     Callers are expected to show the user what will stop before asking.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/platform"
)

// profileOpTimeout bounds one create or resize. It is long because a first
// start downloads a VM image and boots it, and the operation is synchronous for
// the same reason `compose up --wait` is: the exit status is the only thing
// that tells the caller the VM actually came up.
const profileOpTimeout = 10 * time.Minute

// profileNameRe is the accepted profile name. It lives here because this
// package owns the concept: a second spelling elsewhere is how the two drift
// apart, and the env schema calls ValidProfileName rather than keeping its own.
//
// The name is passed as an argv element to colima, which is why the first
// character may not be "-". An alphanumeric-plus-dash rule alone accepts "--rm"
// and "-f": here they would only become an oddly named profile, since the
// preceding --profile consumes the next token whatever it looks like, but a
// value that can pass for a flag has no business being carried to a command
// line at all — and the rule outlives the one call site that is safe today.
var profileNameRe = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]*$`)

// ValidProfileName reports whether s can name a Colima profile.
func ValidProfileName(s string) bool { return profileNameRe.MatchString(s) }

var (
	// ErrProfileExists refuses to treat a create as a resize. They differ in
	// exactly the way that matters — one can destroy running containers — so
	// devhub will not silently upgrade one into the other.
	ErrProfileExists = errors.New("その名前の Colima profile は既にあります")
	// ErrProfileMissing is a resize aimed at nothing.
	ErrProfileMissing = errors.New("その Colima profile はありません")
	// ErrDiskShrink is refused outright. Colima cannot shrink a disk in place;
	// doing it means recreating the VM, which destroys every image and
	// container on it, and unlike a stop that is not something the user can
	// undo by starting the profile again.
	ErrDiskShrink = errors.New("ディスクの縮小はできません（VM の作り直しになり、イメージとコンテナが失われます）")
)

// ProfileSpec is a profile devhub is being asked to create or resize. Sizes are
// in the units colima's own flags take: whole CPUs, GiB of memory, GiB of disk.
// A zero means "leave colima's default", which only makes sense on create.
type ProfileSpec struct {
	Name      string
	CPUs      int
	MemoryGiB int
	DiskGiB   int
	Engine    string // EngineDocker | EngineContainerd; "" means colima's default
}

// ProfileManager creates and resizes Colima profiles. It is an interface for
// the same reason every other seam here is: so tests can assert the argv
// without a machine that boots a VM in response.
type ProfileManager interface {
	Create(ctx context.Context, spec ProfileSpec) error
	Resize(ctx context.Context, spec ProfileSpec) error
}

// adminRunner spawns the two commands that move a VM. It is its own seam, not
// execRunner and not inventoryRunner, because the execaudit Surface is the
// point: those two only ever read, or only ever act inside a declared compose
// project, and this one starts and stops virtual machines. One marker per set
// of bounds is what keeps each claim in the ledger true of everything filed
// under it.
type adminRunner struct{}

func (adminRunner) Run(ctx context.Context, _, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //execaudit:colima-profile
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// colimaAdmin drives `colima start` and `colima stop`.
type colimaAdmin struct {
	runner   commandRunner
	lookPath func(string) (string, error)
	darwin   bool
	// profiles reads the current state. Both operations need it: create must
	// know the name is free, resize must know the profile exists and how big it
	// is now.
	profiles ProfileLister
}

func newColimaAdmin(profiles ProfileLister) *colimaAdmin {
	return &colimaAdmin{
		runner: adminRunner{}, lookPath: exec.LookPath,
		darwin: platform.IsDarwin(), profiles: profiles,
	}
}

// Create brings up a new profile at the requested size. It refuses a name that
// is already taken rather than resizing it: the caller asked for a new VM, and
// quietly restarting an existing one would stop containers nobody mentioned.
func (a *colimaAdmin) Create(ctx context.Context, spec ProfileSpec) error {
	if err := a.check(spec); err != nil {
		return err
	}
	if _, found, err := a.find(ctx, spec.Name); err != nil {
		return err
	} else if found {
		return ErrProfileExists
	}
	return a.start(ctx, spec)
}

// Resize applies a new size to an existing profile. Colima only reads sizes at
// start, so this stops the VM first — which is why the caller is expected to
// have told the user which containers that takes down.
//
// A disk smaller than the current one is refused before anything is stopped:
// that path recreates the VM, and no amount of starting it again brings the
// images back.
func (a *colimaAdmin) Resize(ctx context.Context, spec ProfileSpec) error {
	if err := a.check(spec); err != nil {
		return err
	}
	current, found, err := a.find(ctx, spec.Name)
	if err != nil {
		return err
	}
	if !found {
		return ErrProfileMissing
	}
	if spec.DiskGiB > 0 && current.DiskBytes > 0 && int64(spec.DiskGiB)*gib < current.DiskBytes {
		return fmt.Errorf("%w（現在 %d GiB）", ErrDiskShrink, current.DiskBytes/gib)
	}

	if current.running() {
		if err := a.stop(ctx, spec.Name); err != nil {
			return err
		}
	}
	return a.start(ctx, spec)
}

const gib = 1 << 30

// check validates what devhub can validate before spawning anything: the name
// (which becomes an argv element) and the engine (which must be one devhub has
// an adapter for, so a profile is never created that devhub then cannot drive).
func (a *colimaAdmin) check(spec ProfileSpec) error {
	if !a.darwin {
		return ErrColimaUnsupportedOS
	}
	if !ValidProfileName(spec.Name) {
		return fmt.Errorf("profile 名は英数字と _ - のみです: %q", spec.Name)
	}
	if spec.Engine != "" {
		if supported, reason := engineSupport(spec.Engine); !supported {
			return errors.New(reason)
		}
	}
	if spec.CPUs < 0 || spec.MemoryGiB < 0 || spec.DiskGiB < 0 {
		return errors.New("CPU・メモリ・ディスクは 0 以上で指定してください")
	}
	if _, err := a.lookPath("colima"); err != nil {
		return ErrColimaMissing
	}
	return nil
}

// find looks the profile up by name in colima's own listing rather than
// trusting the caller, so "does this exist" and "how big is it now" are both
// answered by the same source that the panel displays.
func (a *colimaAdmin) find(ctx context.Context, name string) (ColimaProfile, bool, error) {
	list, err := a.profiles.Profiles(ctx)
	if err != nil {
		return ColimaProfile{}, false, err
	}
	for _, p := range list {
		if p.Name == name {
			return p, true, nil
		}
	}
	return ColimaProfile{}, false, nil
}

// start runs `colima start` with the requested size. Sizes are only passed when
// asked for: omitting a flag leaves colima's own value, which on a resize means
// the profile keeps what it had.
func (a *colimaAdmin) start(ctx context.Context, spec ProfileSpec) error {
	args := []string{"start", "--profile", spec.Name}
	if spec.CPUs > 0 {
		// `--cpus`, not `--cpu`: colima also has `--cpu-type`, so the short
		// spelling is ambiguous and rejected.
		args = append(args, "--cpus", strconv.Itoa(spec.CPUs))
	}
	if spec.MemoryGiB > 0 {
		args = append(args, "--memory", strconv.Itoa(spec.MemoryGiB))
	}
	if spec.DiskGiB > 0 {
		args = append(args, "--disk", strconv.Itoa(spec.DiskGiB))
	}
	if spec.Engine != "" {
		args = append(args, "--runtime", spec.Engine)
	}
	return a.run(ctx, args...)
}

// stop shuts the VM down gracefully. --force is deliberately not passed: it
// skips the guest's shutdown, and the whole point of showing the user what will
// stop is that those containers get a chance to exit cleanly.
func (a *colimaAdmin) stop(ctx context.Context, name string) error {
	return a.run(ctx, "stop", "--profile", name)
}

func (a *colimaAdmin) run(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, profileOpTimeout)
	defer cancel()
	_, stderr, err := a.runner.Run(ctx, "", "colima", args...)
	if err != nil {
		return cliError(stderr, err)
	}
	return nil
}

// ProfileTargets names the containers a resize would take down, so the caller
// can show them before asking. It is a read: nothing here stops anything.
//
// The list is what makes the shared-profile case visible. Two environments can
// name the same profile, and resizing for one of them stops the other's
// containers — a consequence the user cannot infer from the environment they
// happen to be looking at.
func (r *Runtime) ProfileTargets(ctx context.Context, name string) ([]Container, error) {
	list, err := r.Colima.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.Name != name {
			continue
		}
		if !p.running() {
			return nil, nil // nothing is up, so nothing goes down
		}
		return r.Inventory.List(ctx, colimaSource(Profile{
			Name: p.Name, Status: p.Status, Engine: p.Engine,
			Context: colimaDockerContext(p.Name), Supported: true,
		}))
	}
	return nil, ErrProfileMissing
}

// describeSpec renders a spec the way colima's own flags read, for messages and
// for the confirmation the panel shows.
func describeSpec(spec ProfileSpec) string {
	var bits []string
	if spec.CPUs > 0 {
		bits = append(bits, fmt.Sprintf("%d CPU", spec.CPUs))
	}
	if spec.MemoryGiB > 0 {
		bits = append(bits, fmt.Sprintf("mem %d GiB", spec.MemoryGiB))
	}
	if spec.DiskGiB > 0 {
		bits = append(bits, fmt.Sprintf("disk %d GiB", spec.DiskGiB))
	}
	if spec.Engine != "" {
		bits = append(bits, spec.Engine)
	}
	if len(bits) == 0 {
		return "colima のデフォルト"
	}
	return strings.Join(bits, " · ")
}
