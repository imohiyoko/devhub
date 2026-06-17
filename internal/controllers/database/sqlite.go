package database

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func openSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}

// queryMaps runs a query and returns each row as name->value, plus column names.
// Values are the driver's natural Go types (int64/float64/string/[]byte/nil).
func queryMaps(db *sql.DB, query string, args ...any) ([]map[string]any, []string, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, cols, rows.Err()
}

func scalarInt(db *sql.DB, query string, args ...any) (int64, error) {
	var n int64
	err := db.QueryRow(query, args...).Scan(&n)
	return n, err
}

func sqliteTables(path string) ([]map[string]any, error) {
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, _, err := queryMaps(db, "SELECT name, type FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	tables := []map[string]any{}
	for _, row := range rows {
		name := asString(row["name"])
		q, err := quoteIdentifier(name)
		if err != nil {
			return nil, err
		}
		count, err := scalarInt(db, "SELECT COUNT(*) AS c FROM "+q)
		if err != nil {
			return nil, err
		}
		tables = append(tables, map[string]any{"name": name, "type": asString(row["type"]), "count": count})
	}
	return tables, nil
}

func sqliteTableNamesTypes(db *sql.DB) ([]map[string]any, error) {
	rows, _, err := queryMaps(db, "SELECT name, type FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{"name": asString(row["name"]), "type": asString(row["type"])})
	}
	return out, nil
}

func sqliteTableMeta(db *sql.DB, table string) (map[string]any, error) {
	rows, _, err := queryMaps(db, "SELECT name, type FROM sqlite_master WHERE name = ? AND type IN ('table', 'view')", table)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("table was not found")
	}
	return map[string]any{"name": asString(rows[0]["name"]), "type": asString(rows[0]["type"])}, nil
}

func sqliteColumns(db *sql.DB, table string) ([]map[string]any, error) {
	q, err := quoteIdentifier(table)
	if err != nil {
		return nil, err
	}
	rows, _, err := queryMaps(db, "PRAGMA table_info("+q+")")
	if err != nil {
		return nil, err
	}
	cols := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		var def any
		if dv := r["dflt_value"]; dv != nil {
			def = asString(dv)
		}
		cols = append(cols, map[string]any{
			"name":    asString(r["name"]),
			"type":    asString(r["type"]),
			"notnull": asInt(r["notnull"]) != 0,
			"default": def,
			"pk":      int(asInt(r["pk"])),
		})
	}
	return cols, nil
}

func sqliteSearchCondition(cols []map[string]any, search string) (string, []any) {
	s := normalizeSearch(search)
	if s == "" {
		return "", nil
	}
	if len(cols) == 0 {
		return " WHERE 0", nil
	}
	pattern := "%" + escapeLikePattern(s) + "%"
	clauses := make([]string, 0, len(cols))
	params := make([]any, 0, len(cols))
	for _, c := range cols {
		q, err := quoteIdentifier(colName(c))
		if err != nil {
			continue
		}
		clauses = append(clauses, "CAST("+q+" AS TEXT) LIKE ? ESCAPE '\\'")
		params = append(params, pattern)
	}
	return " WHERE (" + strings.Join(clauses, " OR ") + ")", params
}

func sqliteRows(path, table string, limit, offset int, search string) (map[string]any, error) {
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	meta, err := sqliteTableMeta(db, table)
	if err != nil {
		return nil, err
	}
	cols, err := sqliteColumns(db, table)
	if err != nil {
		return nil, err
	}
	tableSQL, err := quoteIdentifier(table)
	if err != nil {
		return nil, err
	}
	whereSQL, params := sqliteSearchCondition(cols, search)
	total, err := scalarInt(db, "SELECT COUNT(*) AS c FROM "+tableSQL+whereSQL, params...)
	if err != nil {
		return nil, err
	}

	rowidAvailable := true
	args := append(append([]any{}, params...), limit, offset)
	fetched, _, err := queryMaps(db, "SELECT rowid AS __devhub_rowid__, * FROM "+tableSQL+whereSQL+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		rowidAvailable = false
		fetched, _, err = queryMaps(db, "SELECT * FROM "+tableSQL+whereSQL+" LIMIT ? OFFSET ?", args...)
		if err != nil {
			return nil, err
		}
	}

	rows := []map[string]any{}
	for _, row := range fetched {
		item := make(map[string]any, len(row)+1)
		for k, v := range row {
			item[k] = normalizeValue(v)
		}
		if rowidAvailable {
			item["__devhub_key__"] = map[string]any{"rowid": item["__devhub_rowid__"]}
		}
		rows = append(rows, item)
	}

	keyColumns := []string{}
	if rowidAvailable {
		keyColumns = []string{"rowid"}
	}
	return map[string]any{
		"table":      meta,
		"columns":    cols,
		"rows":       rows,
		"total":      total,
		"limit":      limit,
		"offset":     offset,
		"editable":   asString(meta["type"]) == "table" && rowidAvailable,
		"keyColumns": keyColumns,
	}, nil
}

