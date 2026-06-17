package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestParseMysqlXML(t *testing.T) {
	doc := `some banner noise
<?xml version="1.0"?>
<resultset statement="select" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <row>
    <field name="id">1</field>
    <field name="name">alice</field>
    <field name="email" xsi:nil="true" />
    <field name="bio"></field>
  </row>
  <row>
    <field name="id">2</field>
    <field name="name">bob</field>
    <field name="email">b@e.com</field>
    <field name="bio">hi</field>
  </row>
</resultset>`
	rows, err := parseMysqlXML(doc)
	if err != nil {
		t.Fatalf("parseMysqlXML: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["id"] != "1" || rows[0]["name"] != "alice" {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[0]["email"] != nil {
		t.Errorf("xsi:nil email should be nil, got %v", rows[0]["email"])
	}
	if rows[0]["bio"] != "" {
		t.Errorf("empty bio should be \"\", got %v", rows[0]["bio"])
	}
	if rows[1]["email"] != "b@e.com" {
		t.Errorf("row1 email = %v", rows[1]["email"])
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
