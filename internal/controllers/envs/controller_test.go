package envs

// Characterization tests for the Controller: they pin the current observable
// behavior of the launch/stop/status/registry paths against in-memory fakes
// (no SQLite, no lsof, no terminal spawn), so the upcoming responsibility
// split can be verified behavior-preserving. The fakes mirror the narrow
// interfaces the controller depends on (launchStore, gitService, portsService,
// workspaceService); process spawning is captured through the `start` seam in
// terminal.go.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
	"github.com/imohiyoko/devhub/internal/platform"
)

// --- fakes ---

// fakeStore is a map-based, SQLite-free launchStore. Loads JSON-round-trip the
// stored documents so values come back production-shaped (numbers as float64,
// fresh maps a caller may annotate without corrupting the fixture), mirroring
// the real store's decode-on-load.
type fakeStore struct {
	envs     map[string]any
	launches []any
}

func jsonRoundTrip(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *fakeStore) LoadEnvs() (map[string]any, error) { return jsonRoundTrip(f.envs) }

func (f *fakeStore) SaveEnvs(data map[string]any) error {
	f.envs = data
	return nil
}

func (f *fakeStore) LoadLaunches() (map[string]any, error) {
	return jsonRoundTrip(map[string]any{"launches": f.launches})
}

func (f *fakeStore) AppendLaunch(record map[string]any) error {
	// Mirrors the real store's guard: launch_id is the primary key.
	if id, _ := record["launch_id"].(string); id == "" {
		return errors.New("launch record needs a launch_id")
	}
	f.launches = append(f.launches, record)
	return nil
}

func (f *fakeStore) RemoveLaunch(launchID string) error {
	// Mirrors the real single-row DELETE: a missing id is a no-op.
	kept := f.launches[:0]
	for _, l := range f.launches {
		if m, ok := l.(map[string]any); ok && pStr(m, "launch_id") == launchID {
			continue
		}
		kept = append(kept, l)
	}
	f.launches = kept
	return nil
}

func (f *fakeStore) LoadSettings() (map[string]any, error) {
	// No terminal config: openInTerminal takes the emulator-less runShell path.
	return map[string]any{}, nil
}

type fakeGit struct {
	repos     []gitctl.Repo
	worktrees map[string][]map[string]any // repo path -> `git worktree list` rows
}

func (f *fakeGit) AllRepos() []gitctl.Repo { return f.repos }

func (f *fakeGit) ListWorktrees(repoPath string) ([]map[string]any, error) {
	wts, ok := f.worktrees[repoPath]
	if !ok {
		return nil, errors.New("not a repo")
	}
	return wts, nil
}

type killCall struct{ port, pid int }

type fakePorts struct {
	open    []portsctl.PortEntry
	killErr map[int]error // port -> error KillPortProcess should return
	kills   []killCall
}

func (f *fakePorts) ListOpen() ([]portsctl.PortEntry, error) { return f.open, nil }

func (f *fakePorts) KillPortProcess(port, pid int) error {
	f.kills = append(f.kills, killCall{port, pid})
	return f.killErr[port]
}

// fakeCompose answers compose probes without Docker. Every controller under
// test gets one, so no test can reach the real `docker` binary.
type fakeCompose struct {
	states map[string]map[string]componentState // project -> service -> state
	err    error
	// unavailable is the reason Available reports; nil means Docker is present.
	unavailable error
	calls       []composeSpec
	// runtimes records the runtime of every operation, so a test can assert
	// devhub addressed the engine the environment declared. Turning it into an
	// argv is each adapter's own job and is tested there.
	runtimes []runtimeSpec
	// ups/stops record what apply operated on as "<project>/<services>", so a
	// test can assert both the operation and the scope it was confined to;
	// upErr/stopErr make those operations fail.
	ups     []string
	stops   []string
	upErr   error
	stopErr error
}

func (f *fakeCompose) Available(context.Context) error { return f.unavailable }

func (f *fakeCompose) ServiceStates(_ context.Context, rt runtimeSpec, spec composeSpec) (map[string]componentState, error) {
	f.calls = append(f.calls, spec)
	f.runtimes = append(f.runtimes, rt)
	if f.err != nil {
		return nil, f.err
	}
	return f.states[spec.Project], nil
}

func (f *fakeCompose) Up(_ context.Context, rt runtimeSpec, spec composeSpec) error {
	f.ups = append(f.ups, spec.Project+"/"+strings.Join(spec.Services, ","))
	f.runtimes = append(f.runtimes, rt)
	return f.upErr
}

func (f *fakeCompose) Stop(_ context.Context, rt runtimeSpec, spec composeSpec) error {
	f.stops = append(f.stops, spec.Project+"/"+strings.Join(spec.Services, ","))
	f.runtimes = append(f.runtimes, rt)
	return f.stopErr
}

type fakeWorkspace struct{ opened []string }

func (f *fakeWorkspace) OpenInEditor(path string) { f.opened = append(f.opened, path) }

// spawnLog collects the commands the controller would have spawned. Each
// record also feeds a buffered channel so tests of the async HTTP launch path
// can wait for the goroutine's spawns instead of sleeping.
type spawnLog struct {
	mu   sync.Mutex
	cmds []*exec.Cmd
	ch   chan *exec.Cmd
	// failOn makes record report a failure for commands whose argv contains
	// this substring — how a test simulates a terminal that will not start.
	// Set it before triggering the launch.
	failOn string
}

func (l *spawnLog) record(cmd *exec.Cmd) error {
	l.mu.Lock()
	l.cmds = append(l.cmds, cmd)
	l.mu.Unlock()
	l.ch <- cmd
	if l.failOn != "" && strings.Contains(spawnedCommandLine(cmd), l.failOn) {
		return errors.New("terminal did not start")
	}
	return nil
}

func (l *spawnLog) all() []*exec.Cmd {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.cmds)
}

// testDeps are the optional collaborator fakes for newTestController; nil
// fields get fresh zero-value fakes, so call sites name only what they use.
type testDeps struct {
	git        *fakeGit
	ports      *fakePorts
	ws         *fakeWorkspace
	compose    *fakeCompose
	containerd *fakeCompose
	colima     *fakeColima
}

