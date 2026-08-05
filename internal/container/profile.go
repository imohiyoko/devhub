package container

// Profile lifecycle: the one place in devhub that acts on a Colima VM rather
// than only reading it.
//
// The rule everywhere else in this package is that devhub never starts, stops
// or reconfigures a profile (plan §13). That rule still holds where it was
// aimed: nothing devhub does *on its own* moves a VM. A switch does not, a
// status read does not, a page load does not — those all continue to report a
// stopped profile and hand the user the command. What is new is an explicit
// door, a request whose entire purpose is to move a profile, and nothing walks
// through it as a side effect of something else.
//
// The distinction matters because the operations here are not equally
// dangerous:
//
//   - Create makes a VM that did not exist, and Start brings an existing one
//     back up. There is nothing on either to lose, so the blast radius is zero.
//   - Stop takes down every container in the VM, including any belonging to
//     environments that merely share the profile. Callers are expected to show
//     the user what will stop before asking. It is recoverable — Start is the
//     way back, which is the reason it exists.
//   - Resize cannot be done to a running VM — Colima applies sizes at start —
//     so it is a Stop and a Start with the same blast radius as the Stop, and
//     the same expectation of the caller.

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

	"github.com/imohiyoko/devhub/internal/hostspec"
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
	// ErrProfileMissing is an operation aimed at nothing. Start refuses with it
	// rather than creating the profile: Create is the door for a VM that does
	// not exist, and it takes a size.
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
	// ErrOverHostCapacity is a size this machine cannot back. It is a refusal
	// that only exists because devhub can now see the host (internal/hostspec);
	// before that it passed the request through and colima failed — which for a
	// resize means failing after the stop, leaving the VM down with neither the
	// old size nor the new one.
	//
	// Disk is deliberately not part of it. Lima's images are sparse, so a
	// profile declaring more disk than the volume has free is not a mistake.
	ErrOverHostCapacity = errors.New("この Mac の容量を超えています")
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

// ProfileManager creates, resizes, starts and stops Colima profiles. It is an
// interface for the same reason every other seam here is: so tests can assert
// the argv without a machine that boots a VM in response.
type ProfileManager interface {
	Create(ctx context.Context, spec ProfileSpec) error
	Resize(ctx context.Context, spec ProfileSpec) error
	// CheckResize answers whether a resize would be allowed, without doing it.
	// It exists so a caller can refuse before asking the user to agree to
	// anything: being told "the disk cannot shrink" after consenting to stop a
	// VM full of containers is a worse sequence than being told first.
	CheckResize(ctx context.Context, spec ProfileSpec) error
	// Start brings an existing profile up at the size it already has. No spec,
	// deliberately: a start is not a place to change anything, and colima keeps
	// each profile's configuration, so passing no size flags is what makes the
	// VM come back as it was rather than as whatever a caller happened to send.
	Start(ctx context.Context, name string) error
	// Stop shuts a profile down. Every container in the VM goes with it —
	// including containers belonging to environments that merely share the
	// profile — so callers are expected to show the user that list first, the
	// same way they do for a resize.
	Stop(ctx context.Context, name string) error
	// Limits reports what the host has and what devhub will let one VM take, so
	// a caller can show the cap before someone runs into it. It asks colima
	// nothing, so it costs nothing to call on a path that has already listed.
	Limits() Limits
	// Allocations reports the size each profile was given. Separate from Limits
	// because it needs a `colima list` and Limits does not — a caller that only
	// wants the cap should not pay for a sweep of the machine.
	Allocations(ctx context.Context) ([]Alloc, error)
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
	// host and reserve are the two halves of the capacity cap, kept apart
	// because one is a fact and the other is a policy. host is what the machine
	// physically has; reserve is how much of it the user wants left alone.
	// Injected the way lookPath and darwin are, so a test can state a host
	// without one.
	//
	// Both are funcs rather than values: the reserve is a live setting a user
	// can change between two requests, and reading it at construction would
	// mean the cap in force is the one that existed when devhub booted.
	host    func() hostspec.Spec
	reserve func() Reserve
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

func newColimaAdmin(profiles ProfileLister, reserve func() Reserve) *colimaAdmin {
	if reserve == nil {
		reserve = DefaultReserve
	}
	return &colimaAdmin{
		runner: adminRunner{}, lookPath: exec.LookPath,
		darwin: platform.IsDarwin(), profiles: profiles,
		// Detected once. The machine's cores and memory do not change while
		// devhub is running, and re-running the syscalls per request would buy
		// nothing but a chance to disagree with the figure already on screen.
		host:    sync.OnceValue(func() hostspec.Spec { return hostspec.Detect(hostspec.ColimaDir()) }),
		reserve: reserve,
	}
}

// Create brings up a new profile at the requested size. It refuses a name that
// is already taken rather than resizing it: the caller asked for a new VM, and
// quietly restarting an existing one would stop containers nobody mentioned.
func (a *colimaAdmin) Create(ctx context.Context, spec ProfileSpec) error {
	if err := a.check(spec); err != nil {
		return err
	}
	if err := a.checkCapacity(spec.CPUs, spec.MemoryGiB); err != nil {
		return err
	}
	defer a.lock(spec.Name)()
	if _, found, err := a.find(ctx, spec.Name); err != nil {
		return err
	} else if found {
		return ErrProfileExists
	}
	return a.runStart(ctx, spec)
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
		if err := a.runStop(ctx, spec.Name); err != nil {
			return err
		}
	}
	if err := a.runStart(ctx, spec); err != nil {
		// The stop already happened, so this is the state the user is left in:
		// nothing running, and the new size not applied. Whatever colima said
		// about why, that is the part they need, along with the way back.
		return fmt.Errorf("%w\n（%s への変更に失敗しました。VM は停止しています。`colima start --profile %s` で起動を再試行できます）",
			err, describeSpec(spec), spec.Name)
	}
	return nil
}