func sqliteSearch(path, columnSearch, elementSearch string) (map[string]any, error) {
	cs := normalizeSearch(columnSearch)
	es := normalizeSearch(elementSearch)
	result := map[string]any{"columnMatches": []any{}, "elementMatches": []any{}}
	if cs == "" && es == "" {
		return result, nil
	}
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tables, err := sqliteTableNamesTypes(db)
	if err != nil {
		return nil, err
	}
	colMatches := []any{}
	elemMatches := []any{}
	for _, t := range tables {
		name := asString(t["name"])
		typ := asString(t["type"])
		cols, err := sqliteColumns(db, name)
		if err != nil {
			continue
		}
		if cs != "" {
			if m := matchedColumns(cols, cs); len(m) > 0 {
				colMatches = append(colMatches, map[string]any{"table": name, "type": typ, "columns": m})
			}
		}
		if es != "" {
			q, err := quoteIdentifier(name)
			if err != nil {
				continue
			}
			whereSQL, params := sqliteSearchCondition(cols, es)
			count, err := scalarInt(db, "SELECT COUNT(*) AS c FROM "+q+whereSQL, params...)
			if err != nil || count == 0 {
				continue
			}
			rowsM, _, err := queryMaps(db, "SELECT * FROM "+q+whereSQL+" LIMIT 1", params...)
			if err != nil {
				continue
			}
			firstRow := map[string]any{}
			if len(rowsM) > 0 {
				firstRow = rowsM[0]
			}
			elemMatches = append(elemMatches, map[string]any{
				"table": name, "type": typ, "count": count,
				"sample": rowSearchSample(cols, firstRow, es, 3),
			})
		}
	}
	result["columnMatches"] = colMatches
	result["elementMatches"] = elemMatches
	return result, nil
}

func sqliteUpdate(path, table, column string, key, value any) error {
	db, err := openSQLite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	meta, err := sqliteTableMeta(db, table)
	if err != nil {
		return err
	}
	if asString(meta["type"]) != "table" {
		return fmt.Errorf("only tables can be edited")
	}
	cols, err := sqliteColumns(db, table)
	if err != nil {
		return err
	}
	colNames := map[string]bool{}
	pkNames := map[string]bool{}
	for _, c := range cols {
		n := colName(c)
		colNames[n] = true
		if asInt(c["pk"]) != 0 {
			pkNames[n] = true
		}
	}
	if !colNames[column] {
		return fmt.Errorf("column was not found")
	}
	if pkNames[column] {
		return fmt.Errorf("primary key columns cannot be edited")
	}
	tableSQL, err := quoteIdentifier(table)
	if err != nil {
		return err
	}
	colSQL, err := quoteIdentifier(column)
	if err != nil {
		return err
	}
	res, err := db.Exec("UPDATE "+tableSQL+" SET "+colSQL+" = ? WHERE rowid = ?", value, keyRowid(key))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("row was not found")
	}
	return nil
}

func sqliteInsert(path, table string) (any, error) {
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	meta, err := sqliteTableMeta(db, table)
	if err != nil {
		return nil, err
	}
	if asString(meta["type"]) != "table" {
		return nil, fmt.Errorf("only tables can be edited")
	}
	tableSQL, err := quoteIdentifier(table)
	if err != nil {
		return nil, err
	}
	res, err := db.Exec("INSERT INTO " + tableSQL + " DEFAULT VALUES")
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func sqliteDelete(path, table string, key any) error {
	db, err := openSQLite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	meta, err := sqliteTableMeta(db, table)
	if err != nil {
		return err
	}
	if asString(meta["type"]) != "table" {
		return fmt.Errorf("only tables can be edited")
	}
	tableSQL, err := quoteIdentifier(table)
	if err != nil {
		return err
	}
	res, err := db.Exec("DELETE FROM "+tableSQL+" WHERE rowid = ?", keyRowid(key))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("row was not found")
	}
	return nil
}

func keyRowid(key any) any {
	if m, ok := key.(map[string]any); ok {
		return m["rowid"]
	}
	return key
}

func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return i
	case []byte:
		i, _ := strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64)
		return i
	}
	return 0
}
