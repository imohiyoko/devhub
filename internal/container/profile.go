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
	"sync"
	"time"

	"github.com/imohiyoko/devhub/internal/platform"
)

// profileOpTimeout bounds one colima invocation, not one request: a resize runs
// stop and then start, so it can take up to twice this. Per command rather than
// per operation because the budget is about how long a single colima call may
// hang, and a stop that is still going is not evidence the start will be.
//
// It is long because a first start downloads a VM image and boots it, and the
// operation is synchronous for the same reason `compose up --wait` is: the exit
// status is the only thing that tells the caller the VM actually came up.
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
	// ErrEngineChange refuses to let a resize become an engine swap. The engine
	// is not a size: the containers on the VM live in the runtime's own store,
	// so starting with the other runtime leaves them undeleted and invisible.
	// That is the one outcome the confirmation cannot describe — the stop list
	// names what goes down, and these do not go down, they stop existing as far
	// as anything asking can tell.
	ErrEngineChange = errors.New("engine の変更はできません（profile を作り直してください）")
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
	// CheckResize answers whether a resize would be allowed, without doing it.
	// It exists so a caller can refuse before asking the user to agree to
	// anything: being told "the disk cannot shrink" after consenting to stop a
	// VM full of containers is a worse sequence than being told first.
	CheckResize(ctx context.Context, spec ProfileSpec) error
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
	// locks serialises operations naming the same profile. Without it two
	// requests — two tabs, or an agent and a browser — both pass "does this
	// exist" before either has started anything, and both then run colima
	// against one VM. The check is only worth what the window after it is.
	//
	// Per name, not one lock for everything: an operation here can run for
	// minutes, and two different profiles have nothing to do with each other.
	locks sync.Map // name -> *sync.Mutex
}

// lock takes the named profile's lock and returns the release.
//
// The map keeps an entry per name it has seen. Callers validate the name first,
// so nothing that could not be a profile gets one — but a well-formed name that
// turns out not to exist still does, because the lock has to be held across the
// lookup that discovers that. Moving the check earlier would close the window
// the lock exists for. Entries are a few bytes each behind the API token, which
// is a better trade than the race.
func (a *colimaAdmin) lock(name string) func() {
	v, _ := a.locks.LoadOrStore(name, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
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
	defer a.lock(spec.Name)()
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
	// Validated before the lock so a bad name never mints one, and re-validated
	// inside checkResize, which is the function that must not be bypassable.
	if err := a.check(spec); err != nil {
		return err
	}
	defer a.lock(spec.Name)()

	current, err := a.checkResize(ctx, spec)
	if err != nil {
		return err
	}
	if current.running() {
		if err := a.stop(ctx, spec.Name); err != nil {
			return err
		}
	}
	if err := a.start(ctx, spec); err != nil {
		// The stop already happened, so this is the state the user is left in:
		// nothing running, and the new size not applied. Whatever colima said
		// about why, that is the part they need, along with the way back.
		return fmt.Errorf("%w\n（%s への変更に失敗しました。VM は停止しています。`colima start --profile %s` で起動を再試行できます）",
			err, describeSpec(spec), spec.Name)
	}
	return nil
}

// CheckResize runs every refusal Resize would, and nothing else.
func (a *colimaAdmin) CheckResize(ctx context.Context, spec ProfileSpec) error {
	_, err := a.checkResize(ctx, spec)
	return err
}

// checkResize returns the profile as it stands, or the reason the resize is
// refused. Resize and CheckResize share it so the two can never disagree about
// what is allowed — which is the whole point of offering a dry run.
func (a *colimaAdmin) checkResize(ctx context.Context, spec ProfileSpec) (ColimaProfile, error) {
	if err := a.check(spec); err != nil {
		return ColimaProfile{}, err
	}
	current, found, err := a.find(ctx, spec.Name)
	if err != nil {
		return ColimaProfile{}, err
	}
	if !found {
		return ColimaProfile{}, ErrProfileMissing
	}
	if spec.DiskGiB > 0 {
		// Refused when the current size is unknown, the same way the engine is
		// below. Colima does report a stopped profile's disk today, so this is
		// a narrow case — but it is the wrong one to leave open by default:
		// among everything refused here, only a disk shrink cannot be undone by
		// starting the profile again, because it recreates the VM.
		if current.DiskBytes == 0 {
			return current, fmt.Errorf("%w（現在のディスクサイズを判定できません）", ErrDiskShrink)
		}
		if int64(spec.DiskGiB)*gib < current.DiskBytes {
			return current, fmt.Errorf("%w（現在 %d GiB）", ErrDiskShrink, current.DiskBytes/gib)
		}
	}
	// The panel never asks for this — its request body carries an engine only
	// on a create — but the endpoint is reachable without the panel, and the
	// engine reaches `colima start --runtime` from the same spec the sizes do.
	// Refused here, with the disk shrink, because both have to land before the
	// stop: after it, the VM is already down.
	if spec.Engine != "" && !strings.EqualFold(spec.Engine, current.Engine) {
		have := current.Engine
		if have == "" {
			// A stopped profile reports no engine (plan §6.4), so devhub cannot
			// tell a change from a restatement. It refuses rather than guess:
			// guessing wrong here is the swap this error exists to prevent.
			have = "不明（停止中の profile では colima が報告しません）"
		}
		return current, fmt.Errorf("%w（現在 %s）", ErrEngineChange, have)
	}
	return current, nil
}

const gib = 1 << 30

// Upper bounds on a requested size. These are not colima's limits — devhub does
// not know what the host has — and they are not trying to be: they are the
// values no machine could satisfy, and what matters is where the refusal lands.
//
// A resize that colima rejects has already had its stop run, so the VM is down
// by the time the answer arrives. A number that was never going to work must
// therefore be turned away before anything moves. This does not make every
// colima refusal land early — asking a 16 GiB Mac for 64 GiB still fails at
// colima — which is why Resize's error says how to bring the VM back. It closes
// the case where devhub could have known without asking.
//
// maxDiskGiB also keeps DiskGiB*gib inside int64: past roughly 8.6e9 the
// product wraps negative and the shrink check refuses for a reason that is not
// the true one.
const (
	maxCPUs      = 1024
	maxMemoryGiB = 4096
	maxDiskGiB   = 65536
)

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
	if spec.CPUs > maxCPUs || spec.MemoryGiB > maxMemoryGiB || spec.DiskGiB > maxDiskGiB {
		return fmt.Errorf("サイズが大きすぎます（CPU %d 以下、メモリ %d GiB 以下、ディスク %d GiB 以下）",
			maxCPUs, maxMemoryGiB, maxDiskGiB)
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

// describeSpec renders a spec the way colima's own flags read, for the one
// error that has to say which change failed. The panel builds its own rendering
// from the JSON — this is for the message, not for the screen.
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
