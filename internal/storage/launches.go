package storage

import "encoding/json"

// LoadLaunches reconstructs the {"launches": [...]} document from the launches
// table, ordered like the Python version (launched_at, then rowid).
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

// SaveLaunches replaces the entire launches table in a single transaction.
// Callers serialize load->mutate->save under Store.RegistryMu.
func (s *Store) SaveLaunches(data map[string]any) error {
	var launches []any
	if list, ok := data["launches"].([]any); ok {
		launches = list
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
		b, err := marshalJSON(rec)
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
