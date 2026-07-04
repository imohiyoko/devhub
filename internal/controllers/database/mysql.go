package database

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	// Mirrors the old `mysql` CLI limits: connect (dial) 5s, each statement 30s.
	mysqlConnectTimeout = 5 * time.Second
	mysqlStmtTimeout    = 30 * time.Second
)

// mysqlDSN renders the connection string for p. The password lives only in this
// in-memory DSN — never on a command line (the old CLI path exposed it via argv,
// visible in the host's process list) nor persisted. utf8mb4 is forced via the
// connection collation, matching the CLI's --default-character-set=utf8mb4.
func mysqlDSN(p *connProfile) string {
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(p.host, strconv.Itoa(p.port))
	cfg.User = p.user
	cfg.Passwd = p.password
	cfg.DBName = p.database
	cfg.Timeout = mysqlConnectTimeout
	cfg.Collation = "utf8mb4_general_ci"
	return cfg.FormatDSN()
}

// openMySQL opens a pooled connection to the MySQL/MariaDB server in p. Callers
// run several statements over the one pool and Close it when done, so a single
// API call no longer spawns a subprocess per statement (the old CLI's N+1).
func openMySQL(p *connProfile) (*sql.DB, error) {
	return sql.Open("mysql", mysqlDSN(p))
}

// mysqlEngine adapts the package-level MySQL/MariaDB operations to the dbEngine
// interface. Rows are keyed by primary key.
type mysqlEngine struct{}

func (mysqlEngine) Tables(p *connProfile) ([]map[string]any, error) { return mysqlTables(p) }

func (mysqlEngine) Rows(p *connProfile, table string, limit, offset int, search string) (map[string]any, error) {
	return mysqlRows(p, table, limit, offset, search)
}

func (mysqlEngine) Search(p *connProfile, columnSearch, elementSearch string) (map[string]any, error) {
	return mysqlSearch(p, columnSearch, elementSearch)
}

func (mysqlEngine) Update(p *connProfile, table, column string, key, value any) error {
	return mysqlUpdate(p, table, column, key, value)
}

func (mysqlEngine) Insert(p *connProfile, table string) (any, error) {
	return mysqlInsert(p, table)
}

func (mysqlEngine) Delete(p *connProfile, table string, key any) error {
	return mysqlDelete(p, table, key)
}

// mysqlQuery runs a read on the shared connection with a per-statement timeout
// and returns each row as name->value. Every non-NULL value is returned as a
// string — mirroring the old `mysql --xml` output, and ensuring encoding/json
// never sees a []byte (which it would base64-encode). NULL stays nil.
func mysqlQuery(db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mysqlStmtTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = mysqlValue(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// mysqlScalarInt runs a COUNT-style query returning a single integer, with the
// per-statement timeout.
func mysqlScalarInt(db *sql.DB, query string, args ...any) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mysqlStmtTimeout)
	defer cancel()
	var n int64
	err := db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// mysqlExec runs a write (UPDATE / INSERT / DELETE) with the per-statement
// timeout. Values reach the server as bound parameters, never string-concatenated.
func mysqlExec(db *sql.DB, query string, args ...any) (sql.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mysqlStmtTimeout)
	defer cancel()
	return db.ExecContext(ctx, query, args...)
}

// mysqlValue coerces a scanned driver value into the shape the API returns:
// NULL -> nil, everything else -> string. The text protocol yields []byte while
// the binary protocol (used when a query carries bind parameters) yields typed
// numbers; both are normalized to strings so a column reads the same either way.
func mysqlValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case time.Time:
		return x.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprintf("%v", x)
	}
}

// escapeMySQLLike escapes LIKE wildcards using '!' as the escape character. A
// non-backslash escape keeps the paired ESCAPE '!' clause correct even under
// NO_BACKSLASH_ESCAPES, where a backslash escape literal would misparse.
func escapeMySQLLike(v string) string {
	v = strings.ReplaceAll(v, "!", "!!")
	v = strings.ReplaceAll(v, "%", "!%")
	v = strings.ReplaceAll(v, "_", "!_")
	return v
}

func mysqlTables(p *connProfile) ([]map[string]any, error) {
	db, err := openMySQL(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := mysqlQuery(db, "SELECT TABLE_NAME AS name, TABLE_TYPE AS type, COALESCE(TABLE_ROWS, 0) AS count "+
		"FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME")
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range rows {
		out = append(out, map[string]any{
			"name":  strVal(r, "name"),
			"type":  strings.ToLower(strVal(r, "type")),
			"count": parseInt64(strVal(r, "count")),
		})
	}
	return out, nil
}

func mysqlTableNamesTypes(db *sql.DB) ([]map[string]any, error) {
	rows, err := mysqlQuery(db, "SELECT TABLE_NAME AS name, TABLE_TYPE AS type "+
		"FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME")
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"name": strVal(r, "name"), "type": strings.ToLower(strVal(r, "type"))})
	}
	return out, nil
}

