package envs

// Characterization tests for the Controller: they pin the current observable
// behavior of the launch/stop/status/registry paths against in-memory fakes
// (no SQLite, no lsof, no terminal spawn), so the upcoming responsibility
// split can be verified behavior-preserving. The fakes mirror the narrow
// interfaces the controller depends on (launchStore, gitService, portsService,
// workspaceService); process spawning is captured through the `start` seam in
// terminal.go.

import (
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

type fakeWorkspace struct{ opened []string }

func (f *fakeWorkspace) OpenInEditor(path string) { f.opened = append(f.opened, path) }

// spawnLog collects the commands the controller would have spawned. Each
// record also feeds a buffered channel so tests of the async HTTP launch path
// can wait for the goroutine's spawns instead of sleeping.
type spawnLog struct {
	mu   sync.Mutex
	cmds []*exec.Cmd
	ch   chan *exec.Cmd
}

func (l *spawnLog) record(cmd *exec.Cmd) {
	l.mu.Lock()
	l.cmds = append(l.cmds, cmd)
	l.mu.Unlock()
	l.ch <- cmd
}

func (l *spawnLog) all() []*exec.Cmd {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.cmds)
}

// testDeps are the optional collaborator fakes for newTestController; nil
// fields get fresh zero-value fakes, so call sites name only what they use.
type testDeps struct {
	git   *fakeGit
	ports *fakePorts
	ws    *fakeWorkspace
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
	c := New(store, d.git, d.ports, d.ws)
	log := &spawnLog{ch: make(chan *exec.Cmd, 16)}
	c.spawn = log.record
	c.settle = 0
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
