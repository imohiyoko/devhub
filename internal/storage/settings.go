package storage

import (
	"encoding/json"
	"io/fs"
	"maps"
	"strings"
)

// The settings document is stored as one kv row per key, named
// "<owner>.<key>" (e.g. "settings.editor", "ports.protected_ports"). Ownership
// lives in the row key itself: the owner is the tool that writes the value,
// and writers of different keys upsert disjoint rows, so a patch can never
// lose another writer's update the way the old whole-document
// read-modify-write could. LoadSettings/SaveSettings keep the flat-map
// contract, so callers are unaffected by the row layout.

// settingsOwners maps a settings key to the tool that owns (writes) it. Keys
// absent here default to the "settings" owner. Growing per-tool data
// ownership means adding entries here — no core rewiring.
var settingsOwners = map[string]string{
	"protected_ports": "ports",
	"port_labels":     "ports",
}

// settingsRowKey returns the kv row key holding the given settings key.
func settingsRowKey(key string) string {
	owner := settingsOwners[key]
	if owner == "" {
		owner = "settings"
	}
	return owner + "." + key
}

// readExample decodes an embedded example JSON file (e.g.
// "settings/server.example.json") into a map.
func (s *Store) readExample(name string) (map[string]any, error) {
	b, err := fs.ReadFile(s.assets, name)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func mergeMap(dst, src map[string]any) {
	maps.Copy(dst, src)
}

// LoadSettings returns the merged settings: hardcoded defaults <- embedded
// server.example.json <- legacy 'settings' blob (if the row migration has not
// run yet) <- per-key rows. Rows merge last so they always win over the blob.
func (s *Store) LoadSettings() (map[string]any, error) {
	m := map[string]any{
		"port":                  8765,
		"editor":                "code",
		"open_browser_on_start": true,
		"protected_ports":       []any{},
		"db_local_only":         true,
		"terminal":              map[string]any{},
	}
	if ex, err := s.readExample("settings/server.example.json"); err == nil {
		mergeMap(m, ex)
	}
	raw, err := s.kvGet("settings")
	if err != nil {
		return nil, err
	}
	if raw != nil {
		var stored map[string]any
		if json.Unmarshal(raw, &stored) == nil {
			mergeMap(m, stored)
		}
	}
	rows, err := s.loadSettingsRows()
	if err != nil {
		return nil, err
	}
	mergeMap(m, rows)
	return m, nil
}

// loadSettingsRows reads every per-key settings row back into a flat map:
// all "settings.*" rows plus the keys re-homed to other owners.
func (s *Store) loadSettingsRows() (map[string]any, error) {
	out := map[string]any{}
	rows, err := s.db.Query("SELECT key, value FROM kv WHERE key LIKE 'settings.%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		var val any
		if json.Unmarshal([]byte(v), &val) == nil {
			out[strings.TrimPrefix(k, "settings.")] = val
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for key := range settingsOwners {
		raw, err := s.kvGet(settingsRowKey(key))
		if err != nil {
			return nil, err
		}
		if raw == nil {
			continue
		}
		var val any
		if json.Unmarshal(raw, &val) == nil {
			out[key] = val
		}
	}
	return out, nil
}

// SaveSettings upserts one row per patch key. There is no shared document to
// read-modify-write, so concurrent patches to different keys cannot lose each
// other; the transaction only makes a multi-key patch atomic.
func (s *Store) SaveSettings(patch map[string]any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for k, v := range patch {
		if err := kvSet(tx, settingsRowKey(k), v); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LoadToolSettings returns the per-tool settings document (empty if unset).
func (s *Store) LoadToolSettings(toolID string) (map[string]any, error) {
	raw, err := s.kvGet("tool:" + toolID)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if raw != nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m, nil
}

// SaveToolSettings stores the per-tool settings document.
func (s *Store) SaveToolSettings(toolID string, data map[string]any) error {
	return kvSet(s.db, "tool:"+toolID, data)
}

// LoadConfig returns the git-tool config, seeding from config.example.json on
// first run, falling back to an empty shape.
func (s *Store) LoadConfig() (map[string]any, error) {
	raw, err := s.kvGet("config")
	if err != nil {
		return nil, err
	}
	if raw != nil {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m, nil
	}
	if ex, err := s.readExample("settings/config.example.json"); err == nil {
		_ = s.SaveConfig(ex)
		return ex, nil
	}
	return map[string]any{
		"scan_roots": []any{}, "excludes": []any{}, "pinned_repos": []any{},
		"repo_order": []any{}, "hidden_repos": []any{},
	}, nil
}

// SaveConfig stores the git-tool config document.
func (s *Store) SaveConfig(cfg map[string]any) error {
	return kvSet(s.db, "config", cfg)
}

// LoadEnvs returns the env-launcher definitions, seeding from envs.example.json
// on first run.
func (s *Store) LoadEnvs() (map[string]any, error) {
	raw, err := s.kvGet("envs")
	if err != nil {
		return nil, err
	}
	if raw != nil {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m, nil
	}
	if ex, err := s.readExample("settings/envs.example.json"); err == nil {
		_ = s.SaveEnvs(ex)
		return ex, nil
	}
	return map[string]any{}, nil
}

// SaveEnvs stores the env-launcher definitions document.
func (s *Store) SaveEnvs(data map[string]any) error {
	return kvSet(s.db, "envs", data)
}
