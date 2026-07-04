package database

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestMysqlDSN checks the connection string carries the address, database, and
// utf8mb4 collation, and keeps the connect timeout — without needing a server.
func TestMysqlDSN(t *testing.T) {
	dsn := mysqlDSN(&connProfile{
		host: "127.0.0.1", port: 3306, user: "root", password: "s3cret", database: "shop",
	})
	for _, want := range []string{"@tcp(127.0.0.1:3306)/shop", "collation=utf8mb4_general_ci", "timeout=5s"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q missing %q", dsn, want)
		}
	}

	// IPv6 hosts must be bracketed so host and port stay separable.
	dsn6 := mysqlDSN(&connProfile{host: "::1", port: 3307, database: "d"})
	if !strings.Contains(dsn6, "tcp([::1]:3307)") {
		t.Errorf("IPv6 DSN %q missing bracketed addr", dsn6)
	}
}

// TestMysqlValue checks the coercion to the API's string/nil shape: NULL stays
// nil, []byte and typed numbers both become strings (so a column reads the same
// whether it arrived via the text or binary protocol), and JSON never sees []byte.
func TestMysqlValue(t *testing.T) {
	cases := []struct {
		in   any
		want any
	}{
		{nil, nil},
		{[]byte("hello"), "hello"},
		{"world", "world"},
		{int64(42), "42"},
		{float64(3.5), "3.5"},
		{true, "1"},
		{false, "0"},
	}
	for _, c := range cases {
		if got := mysqlValue(c.in); got != c.want {
			t.Errorf("mysqlValue(%v (%T)) = %v (%T), want %v", c.in, c.in, got, got, c.want)
		}
	}
}

// TestEscapeMysqlLike checks LIKE metacharacters are escaped with '!' so a
// literal search term cannot act as a wildcard.
func TestEscapeMysqlLike(t *testing.T) {
	if got := escapeMySQLLike("a%b_c!d"); got != "a!%b!_c!!d" {
		t.Errorf("escapeMySQLLike = %q", got)
	}
}

// TestMysqlSearchCondition checks the WHERE clause is parameterized (one bound
// pattern per searchable column, no literal interpolation) and that binary/blob
// columns are excluded.
func TestMysqlSearchCondition(t *testing.T) {
	cols := []map[string]any{
		{"name": "title", "type": "varchar"},
		{"name": "body", "type": "text"},
		{"name": "photo", "type": "blob"},
	}
	where, params := mysqlSearchCondition(cols, "foo")
	if strings.Contains(where, "foo") {
		t.Errorf("search term should be bound, not interpolated: %q", where)
	}
	if n := strings.Count(where, "?"); n != 2 {
		t.Errorf("want 2 placeholders (blob excluded), got %d: %q", n, where)
	}
	if len(params) != 2 || params[0] != "%foo%" {
		t.Errorf("params = %v", params)
	}
}

func TestSQLiteRowTypesAndCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT, data BLOB, score REAL, note TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t(name, data, score, note) VALUES (?, ?, ?, ?)`, "hello", []byte{0x00, 0xff}, 3.5, nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// tables + count
	tables, err := sqliteTables(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0]["name"] != "t" || asInt(tables[0]["count"]) != 1 {
		t.Fatalf("tables = %+v", tables)
	}

	// rows + value types (TEXT stays text, BLOB -> 0x hex, NULL -> nil, REAL -> float64)
	res, err := sqliteRows(path, "t", 100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	rows := res["rows"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row["name"] != "hello" {
		t.Errorf("TEXT mishandled: name=%v (%T)", row["name"], row["name"])
	}
	if row["data"] != "0x00ff" {
		t.Errorf("BLOB mishandled: data=%v", row["data"])
	}
	if row["note"] != nil {
		t.Errorf("NULL mishandled: note=%v", row["note"])
	}
	if row["score"] != 3.5 {
		t.Errorf("REAL mishandled: score=%v (%T)", row["score"], row["score"])
	}
	if res["editable"] != true {
		t.Errorf("table should be editable")
	}

	// update a non-pk column by rowid
	if err := sqliteUpdate(path, "t", "name", map[string]any{"rowid": row["__devhub_rowid__"]}, "world"); err != nil {
		t.Fatalf("update: %v", err)
	}
	res2, _ := sqliteRows(path, "t", 100, 0, "")
	if res2["rows"].([]map[string]any)[0]["name"] != "world" {
		t.Error("update did not persist")
	}

	// updating a primary key column is rejected
	if err := sqliteUpdate(path, "t", "id", map[string]any{"rowid": 1}, 5); err == nil {
		t.Error("updating pk should error")
	}

	// search hits the text column
	sres, err := sqliteSearch(path, "name", "world")
	if err != nil {
		t.Fatal(err)
	}
	if len(sres["columnMatches"].([]any)) != 1 {
		t.Errorf("columnMatches = %+v", sres["columnMatches"])
	}
	if len(sres["elementMatches"].([]any)) != 1 {
		t.Errorf("elementMatches = %+v", sres["elementMatches"])
	}

	// insert + delete
	id, err := sqliteInsert(path, "t")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if asInt(id) == 0 {
		t.Errorf("insert lastrowid = %v", id)
	}
	if err := sqliteDelete(path, "t", map[string]any{"rowid": id}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestConnectionFromPayload(t *testing.T) {
	c := New(nil) // sqlite path validation does not touch the store
	if _, err := c.connectionFromPayload(map[string]any{"path": "/no/such/file.db"}); err == nil {
		t.Error("nonexistent sqlite path should error")
	}
	if _, err := c.connectionFromPayload(map[string]any{"connection": map[string]any{"driver": "postgres"}}); err == nil {
		t.Error("unsupported driver should error")
	}
}
