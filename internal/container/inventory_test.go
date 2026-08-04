package container

// Tests for the machine-wide container listing.
//
// The fixtures below keep the exact shape of real `docker ps --all --format
// json` output (Docker 29.4.0) — NDJSON, the same keys, the same comma-joined
// Labels string — with the values replaced. They are hand-written rather than
// captured because a captured sample carries the working directories and
// project names of whoever ran it, which have no business in this repository.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// composeRow is a compose-managed container. project.config_files deliberately
// lists two files: that puts a comma *inside* a label value, which is the case
// the joined-label encoding cannot represent and the one parseLabels has to
// survive. Real compose output looks exactly like this whenever a stack is
// assembled from more than one file.
const composeRow = `{"Command":"\"docker-entrypoint.s…\"","CreatedAt":"2026-07-29 18:33:55 +0900 JST","ID":"66c1d62e2d63","Image":"mysql:8.0","Labels":"com.docker.compose.config-hash=f02373d6,com.docker.compose.container-number=1,com.docker.compose.depends_on=,com.docker.compose.oneoff=False,com.docker.compose.project.config_files=/srv/stack/base.yml,/srv/stack/override.yml,com.docker.compose.project.working_dir=/srv/stack,com.docker.compose.project=platform-local,com.docker.compose.service=mysql,com.docker.compose.version=5.1.2","LocalVolumes":"1","Mounts":"platform-local…","Names":"platform-local-mysql-1","Networks":"platform","Platform":{"architecture":"arm64","os":"linux"},"Ports":"33060/tcp, 0.0.0.0:23306->3306/tcp","RunningFor":"5 days ago","Size":"0B","State":"exited","Status":"Exited (255) 50 seconds ago"}`

// strayRow is what the panel exists for: a container no compose project owns,
// so no environment's declaration can account for it.
const strayRow = `{"Command":"\"redis-server\"","CreatedAt":"2026-08-01 09:00:00 +0900 JST","ID":"aa11bb22cc33","Image":"redis:7","Labels":"","LocalVolumes":"0","Mounts":"","Names":"scratch-redis","Networks":"bridge","Platform":{"architecture":"arm64","os":"linux"},"Ports":"0.0.0.0:6379->6379/tcp","RunningFor":"2 hours ago","Size":"0B","State":"running","Status":"Up 2 hours"}`

// namelessRow is real: Docker reports an empty Names for some containers —
// build intermediates, mostly — and its Image is a bare image ID rather than a
// tag. Three of the ninety containers on the machine this was written on looked
// like this, which is also why parsePS's schema check requires ID *and* Names to
// be empty: keyed on Names alone it would have rejected real output.
const namelessRow = `{"Command":"\"/bin/sh -c ...\"","CreatedAt":"2026-07-05 10:00:00 +0900 JST","ID":"a8e2d0ffbfcb","Image":"0c51c233d05e","Labels":"","LocalVolumes":"0","Mounts":"","Names":"","Networks":"","Platform":{"architecture":"arm64","os":"linux"},"Ports":"","RunningFor":"4 weeks ago","Size":"0B","State":"exited","Status":"Exited (0) 4 weeks ago"}`

func TestParsePS(t *testing.T) {
	got, err := parsePS(composeRow+"\n"+strayRow+"\n", "docker")
	if err != nil {
		t.Fatalf("parsePS: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d containers, want 2", len(got))
	}

	mysql := got[0]
	// The comma inside project.config_files must not have shifted the pairs
	// that follow it; project and service are read from past that point.
	if mysql.Project != "platform-local" || mysql.Service != "mysql" {
		t.Errorf("compose labels = %q/%q, want platform-local/mysql", mysql.Project, mysql.Service)
	}
	if mysql.Name != "platform-local-mysql-1" || mysql.Image != "mysql:8.0" {
		t.Errorf("name/image = %q/%q", mysql.Name, mysql.Image)
	}
	if mysql.State != "exited" || mysql.Running() {
		t.Errorf("state = %q, running = %v", mysql.State, mysql.Running())
	}
	// Status carries Docker's own phrasing: it says *why* a container is not
	// running, which the bare state cannot.
	if !strings.Contains(mysql.Status, "Exited (255)") {
		t.Errorf("status = %q, want Docker's own wording", mysql.Status)
	}
	if mysql.Source != "docker" {
		t.Errorf("source = %q", mysql.Source)
	}

	stray := got[1]
	if stray.Project != "" || stray.Service != "" {
		t.Errorf("a container with no labels reported project %q service %q", stray.Project, stray.Service)
	}
	if !stray.Running() {
		t.Errorf("stray state = %q, want running", stray.State)
	}
}