// fakeColima answers capability probes without Colima — and, on a CI runner
// that happens to have it, without the real one.
type fakeColima struct {
	profiles []colimaProfile
	err      error
	// calls counts probes, so a test can assert a non-Colima environment does
	// not pay for one.
	calls int
}

func (f *fakeColima) Profiles(context.Context) ([]colimaProfile, error) {
	f.calls++
	return f.profiles, f.err
}

// newTestController wires a controller to the fakes, captures spawns in the
// returned log (nothing executes), and zeroes the baton settle delay.
func newTestController(store *fakeStore, d testDeps) (*Controller, *spawnLog) {
	if d.git == nil {
		d.git = &fakeGit{}
	}
	if d.ports == nil {
		d.ports = &fakePorts{}
	}
	if d.ws == nil {
		d.ws = &fakeWorkspace{}
	}
	if d.compose == nil {
		d.compose = &fakeCompose{}
	}
	if d.containerd == nil {
		d.containerd = &fakeCompose{}
	}
	if d.colima == nil {
		d.colima = &fakeColima{err: errColimaMissing}
	}
	c := New(store, d.git, d.ports, d.ws)
	log := &spawnLog{ch: make(chan *exec.Cmd, 16)}
	c.spawn = log.record
	c.settle = 0
	c.compose = d.compose
	// Always replaced, never left as the real adapter: an environment
	// declaring containerd would otherwise shell out to the developer's own
	// Colima during `go test`.
	c.containerd = d.containerd
	c.colima = d.colima
	return c, log
}

// testEnvsDoc is the shared fixture: one env, a baton db process and an offset
// api process depending on it. delay_seconds 0 keeps the launch loop fast.
func testEnvsDoc() map[string]any {
	return map[string]any{
		"environments": []any{
			map[string]any{
				"id": "dev", "name": "Dev",
				"processes": []any{
					map[string]any{"id": "db", "label": "DB", "command": "run-db",
						"port": 3000, "delay_seconds": 0},
					map[string]any{"id": "api", "command": "run-api --port {{port}}",
						"port": 4000, "port_strategy": "offset", "port_env_var": "PORT",
						"depends_on": []any{"db"}, "delay_seconds": 0},
				},
			},
		},
	}
}

// --- StartEnvironment (the CLI path; also the sync core of the HTTP path) ---

func TestStartEnvironmentBatonKillOffsetAssignAndOrder(t *testing.T) {
	store := &fakeStore{envs: testEnvsDoc()}
	ports := &fakePorts{open: []portsctl.PortEntry{
		{Port: 3000, PID: 111}, // db's declared port: baton takes it over
		{Port: 4000, PID: 222}, // api's base port busy: offset assigns 4001
	}}
	c, spawned := newTestController(store, testDeps{ports: ports})

	killed, err := c.StartEnvironment("dev")
	if err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Baton frees only the non-offset process's declared port, with no extra kills.
	if len(killed) != 1 || killed[0] != (BatonKill{Port: 3000, PID: 111}) {
		t.Errorf("killed = %+v, want [{3000 111}]", killed)
	}
	if len(ports.kills) != 1 {
		t.Errorf("ports.kills = %+v, want exactly one kill", ports.kills)
	}

	// One launch record, with the offset process's assigned port persisted.
	if len(store.launches) != 1 {
		t.Fatalf("launches = %d records, want 1", len(store.launches))
	}
	rec := store.launches[0].(map[string]any)
	if pStr(rec, "env_id") != "dev" || pStr(rec, "launch_id") == "" {
		t.Errorf("record env_id/launch_id = %q/%q", pStr(rec, "env_id"), pStr(rec, "launch_id"))
	}
	procs := toAnySlice(rec["processes"])
	if len(procs) != 2 {
		t.Fatalf("record has %d processes, want 2", len(procs))
	}
	if ap := procs[0].(map[string]any)["assigned_port"]; ap != nil {
		t.Errorf("db assigned_port = %v, want nil (baton)", ap)
	}
	if ap := toIntVal(procs[1].(map[string]any)["assigned_port"]); ap != 4001 {
		t.Errorf("api assigned_port = %d, want 4001", ap)
	}

	// Dependency order: db spawns before api; only api carries the offset var.
	cmds := spawned.all()
	if len(cmds) != 2 {
		t.Fatalf("spawned %d commands, want 2", len(cmds))
	}
	dbEnvHasPort := slices.Contains(cmds[0].Env, "PORT=4001")
	apiEnvHasPort := slices.Contains(cmds[1].Env, "PORT=4001")
	if dbEnvHasPort || !apiEnvHasPort {
		t.Errorf("PORT=4001 in env: db=%v api=%v, want db=false api=true", dbEnvHasPort, apiEnvHasPort)
	}
	// The command string (with {{port}} substituted by the assigned port)
	// reaches the shell. On Windows the command travels via SysProcAttr.CmdLine
	// (pinned by TestShellCmdRawCmdLine), so args are only inspectable on Unix.
	if !platform.IsWindows() {
		if args := cmds[0].Args; !slices.Equal(args, []string{"sh", "-c", "run-db"}) {
			t.Errorf("db args = %v", args)
		}
		if args := cmds[1].Args; !slices.Equal(args, []string{"sh", "-c", "run-api --port 4001"}) {
			t.Errorf("api args = %v", args)
		}
	}
}

func TestStartEnvironmentUnknownEnv(t *testing.T) {
	store := &fakeStore{envs: testEnvsDoc()}
	c, spawned := newTestController(store, testDeps{})
	if _, err := c.StartEnvironment("nope"); err == nil || !strings.Contains(err.Error(), "Environment 'nope' not found") {
		t.Errorf("err = %v, want Environment 'nope' not found", err)
	}
	if len(store.launches) != 0 || len(spawned.all()) != 0 {
		t.Errorf("unknown env must not record (%d) or spawn (%d)", len(store.launches), len(spawned.all()))
	}
}

