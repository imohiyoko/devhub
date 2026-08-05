package execaudit

// The second half of the audit: not "where does devhub spawn a process" — that
// is audit_test.go, and it is airtight — but "who reaches those places".
//
// That half used to live only in Surface.Trigger, as prose, and prose is not
// checked. It drifted twice in a row, the same way both times: PR #167/#168 made
// GET /api/containers and the profile endpoints new callers of `colima list
// --json`, and PR #171 did it again with POST /api/containers/{logs,stop,restart}
// — each time landing with the container-runtime Trigger still naming only the
// env-launcher endpoints. Surface.Callers is that prose made structured; this
// file is what holds it to the code.
//
// Be exact about what "holds it to the code" means, because a ledger that
// promises more than it checks is worse than no ledger at all.
//
// # What these tests detect
//
//   - A Callers entry naming an endpoint the server does not serve — a renamed
//     route, a deleted one, a typo, a method that was never registered.
//     (TestCallersNameEndpointsTheServerServes)
//
//   - An endpoint the server serves that no Surface claims and that is not
//     declared exec-free in execFreeEndpoints below. This is the forcing
//     function: both drifts above added endpoints, and neither could land
//     without someone stating what the new endpoint spawns.
//     (TestEveryServedEndpointIsAccountedFor)
//
//   - A caller of one of the other exec seams in internal/container that is
//     missing from container-runtime. That is the exact shape of both drifts,
//     and it is checkable because it follows from the package's structure
//     rather than from any one call site — see the test for the argument.
//     (TestContainerSeamCallersImplyTheProfileProbe)
//
//   - Callers entries that are malformed, unsorted, duplicated or empty.
//     (TestCallersAreWellFormed)
//
//   - An execFreeEndpoints declaration for a path the server no longer routes.
//     The list is a set of claims exactly as Callers is, so it is held to the
//     same standard: a route that is renamed or deleted must not leave its
//     "this one spawns nothing" behind, still counting toward coverage.
//     (TestExecFreeDeclarationsStillMatchARoute)
//
//   - An endpoint that is both claimed by a Surface and declared exec-free.
//     Coverage is satisfied by either, and stops at the first — so an endpoint
//     written off as inert, which later grows an exec seam and is correctly
//     added to a Surface, would keep a line saying it cannot spawn a process.
//     (TestNoEndpointIsBothClaimedAndExecFree)
//
// # What these tests do NOT detect
//
//   - Under-classification, in general: an endpoint listed under some of the
//     Surfaces it reaches but not all of them. Nothing here derives the
//     endpoint→Surface relation from the code — the entries are written by hand
//     and checked only for existence and for coverage of the route table.
//
//     This is not an oversight, it is the limit of what is reachable from the
//     standard library. Deriving that relation means resolving calls through the
//     consumer-declared interfaces this codebase is built on (Lister, Operator,
//     Adapter, ProfileLister, commandRunner). go/ast carries no type
//     information, so it cannot resolve them at all; a class-hierarchy call
//     graph could, but would answer uselessly here, since a single
//     `runner.Run(...)` on a commandRunner field would appear to reach all four
//     seams in internal/container at once — the concrete runner is pinned by a
//     field initializer in an unexported constructor, which is flow information
//     a hierarchy walk does not have. TestContainerSeamCallersImplyTheProfileProbe
//     closes this hole for the one package where it kept opening, by asserting a
//     property of that package rather than by tracing calls. Nowhere else.
//
//   - The method of an endpoint that only a prefix route serves. `POST
//     /api/envs/switch/apply` is checked to be (a) claimed by a registered POST
//     prefix route and (b) still present as a dispatch literal — but the two
//     halves are checked separately, so moving that literal from the POST switch
//     to the GET switch would not fail here.
//
//   - Anything a Surface claims under a whole prefix. `GET /api/git/*` says
//     every GET under /api/git/ runs git; a new endpoint added to that switch is
//     then covered without review. That is a deliberate trade — the alternative
//     is 24 hand-written git entries — and it is why prefix claims are used only
//     where the whole prefix genuinely shares one surface.
//
//   - Anything an execFreeEndpoints entry covers under a whole prefix — the
//     same hole as above, inverted and more dangerous, because a new route
//     lands pre-classified as inert rather than pre-classified as spawning.
//     /api/db/* used to be one entry and would have absorbed a /api/db/backup
//     that shelled out to mysqldump; it is now its six literal paths, and
//     /api/approval/ likewise. The two prefixes left carry an identifier in the
//     path and cannot be enumerated: /api/settings/tool/* and
//     /api/approval/rules/*. Both name one handler, so the trade is small — but
//     it is the same trade, and it is the reason a prefix here needs a reason.
//
//   - Non-HTTP callers. The "startup:" and "cli:" entries are free text and are
//     checked for shape only; nothing verifies that the boot sequence or the CLI
//     still reaches the surface.
//
//   - A phantom endpoint. isRequestPath treats any identifier spelled `path` as
//     the request path, so an unrelated local compared against an "/api/…"
//     literal would be scanned as a route. The failure is loud rather than
//     silent — the phantom shows up as unaccounted and demands a
//     classification — but the message would name an endpoint nobody serves.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/imohiyoko/devhub/internal/tools"
)