// Limits is what the host has and what devhub will let one VM take. It is
// derived from syscalls and a setting — nothing in it comes from colima — which
// is why it is answerable without asking anything and why a caller can hold it
// while colima is busy.
//
// Detected false means devhub cannot see this host, and every number below it
// is meaningless. Callers show nothing rather than showing zeros, and no cap is
// enforced.
type Limits struct {
	Detected      bool
	HostCPUs      int
	HostMemBytes  int64
	FreeDiskBytes int64
	CPUCap        int
	MemCapGiB     int
	Reserve       Reserve
}

// Alloc is one profile and the size it was given. Reported per profile rather
// than pre-totalled because callers want different sums: the panel wants what
// is running, and a start wants that plus the one profile it is about to bring
// up.
type Alloc struct {
	Name    string
	CPUs    int
	MemGiB  int
	Running bool
}

// Limits reports the caps. No context, because there is nothing to cancel.
func (a *colimaAdmin) Limits() Limits {
	spec := a.hostSpec()
	res := a.reserveOrDefault()
	l := Limits{
		Detected: spec.Detected, HostCPUs: spec.CPUs, HostMemBytes: spec.MemoryBytes,
		FreeDiskBytes: spec.FreeDiskBytes, Reserve: res,
	}
	if spec.Detected {
		l.CPUCap = res.CPUCap(spec.CPUs)
		l.MemCapGiB = res.MemoryCapGiB(spec.MemoryBytes)
	}
	return l
}

// Allocations reports what each profile was given. One `colima list`, and the
// caller decides what to add up.
func (a *colimaAdmin) Allocations(ctx context.Context) ([]Alloc, error) {
	list, err := a.profiles.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Alloc, 0, len(list))
	for _, p := range list {
		out = append(out, Alloc{
			Name: p.Name, CPUs: p.CPUs,
			MemGiB: int(p.MemoryBytes / gibDivisor), Running: p.running(),
		})
	}
	return out, nil
}

func (a *colimaAdmin) hostSpec() hostspec.Spec {
	if a.host == nil {
		return hostspec.Spec{}
	}
	return a.host()
}

func (a *colimaAdmin) reserveOrDefault() Reserve {
	if a.reserve == nil {
		return DefaultReserve()
	}
	return a.reserve()
}

// checkCapacity refuses a size this machine cannot back. cpus and memGiB are
// what the VM would end up with; zero means "not being set", which on a create
// leaves colima's own default and on a start means the value was not reported.
//
// Deliberately not called from check(). check() also gates Stop, and a cap that
// blocked stopping an over-sized VM would be the worst possible failure: the
// user would be unable to reclaim the very memory the cap is protecting.
func (a *colimaAdmin) checkCapacity(cpus, memGiB int) error {
	b := a.Limits()
	if !b.Detected {
		// devhub cannot see this host, so it has nothing to refuse with. The
		// absolute limits in check() still apply.
		return nil
	}
	var over []string
	if cpus > b.CPUCap {
		over = append(over, fmt.Sprintf("CPU %d（上限 %d / 実装 %d・予約 %s）",
			cpus, b.CPUCap, b.HostCPUs, b.Reserve.CPU.describe("コア")))
	}
	if memGiB > b.MemCapGiB {
		over = append(over, fmt.Sprintf("メモリ %d GiB（上限 %d GiB / 実装 %d GiB・予約 %s）",
			memGiB, b.MemCapGiB, b.HostMemBytes/gibDivisor, b.Reserve.Memory.describe(" GiB")))
	}
	if len(over) == 0 {
		return nil
	}
	// Both ways out, named. Raising the reserve can put an existing VM out of
	// reach of this panel, and a refusal that does not say how to undo itself
	// leaves the user stuck with a machine they can see and cannot start.
	return fmt.Errorf("%w: %s\n（設定の予約を減らすか、端末の `colima start` を使ってください）",
		ErrOverHostCapacity, strings.Join(over, " / "))
}

