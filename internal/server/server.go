// Package server wires the embedded frontend, security middleware and
// controllers into an HTTP server bound to 127.0.0.1. It ports the routing,
// token-injection and restart behavior of server.py.
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

	databasectl "github.com/imohiyoko/devhub/internal/controllers/database"
	envsctl "github.com/imohiyoko/devhub/internal/controllers/envs"
	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
	settingsctl "github.com/imohiyoko/devhub/internal/controllers/settings"
	workspacectl "github.com/imohiyoko/devhub/internal/controllers/workspace"
	"github.com/imohiyoko/devhub/internal/platform"
	"github.com/imohiyoko/devhub/internal/storage"
)

// Server holds process-lifetime state: the per-session token, the bound port,
// the host allowlist, pre-injected static pages and the controllers.
type Server struct {
	store    *storage.Store
	settings map[string]any
	version  string

	token         string
	port          int
	allowedHosts  map[string]bool
	staticByRoute map[string][]byte
	openBrowser   bool

	httpSrv *http.Server

	settingsCtl  *settingsctl.Controller
	gitCtl       *gitctl.Controller
	workspaceCtl *workspacectl.Controller
	portsCtl     *portsctl.Controller
	databaseCtl  *databasectl.Controller
	envsCtl      *envsctl.Controller
}

// New builds a Server: resolves the token (inheriting DEVHUB_API_TOKEN across a
// restart), the port, the host allowlist, pre-injects the token shim into each
// static page, and constructs the controllers.
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

	script := buildTokenScript(s.token)
	s.staticByRoute = make(map[string][]byte, len(routeFiles))
	for route, file := range routeFiles {
		b, err := fs.ReadFile(assets, file)
		if err != nil {
			return nil, fmt.Errorf("embed read %s: %w", file, err)
		}
		s.staticByRoute[route] = injectToken(b, script)
	}

	s.settingsCtl = settingsctl.New(store)
	s.gitCtl = gitctl.New(store)
	s.workspaceCtl = workspacectl.New(store, s.gitCtl)
	s.portsCtl = portsctl.New(store)
	s.databaseCtl = databasectl.New(store)
	s.envsCtl = envsctl.New(store, s.gitCtl, s.portsCtl, s.workspaceCtl)
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
	fmt.Printf("  platform : %s\n", platform.PyName())
	editor := "code"
	if e, ok := s.settings["editor"].(string); ok && e != "" {
		editor = e
	}
	fmt.Printf("  editor   : %s\n", editor)
	if term, ok := s.settings["terminal"].(map[string]any); ok {
		if sys, ok := term[platform.PyName()].(map[string]any); ok {
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