// execFreeEndpoints are the served endpoints that reach no exec surface, with
// the reason each one is inert. An entry here is a claim, the same as a Callers
// entry is: it says this endpoint cannot spawn a process. A trailing "*" covers
// everything under the path.
//
// The list exists so that TestEveryServedEndpointIsAccountedFor can require
// every endpoint to be classified. Adding a route means adding it to a Surface's
// Callers or to this list — which is the whole point: the decision cannot be
// skipped, only recorded.
var execFreeEndpoints = map[string]string{
	"/api/approval/always-allow": "records a standing answer for the approval queue. It gates the surfaces, it is not one.",
	"/api/approval/pending":      "lists the requests waiting for an answer.",
	"/api/approval/respond":      "answers one waiting request.",
	"/api/approval/rules":        "lists the standing answers.",
	"/api/approval/rules/*":      "deletes one standing answer by id. A prefix because the id is in the path.",
	"/api/config":                "the git tool's repo/exclude configuration document, read and written through the store.",
	"/api/db/delete":             "DELETE against a configured database. internal/controllers/database reaches no exec seam — it drives sql.DB.",
	"/api/db/insert":             "INSERT against a configured database, through sql.DB.",
	"/api/db/rows":               "SELECT against a configured database, through sql.DB.",
	"/api/db/search":             "SELECT against a configured database, through sql.DB.",
	"/api/db/tables":             "the table list of a configured database, through sql.DB.",
	"/api/db/update":             "UPDATE against a configured database, through sql.DB.",
	"/api/docs":                  "lists the documentation compiled into the binary. internal/docs reads an embed.FS once at startup and serves out of memory — there is no file to open, let alone a process to start.",
	"/api/docs/*":                "one embedded document. A prefix because the document name is in the path; the handler is a map lookup over the same embed.FS.",
	"/api/envs":                  "the environment definitions document, read and written through the store. Launching one is /api/envs/launch.",
	"/api/envs/launches/remove":  "drops a launch row from the registry. It explicitly never touches worktrees and never kills anything (see removeLaunch).",
	"/api/info":                  "version, pid and build provenance, all held in memory.",
	"/api/ls":                    "directory listing via os.ReadDir; the workspace annotations come from the store, not from git.",
	"/api/ports/label":           "a per-port label kept in the store.",
	"/api/ports/protected":       "the protected-port set kept in the store.",
	"/api/rebuild/status":        "reads the outcome the last POST /api/rebuild recorded.",
	"/api/repos":                 "the configured repository list from the store (AllRepos), with no git invocation.",
	"/api/settings":              "the settings document, read and written through the store.",
	"/api/settings/tool/*":       "a tool's own namespaced settings document.",
	"/api/tools":                 "the dashboard nav, rendered by the gateway from the registry's Metas.",
	"/api/update/status":         "asks GitHub whether a newer release exists. Network, not exec — the exec is in the apply.",
}

