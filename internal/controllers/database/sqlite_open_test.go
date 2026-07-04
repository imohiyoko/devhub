package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// makeOpenTestDB creates a temp SQLite file with one table and one row and
// returns its path. The file is closed before returning so callers exercise the
// open helpers against an existing, unheld file.
func makeOpenTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "open.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO items(name) VALUES (?)`, "alpha"); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSQLiteReadOnlyOpenRejectsWrites is evidence for criterion 3: viewing a DB
// cannot modify the target file. Views still read (tables/rows return the data),
// a write through the read-only handle fails, and the read-write handle allows
// the same write.
func TestSQLiteReadOnlyOpenRejectsWrites(t *testing.T) {
	path := makeOpenTestDB(t)

	// Views read the seeded data through the read-only path.
	tables, err := sqliteTables(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0]["name"] != "items" || asInt(tables[0]["count"]) != 1 {
		t.Fatalf("tables = %+v", tables)
	}
	res, err := sqliteRows(path, "items", 100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if rows := res["rows"].([]map[string]any); len(rows) != 1 || rows[0]["name"] != "alpha" {
		t.Fatalf("rows = %+v", res["rows"])
	}

	// A read-only handle must reject writes.
	roDB, err := openSQLiteRO(path)
	if err != nil {
		t.Fatal(err)
	}
	defer roDB.Close()
	if _, err := roDB.Exec(`INSERT INTO items(name) VALUES ('beta')`); err == nil {
		t.Error("INSERT through openSQLiteRO should return an error")
	}
	if _, err := roDB.Exec(`UPDATE items SET name = 'gamma' WHERE id = 1`); err == nil {
		t.Error("UPDATE through openSQLiteRO should return an error")
	}

	// A read-write handle must allow the same write.
	rwDB, err := openSQLiteRW(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rwDB.Close()
	if _, err := rwDB.Exec(`INSERT INTO items(name) VALUES ('beta')`); err != nil {
		t.Errorf("INSERT through openSQLiteRW should succeed: %v", err)
	}
}

// TestSQLiteBusyTimeoutPragma is evidence for criterion 2: both open helpers set
// busy_timeout(5000) on their connections, so a lock held by another writer is
// waited out (up to 5s) rather than surfaced as an immediate "database is locked".
func TestSQLiteBusyTimeoutPragma(t *testing.T) {
	path := makeOpenTestDB(t)
	cases := []struct {
		name string
		open func(string) (*sql.DB, error)
	}{
		{"ro", openSQLiteRO},
		{"rw", openSQLiteRW},
	}
	for _, tc := range cases {
		db, err := tc.open(path)
		if err != nil {
			t.Fatalf("%s open: %v", tc.name, err)
		}
		got, err := scalarInt(db, "PRAGMA busy_timeout")
		if err != nil {
			t.Fatalf("%s PRAGMA busy_timeout: %v", tc.name, err)
		}
		if got != 5000 {
			t.Errorf("%s busy_timeout = %d, want 5000", tc.name, got)
		}
		db.Close()
	}
}
