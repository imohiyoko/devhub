// Package server wires the embedded frontend, security middleware and the tool
// registry into an HTTP server bound to 127.0.0.1. It owns the security gate,
// per-session token injection, the dashboard root page and process restart;
// all tool routing is delegated to the core gateway.
package server

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/approval"
	"github.com/imohiyoko/devhub/internal/core"
	docspkg "github.com/imohiyoko/devhub/internal/docs"
	"github.com/imohiyoko/devhub/internal/platform"
	"github.com/imohiyoko/devhub/internal/reqlog"
	"github.com/imohiyoko/devhub/internal/storage"
	"github.com/imohiyoko/devhub/internal/tools"
)

// Server holds process-lifetime state: the per-session token, the bound port,
// the host allowlist, the token-injected pages and the core gateway.
type Server struct {
	store    *storage.Store
	settings map[string]any
	version  string
	edition  string

	token        string
	agentToken   string
	port         int
	allowedHosts map[string]bool
	openBrowser  bool

	// repoRoot is the directory that contains go.mod, used by the rebuild
	// handler to run `go build` / `go run` from the correct working directory.
	// Empty string means rebuild is unavailable (distributed binary with no
	// source tree alongside it).
	repoRoot string

	// dashboard is the token-injected root page ("/"); toolPages holds each
	// tool's token-injected page keyed by tool ID (served by the gateway).
	dashboard []byte

	// shared holds embedded static assets under shared/ (e.g. reorder.js),
	// keyed by URL path ("/shared/reorder.js"). Served by serveSystem; not
	// token-injected (these are plain JS, not HTML pages).
	shared map[string][]byte

	// toolAssets holds embedded per-tool static assets under tools/<tool>/ (the
	// CSS/JS a tool page splits its inline code into), keyed by URL path
	// ("/tools/git/git.css"). Served by serveSystem exactly like shared/ and not
	// token-injected. index.html files are deliberately excluded: those are the
	// token-injected pages served by the gateway and must never be served raw.
	toolAssets map[string][]byte

	httpSrv *http.Server

	// gateway dispatches every tool (registry-driven) plus GET /api/tools.
	// Requests it doesn't claim fall through to serveSystem (root page, /api/info,
	// /api/restart, SPA redirect).
	gateway *core.Gateway

	approvalMgr *approval.Manager

	// instance is a fresh random id minted for this Server. It changes on every
	// (re)start — including a rebuild that re-execs or spawns a replacement — so
	// the frontend can detect "the server I'm talking to is a new process" by
	// comparing /api/info's `instance` against the value it captured before the
	// rebuild, instead of trying to catch the transient down-window (which a fast
	// `go run` restart can slip through entirely, leaving the UI polling forever).
	//
	// It also labels rlog's entries, which is why it is minted per Server rather
	// than once per process: seq restarts at 1 with every ring, so two rings
	// sharing one id would make two different requests indistinguishable in the
	// archive — and the second would be silently discarded as a duplicate.
	instance string

	// rlog is this server's request log. It is in-memory and dies with the
	// process by design (see internal/reqlog); the logs tool is what copies
	// anything worth keeping into the store.
	rlog *reqlog.Ring
}