// containerSeamPackage is the package whose exec seams share a precondition, and
// profileProbeSurface is the surface that precondition belongs to. See
// TestContainerSeamCallersImplyTheProfileProbe.
const (
	containerSeamPackage = "internal/container"
	profileProbeSurface  = "container-runtime"
)

// caller is one parsed Callers entry.
type caller struct {
	method string // "" for startup:/cli: entries
	path   string // without the trailing "*"
	prefix bool
	http   bool
}

// key renders the caller the way execFreeEndpoints is written, so claims from
// both sources can be matched by one helper.
func (c caller) key() string {
	if c.prefix {
		return c.path + "*"
	}
	return c.path
}

// endpoint is one thing a client can call, as discovered from the code.
type endpoint struct {
	method string
	path   string
	prefix bool
	origin string // for error messages
}

var httpMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodHead: true,
	http.MethodOptions: true,
}

// TestCallersAreWellFormed checks the field's own invariants, so a malformed
// entry cannot slip past the tests that read it.
func TestCallersAreWellFormed(t *testing.T) {
	for _, s := range Registry {
		if len(s.Callers) == 0 {
			t.Errorf("Surface %q: Callers must not be empty — every surface has a way in", s.ID)
			continue
		}
		if !slices.IsSorted(s.Callers) {
			t.Errorf("Surface %q: Callers must be kept sorted; got %v", s.ID, s.Callers)
		}
		seen := map[string]bool{}
		for _, raw := range s.Callers {
			if seen[raw] {
				t.Errorf("Surface %q: duplicate Callers entry %q", s.ID, raw)
			}
			seen[raw] = true
			if _, err := parseCaller(raw); err != nil {
				t.Errorf("Surface %q: %v", s.ID, err)
			}
		}
	}
}

// TestCallersNameEndpointsTheServerServes is the forward direction: an endpoint
// a Surface claims must be one the server actually registers. It catches a route
// that was renamed or removed while the ledger kept naming the old one.
//
// It does not catch the reverse — a new caller the ledger never learned about.
// That is TestEveryServedEndpointIsAccountedFor's job, and only at the coarser
// grain described there.
func TestCallersNameEndpointsTheServerServes(t *testing.T) {
	root := moduleRoot(t)
	routes := registeredEndpoints(t)
	system := systemEndpoints(t, root)
	literals := dispatchLiterals(t, root)

	served := append(append([]endpoint{}, routes...), system...)

	for _, s := range Registry {
		for _, raw := range s.Callers {
			c, err := parseCaller(raw)
			if err != nil || !c.http {
				continue // reported by TestCallersAreWellFormed; non-HTTP is free text
			}
			exact, viaPrefix := matchEndpoint(served, c)
			if !exact && !viaPrefix {
				t.Errorf("Surface %q: Callers entry %q names an endpoint no registered route serves.\n"+
					"  Either the route was renamed or removed (update the entry), or the entry is a typo.",
					s.ID, raw)
				continue
			}
			// Matched only by a prefix route: the prefix proves the request would
			// reach that tool's controller, not that the controller still has a
			// branch for this path. Controllers dispatch sub-paths with
			// `switch r.URL.Path { case "...": }`, so require the literal to
			// still be there.
			if !exact && !c.prefix && !literals[c.path] {
				t.Errorf("Surface %q: Callers entry %q is served only by a prefix route, and %q no longer appears as a dispatch literal.\n"+
					"  The controller's `switch r.URL.Path` has no case for it — the endpoint looks renamed or removed.",
					s.ID, raw, c.path)
			}
		}
	}
}