func TestStartEnvironmentMissingWorktreeFails(t *testing.T) {
	doc := map[string]any{"environments": []any{
		map[string]any{"id": "dev", "processes": []any{
			map[string]any{"id": "api", "command": "run",
				"binding": map[string]any{"repo_path": "/repo/a", "branch": "feat"}},
		}},
	}}
	store := &fakeStore{envs: doc}
	// The repo exists but has no on-disk worktree for the branch.
	git := &fakeGit{worktrees: map[string][]map[string]any{
		"/repo/a": {{"branch": "feat", "exists": false, "path": "/wt/feat"}},
	}}
	c, spawned := newTestController(store, testDeps{git: git})
	_, err := c.StartEnvironment("dev")
	if err == nil || !strings.Contains(err.Error(), "worktree が見つかりません") {
		t.Errorf("err = %v, want missing-worktree error", err)
	}
	if len(store.launches) != 0 || len(spawned.all()) != 0 {
		t.Errorf("failed resolve must not record (%d) or spawn (%d)", len(store.launches), len(spawned.all()))
	}
}

func TestStartEnvironmentUsesEnvWorktreeCwd(t *testing.T) {
	doc := map[string]any{"environments": []any{
		map[string]any{"id": "dev",
			"worktree": map[string]any{"enabled": true, "repo_path": "/repo/a", "branch": "feat"},
			"processes": []any{
				map[string]any{"id": "api", "command": "run", "delay_seconds": 0},
			}},
	}}
	store := &fakeStore{envs: doc}
	git := &fakeGit{worktrees: map[string][]map[string]any{
		"/repo/a": {{"branch": "feat", "exists": true, "path": "/wt/feat"}},
	}}
	c, spawned := newTestController(store, testDeps{git: git})
	if _, err := c.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	if cmds := spawned.all(); len(cmds) != 1 || cmds[0].Dir != "/wt/feat" {
		t.Fatalf("spawn cwd = %+v, want 1 spawn in /wt/feat", cmds)
	}
	rec := store.launches[0].(map[string]any)
	if pStr(rec, "worktree_path") != "/wt/feat" || pStr(rec, "branch") != "feat" || pStr(rec, "repo_path") != "/repo/a" {
		t.Errorf("record worktree fields = %q/%q/%q", pStr(rec, "worktree_path"), pStr(rec, "branch"), pStr(rec, "repo_path"))
	}
}

// TestStartEnvironmentV2HostComponents locks backward compatibility of the
// existing launch path over a version 2 document: host_process components
// launch exactly like the equivalent v1 processes (baton kill, offset assign,
// dependency order, registry record), per the plan's "host_processのみの環境は
// 従来どおり動く" completion condition.
func TestStartEnvironmentV2HostComponents(t *testing.T) {
	store := &fakeStore{envs: map[string]any{
		"version": 2,
		"environments": []any{map[string]any{
			"id": "dev", "name": "Dev",
			"components": []any{
				map[string]any{"id": "db", "kind": "host_process", "lifecycle": "shared",
					"command": "run-db", "port": 3000, "delay_seconds": 0},
				map[string]any{"id": "api", "command": "run-api --port {{port}}",
					"port": 4000, "port_strategy": "offset", "port_env_var": "PORT",
					"depends_on": []any{"db"}, "delay_seconds": 0},
			},
			"scenarios": []any{map[string]any{"id": "main", "components": []any{"api"}}},
		}},
	}}
	ports := &fakePorts{open: []portsctl.PortEntry{
		{Port: 3000, PID: 111},
		{Port: 4000, PID: 222},
	}}
	c, spawned := newTestController(store, testDeps{ports: ports})

	killed, err := c.StartEnvironment("dev")
	if err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	if len(killed) != 1 || killed[0] != (BatonKill{Port: 3000, PID: 111}) {
		t.Errorf("killed = %+v, want [{3000 111}]", killed)
	}
	if len(store.launches) != 1 {
		t.Fatalf("launches = %d records, want 1", len(store.launches))
	}
	procs := toAnySlice(store.launches[0].(map[string]any)["processes"])
	if len(procs) != 2 || toIntVal(procs[1].(map[string]any)["assigned_port"]) != 4001 {
		t.Errorf("record processes = %+v, want db+api with api assigned 4001", procs)
	}
	cmds := spawned.all()
	if len(cmds) != 2 {
		t.Fatalf("spawned %d commands, want 2 in dependency order", len(cmds))
	}
	if !slices.Contains(cmds[1].Env, "PORT=4001") || slices.Contains(cmds[0].Env, "PORT=4001") {
		t.Errorf("offset var must reach only the api spawn")
	}
}

// --- StopEnvironment / EnvStatuses (the CLI read/stop surface) ---

// TestStartEnvironmentReportsSpawnFailure pins the behavior change of plan
// §6.7: a terminal that will not start used to be discarded, and is now
// reported. The rest of the environment still launches — a partial launch is
// reported, not truncated.
func TestStartEnvironmentReportsSpawnFailure(t *testing.T) {
	store := &fakeStore{envs: testEnvsDoc()}
	c, spawned := newTestController(store, testDeps{})
	spawned.failOn = "run-db"

	_, err := c.StartEnvironment("dev")
	if err == nil {
		t.Fatal("a terminal that did not start must be reported")
	}
	if !strings.Contains(err.Error(), "'db'") {
		t.Errorf("err = %v, want the failing process named", err)
	}
	if strings.Contains(err.Error(), "'api'") {
		t.Errorf("err = %v, want only the failing process named", err)
	}
	// The dependent process was still attempted: one failure does not abort
	// the launch. Checking which commands ran (not just how many) is what
	// separates "db failed, api still launched" from "db was retried twice".
	cmds := spawned.all()
	if len(cmds) != 2 {
		t.Fatalf("spawned %d commands, want both attempted", len(cmds))
	}
	launched := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		launched = append(launched, spawnedCommandLine(cmd))
	}
	if !strings.Contains(launched[0], "run-db") || !strings.Contains(launched[1], "run-api") {
		t.Errorf("launched %v, want db attempted then api launched", launched)
	}
	// The launch record is written as before, so the UI still lists the launch.
	if len(store.launches) != 1 {
		t.Errorf("launches = %d, want the record kept", len(store.launches))
	}
}

