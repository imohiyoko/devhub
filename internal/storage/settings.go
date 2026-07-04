package storage

import (
	"encoding/json"
	"io/fs"
	"maps"
)

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
// server.example.json <- stored kv 'settings'.
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
	return m, nil
}

// SaveSettings shallow-merges patch into the stored settings document.
func (s *Store) SaveSettings(patch map[string]any) error {
	cur := map[string]any{}
	if raw, err := s.kvGet("settings"); err != nil {
		return err
	} else if raw != nil {
		_ = json.Unmarshal(raw, &cur)
	}
	mergeMap(cur, patch)
	return kvSet(s.db, "settings", cur)
}

// The per-tool settings document (kv key "tool:<id>") is now owned by the
// settings tool through the core.Store seam: it holds a core.Namespace(store,
// "tool") view and reads/writes "tool:<id>" via Get/Set. The typed accessors
// that used to live here were removed so per-tool data ownership is structural,
// not a storage-method convention. Existing keys are unchanged, so no data
// migration is needed.

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
