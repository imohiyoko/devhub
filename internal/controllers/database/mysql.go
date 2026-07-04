package database

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

type xmlField struct {
	Text  string     `xml:",chardata"`
	Attrs []xml.Attr `xml:",any,attr"`
}

type xmlRow struct {
	Fields []xmlField `xml:"field"`
}

type xmlResultset struct {
	Rows []xmlRow `xml:"row"`
}

type xmlRoot struct {
	XMLName    xml.Name
	Rows       []xmlRow       `xml:"row"`       // when root itself is <resultset>
	Resultsets []xmlResultset `xml:"resultset"` // when root wraps several
}

// parseMysqlXML parses `mysql --xml` output. Field values are strings; a field
// carrying an xsi:nil="true" attribute becomes nil. Mirrors parse_mysql_xml.
func parseMysqlXML(output string) ([]map[string]any, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return []map[string]any{}, nil
	}
	if idx := strings.Index(output, "<?xml"); idx > 0 {
		output = output[idx:]
	}
	var root xmlRoot
	if err := xml.Unmarshal([]byte(output), &root); err != nil {
		return nil, err
	}
	var resultsets []xmlResultset
	if root.XMLName.Local == "resultset" {
		resultsets = []xmlResultset{{Rows: root.Rows}}
	} else {
		resultsets = root.Resultsets
	}
	rows := []map[string]any{}
	for _, rs := range resultsets {
		for _, row := range rs.Rows {
			item := map[string]any{}
			for _, f := range row.Fields {
				name := ""
				isNull := false
				for _, a := range f.Attrs {
					switch {
					case a.Name.Local == "name":
						name = a.Value
					case strings.HasSuffix(a.Name.Local, "nil") && a.Value == "true":
						isNull = true
					}
				}
				if isNull {
					item[name] = nil
				} else {
					item[name] = f.Text
				}
			}
			rows = append(rows, item)
		}
	}
	return rows, nil
}