// TestEveryServedEndpointIsAccountedFor is the reverse direction, and the one
// that would have failed on both of the drifts this file exists for: every
// endpoint the server serves must be claimed by some Surface or declared
// exec-free. A new route cannot land without someone saying what it spawns.
//
// The grain is the path, not the (method, path) pair. Methods are recoverable
// for routes the tool registry declares, but not for the sub-paths controllers
// dispatch internally, and requiring a method there would force phantom entries
// for the verb that does not exist. A path claimed under the wrong method is
// therefore not caught here — TestCallersNameEndpointsTheServerServes catches it
// only when the path is an exact route.
func TestEveryServedEndpointIsAccountedFor(t *testing.T) {
	root := moduleRoot(t)
	routes := registeredEndpoints(t)
	system := systemEndpoints(t, root)
	literals := dispatchLiterals(t, root)

	paths := servedPaths(routes, system, literals)
	claimed := claimedPaths(t)

	var unaccounted []string
	for p, origin := range paths {
		if matchesAny(claimed, p) || matchesAny(execFreeEndpoints, p) {
			continue
		}
		unaccounted = append(unaccounted, p+"  ("+origin+")")
	}
	if len(unaccounted) > 0 {
		slices.Sort(unaccounted)
		t.Errorf("endpoint(s) no Surface claims and no exec-free declaration covers:\n  %s\n"+
			"Add each one to the Callers of every exec Surface it can reach, or to execFreeEndpoints in this file with the reason it spawns nothing.",
			strings.Join(unaccounted, "\n  "))
	}
}

// TestExecFreeDeclarationsStillMatchARoute holds execFreeEndpoints to the same
// standard as Callers.
//
// Both are claims about an endpoint, and the file says so — but only one of them
// was checked. A Callers entry naming a route that no longer exists fails
// TestCallersNameEndpointsTheServerServes; the same mistake in execFreeEndpoints
// used to pass, so a renamed or deleted route left its "this one spawns nothing"
// behind, still counting toward coverage for a path nobody serves.
func TestExecFreeDeclarationsStillMatchARoute(t *testing.T) {
	root := moduleRoot(t)
	routes := registeredEndpoints(t)
	paths := servedPaths(routes, systemEndpoints(t, root), dispatchLiterals(t, root))

	var stale []string
	for key := range execFreeEndpoints {
		if declarationIsCurrent(key, paths, routes) {
			continue
		}
		stale = append(stale, key)
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		t.Errorf("execFreeEndpoints declares path(s) the server does not serve:\n  %s\n"+
			"Remove each one, or fix the path if the route was renamed.", strings.Join(stale, "\n  "))
	}
}

// declarationIsCurrent reports whether an execFreeEndpoints key still describes
// something the server routes.
//
// Two ways to be current, because there are two kinds of endpoint here. Most
// resolve to a concrete path the scan found. A few sit under a prefix route that
// fans out to no dispatch literals at all — /api/settings/tool/ takes the tool
// name from the path rather than switching on a list of them — and servedPaths
// deliberately omits those, since the prefix pattern is not itself an endpoint.
// A declaration covering such a prefix is still current: the server does route
// everything under it.
func declarationIsCurrent(key string, paths map[string]string, routes []endpoint) bool {
	one := map[string]string{key: ""}
	for p := range paths {
		if matchesAny(one, p) {
			return true
		}
	}
	pre, isPrefix := strings.CutSuffix(key, "*")
	if !isPrefix {
		return false
	}
	for _, e := range routes {
		if e.prefix && strings.HasPrefix(pre, e.path) {
			return true
		}
	}
	return false
}

// TestNoEndpointIsBothClaimedAndExecFree closes the other half of the same gap.
//
// TestEveryServedEndpointIsAccountedFor accepts an endpoint that is claimed by a
// Surface *or* declared exec-free, and stops at the first match. So an endpoint
// written off as inert, which later grows an exec seam and is correctly added to
// a Surface's Callers, keeps its stale exec-free line — and the line goes on
// saying the endpoint cannot spawn a process while the ledger next to it says it
// can. Nothing would have complained.
//
// execFreeEndpoints is keyed by path with no method, so this is a per-path rule.
// If devhub ever needs one method of a path to be inert while another spawns,
// this test is what will say so — by failing, which is the outcome that asks for
// a decision instead of hiding one. The fix then is to give the key a method,
// not to delete the check.
func TestNoEndpointIsBothClaimedAndExecFree(t *testing.T) {
	if both := overlappingClaims(claimedPaths(t), execFreeEndpoints); len(both) > 0 {
		t.Errorf("endpoint(s) both claimed by a Surface and declared exec-free:\n  %s\n"+
			"An endpoint that reaches an exec seam is not exec-free. Drop the execFreeEndpoints entry.",
			strings.Join(both, "\n  "))
	}
}