func mysqlColumns(db *sql.DB, table string) ([]map[string]any, error) {
	rows, err := mysqlQuery(db, "SELECT COLUMN_NAME AS name, DATA_TYPE AS type, IS_NULLABLE AS nullable, "+
		"COLUMN_DEFAULT AS dflt, COLUMN_KEY AS column_key, EXTRA AS extra "+
		"FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? "+
		"ORDER BY ORDINAL_POSITION", table)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("table was not found")
	}
	cols := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		pk := 0
		if strVal(r, "column_key") == "PRI" {
			pk = 1
		}
		cols = append(cols, map[string]any{
			"name":    strVal(r, "name"),
			"type":    strVal(r, "type"),
			"notnull": strVal(r, "nullable") == "NO",
			"default": r["dflt"], // string or nil
			"pk":      pk,
			"extra":   strVal(r, "extra"),
		})
	}
	return cols, nil
}

// mysqlSearchCondition builds a parameterized WHERE clause matching any
// searchable column against the pattern, plus the bound pattern values.
func mysqlSearchCondition(cols []map[string]any, search string) (string, []any) {
	s := normalizeSearch(search)
	if s == "" {
		return "", nil
	}
	cols = searchableColumns(cols)
	if len(cols) == 0 {
		return " WHERE 0", nil
	}
	pattern := "%" + escapeMySQLLike(s) + "%"
	clauses := make([]string, 0, len(cols))
	params := make([]any, 0, len(cols))
	for _, col := range cols {
		q, err := mysqlIdentifier(colName(col))
		if err != nil {
			continue
		}
		clauses = append(clauses, "CAST("+q+" AS CHAR) LIKE ? ESCAPE '!'")
		params = append(params, pattern)
	}
	return " WHERE (" + strings.Join(clauses, " OR ") + ")", params
}