// TestParsePSAcceptsBothShapes: the release decides whether it prints an array
// or one object per line, and devhub pins neither — the same choice
// parseComposePS already makes for `compose ps`.
func TestParsePSAcceptsBothShapes(t *testing.T) {
	array := "[" + composeRow + "," + strayRow + "]"
	fromArray, err := parsePS(array, "docker")
	if err != nil {
		t.Fatalf("array: %v", err)
	}
	fromLines, err := parsePS(composeRow+"\n"+strayRow, "docker")
	if err != nil {
		t.Fatalf("lines: %v", err)
	}
	if !slices.Equal(fromArray, fromLines) {
		t.Errorf("the two shapes parsed differently:\n array = %+v\n lines = %+v", fromArray, fromLines)
	}

	// No containers is not an error, and must not be confused with one.
	if got, err := parsePS("   \n", "docker"); err != nil || len(got) != 0 {
		t.Errorf("empty output = %v, %v; want no containers and no error", got, err)
	}
}

// TestParsePSRejectsAnUnknownSchema is the loudness rule. If a future release
// renames the keys, valid JSON still decodes — into empty structs. Reporting
// that as an empty list would tell the user this machine has no containers,
// which is the one wrong answer a panel for finding stray containers must never
// give.
func TestParsePSRejectsAnUnknownSchema(t *testing.T) {
	renamed := `{"container_id":"66c1d62e2d63","container_name":"platform-local-mysql-1","state":"exited"}`
	got, err := parsePS(renamed, "docker")
	if err == nil {
		t.Fatalf("parsed %d containers from output with no recognised keys; want an error", len(got))
	}
	if !strings.Contains(err.Error(), "ID/Names") {
		t.Errorf("error = %q, want it to name the fields that were missing", err)
	}
}

// TestParseLabelsPrefersTheFirstOccurrence covers the one way the joined-label
// encoding can be abused: a label whose value smuggles in a second pair. Docker
// sorts by the joined "k=v" string, so a forgery hidden in `description` lands
// after the genuine compose label — last-wins would take it.
func TestParseLabelsPrefersTheFirstOccurrence(t *testing.T) {
	labels := "com.docker.compose.project=mine,com.docker.compose.service=db," +
		"description=hello,com.docker.compose.project=someone-elses"
	got := parseLabels(labels)
	if got[labelComposeProject] != "mine" {
		t.Errorf("project = %q, want mine — a forged pair later in the string won", got[labelComposeProject])
	}

	// A value with a comma leaves a fragment carrying no "=", which is dropped
	// rather than becoming a key.
	withList := "com.docker.compose.project.config_files=/a.yml,/b.yml,com.docker.compose.project=p"
	if got := parseLabels(withList); got[labelComposeProject] != "p" {
		t.Errorf("project = %q, want p", got[labelComposeProject])
	}
}

func TestInventoryArgv(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  Source
		bin  string
		want []string
	}{
		{"ambient docker", Source{ID: "docker"}, "docker",
			[]string{"ps", "--all", "--format", "json"}},
		{"colima docker profile", Source{ID: "colima:dev", Context: "colima-dev", Engine: EngineDocker}, "docker",
			[]string{"--context", "colima-dev", "ps", "--all", "--format", "json"}},
		{"colima containerd profile", Source{ID: "colima:ctr", Profile: "ctr", Engine: EngineContainerd}, "colima",
			[]string{"nerdctl", "--profile", "ctr", "--", "ps", "-a", "--format", "json"}},
	} {
		runner := &fakeRunner{}
		if _, err := (&cliInventory{runner: runner}).List(context.Background(), tc.src); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		call := runner.calls[0]
		if call.name != tc.bin {
			t.Errorf("%s: ran %q, want %q", tc.name, call.name, tc.bin)
		}
		if !slices.Equal(call.args, tc.want) {
			t.Errorf("%s: args = %v\nwant %v", tc.name, call.args, tc.want)
		}
		// Same rule as every other command in this package: the caller sets no
		// deadline, so the method must.
		if !call.bounded {
			t.Errorf("%s: listed with no deadline", tc.name)
		}
	}
}