// overlappingClaims is the comparison itself, separated from the scan so it can
// be driven with fixtures. The first version of this could not be: it read the
// real registry, found nothing, and passed — which it also did with the bug
// TestOverlapDetectionSeesPrefixClaims now pins down.
func overlappingClaims(claimed map[string][]string, execFree map[string]string) []string {
	var both []string
	for claim, surfaces := range claimed {
		// A prefix claim is written "/api/x*", and execFree keys are bare
		// paths, so the marker has to come off before they can be compared.
		// Claims carry no method: claimedPaths keys on caller.key().
		if !matchesAny(execFree, strings.TrimSuffix(claim, "*")) {
			continue
		}
		list := slices.Clone(surfaces)
		slices.Sort(list)
		both = append(both, claim+"  (claimed by "+strings.Join(list, ", ")+")")
	}
	slices.Sort(both)
	return both
}

// TestOverlapDetectionSeesPrefixClaims is a regression case for the comparison
// above, which used to strip the "*" only inside a branch that could never run:
// it split the claim on a space to drop a method that claimed keys do not carry.
// Exact claims still matched, so the test passed — for the wrong reason, and
// blind to exactly the shape it was added to catch.
func TestOverlapDetectionSeesPrefixClaims(t *testing.T) {
	for _, tc := range []struct {
		name     string
		claimed  map[string][]string
		execFree map[string]string
		want     bool
	}{
		{"prefix claim over an exact exec-free path",
			map[string][]string{"/api/containers/profiles*": {"colima-profile"}},
			map[string]string{"/api/containers/profiles": "pretend"}, true},
		{"exact claim over an exact exec-free path",
			map[string][]string{"/api/info": {"rebuild"}},
			map[string]string{"/api/info": "pretend"}, true},
		{"exact claim under an exec-free prefix",
			map[string][]string{"/api/db/backup": {"database"}},
			map[string]string{"/api/db/*": "pretend"}, true},
		{"no overlap",
			map[string][]string{"/api/ports/kill": {"ports-kill"}},
			map[string]string{"/api/info": "pretend"}, false},
	} {
		got := overlappingClaims(tc.claimed, tc.execFree)
		if (len(got) > 0) != tc.want {
			t.Errorf("%s: overlap = %v, want detected=%v", tc.name, got, tc.want)
		}
	}

	// Every surface claiming an endpoint is named, not just whichever the map
	// iteration reached last: an error that under-reports who is involved sends
	// the reader to one of several ledgers at random.
	got := overlappingClaims(
		map[string][]string{"/api/containers/logs": {"containers-list", "container-runtime"}},
		map[string]string{"/api/containers/logs": "pretend"})
	if len(got) != 1 || !strings.Contains(got[0], "container-runtime, containers-list") {
		t.Errorf("attribution = %v, want both surfaces named in sorted order", got)
	}
}

