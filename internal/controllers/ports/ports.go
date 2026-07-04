// Package ports implements the open-TCP-port endpoints (/api/ports and
// /api/ports/{label,protected,kill}).
package ports

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/platform"
	"github.com/imohiyoko/devhub/internal/portutil"
)

var listenNameRe = regexp.MustCompile(`(?:TCP\s+)?(.+):(\d+)\s+\(LISTEN\)$`)

// PortEntry is one listening socket as reported to the frontend.
type PortEntry struct {
	Command   string `json:"command"`
	PID       int    `json:"pid"`
	User      string `json:"user"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Label     string `json:"label"`
	Self      bool   `json:"self"`
	Protected bool   `json:"protected"`
}

type rawPort struct {
	command string
	pid     int
	user    string
	host    string
	port    int
}

// settingsStore is the narrow persistence the ports controller needs: it reads
// and writes the shared settings document, where port_labels and protected_ports
// live (in the settings allowlist). It does not own a private keyspace, so it
// depends on these typed helpers, not the raw key/value seam. *storage.Store
// satisfies it.
type settingsStore interface {
	LoadSettings() (map[string]any, error)
	SaveSettings(patch map[string]any) error
}

// Controller serves port endpoints backed by the store (labels/protected list).
type Controller struct{ store settingsStore }

// New returns a ports controller.
func New(store settingsStore) *Controller { return &Controller{store: store} }

func (c *Controller) portLabels() map[string]string {
	settings, _ := c.store.LoadSettings()
	out := map[string]string{}
	if m, ok := settings["port_labels"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func (c *Controller) protectedPorts() []int {
	settings, _ := c.store.LoadSettings()
	pl, _ := portutil.NormalizePortList(settings["protected_ports"], false)
	return pl
}

// listOpen returns all listening ports annotated with label/self/protected,
// sorted by (port, pid).
func (c *Controller) listOpen() ([]PortEntry, error) {
	raw, err := listRaw()
	if err != nil {
		return nil, err
	}
	labels := c.portLabels()
	protected := map[int]bool{}
	for _, p := range c.protectedPorts() {
		protected[p] = true
	}
	self := os.Getpid()
	out := make([]PortEntry, 0, len(raw))
	for _, r := range raw {
		out = append(out, PortEntry{
			Command:   r.command,
			PID:       r.pid,
			User:      r.user,
			Host:      r.host,
			Port:      r.port,
			Label:     labels[strconv.Itoa(r.port)],
			Self:      r.pid == self,
			Protected: protected[r.port],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].PID < out[j].PID
	})
	return out, nil
}

func listRaw() ([]rawPort, error) {
	if platform.IsWindows() {
		return listWindows()
	}
	return listUnix()
}

func listUnix() ([]rawPort, error) {
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output() //execaudit:ports-list
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("lsof command was not found. Please install lsof (e.g. `sudo apt install lsof`).")
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// lsof returns 1 when some handles are inaccessible but still prints.
			if ee.ExitCode() != 1 {
				return nil, fmt.Errorf("%s", errMsg(ee, "failed to list ports"))
			}
		} else {
			return nil, err
		}
	}
	return parseLsof(string(out)), nil
}

// parseLsof parses `lsof -nP -iTCP -sTCP:LISTEN` output (header line skipped).
func parseLsof(out string) []rawPort {
	var ports []rawPort
	for i, line := range strings.Split(out, "\n") {
		if i == 0 { // header
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 9 {
			continue
		}
		m := listenNameRe.FindStringSubmatch(strings.Join(parts[7:], " "))
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		ports = append(ports, rawPort{
			command: strings.ReplaceAll(parts[0], `\x20`, " "),
			pid:     pid,
			user:    parts[2],
			host:    m[1],
			port:    port,
		})
	}
	return ports
}

func listWindows() ([]rawPort, error) {
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output() //execaudit:ports-list
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("%s", errMsg(ee, "failed to list ports"))
		}
		return nil, err
	}
	return parseNetstat(string(out)), nil
}

// parseNetstat parses `netstat -ano -p tcp` output, keeping LISTENING rows.
func parseNetstat(out string) []rawPort {
	var ports []rawPort
	for line := range strings.SplitSeq(out, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 5 || strings.ToUpper(parts[0]) != "TCP" || strings.ToUpper(parts[3]) != "LISTENING" {
			continue
		}
		address := parts[1]
		idx := strings.LastIndex(address, ":")
		if idx < 0 {
			continue
		}
		host, portStr := address[:idx], address[idx+1:]
		pid, err := strconv.Atoi(parts[4])
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		ports = append(ports, rawPort{command: "", pid: pid, user: "", host: host, port: port})
	}
	return ports
}

func errMsg(ee *exec.ExitError, fallback string) string {
	if s := strings.TrimSpace(string(ee.Stderr)); s != "" {
		return s
	}
	return fallback
}

// ListOpen returns all listening ports (exported for the env-launcher).
func (c *Controller) ListOpen() ([]PortEntry, error) { return c.listOpen() }

// KillPortProcess kills the process on port/pid after the usual safety checks
// (exported for the env-launcher).
func (c *Controller) KillPortProcess(port, pid int) error { return c.killPortProcess(port, pid) }

// ListListening returns the LISTEN sockets without the store-backed
// annotations (labels / protected / self are zero). It exists for the CLI
// (cmd/devhub), which needs listener discovery even when the settings store
// cannot be opened — e.g. `devhub status` while the DB is unreadable.
func ListListening() ([]PortEntry, error) {
	raw, err := listRaw()
	if err != nil {
		return nil, err
	}
	out := make([]PortEntry, 0, len(raw))
	for _, r := range raw {
		out = append(out, PortEntry{Command: r.command, PID: r.pid, User: r.user, Host: r.host, Port: r.port})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].PID < out[j].PID
	})
	return out, nil
}

// KillPID terminates pid with none of killPortProcess's safety checks
// (protected ports, self-PID, port ownership). Those checks protect *other*
// applications and the serving process itself; `devhub stop` targets a devhub
// instance it has just verified via /ai-api/info, where they would wrongly
// refuse. Callers must do such verification — never expose this to a request
// path.
func KillPID(pid int) error { return killProcess(pid) }

// HandleGet serves GET /api/ports.
func (c *Controller) HandleGet(w http.ResponseWriter, _ *http.Request) error {
	list, err := c.listOpen()
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ports":           list,
		"protected_ports": c.protectedPorts(),
	})
	return nil
}

// HandlePost serves POST /api/ports/{label,protected,kill}.
func (c *Controller) HandlePost(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	switch r.URL.Path {
	case "/api/ports/label":
		port, ok := coerceInt(data["port"])
		if !ok {
			return httpx.Errorf(http.StatusBadRequest, "invalid port")
		}
		label, _ := data["label"].(string)
		if err := c.savePortLabel(port, label); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "/api/ports/protected":
		normalized, err := portutil.NormalizePortList(data["ports"], true)
		if err != nil {
			return err
		}
		if err := c.store.SaveSettings(map[string]any{"protected_ports": normalized}); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "protected_ports": normalized})
	case "/api/ports/kill":
		port, ok1 := coerceInt(data["port"])
		pid, ok2 := coerceInt(data["pid"])
		if !ok1 || !ok2 {
			return httpx.Errorf(http.StatusBadRequest, "invalid port or pid")
		}
		if err := c.killPortProcess(port, pid); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	return nil
}

func (c *Controller) savePortLabel(port int, label string) error {
	labels := c.portLabels()
	key := strconv.Itoa(port)
	label = strings.TrimSpace(label)
	if label != "" {
		labels[key] = label
	} else {
		delete(labels, key)
	}
	// Convert to map[string]any for storage.
	out := make(map[string]any, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return c.store.SaveSettings(map[string]any{"port_labels": out})
}

func (c *Controller) killPortProcess(port, pid int) error {
	if slices.Contains(c.protectedPorts(), port) {
		return httpx.Errorf(http.StatusForbidden, "port %d is protected", port)
	}
	if pid == os.Getpid() {
		return httpx.Errorf(http.StatusForbidden, "devhub itself cannot be killed from this tool")
	}
	list, err := c.listOpen()
	if err != nil {
		return err
	}
	found := false
	for _, e := range list {
		if e.Port == port && e.PID == pid {
			found = true
			break
		}
	}
	if !found {
		return httpx.Errorf(http.StatusNotFound, "port process was not found")
	}
	return killProcess(pid)
}

// coerceInt accepts a JSON number or numeric string.
func coerceInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}
