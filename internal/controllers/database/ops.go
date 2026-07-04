package database

import (
	"fmt"
	"strings"
)

// Driver dispatchers: each routes to the sqlite or mysql implementation.

func (c *Controller) dbTables(p *connProfile) ([]map[string]any, error) {
	if p.driver == "sqlite" {
		return sqliteTables(p.path)
	}
	return mysqlTables(p)
}

func (c *Controller) dbRows(p *connProfile, table string, limit, offset int, search string) (map[string]any, error) {
	if p.driver == "sqlite" {
		return sqliteRows(p.path, table, limit, offset, search)
	}
	return mysqlRows(p, table, limit, offset, search)
}

func (c *Controller) dbSearch(p *connProfile, columnSearch, elementSearch string) (map[string]any, error) {
	if p.driver == "sqlite" {
		return sqliteSearch(p.path, columnSearch, elementSearch)
	}
	return mysqlSearch(p, columnSearch, elementSearch)
}

func (c *Controller) dbUpdate(p *connProfile, table, column string, key, value any) error {
	if p.driver == "sqlite" {
		return sqliteUpdate(p.path, table, column, key, value)
	}
	return mysqlUpdate(p, table, column, key, value)
}

func (c *Controller) dbInsert(p *connProfile, table string) (any, error) {
	if p.driver == "sqlite" {
		return sqliteInsert(p.path, table)
	}
	return mysqlInsert(p, table)
}

func (c *Controller) dbDelete(p *connProfile, table string, key any) error {
	if p.driver == "sqlite" {
		return sqliteDelete(p.path, table, key)
	}
	return mysqlDelete(p, table, key)
}

// --- shared, driver-agnostic helpers over columns ([]map[string]any) ---

func colName(col map[string]any) string { s, _ := col["name"].(string); return s }
func colType(col map[string]any) string { s, _ := col["type"].(string); return strings.ToLower(s) }

var nonSearchableTypes = map[string]bool{
	"binary": true, "varbinary": true, "blob": true, "tinyblob": true,
	"mediumblob": true, "longblob": true, "geometry": true, "point": true,
	"linestring": true, "polygon": true, "multipoint": true, "multilinestring": true,
	"multipolygon": true, "geometrycollection": true,
}

func searchableColumns(cols []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(cols))
	for _, c := range cols {
		if !nonSearchableTypes[colType(c)] {
			out = append(out, c)
		}
	}
	return out
}

func matchedColumns(cols []map[string]any, search string) []map[string]any {
	s := strings.ToLower(normalizeSearch(search))
	if s == "" {
		return nil
	}
	var out []map[string]any
	for _, c := range cols {
		if strings.Contains(strings.ToLower(colName(c)), s) || strings.Contains(colType(c), s) {
			out = append(out, c)
		}
	}
	return out
}

func rowSearchSample(cols []map[string]any, row map[string]any, search string, limit int) []map[string]any {
	s := strings.ToLower(normalizeSearch(search))
	sample := []map[string]any{}
	if s == "" || len(row) == 0 {
		return sample
	}
	for _, col := range cols {
		name := colName(col)
		value, ok := row[name]
		if !ok || value == nil {
			continue
		}
		normalized := normalizeValue(value)
		if strings.Contains(strings.ToLower(toStr(normalized)), s) {
			sample = append(sample, map[string]any{"column": name, "value": normalized})
			if len(sample) >= limit {
				break
			}
		}
	}
	return sample
}

func toStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