// TestContainerSeamCallersImplyTheProfileProbe is the one place a Callers set is
// checked against another Callers set rather than against the route table, and it
// is where both known drifts would have been caught.
//
// The argument is structural, not a traced call path. internal/container has
// four exec seams, and three of them — the inventory listing, the container
// operations and the VM admin — act on a Source or a Profile. Neither can be
// conjured from a request: both are built from what `colima list --json` reports,
// which is the container-runtime seam. So a request that reaches any of those
// three has already caused a profile probe, and container-runtime's Callers must
// be a superset of theirs.
//
// The membership is derived, not listed: any Surface whose exec call sites all
// live in internal/container joins the invariant automatically, so a fifth seam
// added there is covered the day it appears. If such a seam genuinely does not
// need the profile list, this test will say so by failing — a false alarm that
// asks for a decision, rather than silence that hides one.
func TestContainerSeamCallersImplyTheProfileProbe(t *testing.T) {
	root := moduleRoot(t)
	sites := discoverExecCallSites(t, root)

	// Which packages does each surface spawn from?
	pkgs := map[string]map[string]bool{}
	for _, cs := range sites {
		if cs.id == "" {
			continue // reported by TestExecCallSitesAreRegistered
		}
		dir := filepath.ToSlash(filepath.Dir(relPath(root, cs.file)))
		if pkgs[cs.id] == nil {
			pkgs[cs.id] = map[string]bool{}
		}
		pkgs[cs.id][dir] = true
	}

	var seams []string
	for id, dirs := range pkgs {
		if len(dirs) == 1 && dirs[containerSeamPackage] {
			seams = append(seams, id)
		}
	}
	slices.Sort(seams)

	// If the probe surface is not among them the assumption this test rests on
	// has moved; fail loudly rather than vacuously passing.
	if !slices.Contains(seams, profileProbeSurface) {
		t.Fatalf("no Surface %q spawns from %s (found %v); this test's premise no longer holds — revisit it",
			profileProbeSurface, containerSeamPackage, seams)
	}

	probe := map[string]string{}
	for _, s := range Registry {
		if s.ID != profileProbeSurface {
			continue
		}
		for _, raw := range s.Callers {
			if c, err := parseCaller(raw); err == nil && c.http {
				probe[c.key()] = raw
			}
		}
	}

	for _, s := range Registry {
		if s.ID == profileProbeSurface || !slices.Contains(seams, s.ID) {
			continue
		}
		for _, raw := range s.Callers {
			c, err := parseCaller(raw)
			if err != nil || !c.http {
				continue
			}
			if matchesAny(probe, c.path) {
				continue
			}
			t.Errorf("Surface %q lists caller %q, but Surface %q does not.\n"+
				"  Everything in %s acts on a Source or Profile that only `colima list --json` can produce, so this endpoint causes that probe too.\n"+
				"  Add it to %q's Callers (and to its Trigger), or explain in this test why this seam is the exception.",
				s.ID, raw, profileProbeSurface, containerSeamPackage, profileProbeSurface)
		}
	}
}

// claimedPaths collects every path any Surface claims, keyed the way
// execFreeEndpoints is (a trailing "*" means prefix), so both can be matched by
// the same helper.
// servedPaths is every path the server answers on, mapped to where the scan
// found it.
//
// Prefix routes are not endpoints themselves unless the pattern is also a real
// path: /api/containers/profiles is one (it is the create endpoint), /api/git/
// is not. Everything a prefix route fans out to is the set of dispatch literals
// underneath it.
func servedPaths(routes, system []endpoint, literals map[string]bool) map[string]string {
	paths := map[string]string{} // path -> where it came from
	add := func(p, origin string) {
		if _, ok := paths[p]; !ok {
			paths[p] = origin
		}
	}
	for _, e := range routes {
		if !e.prefix || !strings.HasSuffix(e.path, "/") {
			add(e.path, e.origin)
		}
	}
	for _, e := range system {
		add(e.path, e.origin)
	}
	for lit := range literals {
		add(lit, "dispatch literal")
	}
	return paths
}

// claimedPaths maps each claimed endpoint to every Surface that claims it.
//
// A slice rather than one ID because endpoints routinely have several: an
// operation on a container reaches containers-control for what it spawns
// itself, containers-list for the sweep that resolves it, and container-runtime
// for the profile probe underneath that. Keeping only the last one seen would
// make an error message point at one of them at random.
func claimedPaths(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, s := range Registry {
		for _, raw := range s.Callers {
			c, err := parseCaller(raw)
			if err != nil || !c.http {
				continue
			}
			out[c.key()] = append(out[c.key()], s.ID)
		}
	}
	return out
}