// TestLaunchEnvironmentAsyncCannotReport documents the limit of §6.7: the HTTP
// launch path answers before the terminals open, so its failures still go
// nowhere. Only the inline paths report.
func TestLaunchEnvironmentAsyncCannotReport(t *testing.T) {
	c, spawned := newTestController(&fakeStore{envs: testEnvsDoc()}, testDeps{})
	spawned.failOn = "run-db"

	if err := c.launchEnvironment("dev"); err != nil {
		t.Errorf("the async path cannot report spawn failures, got %v", err)
	}
	for range 2 { // drain the goroutine's spawns before the test ends
		<-spawned.ch
	}
}

func TestStopEnvironmentKillsAvoidsAndReportsErrors(t *testing.T) {
	store := &fakeStore{
		envs: testEnvsDoc(),
		launches: []any{map[string]any{"env_id": "dev", "launch_id": "l1", "processes": []any{
			map[string]any{"id": "api", "port": float64(4000), "assigned_port": float64(4001)},
		}}},
	}
	ports := &fakePorts{
		open: []portsctl.PortEntry{
			{Port: 3000, PID: 11}, // declared; in avoid list -> reported, not killed
			{Port: 4001, PID: 22}, // assigned in the launch record -> killed
			{Port: 4000, PID: 33}, // api's base port -> kill fails
			{Port: 9999, PID: 44}, // unrelated listener -> untouched
		},
		killErr: map[int]error{4000: errors.New("kill refused")},
	}
	c, _ := newTestController(store, testDeps{ports: ports})

	out, err := c.StopEnvironment("dev", 3000)
	if err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}
	// Outcomes come back in sorted port order over the live targets.
	if len(out) != 3 {
		t.Fatalf("outcomes = %+v, want 3", out)
	}
	if !(out[0].Port == 3000 && out[0].Avoided && out[0].Err == nil) {
		t.Errorf("out[0] = %+v, want avoided 3000", out[0])
	}
	if !(out[1].Port == 4000 && out[1].Err != nil) {
		t.Errorf("out[1] = %+v, want 4000 with kill error", out[1])
	}
	if !(out[2].Port == 4001 && !out[2].Avoided && out[2].Err == nil) {
		t.Errorf("out[2] = %+v, want killed 4001", out[2])
	}
	// The avoided port and the unrelated listener were never passed to kill.
	for _, k := range ports.kills {
		if k.port == 3000 || k.port == 9999 {
			t.Errorf("port %d must not be killed", k.port)
		}
	}
}

func TestEnvStatuses(t *testing.T) {
	doc := testEnvsDoc()
	doc["environments"] = append(toAnySlice(doc["environments"]),
		map[string]any{"id": "idle", "name": "Idle", "processes": []any{}})
	store := &fakeStore{
		envs: doc,
		launches: []any{map[string]any{"env_id": "dev", "launch_id": "l1", "processes": []any{
			map[string]any{"id": "api", "assigned_port": float64(4001)},
		}}},
	}
	ports := &fakePorts{open: []portsctl.PortEntry{
		{Port: 3000, PID: 1}, {Port: 4001, PID: 2}, {Port: 9999, PID: 3},
	}}
	c, _ := newTestController(store, testDeps{ports: ports})

	got, err := c.EnvStatuses()
	if err != nil {
		t.Fatalf("EnvStatuses: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("statuses = %+v, want 2 (definition order)", got)
	}
	if got[0].ID != "dev" || got[0].Name != "Dev" || got[0].Processes != 2 {
		t.Errorf("dev status = %+v", got[0])
	}
	if !slices.Equal(got[0].LivePorts, []int{3000, 4001}) {
		t.Errorf("dev LivePorts = %v, want [3000 4001]", got[0].LivePorts)
	}
	if got[1].ID != "idle" || len(got[1].LivePorts) != 0 {
		t.Errorf("idle status = %+v, want no live ports", got[1])
	}
}

// --- HTTP handlers ---

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON: %v (%s)", err, rr.Body.String())
	}
	return m
}

func TestHandleGetEnvsAndWorktrees(t *testing.T) {
	store := &fakeStore{envs: testEnvsDoc()}
	git := &fakeGit{
		repos: []gitctl.Repo{{Name: "a", Path: "/repo/a"}, {Name: "broken", Path: "/repo/x"}},
		worktrees: map[string][]map[string]any{
			// /repo/x is missing on purpose: a repo whose listing fails is skipped.
			"/repo/a": {{"branch": "main", "exists": true, "path": "/repo/a"}},
		},
	}
	c, _ := newTestController(store, testDeps{git: git})

	rr := httptest.NewRecorder()
	if err := c.HandleGet(rr, httptest.NewRequest("GET", "/api/envs", nil)); err != nil {
		t.Fatalf("GET /api/envs: %v", err)
	}
	if envs := toAnySlice(decodeJSON(t, rr)["environments"]); len(envs) != 1 {
		t.Errorf("GET /api/envs environments = %v", envs)
	}

	rr = httptest.NewRecorder()
	if err := c.HandleGet(rr, httptest.NewRequest("GET", "/api/envs/worktrees", nil)); err != nil {
		t.Fatalf("GET /api/envs/worktrees: %v", err)
	}
	body := decodeJSON(t, rr)
	repos := toAnySlice(body["repos"])
	if len(repos) != 1 || pStr(repos[0].(map[string]any), "name") != "a" {
		t.Errorf("worktree inventory = %v, want only repo a", repos)
	}
	if _, ok := body["home"].(string); !ok {
		t.Error("worktree inventory missing home")
	}

	if err := c.HandleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/envs/bogus", nil)); err == nil {
		t.Error("unknown GET path should error")
	}
}

// v2SwitchDoc is a v2 document with a shared component and two scenarios —
// the switch endpoints' fixture.
func v2SwitchDoc() map[string]any {
	return map[string]any{
		"version": 2,
		"environments": []any{map[string]any{
			"id": "micro", "name": "Micro",
			"components": []any{
				map[string]any{"id": "db", "label": "DB", "lifecycle": "shared", "command": "run-db", "port": 3000},
				map[string]any{"id": "acc", "command": "run-acc", "port": 4000, "depends_on": []any{"db"}},
				map[string]any{"id": "bill", "command": "run-bill", "port": 5000, "depends_on": []any{"db"}},
			},
			"scenarios": []any{
				map[string]any{"id": "accounting", "name": "会計", "components": []any{"acc"}},
				map[string]any{"id": "billing", "name": "請求", "components": []any{"bill"}},
			},
		}},
	}
}