// New builds a Server: resolves the token (inheriting DEVHUB_API_TOKEN across a
// restart), the port and host allowlist, builds the tool registry, and injects
// the token shim into the dashboard and every tool page.
func New(store *storage.Store, assets, docsFS fs.FS, settings map[string]any, noBrowser bool, version string) (*Server, error) {
	s := &Server{store: store, settings: settings, version: version, edition: platform.Edition(version)}
	s.repoRoot = findRepoRoot()

	s.token = os.Getenv("DEVHUB_API_TOKEN")
	if s.token == "" {
		s.token = generateToken()
	}
	// Drop it from the environment so launched child processes (editor,
	// terminal) never inherit the API token.
	_ = os.Unsetenv("DEVHUB_API_TOKEN")
	agentToken, err := store.AgentToken()
	if err != nil {
		return nil, fmt.Errorf("load ai-api token: %w", err)
	}
	s.agentToken = agentToken

	s.port = 8765
	if p, ok := toInt(settings["port"]); ok {
		s.port = p
	}
	if env := os.Getenv("DEVHUB_PORT"); env != "" {
		if p, err := strconv.Atoi(env); err == nil {
			s.port = p
		}
	}

	s.allowedHosts = map[string]bool{
		fmt.Sprintf("localhost:%d", s.port): true,
		fmt.Sprintf("127.0.0.1:%d", s.port): true,
		fmt.Sprintf("[::1]:%d", s.port):     true,
	}
	if s.port == 80 || s.port == 443 {
		s.allowedHosts["localhost"] = true
		s.allowedHosts["127.0.0.1"] = true
		s.allowedHosts["[::1]"] = true
	}

	openByCfg := true
	if b, ok := settings["open_browser_on_start"].(bool); ok {
		openByCfg = b
	}
	s.openBrowser = openByCfg && !noBrowser

	// Build the registry, then derive everything else from it: each tool's page
	// is read from the embed FS and token-injected, keyed by tool ID for the
	// gateway's pageFn. The dashboard root is the one page that is not a tool.
	// Parsed once at boot, not per request: a malformed doc should stop startup
	// here rather than surface on the first agent that asks for one.
	docSet, err := docspkg.Load(docsFS)
	if err != nil {
		return nil, fmt.Errorf("load docs: %w", err)
	}
	// The instance id scopes archived entries to the run they came from, so a
	// seq is never ambiguous across restarts. The ring carries it: everything
	// downstream reads it from there, so there is no second copy to fall out of
	// step with the counter.
	s.instance = generateToken()
	s.rlog = reqlog.New(reqlog.Capacity, s.instance)
	reg := tools.Registry(store, docSet, s.rlog)
	script := buildTokenScript(s.token)

	toolPages := map[string][]byte{}
	for _, t := range reg.Tools() {
		m := t.Meta()
		if m.Page == "" {
			continue
		}
		b, err := fs.ReadFile(assets, m.Page)
		if err != nil {
			return nil, fmt.Errorf("embed read %s: %w", m.Page, err)
		}
		toolPages[m.ID] = injectToken(b, script)
	}

	db, err := fs.ReadFile(assets, "dashboard/index.html")
	if err != nil {
		return nil, fmt.Errorf("embed read dashboard/index.html: %w", err)
	}
	s.dashboard = injectToken(db, script)

	// Pre-read shared/ static assets (JS + theme.css shared across tool pages)
	// keyed by URL path, mirroring how tool pages are cached. No token injection.
	s.shared = map[string][]byte{}
	if err := fs.WalkDir(assets, "shared", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(assets, p)
		if err != nil {
			return fmt.Errorf("embed read %s: %w", p, err)
		}
		s.shared["/"+p] = b
		return nil
	}); err != nil {
		return nil, err
	}

	// Pre-read tools/ sub-assets (the CSS/JS that tool pages reference via
	// <link>/<script src>) keyed by URL path, mirroring shared/. index.html files
	// are skipped: the gateway serves those token-injected, so serving them raw
	// here would leak an un-shimmed page.
	s.toolAssets = map[string][]byte{}
	if err := fs.WalkDir(assets, "tools", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if path.Base(p) == "index.html" {
			return nil
		}
		b, err := fs.ReadFile(assets, p)
		if err != nil {
			return fmt.Errorf("embed read %s: %w", p, err)
		}
		s.toolAssets["/"+p] = b
		return nil
	}); err != nil {
		return nil, err
	}

	pageFn := func(toolID string) ([]byte, bool) {
		b, ok := toolPages[toolID]
		return b, ok
	}
	// Upstreams is nil: every tool runs in-process (single binary). Point an
	// entry at a URL to extract that tool to its own service — no code change.
	s.gateway = core.NewGateway(reg, nil, pageFn)
	s.gateway.Next = http.HandlerFunc(s.serveSystem)
	s.approvalMgr = approval.NewManager(store)
	return s, nil
}

// Run binds the listener (retrying briefly to tolerate a restart releasing the
// port) and serves until the process is replaced or the server is closed.
func (s *Server) Run() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	var ln net.Listener
	var err error
	deadline := time.Now().Add(2 * time.Second)
	reclaimed := false
	for {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		// A previous devhub instance may still own the port — most often an
		// orphaned process left behind by a failed rebuild (e.g. the compiled
		// child of `go run` whose parent was killed). Reclaim it once, then keep
		// retrying so the freed port can be bound. reclaimStaleDevhubPort only
		// ever kills a process named "devhub", never an unrelated application,
		// so a genuine port clash with another tool still surfaces as an error.
		if !reclaimed {
			if pid := reclaimStaleDevhubPort(s.port); pid != 0 {
				fmt.Fprintf(os.Stderr, "devhub: reclaimed port %d from a stale instance (pid %d)\n", s.port, pid)
				reclaimed = true
				deadline = time.Now().Add(2 * time.Second)
				continue
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	s.httpSrv = &http.Server{Handler: s}

	if s.openBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			browserOpen(fmt.Sprintf("http://localhost:%d", s.port))
		}()
	}

	s.printBanner()

	if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) printBanner() {
	fmt.Printf("devhub → http://localhost:%d  (Ctrl+C to quit)\n", s.port)
	fmt.Printf("  platform : %s\n", platform.SystemName())
	editor := "code"
	if e, ok := s.settings["editor"].(string); ok && e != "" {
		editor = e
	}
	fmt.Printf("  editor   : %s\n", editor)
	if term, ok := s.settings["terminal"].(map[string]any); ok {
		if sys, ok := term[platform.SystemName()].(map[string]any); ok {
			emu, _ := sys["emulator"].(string)
			sh, _ := sys["shell"].(string)
			if emu != "" || sh != "" {
				fmt.Printf("  terminal : %s / %s\n", orQ(emu), orQ(sh))
			}
		}
	}
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i, true
		}
	}
	return 0, false
}
