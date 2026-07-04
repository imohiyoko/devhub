package storage

import "database/sql"

// Get and Set are the raw byte-oriented half of the kv table: opaque bytes in,
// opaque bytes out, no schema knowledge. They exist so *Store satisfies the
// core.Store seam a tool depends on (see internal/core/store.go). A tool handed
// core.Namespace(store, id) sees only these two methods, transparently key-
// prefixed with its ID — that is how per-tool data ownership becomes structural
// rather than a naming convention. The typed helpers (LoadSettings, LoadConfig,
// LoadLaunches, …) stay for the rich/transactional documents that are not yet
// a plain key/value read.

// Get returns the raw bytes stored under key, or nil (no error) when the key is
// absent — matching the core.Store contract.
func (s *Store) Get(key string) ([]byte, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(v), nil
}

// Set writes raw bytes under key. The caller owns the encoding; the settings
// tool, for example, stores JSON documents through this seam.
func (s *Store) Set(key string, value []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(value), now(),
	)
	return err
}