func TestHandleGetState(t *testing.T) {
	store := &fakeStore{envs: v2SwitchDoc()}
	ports := &fakePorts{open: []portsctl.PortEntry{{Port: 3000, PID: 11}, {Port: 4000, PID: 22}}}
	c, _ := newTestController(store, testDeps{ports: ports})

	rr := httptest.NewRecorder()
	if err := c.HandleGet(rr, httptest.NewRequest("GET", "/api/envs/state", nil)); err != nil {
		t.Fatalf("GET /api/envs/state: %v", err)
	}
	envs := toAnySlice(decodeJSON(t, rr)["environments"])
	if len(envs) != 1 {
		t.Fatalf("environments = %v", envs)
	}
	env := envs[0].(map[string]any)
	if len(toAnySlice(env["scenarios"])) != 2 {
		t.Errorf("scenarios = %v", env["scenarios"])
	}
	states := map[string]string{}
	for _, compAny := range toAnySlice(env["components"]) {
		comp := compAny.(map[string]any)
		states[pStr(comp, "id")] = pStr(comp, "state")
	}
	want := map[string]string{"db": "running", "acc": "running", "bill": "stopped"}
	for id, state := range want {
		if states[id] != state {
			t.Errorf("%s state = %q, want %q", id, states[id], state)
		}
	}
	db := toAnySlice(env["components"])[0].(map[string]any)
	if db["shared"] != true || pStr(db, "label") != "DB" || pStr(db, "kind") != kindHostProcess {
		t.Errorf("component metadata = %v", db)
	}
	// The declared execution base travels with the state so the environment
	// card can show it; a document that declares none reports the default.
	if got := pStr(pMap(env, "runtime"), "provider"); got != providerDocker {
		t.Errorf("runtime provider = %q, want %q", got, providerDocker)
	}

	// A v1 document reports through its generated components and default
	// scenario, so the state view works before any migration.
	c, _ = newTestController(&fakeStore{envs: testEnvsDoc()}, testDeps{ports: ports})
	rr = httptest.NewRecorder()
	if err := c.HandleGet(rr, httptest.NewRequest("GET", "/api/envs/state", nil)); err != nil {
		t.Fatalf("GET /api/envs/state (v1): %v", err)
	}
	v1 := toAnySlice(decodeJSON(t, rr)["environments"])[0].(map[string]any)
	if len(toAnySlice(v1["components"])) != 2 {
		t.Errorf("v1 components = %v, want the two processes", v1["components"])
	}
	scenarios := toAnySlice(v1["scenarios"])
	if len(scenarios) != 1 || pStr(scenarios[0].(map[string]any), "id") != defaultScenarioID {
		t.Errorf("v1 scenarios = %v, want the default scenario", scenarios)
	}
}

// composeDoc is a v2 document whose shared database is a compose_service, plus
// two components sharing one compose project.
func composeDoc() map[string]any {
	spec := func(project string, services ...any) map[string]any {
		return map[string]any{"cwd": "~/platform", "files": []any{"compose.yml"},
			"project": project, "services": services}
	}
	return map[string]any{
		"version": 2,
		"environments": []any{map[string]any{
			"id": "micro",
			"components": []any{
				map[string]any{"id": "mysql", "kind": "compose_service", "lifecycle": "shared",
					"compose": spec("platform-local", "mysql")},
				map[string]any{"id": "redis", "kind": "compose_service", "lifecycle": "shared",
					"compose": spec("platform-local", "redis")},
				map[string]any{"id": "api", "kind": "compose_service",
					"compose": spec("accounting-local", "api", "worker")},
			},
			"scenarios": []any{map[string]any{"id": "acc", "components": []any{"api"}}},
		}},
	}
}

func TestComposeStates(t *testing.T) {
	compose := &fakeCompose{states: map[string]map[string]componentState{
		"platform-local":   {"mysql": stateRunning, "redis": stateStopped},
		"accounting-local": {"api": stateRunning, "worker": stateRunning},
	}}
	c, _ := newTestController(&fakeStore{envs: composeDoc()}, testDeps{compose: compose})
	env, err := c.findEnv("micro")
	if err != nil {
		t.Fatalf("findEnv: %v", err)
	}

	states := c.composeStates(env)
	for id, want := range map[string]componentState{
		"mysql": stateRunning, "redis": stateStopped, "api": stateRunning,
	} {
		if states[id].State != want {
			t.Errorf("%s = %+v, want %q", id, states[id], want)
		}
	}
	// mysql and redis share one project, so they cost one probe, not two.
	if len(compose.calls) != 2 {
		t.Errorf("probes = %d (%v), want one per compose project", len(compose.calls), compose.calls)
	}

	// A probe failure leaves those components unknown — carrying Docker's own
	// message — instead of failing the whole request.
	compose = &fakeCompose{err: errors.New("Cannot connect to the Docker daemon")}
	c, _ = newTestController(&fakeStore{envs: composeDoc()}, testDeps{compose: compose})
	states = c.composeStates(env)
	for _, id := range []string{"mysql", "redis", "api"} {
		if states[id].State != stateUnknown || !strings.Contains(states[id].Reason, "Cannot connect") {
			t.Errorf("%s on probe failure = %+v", id, states[id])
		}
	}
}

