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
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/approval"
	"github.com/imohiyoko/devhub/internal/core"
	"github.com/imohiyoko/devhub/internal/platform"
	"github.com/imohiyoko/devhub/internal/storage"
	"github.com/imohiyoko/devhub/internal/tools"
)

// Server holds process-lifetime state: the per-session token, the bound port,
// the host allowlist, the token-injected pages and the core gateway.
type Server struct {
	store    *storage.Store
	settings map[string]any
	version  string

	token        string
	port         int
	allowedHosts map[string]bool
	openBrowser  bool

	// dashboard is the token-injected root page ("/"); toolPages holds each
	// tool's token-injected page keyed by tool ID (served by the gateway).
	dashboard []byte

	// shared holds embedded static assets under shared/ (e.g. reorder.js),
	// keyed by URL path ("/shared/reorder.js"). Served by serveSystem; not
	// token-injected (these are plain JS, not HTML pages).
	shared map[string][]byte

	httpSrv *http.Server

	// gateway dispatches every tool (registry-driven) plus GET /api/tools.
	// Requests it doesn't claim fall through to serveSystem (root page, /api/info,
	// /api/restart, SPA redirect).
	gateway *core.Gateway

	approvalMgr *approval.Manager
}

// New builds a Server: resolves the token (inheriting DEVHUB_API_TOKEN across a
// restart), the port and host allowlist, builds the tool registry, and injects
// the token shim into the dashboard and every tool page.
func New(store *storage.Store, assets fs.FS, settings map[string]any, noBrowser bool, version string) (*Server, error) {
	s := &Server{store: store, settings: settings, version: version}

	s.token = os.Getenv("DEVHUB_API_TOKEN")
	if s.token == "" {
		s.token = generateToken()
	}
	// Drop it from the environment so launched child processes (editor,
	// terminal) never inherit the API token.
	_ = os.Unsetenv("DEVHUB_API_TOKEN")

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
	reg := tools.Registry(store)
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

	// Pre-read shared/ static assets (plain JS shared across tool pages) keyed
	// by URL path, mirroring how tool pages are cached. No token injection.
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
	for {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
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
