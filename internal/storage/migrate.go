package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// migrate performs the one-time import of legacy settings/*.json into SQLite,
// guarded by meta.migrated. It reads from the on-disk settings dir (not the
// embedded examples) so a user upgrading from the Python app keeps their state.
// An upgrade that reuses the same ~/.devhub already has meta.migrated set by the
// Python app, so this is a no-op there.
func (s *Store) migrate() error {
	var v string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = 'migrated'").Scan(&v)
	if err == nil {
		return nil // already migrated
	}
	if err != sql.ErrNoRows {
		return err
	}

	// 1) Config-shaped docs + the migrated flag commit together.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, m := range []struct{ key, file string }{
		{"config", "config.json"},
		{"settings", "server.json"},
		{"envs", "envs.json"},
	} {
		if doc, ok := s.readDiskJSON(m.file); ok {
			if err := kvSet(tx, m.key, doc); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(s.settingsDir, "tools")); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".example.json") {
				continue
			}
			if doc, ok := s.readDiskJSONPath(filepath.Join(s.settingsDir, "tools", name)); ok {
				toolID := strings.TrimSuffix(name, ".json")
				if err := kvSet(tx, "tool:"+toolID, doc); err != nil {
					_ = tx.Rollback()
					return err
				}
			}
		}
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('migrated', '1')"); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// 2) Launches: separate transaction, skip individual bad records.
	if doc, ok := s.readDiskJSONPath(filepath.Join(s.settingsDir, "launches.json")); ok {
		if m, ok := doc.(map[string]any); ok {
			if list, ok := m["launches"].([]any); ok {
				s.importLaunches(list)
			}
		}
	}
	return nil
}

func (s *Store) readDiskJSON(file string) (any, bool) {
	return s.readDiskJSONPath(filepath.Join(s.settingsDir, file))
}

func (s *Store) readDiskJSONPath(path string) (any, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	return v, true
}

func (s *Store) importLaunches(list []any) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	for _, item := range list {
		rec, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := rec["launch_id"].(string)
		if id == "" {
			continue
		}
		data, err := marshalJSON(rec)
		if err != nil {
			continue
		}
		launchedAt, _ := rec["launched_at"].(string)
		_, _ = tx.Exec(
			"INSERT OR REPLACE INTO launches (launch_id, data, launched_at) VALUES (?, ?, ?)",
			id, string(data), launchedAt,
		)
	}
	if err := tx.Commit(); err != nil {
		// Best-effort migration: don't fail startup, but make the failure visible.
		fmt.Fprintf(os.Stderr, "storage: launches migration commit failed: %v\n", err)
	}
}