// fakeLister answers as an engine without one, and records which sources were
// actually asked. The mutex is not ceremony: Containers calls List from one
// goroutine per source, so the recording genuinely runs concurrently — `go test
// -race` reports the unguarded version, which is a useful reminder that the
// concurrency under test is real.
type fakeLister struct {
	bySource map[string][]Container
	err      map[string]error

	mu    sync.Mutex
	asked []string
}

func (f *fakeLister) List(_ context.Context, src Source) ([]Container, error) {
	f.mu.Lock()
	f.asked = append(f.asked, src.ID)
	f.mu.Unlock()
	if err := f.err[src.ID]; err != nil {
		return nil, err
	}
	return f.bySource[src.ID], nil
}

// askedSources is the recorded list, safe to read after Containers returns.
func (f *fakeLister) askedSources() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// TestContainersReportsUnlistableSources is the decision that a source which
// cannot be listed is shown with its reason rather than dropped: a stopped
// profile's containers still exist, and hiding the profile makes that
// indistinguishable from owning none.
func TestContainersReportsUnlistableSources(t *testing.T) {
	r := newTestRuntime(testDeps{
		colima: &fakeColima{profiles: []ColimaProfile{
			{Name: "up", Status: "Running", Engine: EngineDocker},
			{Name: "down", Status: "Stopped"},
		}},
	})
	lister := &fakeLister{bySource: map[string][]Container{
		"colima:up": {{ID: "a", Name: "one", Source: "colima:up"}},
	}}
	r.Inventory = lister

	sources, all := r.Containers(context.Background())

	byID := map[string]Source{}
	for _, s := range sources {
		byID[s.ID] = s
	}
	if s, ok := byID["colima:down"]; !ok {
		t.Fatal("the stopped profile is missing from the sources entirely")
	} else if s.Available || !strings.Contains(s.Reason, "colima start -p down") {
		t.Errorf("stopped profile: available=%v reason=%q; want unavailable with the command that fixes it", s.Available, s.Reason)
	}
	if s := byID["colima:up"]; !s.Available {
		t.Errorf("the running profile came back unavailable: %q", s.Reason)
	}
	if slices.Contains(lister.askedSources(), "colima:down") {
		t.Error("listed a stopped profile; devhub must not reach into a VM that is not running")
	}
	if len(all) != 1 || all[0].ID != "a" {
		t.Errorf("containers = %+v, want the one from the running profile", all)
	}
}

// TestContainersKeepsGoingWhenOneSourceFails: one unreachable engine must not
// cost the user the containers on the others, and its reason has to survive to
// the panel.
func TestContainersKeepsGoingWhenOneSourceFails(t *testing.T) {
	r := newTestRuntime(testDeps{
		colima: &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", Engine: EngineDocker}}},
	})
	r.Inventory = &fakeLister{
		bySource: map[string][]Container{"colima:dev": {{ID: "b", Source: "colima:dev"}}},
		err:      map[string]error{ProviderDocker: errors.New("daemon が応答しません")},
	}

	sources, all := r.Containers(context.Background())
	for _, s := range sources {
		if s.ID == ProviderDocker {
			if s.Available || s.Reason != "daemon が応答しません" {
				t.Errorf("failed source: available=%v reason=%q", s.Available, s.Reason)
			}
		}
	}
	if len(all) != 1 || all[0].ID != "b" {
		t.Errorf("containers = %+v, want the working source's row to survive", all)
	}
}

