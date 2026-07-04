// Package database implements the db-table endpoints (/api/db/*). Both engines
// use database/sql with pure-Go drivers: SQLite via modernc, MySQL/MariaDB via
// go-sql-driver — so the binary stays dependency-free and cross-compiles with
// CGO disabled.
package database

import (
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/pathutil"
	"github.com/imohiyoko/devhub/internal/sanitize"
	"github.com/imohiyoko/devhub/internal/storage"
)

// Controller serves db-table endpoints. It reads db_local_only from settings.
type Controller struct{ store *storage.Store }

// New returns a database controller.
func New(store *storage.Store) *Controller { return &Controller{store: store} }

// connProfile is a normalized connection (sqlite path or mysql coordinates).
type connProfile struct {
	driver   string
	path     string
	host     string
	port     int
	user     string
	password string
	database string
	raw      map[string]any // normalized profile, for the sanitized response
}

func (p *connProfile) sanitized() map[string]any { return sanitize.DBConnection(p.raw) }

func sqliteDBPath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("database path is required")
	}
	path := pathutil.AbsExpand(raw)
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return "", fmt.Errorf("database file was not found")
	}
	return path, nil
}

func (c *Controller) isLocalDBHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" || h == "localhost.localdomain" {
		return true
	}
	return isLoopbackIP(h)
}

func (c *Controller) ensureLocalDBHost(host string) error {
	settings, _ := c.store.LoadSettings()
	localOnly := true
	if b, ok := settings["db_local_only"].(bool); ok {
		localOnly = b
	}
	if localOnly && !c.isLocalDBHost(host) {
		return fmt.Errorf("external database hosts are disabled; use localhost, 127.0.0.1, or ::1")
	}
	return nil
}

// connectionFromPayload normalizes a request into a connProfile (mirrors
// connection_from_payload).
func (c *Controller) connectionFromPayload(data map[string]any) (*connProfile, error) {
	profileMap, _ := data["connection"].(map[string]any)
	if len(profileMap) == 0 {
		profileMap = map[string]any{}
	}
	if p, ok := data["path"].(string); ok && p != "" {
		if _, hasDriver := profileMap["driver"]; !hasDriver {
			profileMap = map[string]any{"driver": "sqlite", "path": p}
		}
	}

	driver := "sqlite"
	if d, ok := profileMap["driver"].(string); ok && d != "" {
		driver = strings.ToLower(d)
	}
	if driver == "mariadb" {
		driver = "mysql"
	}
	if driver != "sqlite" && driver != "mysql" {
		return nil, fmt.Errorf("unsupported database driver")
	}

	raw := map[string]any{}
	maps.Copy(raw, profileMap)
	raw["driver"] = driver

	prof := &connProfile{driver: driver, raw: raw}
	if driver == "sqlite" {
		rawPath, _ := profileMap["path"].(string)
		path, err := sqliteDBPath(rawPath)
		if err != nil {
			return nil, err
		}
		prof.path = path
		raw["path"] = path
		return prof, nil
	}

	// mysql
	prof.host = strData(profileMap, "host")
	if prof.host == "" {
		prof.host = "127.0.0.1"
	}
	if err := c.ensureLocalDBHost(prof.host); err != nil {
		return nil, err
	}
	prof.port = 3306
	if v, ok := toInt(profileMap["port"]); ok && v != 0 {
		prof.port = v
	}
	prof.user = strData(profileMap, "user")
	prof.password = strData(profileMap, "password")
	prof.database = strData(profileMap, "database")
	if prof.database == "" {
		return nil, fmt.Errorf("database name is required")
	}
	raw["host"] = prof.host
	raw["port"] = prof.port
	raw["user"] = prof.user
	raw["password"] = prof.password
	raw["database"] = prof.database
	return prof, nil
}

// HandleGet serves GET /api/db/tables and /api/db/rows (sqlite path via query).
func (c *Controller) HandleGet(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	switch r.URL.Path {
	case "/api/db/tables":
		prof, err := c.connectionFromPayload(map[string]any{"path": q.Get("path")})
		if err != nil {
			return err
		}
		tables, err := c.dbTables(prof)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connection": prof.sanitized(), "tables": tables})
		return nil
	case "/api/db/rows":
		prof, err := c.connectionFromPayload(map[string]any{"path": q.Get("path")})
		if err != nil {
			return err
		}
		res, err := c.dbRows(prof, q.Get("table"), clampLimit(q.Get("limit")), clampOffset(q.Get("offset")), q.Get("search"))
		if err != nil {
			return err
		}
		res["connection"] = prof.sanitized()
		httpx.WriteJSON(w, http.StatusOK, res)
		return nil
	}
	return httpx.Errorf(http.StatusNotFound, "not found")
}

// HandlePost serves POST /api/db/{tables,rows,search,update,insert,delete}.
func (c *Controller) HandlePost(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	prof, err := c.connectionFromPayload(data)
	if err != nil {
		return err
	}
	switch r.URL.Path {
	case "/api/db/tables":
		tables, err := c.dbTables(prof)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connection": prof.sanitized(), "tables": tables})
	case "/api/db/rows":
		res, err := c.dbRows(prof, strData(data, "table"), clampLimitN(data["limit"]), clampOffsetN(data["offset"]), strData(data, "search"))
		if err != nil {
			return err
		}
		res["connection"] = prof.sanitized()
		httpx.WriteJSON(w, http.StatusOK, res)
	case "/api/db/search":
		res, err := c.dbSearch(prof, strData(data, "columnSearch"), strData(data, "elementSearch"))
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, res)
	case "/api/db/update":
		if err := c.dbUpdate(prof, strData(data, "table"), strData(data, "column"), data["key"], data["value"]); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "/api/db/insert":
		lastID, err := c.dbInsert(prof, strData(data, "table"))
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "lastrowid": lastID})
	case "/api/db/delete":
		if err := c.dbDelete(prof, strData(data, "table"), data["key"]); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	return nil
}

// --- shared SQL/text helpers ---

func quoteIdentifier(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("invalid identifier")
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
}

func mysqlIdentifier(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("invalid identifier")
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
}

// normalizeValue mirrors normalize_sqlite_value: bytes -> "0x"+hex, else as-is.
func normalizeValue(v any) any {
	if b, ok := v.([]byte); ok {
		const hexd = "0123456789abcdef"
		out := make([]byte, 0, 2+len(b)*2)
		out = append(out, '0', 'x')
		for _, c := range b {
			out = append(out, hexd[c>>4], hexd[c&0x0f])
		}
		return string(out)
	}
	return v
}

func normalizeSearch(v string) string {
	s := strings.TrimSpace(v)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func escapeLikePattern(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "%", `\%`)
	v = strings.ReplaceAll(v, "_", `\_`)
	return v
}

func isLoopbackIP(h string) bool {
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// --- small value coercion helpers ---

func strData(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i, true
		}
	}
	return 0, false
}

func clampLimit(s string) int  { return clamp(parseIntDefault(s, 100), 1, 500) }
func clampOffset(s string) int { return maxInt(parseIntDefault(s, 0), 0) }
func clampLimitN(v any) int {
	n, ok := toInt(v)
	if !ok {
		n = 100
	}
	return clamp(n, 1, 500)
}
func clampOffsetN(v any) int {
	n, ok := toInt(v)
	if !ok {
		n = 0
	}
	return maxInt(n, 0)
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	return def
}

func clamp(v, lo, hi int) int { return maxInt(lo, minInt(v, hi)) }
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
