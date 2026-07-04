package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/imohiyoko/devhub/internal/jsonx"
)

// migrate performs the one-time import of legacy settings/*.json into SQLite,
// guarded by meta.migrated. It reads from the on-disk settings dir (not the
// embedded examples) so an upgrading user keeps their state. An upgrade that
// reuses the same ~/.devhub already has meta.migrated set, so this is a no-op
// there.
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

// migrateSettingsRows explodes the legacy 'settings' kv blob into one row per
// key ("<owner>.<key>") and deletes the blob, all in one transaction. The
// blob's presence is the migration flag: a successful run leaves nothing to
// migrate, and a failed run is retried on the next startup because the blob
// is still there. If both blob and rows exist, the blob wins — it can only be
// newer (written by an older binary after the rows appeared).
func (s *Store) migrateSettingsRows() error {
	raw, err := s.kvGet("settings")
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("settings blob is not an object: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for k, v := range doc {
		if err := kvSet(tx, settingsRowKey(k), v); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM kv WHERE key = 'settings'"); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
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
		data, err := jsonx.Marshal(rec)
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
