// Package storage backs all devhub state with a single SQLite file
// ($DEVHUB_HOME/settings/devhub.db) using the pure-Go modernc driver, so the
// binary cross-compiles with CGO disabled.
package storage

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/imohiyoko/devhub/internal/pathutil"

	_ "modernc.org/sqlite"
)

// Store is the application state store. It is safe for concurrent use; the mutex
// only serializes the launch-registry read-modify-write sequence.
type Store struct {
	db          *sql.DB
	assets      fs.FS
	settingsDir string

	// migrationWarnings holds messages for best-effort migrations that failed
	// during Open. Read via MigrationWarnings so the GUI can surface a failure
	// that would otherwise be visible only on stderr.
	migrationWarnings []string

	// RegistryMu serializes load->mutate->save of the launch registry.
	RegistryMu sync.Mutex
}

// execer is satisfied by both *sql.DB and *sql.Tx so kv helpers work inside or
// outside a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Open initializes (and one-time-migrates) the DB under home/settings and seeds
// from the embedded example files in assets.
func Open(home string, assets fs.FS) (*Store, error) {
	settingsDir := filepath.Join(home, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(settingsDir, "devhub.db")
	// PRAGMAs go in the DSN so every pooled connection inherits them (a bare
	// PRAGMA after Open would only affect one connection). The path is a
	// percent-encoded file: URI (pathutil.FileURI): the driver hands a "file:"
	// DSN to SQLite's URI parser, so a home directory containing '#' or '%'
	// would otherwise resolve to the wrong path.
	dsn := pathutil.FileURI(dbPath) + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, assets: assets, settingsDir: settingsDir}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		// Best-effort: a migration failure must not block startup.
		s.recordMigrationWarning("JSON->SQLite migration failed", err)
	}
	if err := s.migrateSettingsRows(); err != nil {
		// Best-effort too: LoadSettings still reads the legacy blob until the
		// explosion into rows succeeds on a later startup.
		s.recordMigrationWarning("settings blob->rows migration failed", err)
	}
	return s, nil
}

// recordMigrationWarning logs a best-effort migration failure to stderr and
// retains it for MigrationWarnings so the dashboard can flag it.
func (s *Store) recordMigrationWarning(what string, err error) {
	msg := fmt.Sprintf("%s: %v", what, err)
	fmt.Fprintln(os.Stderr, "storage: "+msg)
	s.migrationWarnings = append(s.migrationWarnings, msg)
}

// MigrationWarnings returns messages for any best-effort migration that failed
// during Open; it is empty when startup migrations were clean. Such migrations
// don't block startup, so this is how the UI learns a failure happened.
func (s *Store) MigrationWarnings() []string { return s.migrationWarnings }

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE IF NOT EXISTS launches (launch_id TEXT PRIMARY KEY, data TEXT NOT NULL, launched_at TEXT)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func now() string { return time.Now().Format(time.RFC3339) }

// marshalJSON encodes v without HTML escaping.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// kvGet returns the raw JSON stored under key, or nil if absent. It is the
// JSON-typed view over the same bytes Get returns.
func (s *Store) kvGet(key string) (json.RawMessage, error) {
	b, err := s.Get(key)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func kvSet(e execer, key string, value any) error {
	b, err := marshalJSON(value)
	if err != nil {
		return err
	}
	_, err = e.Exec(
		`INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(b), now(),
	)
	return err
}

// LoadAIRules returns the raw JSON of AI rules.
func (s *Store) LoadAIRules() (json.RawMessage, error) {
	return s.kvGet("ai_rules")
}

// SaveAIRules saves the raw JSON of AI rules.
func (s *Store) SaveAIRules(data json.RawMessage) error {
	return kvSet(s.db, "ai_rules", data)
}