// TestSwitchPlanEndpointCarriesRuntimeWarnings guards the UI/CLI split: the
// HTTP handler used to re-derive the plan itself instead of calling
// PlanSwitch, so the browser silently lost warnings the CLI showed (plan
// §6.5). Both surfaces must answer with the same plan.
func TestSwitchPlanEndpointCarriesRuntimeWarnings(t *testing.T) {
	doc := composeDoc()
	doc["environments"].([]any)[0].(map[string]any)["runtime"] =
		map[string]any{"provider": "colima", "profile": "dev"}
	c, _ := newTestController(&fakeStore{envs: doc}, testDeps{
		colima: &fakeColima{profiles: []colimaProfile{{Name: "dev", Status: "Stopped"}}},
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/envs/switch/plan", nil)
	if err := c.HandlePost(rr, req, map[string]any{"env_id": "micro", "scenario_id": "acc"}); err != nil {
		t.Fatalf("switch plan: %v", err)
	}

	var found bool
	for _, w := range toAnySlice(decodeJSON(t, rr)["warnings"]) {
		if s, _ := w.(string); strings.Contains(s, "colima start -p dev") {
			found = true
		}
	}
	if !found {
		t.Errorf("plan response has no stopped-profile warning: %s", rr.Body.String())
	}

	// The CLI path must agree with it.
	plan, err := c.PlanSwitch("micro", SwitchTarget{ScenarioID: "acc"})
	if err != nil {
		t.Fatalf("PlanSwitch: %v", err)
	}
	if len(plan.Warnings) == 0 {
		t.Error("PlanSwitch dropped the runtime warnings")
	}
}

// TestComposeOperationsCarryTheEnvironmentContext follows the declared runtime
// all the way to the adapter, on every path that touches containers. A context
// that reached only some of them would silently probe one engine and start
// containers on another.
func TestComposeOperationsCarryTheEnvironmentContext(t *testing.T) {
	doc := composeDoc()
	env := doc["environments"].([]any)[0].(map[string]any)
	env["runtime"] = map[string]any{"provider": "colima", "profile": "dev"}

	apply := func(t *testing.T, running componentState, target SwitchTarget) *fakeCompose {
		t.Helper()
		compose := &fakeCompose{states: map[string]map[string]componentState{
			"platform-local":   {"mysql": stateRunning, "redis": stateRunning},
			"accounting-local": {"api": running, "worker": running},
		}}
		c, _ := newTestController(&fakeStore{envs: doc}, testDeps{
			compose: compose,
			colima:  &fakeColima{profiles: []colimaProfile{{Name: "dev", Status: "Running", Engine: engineDocker}}},
		})
		if _, _, err := c.ApplySwitch("micro", target, ""); err != nil {
			t.Fatalf("ApplySwitch: %v", err)
		}
		if len(compose.runtimes) == 0 {
			t.Fatal("no compose operation was recorded")
		}
		for i, rt := range compose.runtimes {
			if got := dockerContextFor(rt); got != "colima-dev" {
				t.Errorf("operation %d addressed context %q, want colima-dev", i, got)
			}
		}
		return compose
	}

	// Empty target: the scenario component is running, so it is probed and
	// stopped while the shared ones are kept.
	if stopped := apply(t, stateRunning, SwitchTarget{Components: []string{}}); len(stopped.stops) == 0 {
		t.Error("nothing was stopped; the fixture no longer exercises the stop path")
	}
	// Scenario target with that component down: probed and started.
	if started := apply(t, stateStopped, SwitchTarget{ScenarioID: "acc"}); len(started.ups) == 0 {
		t.Error("nothing was started; the fixture no longer exercises the start path")
	}
}

// TestContainerdEnvironmentUsesTheContainerdAdapter follows a declared engine
// through the controller. Selecting the wrong adapter would not fail loudly —
// it would address a Docker context that happens to exist and report on the
// wrong containers — so the assertion is that the Docker adapter is not
// touched at all.
func TestContainerdEnvironmentUsesTheContainerdAdapter(t *testing.T) {
	doc := composeDoc()
	doc["environments"].([]any)[0].(map[string]any)["runtime"] =
		map[string]any{"provider": "colima", "profile": "dev", "engine": "containerd"}

	docker := &fakeCompose{}
	containerd := &fakeCompose{states: map[string]map[string]componentState{
		"platform-local":   {"mysql": stateRunning, "redis": stateRunning},
		"accounting-local": {"api": stateStopped, "worker": stateStopped},
	}}
	c, _ := newTestController(&fakeStore{envs: doc}, testDeps{
		compose: docker, containerd: containerd,
		colima: &fakeColima{profiles: []colimaProfile{{Name: "dev", Status: "Running", Engine: engineContainerd}}},
	})

	plan, results, err := c.ApplySwitch("micro", SwitchTarget{ScenarioID: "acc"}, "")
	if err != nil {
		t.Fatalf("ApplySwitch: %v", err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s %s: %v", r.Action, r.Step.ID, r.Err)
		}
	}
	if len(containerd.ups) == 0 || len(containerd.calls) == 0 {
		t.Errorf("containerd adapter unused: ups=%v probes=%v", containerd.ups, containerd.calls)
	}
	if len(docker.calls)+len(docker.ups)+len(docker.stops) != 0 {
		t.Errorf("docker adapter was used for a containerd environment: %+v", docker)
	}
	// The readiness gap is stated before the switch, not discovered after it.
	var warned bool
	for _, w := range plan.Warnings {
		if strings.Contains(w, "--wait") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no readiness warning for containerd: %v", plan.Warnings)
	}
}

// TestSwitchPlanWithComposeKeepsSharedService is the flagship use case end to
// end through the API: the shared database is running under Compose, so
// switching scenarios keeps it rather than restarting it.
func TestSwitchPlanWithComposeKeepsSharedService(t *testing.T) {
	compose := &fakeCompose{states: map[string]map[string]componentState{
		"platform-local":   {"mysql": stateRunning, "redis": stateRunning},
		"accounting-local": {"api": stateStopped, "worker": stateStopped},
	}}
	c, _ := newTestController(&fakeStore{envs: composeDoc()}, testDeps{compose: compose})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/envs/switch/plan", nil)
	if err := c.HandlePost(rr, req, map[string]any{"env_id": "micro", "scenario_id": "acc"}); err != nil {
		t.Fatalf("switch plan: %v", err)
	}
	plan := decodeJSON(t, rr)
	ids := func(key string) []string {
		var out []string
		for _, s := range toAnySlice(plan[key]) {
			out = append(out, pStr(s.(map[string]any), "id"))
		}
		return out
	}
	if !slices.Equal(ids("keep"), []string{"mysql", "redis"}) {
		t.Errorf("keep = %v, want the shared compose services kept", ids("keep"))
	}
	if !slices.Equal(ids("start"), []string{"api"}) {
		t.Errorf("start = %v, want [api]", ids("start"))
	}
	if len(ids("stop")) != 0 {
		t.Errorf("stop = %v, want nothing stopped", ids("stop"))
	}
	if warnings := toAnySlice(plan["warnings"]); len(warnings) != 0 {
		t.Errorf("warnings = %v, want none once Compose state is observable", warnings)
	}
}

func TestHandlePostSwitchPlan(t *testing.T) {
	store := &fakeStore{envs: v2SwitchDoc()}
	ports := &fakePorts{open: []portsctl.PortEntry{{Port: 3000, PID: 11}, {Port: 4000, PID: 22}}}
	c, _ := newTestController(store, testDeps{ports: ports})
	req := httptest.NewRequest("POST", "/api/envs/switch/plan", nil)

	rr := httptest.NewRecorder()
	if err := c.HandlePost(rr, req, map[string]any{"env_id": "micro", "scenario_id": "billing"}); err != nil {
		t.Fatalf("switch plan: %v", err)
	}
	plan := decodeJSON(t, rr)
	ids := func(key string) []string {
		var out []string
		for _, s := range toAnySlice(plan[key]) {
			out = append(out, pStr(s.(map[string]any), "id"))
		}
		return out
	}
	if !slices.Equal(ids("stop"), []string{"acc"}) || !slices.Equal(ids("keep"), []string{"db"}) ||
		!slices.Equal(ids("start"), []string{"bill"}) {
		t.Errorf("plan = stop%v keep%v start%v", ids("stop"), ids("keep"), ids("start"))
	}
	if pStr(plan, "scenario_id") != "billing" || pStr(plan, "env_id") != "micro" {
		t.Errorf("plan header = %v", plan)
	}
	if _, ok := plan["warnings"].([]any); !ok {
		t.Errorf("warnings must always be an array, got %#v", plan["warnings"])
	}
	// Nothing was launched or killed: planning is side-effect free.
	if len(ports.kills) != 0 {
		t.Errorf("planning killed ports: %v", ports.kills)
	}

	// An explicit selection is the other accepted target: an empty one keeps
	// only the shared component.
	rr = httptest.NewRecorder()
	if err := c.HandlePost(rr, req, map[string]any{"env_id": "micro", "components": []any{}}); err != nil {
		t.Fatalf("switch plan with selection: %v", err)
	}
	plan = decodeJSON(t, rr)
	if !slices.Equal(ids("stop"), []string{"acc"}) || !slices.Equal(ids("keep"), []string{"db"}) {
		t.Errorf("empty selection = stop%v keep%v, want the shared component kept", ids("stop"), ids("keep"))
	}

	for _, c2 := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"missing env_id", map[string]any{"scenario_id": "billing"}, "env_id is required"},
		{"no target", map[string]any{"env_id": "micro"}, "exactly one of scenario_id or components"},
		{"both targets", map[string]any{"env_id": "micro", "scenario_id": "billing", "components": []any{"acc"}},
			"exactly one of scenario_id or components"},
		{"malformed selection", map[string]any{"env_id": "micro", "components": "acc"},
			"components must be an array"},
		{"unknown env", map[string]any{"env_id": "ghost", "scenario_id": "billing"}, "not found"},
		{"unknown scenario", map[string]any{"env_id": "micro", "scenario_id": "ghost"}, "Scenario 'ghost' not found"},
	} {
		err := c.HandlePost(httptest.NewRecorder(), req, c2.body)
		if err == nil || !strings.Contains(err.Error(), c2.want) {
			t.Errorf("%s: err = %v, want containing %q", c2.name, err, c2.want)
		}
	}
}

