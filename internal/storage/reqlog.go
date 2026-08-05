package storage

import (
	"strings"
	"time"
)

// RequestLogRow is one archived request. It mirrors reqlog.Entry, but the two
// types stay separate on purpose: reqlog is a dependency-free in-memory package
// and storage knows only SQL, so neither imports the other. The logs controller
// converts between them.
type RequestLogRow struct {
	Instance string `json:"instance"`
	Seq      int64  `json:"seq"`

	TS      string `json:"ts"` // RFC3339
	Surface string `json:"surface"`
	Method  string `json:"method"`
	Path    string `json:"path"`

	Status int   `json:"status"`
	DurMs  int64 `json:"dur_ms"`
	Bytes  int   `json:"bytes"`

	Approval string `json:"approval,omitempty"`
	Code     string `json:"code,omitempty"`
	Body     string `json:"body,omitempty"`
	Err      string `json:"err,omitempty"`

	ArchivedAt string `json:"archived_at,omitempty"`
}

// RequestLogFilter selects archived rows. It mirrors reqlog.Filter; a zero field
// means "do not filter on this".
type RequestLogFilter struct {
	Since, Until string // RFC3339, empty for unbounded
	Surface      string
	Method       string
	PathPrefix   string
	Approval     string
	Code         string
	StatusMin    int
	StatusMax    int
	MinDurMs     int64
	Text         string
	Limit        int
}

// ArchiveRequestLogs persists rows, skipping any already archived.
//
// (instance, seq) is unique, and instance changes every time devhub starts, so
// the pair identifies a request for as long as it can be archived at all. That
// makes archiving idempotent: a user who archives "today's errors" and then
// "everything from this hour" gets one row per request, not two.
func (s *Store) ArchiveRequestLogs(rows []RequestLogRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	now := now()
	inserted := 0
	for _, r := range rows {
		res, err := tx.Exec(
			`INSERT OR IGNORE INTO request_log_archive
			 (instance, seq, ts, surface, method, path, status, dur_ms, bytes, approval, code, body, err, archived_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Instance, r.Seq, r.TS, r.Surface, r.Method, r.Path,
			r.Status, r.DurMs, r.Bytes, r.Approval, r.Code, r.Body, r.Err, now,
		)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if n, err := res.RowsAffected(); err == nil {
			inserted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// QueryRequestLogArchive returns archived rows matching f, newest first.
func (s *Store) QueryRequestLogArchive(f RequestLogFilter) ([]RequestLogRow, error) {
	where, args := requestLogWhere(f)
	q := `SELECT instance, seq, ts, surface, method, path, status, dur_ms, bytes,
	             approval, code, body, err, archived_at
	      FROM request_log_archive` + where + ` ORDER BY ts DESC, seq DESC`
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RequestLogRow{}
	for rows.Next() {
		var r RequestLogRow
		if err := rows.Scan(&r.Instance, &r.Seq, &r.TS, &r.Surface, &r.Method, &r.Path,
			&r.Status, &r.DurMs, &r.Bytes, &r.Approval, &r.Code, &r.Body, &r.Err, &r.ArchivedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRequestLogArchive removes archived rows matching f and reports how many
// went. A zero filter clears the archive.
func (s *Store) DeleteRequestLogArchive(f RequestLogFilter) (int, error) {
	where, args := requestLogWhere(f)
	res, err := s.db.Exec("DELETE FROM request_log_archive"+where, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// requestLogWhere renders a filter as a WHERE clause and its arguments. Every
// value is bound, never interpolated.
func requestLogWhere(f RequestLogFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, arg any) {
		conds = append(conds, cond)
		args = append(args, arg)
	}

	if f.Since != "" {
		add("ts >= ?", f.Since)
	}
	if f.Until != "" {
		add("ts <= ?", f.Until)
	}
	if f.Surface != "" {
		add("surface = ?", f.Surface)
	}
	if f.Method != "" {
		// Upper-cased both sides: HTTP methods are stored as sent, and a filter
		// typed as "post" should still match.
		add("UPPER(method) = ?", strings.ToUpper(f.Method))
	}
	if f.PathPrefix != "" {
		add("path LIKE ?", escapeLike(f.PathPrefix)+"%")
		conds[len(conds)-1] += ` ESCAPE '\'`
	}
	if f.Approval != "" {
		add("approval = ?", f.Approval)
	}
	if f.Code != "" {
		add("code = ?", f.Code)
	}
	if f.StatusMin != 0 {
		add("status >= ?", f.StatusMin)
	}
	if f.StatusMax != 0 {
		add("status <= ?", f.StatusMax)
	}
	if f.MinDurMs != 0 {
		add("dur_ms >= ?", f.MinDurMs)
	}
	if f.Text != "" {
		// LIKE is already case-insensitive for ASCII in SQLite; the columns this
		// searches (path, redacted body, error excerpt) are effectively ASCII.
		pat := "%" + escapeLike(f.Text) + "%"
		conds = append(conds, `(path LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\' OR err LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat, pat)
	}

	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// escapeLike neutralizes LIKE's wildcards so a user searching for "100%" does
// not match everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// RFC3339Milli is the timestamp format archived rows use. Fixed-width and
// lexicographically ordered, so the string comparisons above sort like times.
const RFC3339Milli = "2006-01-02T15:04:05.000Z07:00"

// FormatLogTime renders a timestamp for the archive, in UTC.
//
// The UTC part is what makes "lexicographic order is time order" true without a
// caveat. Keeping the local offset holds everywhere except the DST fall-back
// hour, where two different instants share a wall-clock prefix and the offset —
// compared as text, after the digits that tie — orders them backwards. One hour
// a year, and never in JST, but the comparison is the same code every other
// hour of the year, so it may as well be unconditionally right.
//
// Filter bounds are formatted through here too, so both sides of every ts
// comparison are UTC.
func FormatLogTime(t time.Time) string { return t.UTC().Format(RFC3339Milli) }
