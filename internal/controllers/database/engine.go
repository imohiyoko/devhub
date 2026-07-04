package database

// dbEngine is the per-driver backend behind the /api/db/* endpoints. Every
// method receives the normalized connProfile (SQLite reads p.path; MySQL reads
// the host/port/user/... coordinates) and returns the same response shapes
// regardless of engine, so the frontend is engine-agnostic. The key model
// differs per engine — SQLite keys rows by rowid, MySQL by primary key — but
// that difference is confined to each implementation.
type dbEngine interface {
	Tables(p *connProfile) ([]map[string]any, error)
	Rows(p *connProfile, table string, limit, offset int, search string) (map[string]any, error)
	Search(p *connProfile, columnSearch, elementSearch string) (map[string]any, error)
	Update(p *connProfile, table, column string, key, value any) error
	Insert(p *connProfile, table string) (any, error)
	Delete(p *connProfile, table string, key any) error
}

// engines maps a driver name to its backend. This registry is the single source
// of truth for which drivers are supported: connectionFromPayload validates the
// requested driver against these keys, and HandleGet/HandlePost dispatch through
// the resolved engine — so there is no per-driver string branching anywhere else.
//
// Adding an engine (e.g. PostgreSQL) is two edits: implement dbEngine in a new
// file (postgres.go — quoteIdentifier already produces PostgreSQL-compatible
// identifier quoting, and the ops.go column helpers are engine-agnostic), then
// register it here with one line. No dispatcher or handler changes are needed.
var engines = map[string]dbEngine{
	"sqlite": sqliteEngine{},
	"mysql":  mysqlEngine{},
}