// matchesAny reports whether path is covered by one of the keys, where a key
// ending in "*" covers everything with that prefix.
// matchesAny is generic in the value because it only ever reads keys, and the
// two maps it is asked about carry different values: a reason for exec-free
// paths, a list of surfaces for claims.
func matchesAny[V any](keys map[string]V, path string) bool {
	for k := range keys {
		if pre, ok := strings.CutSuffix(k, "*"); ok {
			if strings.HasPrefix(path, pre) {
				return true
			}
			continue
		}
		if k == path {
			return true
		}
	}
	return false
}

// matchEndpoint reports whether any served endpoint matches c exactly, and
// whether any prefix endpoint would route it.
func matchEndpoint(served []endpoint, c caller) (exact, viaPrefix bool) {
	for _, e := range served {
		if e.method != c.method {
			continue
		}
		if c.prefix {
			// A prefix claim must correspond to a prefix route with the same
			// pattern, or it is claiming more than the router grants.
			if e.prefix && e.path == c.path {
				exact = true
			}
			continue
		}
		switch {
		case !e.prefix && e.path == c.path:
			exact = true
		case e.prefix && strings.HasPrefix(c.path, e.path):
			viaPrefix = true
		}
	}
	return exact, viaPrefix
}

// parseCaller parses one Callers entry. See the Callers doc comment in
// registry.go for the grammar.
func parseCaller(raw string) (caller, error) {
	c := caller{}
	switch {
	case strings.HasPrefix(raw, "startup:"), strings.HasPrefix(raw, "cli:"):
		_, rest, _ := strings.Cut(raw, ":")
		if strings.TrimSpace(rest) == "" {
			return c, malformedCaller(raw, "non-HTTP entry needs a description after the colon")
		}
		return c, nil
	}
	method, path, ok := strings.Cut(raw, " ")
	if !ok {
		return c, malformedCaller(raw, `expected "<METHOD> <path>", "startup: <what>" or "cli: <what>"`)
	}
	if !httpMethods[method] {
		return c, malformedCaller(raw, "unknown HTTP method "+strconv.Quote(method))
	}
	if !strings.HasPrefix(path, "/") {
		return c, malformedCaller(raw, "path must start with /")
	}
	if strings.Contains(path, " ") {
		return c, malformedCaller(raw, "path must not contain a space")
	}
	c.http, c.method = true, method
	c.path, c.prefix = strings.CutSuffix(path, "*")
	return c, nil
}

func malformedCaller(raw, why string) error {
	return fmt.Errorf("malformed Callers entry %s: %s", strconv.Quote(raw), why)
}

// registeredEndpoints reads the route table off the real composition root, so
// the guard compares against what the binary serves rather than a copy of it.
// Nil/zero dependencies are enough: constructors only stash them, and Routes()
// never touches them.
func registeredEndpoints(t *testing.T) []endpoint {
	t.Helper()
	var out []endpoint
	for _, tool := range tools.Registry(nil, nil).Tools() {
		id := tool.Meta().ID
		for _, r := range tool.Routes() {
			out = append(out, endpoint{method: r.Method, path: r.Pattern, prefix: r.Prefix, origin: "tool " + id})
		}
	}
	if len(out) == 0 {
		t.Fatal("no routes found in the tool registry; the composition root or core.Route changed shape")
	}
	return out
}

