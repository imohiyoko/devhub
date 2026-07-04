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

// AppendLaunch appends record to the launch registry as a single-row INSERT.
// Row-level writes are atomic at the SQL layer, so concurrent appenders — in
// this process or another (`devhub env start` runs in its own process while
// the server may be writing too) — cannot lose each other's records the way
// the old load->mutate->save-the-whole-table sequence could. RegistryMu is
// still held so in-process compositions over the registry stay serialized,
// but cross-process correctness comes from SQLite (WAL + busy_timeout), not
// the mutex.
func (s *Store) AppendLaunch(record map[string]any) error {
	s.RegistryMu.Lock()
	defer s.RegistryMu.Unlock()
	id, _ := record["launch_id"].(string)
	if id == "" {
		// launch_id is the PRIMARY KEY; an id-less record would collide under
		// INSERT OR REPLACE. SaveLaunches drops such records on bulk writes —
		// reject here so a caller bug surfaces instead of silently vanishing.
		return errors.New("launch record needs a launch_id")
	}
	b, err := jsonx.Marshal(record)
	if err != nil {
		return err
	}
	launchedAt, _ := record["launched_at"].(string)
	_, err = s.db.Exec(
		"INSERT OR REPLACE INTO launches (launch_id, data, launched_at) VALUES (?, ?, ?)",
		id, string(b), launchedAt,
	)
	return err
}

// RemoveLaunch drops the launch with launchID as a single-row DELETE (atomic
// and cross-process safe, like AppendLaunch). A missing launchID is a no-op
// here; the caller checks existence separately so it can return its own error
// message.
func (s *Store) RemoveLaunch(launchID string) error {
	s.RegistryMu.Lock()
	defer s.RegistryMu.Unlock()
	_, err := s.db.Exec("DELETE FROM launches WHERE launch_id = ?", launchID)
	return err
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