// TestParsePSKeepsNamelessContainers: a container Docker reports with no name
// is still a container, and on a machine with a build history there are
// several. Dropping them, or letting them reach the panel as a blank row, both
// defeat the point of an inventory.
func TestParsePSKeepsNamelessContainers(t *testing.T) {
	got, err := parsePS(namelessRow, "docker")
	if err != nil {
		t.Fatalf("parsePS: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d containers, want 1", len(got))
	}
	if got[0].Name != "" {
		t.Errorf("name = %q; the CLI reported none and that is worth keeping", got[0].Name)
	}
	if got[0].DisplayName() != "a8e2d0ffbfcb" {
		t.Errorf("display name = %q, want the ID to stand in", got[0].DisplayName())
	}
}

// TestAmbientDockerIsCollapsedIntoTheProfileItPointsAt is the case the
// daemon-less verification could not reach: `colima start` makes the profile's
// context current, and a DOCKER_HOST aimed at the profile's socket does the
// same and outlives `colima stop`. Either way the ambient source and the
// profile are one daemon, and every container would otherwise be listed twice.
//
// The detection is by container ID rather than context name on purpose. With
// DOCKER_HOST set, `docker context show` reports "default" while the socket is
// the profile's — name comparison would miss exactly the configuration this
// machine is in.
func TestAmbientDockerIsCollapsedIntoTheProfileItPointsAt(t *testing.T) {
	same := []Container{{ID: "dead", Name: "one"}, {ID: "beef", Name: "two"}}
	r := newTestRuntime(testDeps{
		colima: &fakeColima{profiles: []ColimaProfile{{Name: "default", Status: "Running", Engine: EngineDocker}}},
	})
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		ProviderDocker:   same,
		"colima:default": same,
	}}

	sources, all := r.Containers(context.Background())

	if len(all) != 2 {
		t.Errorf("got %d containers, want 2 — the same daemon was listed twice", len(all))
	}
	byID := map[string]Source{}
	for _, s := range sources {
		byID[s.ID] = s
	}
	if got := byID[ProviderDocker].AliasOf; got != "colima:default" {
		t.Errorf("ambient AliasOf = %q, want colima:default", got)
	}
	if byID["colima:default"].AliasOf != "" {
		t.Error("the profile was folded into the ambient source; it is the more specific of the two and should keep the rows")
	}
	// The rows stay attributed to the profile, which can say which VM they are on.
	for _, c := range all {
		if c.Source == ProviderDocker {
			t.Errorf("container %s still attributed to the folded source", c.ID)
		}
	}
}

// TestDistinctDaemonsAreNotCollapsed: Docker Desktop alongside Colima is two
// real engines, and folding them would hide half the machine.
func TestDistinctDaemonsAreNotCollapsed(t *testing.T) {
	r := newTestRuntime(testDeps{
		colima: &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", Engine: EngineDocker}}},
	})
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		ProviderDocker: {{ID: "aaa", Name: "desktop-one"}},
		"colima:dev":   {{ID: "bbb", Name: "vm-one"}},
	}}

	sources, all := r.Containers(context.Background())
	if len(all) != 2 {
		t.Fatalf("got %d containers, want both", len(all))
	}
	for _, s := range sources {
		if s.AliasOf != "" {
			t.Errorf("%s was folded into %s; the two daemons are unrelated", s.ID, s.AliasOf)
		}
	}
}

// TestCollapseSurvivesAListingThatMovedUnderIt: the two listings are
// concurrent and `ps` prints newest first, so a container created between them
// shifts one list's head. Matching only the first row would miss the overlap
// and show the whole machine twice.
func TestCollapseSurvivesAListingThatMovedUnderIt(t *testing.T) {
	shared := []Container{{ID: "old1"}, {ID: "old2"}}
	r := newTestRuntime(testDeps{
		colima: &fakeColima{profiles: []ColimaProfile{{Name: "default", Status: "Running", Engine: EngineDocker}}},
	})
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		// The ambient listing landed a moment later and picked up a new row.
		ProviderDocker:   append([]Container{{ID: "brandnew"}}, shared...),
		"colima:default": shared,
	}}

	sources, all := r.Containers(context.Background())
	for _, s := range sources {
		if s.ID == ProviderDocker && s.AliasOf == "" {
			t.Error("the ambient source was not collapsed; its first row differed but the rest overlap")
		}
	}
	if len(all) != 2 {
		t.Errorf("got %d containers, want 2", len(all))
	}
}