func mysqlRows(p *connProfile, table string, limit, offset int, search string) (map[string]any, error) {
	db, err := openMySQL(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	cols, err := mysqlColumns(db, table)
	if err != nil {
		return nil, err
	}
	var pkColumns []string
	for _, col := range cols {
		if asInt(col["pk"]) != 0 {
			pkColumns = append(pkColumns, colName(col))
		}
	}
	tableSQL, err := mysqlIdentifier(table)
	if err != nil {
		return nil, err
	}
	whereSQL, params := mysqlSearchCondition(cols, search)
	total, err := mysqlScalarInt(db, "SELECT COUNT(*) FROM "+tableSQL+whereSQL, params...)
	if err != nil {
		return nil, err
	}
	orderSQL := ""
	if len(pkColumns) > 0 {
		quoted := make([]string, 0, len(pkColumns))
		for _, pc := range pkColumns {
			q, err := mysqlIdentifier(pc)
			if err != nil {
				return nil, err
			}
			quoted = append(quoted, q)
		}
		orderSQL = " ORDER BY " + strings.Join(quoted, ", ")
	}
	args := append(append([]any{}, params...), limit, offset)
	fetched, err := mysqlQuery(db, "SELECT * FROM "+tableSQL+whereSQL+orderSQL+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, row := range fetched {
		item := map[string]any{}
		maps.Copy(item, row)
		if len(pkColumns) > 0 {
			item["__devhub_key__"] = tableKeyForRow(row, pkColumns)
		} else {
			item["__devhub_key__"] = map[string]any{}
		}
		rows = append(rows, item)
	}
	keyColumns := pkColumns
	if keyColumns == nil {
		keyColumns = []string{}
	}
	return map[string]any{
		"table":      map[string]any{"name": table, "type": "table"},
		"columns":    cols,
		"rows":       rows,
		"total":      total,
		"limit":      limit,
		"offset":     offset,
		"editable":   len(pkColumns) > 0,
		"keyColumns": keyColumns,
	}, nil
}

func mysqlSearch(p *connProfile, columnSearch, elementSearch string) (map[string]any, error) {
	cs := normalizeSearch(columnSearch)
	es := normalizeSearch(elementSearch)
	result := map[string]any{"columnMatches": []any{}, "elementMatches": []any{}}
	if cs == "" && es == "" {
		return result, nil
	}
	db, err := openMySQL(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tables, err := mysqlTableNamesTypes(db)
	if err != nil {
		return nil, err
	}
	colMatches := []any{}
	elemMatches := []any{}
	for _, t := range tables {
		name := strVal(t, "name")
		typ := strVal(t, "type")
		cols, err := mysqlColumns(db, name)
		if err != nil {
			continue
		}
		if cs != "" {
			if m := matchedColumns(cols, cs); len(m) > 0 {
				colMatches = append(colMatches, map[string]any{"table": name, "type": typ, "columns": m})
			}
		}
		if es != "" {
			tableSQL, err := mysqlIdentifier(name)
			if err != nil {
				continue
			}
			whereSQL, params := mysqlSearchCondition(cols, es)
			count, err := mysqlScalarInt(db, "SELECT COUNT(*) FROM "+tableSQL+whereSQL, params...)
			if err != nil || count == 0 {
				continue
			}
			rowsM, err := mysqlQuery(db, "SELECT * FROM "+tableSQL+whereSQL+" LIMIT 1", params...)
			if err != nil {
				continue
			}
			first := map[string]any{}
			if len(rowsM) > 0 {
				first = rowsM[0]
			}
			elemMatches = append(elemMatches, map[string]any{
				"table": name, "type": typ, "count": count,
				"sample": rowSearchSample(cols, first, es, 3),
			})
		}
	}
	result["columnMatches"] = colMatches
	result["elementMatches"] = elemMatches
	return result, nil
}

func mysqlUpdate(p *connProfile, table, column string, key, value any) error {
	db, err := openMySQL(p)
	if err != nil {
		return err
	}
	defer db.Close()
	cols, err := mysqlColumns(db, table)
	if err != nil {
		return err
	}
	colNames := map[string]bool{}
	var pkColumns []string
	for _, col := range cols {
		n := colName(col)
		colNames[n] = true
		if asInt(col["pk"]) != 0 {
			pkColumns = append(pkColumns, n)
		}
	}
	if !colNames[column] {
		return fmt.Errorf("column was not found")
	}
	if len(pkColumns) == 0 {
		return fmt.Errorf("table has no primary key")
	}
	if slices.Contains(pkColumns, column) {
		return fmt.Errorf("primary key columns cannot be edited")
	}
	tableSQL, err := mysqlIdentifier(table)
	if err != nil {
		return err
	}
	colSQL, err := mysqlIdentifier(column)
	if err != nil {
		return err
	}
	where, whereArgs, err := pkWhere(pkColumns, key)
	if err != nil {
		return err
	}
	args := append([]any{value}, whereArgs...)
	_, err = mysqlExec(db, "UPDATE "+tableSQL+" SET "+colSQL+" = ? WHERE "+where, args...)
	return err
}

func mysqlInsert(p *connProfile, table string) (any, error) {
	db, err := openMySQL(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := mysqlColumns(db, table); err != nil {
		return nil, err
	}
	tableSQL, err := mysqlIdentifier(table)
	if err != nil {
		return nil, err
	}
	if _, err := mysqlExec(db, "INSERT INTO "+tableSQL+" () VALUES ()"); err != nil {
		return nil, err
	}
	return nil, nil
}

func mysqlDelete(p *connProfile, table string, key any) error {
	db, err := openMySQL(p)
	if err != nil {
		return err
	}
	defer db.Close()
	cols, err := mysqlColumns(db, table)
	if err != nil {
		return err
	}
	var pkColumns []string
	for _, col := range cols {
		if asInt(col["pk"]) != 0 {
			pkColumns = append(pkColumns, colName(col))
		}
	}
	if len(pkColumns) == 0 {
		return fmt.Errorf("table has no primary key")
	}
	tableSQL, err := mysqlIdentifier(table)
	if err != nil {
		return err
	}
	where, whereArgs, err := pkWhere(pkColumns, key)
	if err != nil {
		return err
	}
	_, err = mysqlExec(db, "DELETE FROM "+tableSQL+" WHERE "+where, whereArgs...)
	return err
}

// pkWhere builds "`c1` = ? AND `c2` = ?" plus the bound key values, in column
// order.
func pkWhere(pkColumns []string, key any) (string, []any, error) {
	parts := make([]string, 0, len(pkColumns))
	args := make([]any, 0, len(pkColumns))
	for _, c := range pkColumns {
		q, err := mysqlIdentifier(c)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, q+" = ?")
		args = append(args, keyGet(key, c))
	}
	return strings.Join(parts, " AND "), args, nil
}

func tableKeyForRow(row map[string]any, pkColumns []string) map[string]any {
	out := map[string]any{}
	for _, name := range pkColumns {
		out[name] = row[name]
	}
	return out
}

func keyGet(key any, name string) any {
	if m, ok := key.(map[string]any); ok {
		return m[name]
	}
	return nil
}

func strVal(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func parseInt64(s string) int64 {
	i, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return i
}