// mysqlRun executes a SQL statement via the `mysql --xml` client and parses the
// result. The password is passed via MYSQL_PWD, never on the command line.
func mysqlRun(p *connProfile, query string) ([]map[string]any, error) {
	if _, err := exec.LookPath("mysql"); err != nil {
		return nil, fmt.Errorf("mysql command was not found")
	}
	args := []string{
		"--xml", "--default-character-set=utf8mb4", "--protocol=TCP", "--connect-timeout=5",
		"-h", p.host, "-P", strconv.Itoa(p.port), "-u", p.user, "-e", query,
	}
	if p.database != "" {
		// "--" stops option parsing so a database name beginning with "-" cannot be
		// smuggled to the mysql client as a flag. Without it, a value like
		// "--host=evil" would be parsed as a client option and override the
		// connection host, defeating the db_local_only guard (which validates only
		// p.host).
		args = append(args, "--", p.database)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mysql", args...) //execaudit:mysql
	cmd.Env = os.Environ()
	if p.password != "" {
		cmd.Env = append(cmd.Env, "MYSQL_PWD="+p.password)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = "mysql command failed"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return parseMysqlXML(out.String())
}

func (c *Controller) mysqlTables(p *connProfile) ([]map[string]any, error) {
	rows, err := mysqlRun(p, "SELECT TABLE_NAME AS name, TABLE_TYPE AS type, COALESCE(TABLE_ROWS, 0) AS count "+
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

func (c *Controller) mysqlTableNamesTypes(p *connProfile) ([]map[string]any, error) {
	rows, err := mysqlRun(p, "SELECT TABLE_NAME AS name, TABLE_TYPE AS type "+
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

func (c *Controller) mysqlColumns(p *connProfile, table string) ([]map[string]any, error) {
	rows, err := mysqlRun(p, "SELECT COLUMN_NAME AS name, DATA_TYPE AS type, IS_NULLABLE AS nullable, "+
		"COLUMN_DEFAULT AS dflt, COLUMN_KEY AS column_key, EXTRA AS extra "+
		"FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = "+sqlLiteral(table)+
		" ORDER BY ORDINAL_POSITION")
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
			"default": r["dflt"], // string or nil (XML)
			"pk":      pk,
			"extra":   strVal(r, "extra"),
		})
	}
	return cols, nil
}

func (c *Controller) mysqlSearchCondition(cols []map[string]any, search string) string {
	s := normalizeSearch(search)
	if s == "" {
		return ""
	}
	cols = searchableColumns(cols)
	if len(cols) == 0 {
		return " WHERE 0"
	}
	pattern := sqlLiteral("%" + escapeLikePattern(s) + "%")
	escapeSQL := sqlLiteral("\\")
	clauses := make([]string, 0, len(cols))
	for _, col := range cols {
		q, err := mysqlIdentifier(colName(col))
		if err != nil {
			continue
		}
		clauses = append(clauses, "CAST("+q+" AS CHAR) LIKE "+pattern+" ESCAPE "+escapeSQL)
	}
	return " WHERE (" + strings.Join(clauses, " OR ") + ")"
}

func (c *Controller) mysqlRows(p *connProfile, table string, limit, offset int, search string) (map[string]any, error) {
	cols, err := c.mysqlColumns(p, table)
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
	whereSQL := c.mysqlSearchCondition(cols, search)
	totalRows, err := mysqlRun(p, "SELECT COUNT(*) AS c FROM "+tableSQL+whereSQL)
	if err != nil {
		return nil, err
	}
	var total int64
	if len(totalRows) > 0 {
		total = parseInt64(strVal(totalRows[0], "c"))
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
	fetched, err := mysqlRun(p, fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d", tableSQL, whereSQL, orderSQL, limit, offset))
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

func (c *Controller) mysqlSearch(p *connProfile, columnSearch, elementSearch string) (map[string]any, error) {
	cs := normalizeSearch(columnSearch)
	es := normalizeSearch(elementSearch)
	result := map[string]any{"columnMatches": []any{}, "elementMatches": []any{}}
	if cs == "" && es == "" {
		return result, nil
	}
	tables, err := c.mysqlTableNamesTypes(p)
	if err != nil {
		return nil, err
	}
	colMatches := []any{}
	elemMatches := []any{}
	for _, t := range tables {
		name := strVal(t, "name")
		typ := strVal(t, "type")
		cols, err := c.mysqlColumns(p, name)
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
			whereSQL := c.mysqlSearchCondition(cols, es)
			countRows, err := mysqlRun(p, "SELECT COUNT(*) AS c FROM "+tableSQL+whereSQL)
			if err != nil {
				continue
			}
			var count int64
			if len(countRows) > 0 {
				count = parseInt64(strVal(countRows[0], "c"))
			}
			if count == 0 {
				continue
			}
			rowsM, err := mysqlRun(p, "SELECT * FROM "+tableSQL+whereSQL+" LIMIT 1")
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

func (c *Controller) mysqlUpdate(p *connProfile, table, column string, key, value any) error {
	cols, err := c.mysqlColumns(p, table)
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
	where, err := pkWhere(pkColumns, key)
	if err != nil {
		return err
	}
	_, err = mysqlRun(p, "UPDATE "+tableSQL+" SET "+colSQL+" = "+sqlLiteral(value)+" WHERE "+where)
	return err
}

func (c *Controller) mysqlInsert(p *connProfile, table string) (any, error) {
	if _, err := c.mysqlColumns(p, table); err != nil {
		return nil, err
	}
	tableSQL, err := mysqlIdentifier(table)
	if err != nil {
		return nil, err
	}
	if _, err := mysqlRun(p, "INSERT INTO "+tableSQL+" () VALUES ()"); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *Controller) mysqlDelete(p *connProfile, table string, key any) error {
	cols, err := c.mysqlColumns(p, table)
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
	where, err := pkWhere(pkColumns, key)
	if err != nil {
		return err
	}
	_, err = mysqlRun(p, "DELETE FROM "+tableSQL+" WHERE "+where)
	return err
}

// pkWhere builds "`c1` = '..' AND `c2` = '..'" from the primary key columns.
func pkWhere(pkColumns []string, key any) (string, error) {
	parts := make([]string, 0, len(pkColumns))
	for _, c := range pkColumns {
		q, err := mysqlIdentifier(c)
		if err != nil {
			return "", err
		}
		parts = append(parts, q+" = "+sqlLiteral(keyGet(key, c)))
	}
	return strings.Join(parts, " AND "), nil
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