// systemEndpoints finds the endpoints that are not owned by a tool — the process
// and approval routes the server dispatches itself. They are a switch, not a
// table, so they are read out of the source: every `r.Method == http.MethodX &&
// <path> == "/api/..."` pair, in either operand order.
func systemEndpoints(t *testing.T, root string) []endpoint {
	t.Helper()
	var out []endpoint
	forEachGoFile(t, root, func(path string, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			bin, ok := n.(*ast.BinaryExpr)
			if !ok || bin.Op != token.LAND {
				return true
			}
			method, mOK := methodComparison(bin.X)
			p, pOK := pathComparison(bin.Y)
			if !mOK || !pOK {
				method, mOK = methodComparison(bin.Y)
				p, pOK = pathComparison(bin.X)
			}
			if mOK && pOK {
				out = append(out, endpoint{method: method, path: p, origin: relPath(root, path)})
			}
			return true
		})
	})
	if len(out) == 0 {
		t.Fatal("no system endpoints found; the `r.Method == ... && path == ...` dispatch shape changed and this scanner is now blind")
	}
	return out
}

// methodComparison matches `<anything>.Method == http.MethodX` and returns the
// method.
func methodComparison(e ast.Expr) (string, bool) {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok || bin.Op != token.EQL {
		return "", false
	}
	for _, pair := range [2][2]ast.Expr{{bin.X, bin.Y}, {bin.Y, bin.X}} {
		sel, ok := pair[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Method" {
			continue
		}
		lit, ok := pair[1].(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := lit.X.(*ast.Ident)
		if !ok || pkg.Name != "http" || !strings.HasPrefix(lit.Sel.Name, "Method") {
			continue
		}
		m := strings.ToUpper(strings.TrimPrefix(lit.Sel.Name, "Method"))
		if httpMethods[m] {
			return m, true
		}
	}
	return "", false
}

// pathComparison matches `path == "/api/..."` or `r.URL.Path == "/api/..."` and
// returns the literal.
func pathComparison(e ast.Expr) (string, bool) {
	if bin, ok := e.(*ast.BinaryExpr); ok && bin.Op == token.EQL {
		for _, pair := range [2][2]ast.Expr{{bin.X, bin.Y}, {bin.Y, bin.X}} {
			if !isRequestPath(pair[0]) {
				continue
			}
			if s, ok := apiStringLit(pair[1]); ok {
				return s, true
			}
		}
		return "", false
	}
	// The other spelling: strings.HasPrefix(path, "/api/…"), which is how a
	// route carrying an identifier in the path is dispatched — DELETE
	// /api/approval/rules/{id}. Reported with the trailing slash it matched on,
	// so a declaration covering the prefix can find it.
	return hasPrefixPath(e)
}

// hasPrefixPath matches `strings.HasPrefix(<request path>, "/api/…")`.
func hasPrefixPath(e ast.Expr) (string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "HasPrefix" {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "strings" {
		return "", false
	}
	if !isRequestPath(call.Args[0]) {
		return "", false
	}
	return apiStringLit(call.Args[1])
}

// isRequestPath matches the two spellings the codebase uses for the request
// path: the `path` local and `r.URL.Path` itself.
func isRequestPath(e ast.Expr) bool {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name == "path"
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Path" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "URL"
}

// dispatchLiterals collects the sub-paths controllers dispatch on internally:
// the case values of every `switch r.URL.Path` (or `switch path`). Those are the
// endpoints behind a prefix route, which the route table alone cannot show.
func dispatchLiterals(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	forEachGoFile(t, root, func(_ string, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok || sw.Tag == nil || !isRequestPath(sw.Tag) {
				return true
			}
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, v := range cc.List {
					if s, ok := apiStringLit(v); ok {
						out[s] = true
					}
				}
			}
			return true
		})
	})
	if len(out) == 0 {
		t.Fatal("no `switch r.URL.Path` dispatch literals found; controllers changed how they dispatch and this scanner is now blind")
	}
	return out
}

// apiStringLit returns the value of a string literal that looks like an API
// path. Restricting to /api/ keeps page and asset paths out of the endpoint set.
func apiStringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil || !strings.HasPrefix(s, "/api/") {
		return "", false
	}
	return s, true
}

// forEachGoFile parses every non-test .go file in the module, skipping the same
// directories discoverExecCallSites does so the two scanners see one codebase.
func forEachGoFile(t *testing.T, root string, fn func(path string, f *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "node_modules", "testdata", "vendor", "scripts":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		fn(path, f)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