func TestHandleGetLaunchesEnriched(t *testing.T) {
	wt := t.TempDir()
	store := &fakeStore{
		envs: testEnvsDoc(),
		launches: []any{map[string]any{
			"env_id": "dev", "launch_id": "l1", "worktree_path": wt,
			"processes": []any{
				map[string]any{"id": "db", "port": "3000-3001"},
				map[string]any{"id": "api", "port": float64(4000), "assigned_port": float64(4001)},
				map[string]any{"id": "worker", "worktree_path": "/gone"},
			},
		}},
	}
	ports := &fakePorts{open: []portsctl.PortEntry{
		{Port: 3001, PID: 7}, // db bound the second port of its range
		{Port: 4001, PID: 8}, // api on its assigned (not declared) port
	}}
	c, _ := newTestController(store, testDeps{ports: ports})

	rr := httptest.NewRecorder()
	if err := c.HandleGet(rr, httptest.NewRequest("GET", "/api/envs/launches", nil)); err != nil {
		t.Fatalf("GET /api/envs/launches: %v", err)
	}
	launches := toAnySlice(decodeJSON(t, rr)["launches"])
	if len(launches) != 1 {
		t.Fatalf("launches = %v", launches)
	}
	rec := launches[0].(map[string]any)
	if rec["worktree_exists"] != true {
		t.Error("existing worktree_path should report worktree_exists true")
	}
	procs := toAnySlice(rec["processes"])
	db := procs[0].(map[string]any)
	if db["running"] != true || len(toAnySlice(db["live_ports"])) != 1 {
		t.Errorf("db enrichment = %v, want running on 3001", db)
	}
	api := procs[1].(map[string]any)
	// assigned_port takes precedence over the declared spec for liveness.
	apiLive := toAnySlice(api["live_ports"])
	if api["running"] != true || len(apiLive) != 1 || toIntVal(apiLive[0].(map[string]any)["port"]) != 4001 {
		t.Errorf("api enrichment = %v, want running on 4001", api)
	}
	worker := procs[2].(map[string]any)
	if worker["running"] != false || worker["worktree_exists"] != false {
		t.Errorf("worker enrichment = %v, want stopped with missing worktree", worker)
	}
}