// CheckResize runs every refusal Resize would, and nothing else.
func (a *colimaAdmin) CheckResize(ctx context.Context, spec ProfileSpec) error {
	_, err := a.checkResize(ctx, spec)
	return err
}

// Start brings an existing profile up. It passes no size flags at all, which is
// what makes it a start rather than a resize: colima keeps each profile's
// configuration, so an omitted flag means "the value this profile already has".
//
// A profile that is already running is left alone rather than restarted. The
// caller asked for it to be up, it is up, and `colima start` on a running VM is
// not free — treating this as "make it so" is the reading that cannot surprise
// anyone with a stop they did not ask for.
func (a *colimaAdmin) Start(ctx context.Context, name string) error {
	spec := ProfileSpec{Name: name}
	if err := a.check(spec); err != nil {
		return err
	}
	defer a.lock(name)()

	current, found, err := a.find(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		// Refused rather than created. Create is the door for a VM that does not
		// exist, and it takes a size; starting a name that happens to be free
		// would make a default-sized VM out of a typo.
		return ErrProfileMissing
	}
	if current.running() {
		return nil
	}
	// The size to judge is the profile's own, not the spec's — a start carries
	// no sizes. This is the check the user asked for by name: a VM whose
	// declared size no longer fits inside the cap does not come up from here.
	//
	// It runs after the "already running" answer above, so raising the reserve
	// can never make a running VM look like something devhub must refuse.
	if err := a.checkCapacity(current.CPUs, int(current.MemoryBytes/gibDivisor)); err != nil {
		return err
	}
	return a.runStart(ctx, spec)
}

// Stop shuts a profile down, taking every container in it with it. Callers are
// expected to have shown the user that list — Runtime.ProfileTargets answers
// it — for the same reason a resize does: the containers that go down may
// belong to an environment that merely shares the profile and is nowhere on the
// screen the user is looking at.
//
// Unlike a resize this is recoverable by pressing the other button, which is
// why there is no refusal here beyond "that profile does not exist": the VM's
// disk, images and containers all survive a stop.
func (a *colimaAdmin) Stop(ctx context.Context, name string) error {
	if err := a.check(ProfileSpec{Name: name}); err != nil {
		return err
	}
	defer a.lock(name)()

	current, found, err := a.find(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		return ErrProfileMissing
	}
	if !current.running() {
		// Already where the caller wanted it. Spawning `colima stop` against a
		// stopped VM would only turn a satisfied request into an error message
		// about a state nobody needs to fix.
		return nil
	}
	return a.runStop(ctx, name)
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
	// The size to judge is what the VM would end up with, not what the request
	// named. An omitted size on a resize means "keep what this profile has" —
	// the flag is simply not passed to `colima start` — so judging the request
	// alone would let a 64 GiB VM be restarted at 64 GiB by asking only for a
	// CPU change. Start refuses that same profile by its own size; the two
	// paths have to agree.
	//
	// Still before the stop: Resize calls this first, and nothing below it runs
	// a command. A resize that could never work must not get as far as taking
	// the VM down.
	if err := a.checkCapacity(sizeOr(spec.CPUs, current.CPUs),
		sizeOr(spec.MemoryGiB, int(current.MemoryBytes/gibDivisor))); err != nil {
		return current, err
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

// sizeOr resolves what a resize would leave in place. Zero in a spec means the
// flag is omitted, and colima then keeps the profile's own value — so the
// current one is the honest answer, not zero.
//
// A current of zero (colima did not report it) yields zero, which checkCapacity
// reads as "not being set" and lets through. That is the right way to fail
// here: this refusal is recoverable by starting the profile again, unlike the
// disk shrink above, which refuses outright when the current size is unknown.
func sizeOr(asked, current int) int {
	if asked > 0 {
		return asked
	}
	return current
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

// runStart runs `colima start` with the requested size. Sizes are only passed
// when asked for: omitting a flag leaves colima's own value, which on a resize
// means the profile keeps what it had.
//
// Named apart from the exported Start so the two cannot be confused at a
// glance: this one takes a spec and does no checking, Start takes a name and
// does all of it.
func (a *colimaAdmin) runStart(ctx context.Context, spec ProfileSpec) error {
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

// runStop shuts the VM down gracefully. --force is deliberately not passed: it
// skips the guest's shutdown, and the whole point of showing the user what will
// stop is that those containers get a chance to exit cleanly.
func (a *colimaAdmin) runStop(ctx context.Context, name string) error {
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
// It lists everything in the VM, not only what some definition declared, and
// that is what makes the cost visible: a resize stops the profile, so a
// container devhub never declared goes down with the rest. Two environments
// naming the same profile is one instance of that, and the one no definition
// reveals on its own.
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
