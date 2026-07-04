package storage

import (
	"encoding/json"
	"errors"

	"github.com/imohiyoko/devhub/internal/jsonx"
)

// LoadLaunches reconstructs the {"launches": [...]} document from the launches
// table, ordered by launched_at, then rowid.
func (s *Store) LoadLaunches() (map[string]any, error) {
	rows, err := s.db.Query("SELECT data FROM launches ORDER BY launched_at, rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	launches := []any{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var rec any
		if err := json.Unmarshal([]byte(data), &rec); err != nil {
			return nil, err
		}
		launches = append(launches, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"launches": launches}, nil
}

// AppendLaunch appends record to the launch registry, serializing the
// load->mutate->save under RegistryMu so two concurrent launches cannot lose
// each other's records. It is the write half of the registry the env-launcher
// drives through the store seam: RegistryMu is a Store field an interface
// cannot express, so this method (not the raw mutex) is what envs depends on.
func (s *Store) AppendLaunch(record map[string]any) error {
	s.RegistryMu.Lock()
	defer s.RegistryMu.Unlock()
	data, err := s.LoadLaunches()
	if err != nil {
		return err
	}
	list, _ := data["launches"].([]any)
	data["launches"] = append(list, record)
	return s.SaveLaunches(data)
}

// RemoveLaunch drops the launch with launchID from the registry, serializing the
// load->mutate->save under RegistryMu. A missing launchID is a no-op here; the
// caller checks existence separately so it can return its own error message.
func (s *Store) RemoveLaunch(launchID string) error {
	s.RegistryMu.Lock()
	defer s.RegistryMu.Unlock()
	data, err := s.LoadLaunches()
	if err != nil {
		return err
	}
	list, _ := data["launches"].([]any)
	filtered := make([]any, 0, len(list))
	for _, l := range list {
		if m, ok := l.(map[string]any); ok {
			if id, _ := m["launch_id"].(string); id == launchID {
				continue
			}
		}
		filtered = append(filtered, l)
	}
	data["launches"] = filtered
	return s.SaveLaunches(data)
}

// SaveLaunches replaces the entire launches table in a single transaction.
// Callers serialize load->mutate->save under Store.RegistryMu (see
// AppendLaunch / RemoveLaunch, which own that sequence for the env-launcher).
func (s *Store) SaveLaunches(data map[string]any) error {
	// Normalize the launches value up front. A silent type mismatch here would
	// run the DELETE below with nothing to re-insert, wiping the whole table.
	var launches []any
	switch list := data["launches"].(type) {
	case nil:
		launches = nil
	case []any:
		launches = list
	case []map[string]any:
		launches = make([]any, len(list))
		for i, rec := range list {
			launches[i] = rec
		}
	default:
		return errors.New("launches must be an array")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM launches"); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, item := range launches {
		rec, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := rec["launch_id"].(string)
		if id == "" {
			// launch_id is the PRIMARY KEY. Two empty ids would collide under
			// INSERT OR REPLACE and one launch would silently vanish, so skip
			// id-less records here just as importLaunches does on migration.
			continue
		}
		b, err := jsonx.Marshal(rec)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		launchedAt, _ := rec["launched_at"].(string)
		if _, err := tx.Exec(
			"INSERT OR REPLACE INTO launches (launch_id, data, launched_at) VALUES (?, ?, ?)",
			id, string(b), launchedAt,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