func TestHandlePostSaveEnvsValidates(t *testing.T) {
	store := &fakeStore{envs: map[string]any{}}
	c, _ := newTestController(store, testDeps{})
	req := httptest.NewRequest("POST", "/api/envs", nil)

	rr := httptest.NewRecorder()
	if err := c.HandlePost(rr, req, testEnvsDoc()); err != nil {
		t.Fatalf("valid save: %v", err)
	}
	if decodeJSON(t, rr)["ok"] != true {
		t.Errorf("save response = %s", rr.Body.String())
	}
	if envs := toAnySlice(store.envs["environments"]); len(envs) != 1 {
		t.Errorf("saved doc = %v", store.envs)
	}

	// A duplicate environment id is rejected and must not overwrite the store.
	dup := map[string]any{"environments": []any{
		map[string]any{"id": "dev"}, map[string]any{"id": "dev"},
	}}
	if err := c.HandlePost(httptest.NewRecorder(), req, dup); err == nil || !strings.Contains(err.Error(), "Duplicate environment ID") {
		t.Errorf("duplicate save err = %v", err)
	}
	if envs := toAnySlice(store.envs["environments"]); len(envs) != 1 {
		t.Error("invalid save must not replace the stored document")
	}
}

func TestHandlePostLaunchEnvRecordsAndSpawnsAsync(t *testing.T) {
	store := &fakeStore{envs: map[string]any{"environments": []any{
		map[string]any{"id": "dev", "name": "Dev", "processes": []any{
			map[string]any{"id": "api", "command": "run-api", "delay_seconds": 0},
		}},
	}}}
	c, spawned := newTestController(store, testDeps{})
	req := httptest.NewRequest("POST", "/api/envs/launch", nil)

	rr := httptest.NewRecorder()
	if err := c.HandlePost(rr, req, map[string]any{"env_id": "dev"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if decodeJSON(t, rr)["ok"] != true {
		t.Errorf("launch response = %s", rr.Body.String())
	}
	// The record is written before the response returns; the terminal spawn
	// happens on a goroutine afterwards, so wait for it through the log channel.
	if len(store.launches) != 1 {
		t.Errorf("launch must append a record, got %d", len(store.launches))
	}
	select {
	case <-spawned.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("async launch never spawned the process")
	}

	if err := c.HandlePost(httptest.NewRecorder(), req, map[string]any{}); err == nil || !strings.Contains(err.Error(), "env_id is required") {
		t.Errorf("missing env_id err = %v", err)
	}
}

func TestHandlePostLaunchSingleProcess(t *testing.T) {
	store := &fakeStore{envs: testEnvsDoc()}
	ports := &fakePorts{open: []portsctl.PortEntry{{Port: 4000, PID: 9}}}
	c, spawned := newTestController(store, testDeps{ports: ports})
	req := httptest.NewRequest("POST", "/api/envs/launch/process", nil)

	rr := httptest.NewRecorder()
	if err := c.HandlePost(rr, req, map[string]any{"env_id": "dev", "process_id": "api"}); err != nil {
		t.Fatalf("launch process: %v", err)
	}
	if cmds := spawned.all(); len(cmds) != 1 || !slices.Contains(cmds[0].Env, "PORT=4001") {
		t.Fatalf("spawn = %+v, want one spawn with PORT=4001", cmds)
	}
	// A single-process launch is not recorded in the registry (current behavior).
	if len(store.launches) != 0 {
		t.Errorf("single-process launch must not append a record, got %d", len(store.launches))
	}

	err := c.HandlePost(httptest.NewRecorder(), req, map[string]any{"env_id": "dev", "process_id": "nope"})
	if err == nil || !strings.Contains(err.Error(), "Process 'nope' not found") {
		t.Errorf("unknown process err = %v", err)
	}

	// This path runs inline, so a terminal that will not start is reported to
	// the caller instead of vanishing (plan §6.7).
	c, spawned = newTestController(&fakeStore{envs: testEnvsDoc()}, testDeps{ports: ports})
	spawned.failOn = "run-api"
	err = c.HandlePost(httptest.NewRecorder(), req, map[string]any{"env_id": "dev", "process_id": "api"})
	if err == nil || !strings.Contains(err.Error(), "'api'") {
		t.Errorf("spawn failure err = %v, want the process named", err)
	}
}

func TestHandlePostLaunchRegistryActions(t *testing.T) {
	wt := t.TempDir()
	store := &fakeStore{
		envs: map[string]any{},
		launches: []any{
			map[string]any{"launch_id": "l1", "worktree_path": wt},
			map[string]any{"launch_id": "l2"},
		},
	}
	ws := &fakeWorkspace{}
	c, _ := newTestController(store, testDeps{ws: ws})

	// remove: drops exactly the requested record.
	req := httptest.NewRequest("POST", "/api/envs/launches/remove", nil)
	if err := c.HandlePost(httptest.NewRecorder(), req, map[string]any{"launch_id": "l2"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(store.launches) != 1 || pStr(store.launches[0].(map[string]any), "launch_id") != "l1" {
		t.Errorf("launches after remove = %v", store.launches)
	}
	if err := c.HandlePost(httptest.NewRecorder(), req, map[string]any{"launch_id": "ghost"}); err == nil || !strings.Contains(err.Error(), "launch record not found") {
		t.Errorf("remove unknown err = %v", err)
	}
	if err := c.HandlePost(httptest.NewRecorder(), req, map[string]any{"launch_id": "l1", "force": "yes"}); err == nil || !strings.Contains(err.Error(), "force must be a boolean") {
		t.Errorf("non-bool force err = %v", err)
	}

	// open: editor target routes to the workspace; bad target errors.
	openReq := httptest.NewRequest("POST", "/api/envs/launches/open", nil)
	if err := c.HandlePost(httptest.NewRecorder(), openReq, map[string]any{"launch_id": "l1", "target": "editor"}); err != nil {
		t.Fatalf("open editor: %v", err)
	}
	if !slices.Equal(ws.opened, []string{wt}) {
		t.Errorf("opened = %v, want [%s]", ws.opened, wt)
	}
	if err := c.HandlePost(httptest.NewRecorder(), openReq, map[string]any{"launch_id": "l1", "target": "bogus"}); err == nil || !strings.Contains(err.Error(), "invalid target") {
		t.Errorf("bad target err = %v", err)
	}

	// Unknown POST path 404s.
	if err := c.HandlePost(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/envs/bogus", nil), map[string]any{}); err == nil {
		t.Error("unknown POST path should error")
	}
}
