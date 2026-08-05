// Package logs serves the request-log endpoints: searching the live in-memory
// ring, copying selected entries into the store, and clearing either one.
//
// It is the seam between two packages that deliberately do not know about each
// other — reqlog holds no SQL and storage holds no ring — so the conversion
// between reqlog.Entry and storage.RequestLogRow lives here.
package logs

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/reqlog"
	"github.com/imohiyoko/devhub/internal/storage"
)

// archiveStore is the narrow persistence this controller needs. Declared here,
// in the consumer, so the controller can be built against a fake in tests;
// *storage.Store satisfies it.
type archiveStore interface {
	ArchiveRequestLogs(rows []storage.RequestLogRow) (int, error)
	QueryRequestLogArchive(f storage.RequestLogFilter) ([]storage.RequestLogRow, error)
	DeleteRequestLogArchive(f storage.RequestLogFilter) (int, error)
}

// Controller serves /api/logs.
type Controller struct {
	ring  *reqlog.Ring
	store archiveStore
}

// New returns a logs controller.
func New(ring *reqlog.Ring, store archiveStore) *Controller {
	return &Controller{ring: ring, store: store}
}

// instance identifies the run an archived row came from. Paired with an entry's
// seq it uniquely names a request, which is what lets a repeated archive be a
// no-op instead of a duplicate. It is read from the ring rather than stored
// alongside it so it cannot drift from the counter it is paired with.
func (c *Controller) instance() string { return c.ring.Instance() }

// defaultLimit bounds a query that did not ask for one, so opening the page
// cannot serialize the whole ring.
const defaultLimit = 200

// HandleGet serves GET /api/logs — a search over either the live ring or the
// archive, selected by ?source=. Both accept the same filter parameters, so a
// query can be moved between them by changing one value.
func (c *Controller) HandleGet(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	source := q.Get("source")
	if source == "" {
		source = "live"
	}

	switch source {
	case "live":
		f, err := parseFilter(q)
		if err != nil {
			return err
		}
		entries := c.ring.Query(f)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"source":  "live",
			"entries": entries,
			"total":   c.ring.Len(),
		})
		return nil

	case "archive":
		f, err := parseArchiveFilter(q)
		if err != nil {
			return err
		}
		rows, err := c.store.QueryRequestLogArchive(f)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"source": "archive", "entries": rows})
		return nil
	}
	return badSource(source)
}

// HandleArchive serves POST /api/logs/archive: copy every live entry matching
// the filter into the store. Entries already archived are skipped, so repeating
// an overlapping selection adds nothing.
func (c *Controller) HandleArchive(w http.ResponseWriter, r *http.Request) error {
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		return err
	}
	// Archiving is an explicit "keep this", so it ignores the display limit and
	// takes everything the filter selects.
	f.Limit = 0

	entries := c.ring.Query(f)
	rows := make([]storage.RequestLogRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, storage.RequestLogRow{
			Instance: c.instance(),
			Seq:      e.Seq,
			TS:       storage.FormatLogTime(e.TS),
			Surface:  string(e.Surface),
			Method:   e.Method,
			Path:     e.Path,
			Status:   e.Status,
			DurMs:    e.DurMs,
			Bytes:    e.Bytes,
			Approval: e.Approval,
			Code:     e.Code,
			Body:     e.Body,
			Err:      e.Err,
		})
	}
	inserted, err := c.store.ArchiveRequestLogs(rows)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"matched":  len(rows),
		"archived": inserted,
		"skipped":  len(rows) - inserted, // already archived
	})
	return nil
}

// HandleClear serves POST /api/logs/clear: empty the live ring, or delete the
// archived rows matching the filter.
//
// The two are deliberately asymmetric, and the asymmetry is worth stating
// because a caller that sends the same filter to both gets very different
// results. A ring has no partial reset — clearing it is all or nothing — so
// live ignores the filter entirely. The archive is rows, so it honours it. The
// page's confirmation says which one it is about to do.
func (c *Controller) HandleClear(w http.ResponseWriter, r *http.Request) error {
	switch source := r.URL.Query().Get("source"); source {
	case "", "live":
		c.ring.Clear()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"source": "live", "cleared": true})
		return nil
	case "archive":
		f, err := parseArchiveFilter(r.URL.Query())
		if err != nil {
			return err
		}
		f.Limit = 0 // a delete is not a page of results
		n, err := c.store.DeleteRequestLogArchive(f)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"source": "archive", "deleted": n})
		return nil
	default:
		return badSource(source)
	}
}

func badSource(got string) error {
	return httpx.Errorf(http.StatusBadRequest, "unknown source %q", got).WithHint(
		"bad_source", `source must be "live" (the in-memory log, default) or "archive" (what was kept in the store).`)
}

// parseFilter builds a live-log filter from query parameters.
func parseFilter(q map[string][]string) (reqlog.Filter, error) {
	get := func(k string) string { return firstValue(q, k) }

	f := reqlog.Filter{
		Surface:    reqlog.Surface(get("surface")),
		Method:     get("method"),
		PathPrefix: get("path"),
		Approval:   get("approval"),
		Code:       get("code"),
		Text:       get("q"),
		Limit:      defaultLimit,
	}
	var err error
	if f.Since, err = parseTime(get("since")); err != nil {
		return f, err
	}
	if f.Until, err = parseTime(get("until")); err != nil {
		return f, err
	}
	if f.StatusMin, err = parseInt(get("status_min")); err != nil {
		return f, err
	}
	if f.StatusMax, err = parseInt(get("status_max")); err != nil {
		return f, err
	}
	minDur, err := parseInt(get("min_dur_ms"))
	if err != nil {
		return f, err
	}
	f.MinDurMs = int64(minDur)
	if limit, err := parseInt(get("limit")); err != nil {
		return f, err
	} else if limit > 0 {
		f.Limit = limit
	}
	if err := validateSurface(f.Surface); err != nil {
		return f, err
	}
	return f, nil
}

// parseArchiveFilter mirrors parseFilter for the SQL side. The two filter types
// are kept apart because one compares time.Time and the other compares stored
// strings; parsing here means an invalid time is rejected the same way for both.
func parseArchiveFilter(q map[string][]string) (storage.RequestLogFilter, error) {
	live, err := parseFilter(q)
	if err != nil {
		return storage.RequestLogFilter{}, err
	}
	f := storage.RequestLogFilter{
		Surface:    string(live.Surface),
		Method:     live.Method,
		PathPrefix: live.PathPrefix,
		Approval:   live.Approval,
		Code:       live.Code,
		StatusMin:  live.StatusMin,
		StatusMax:  live.StatusMax,
		MinDurMs:   live.MinDurMs,
		Text:       live.Text,
		Limit:      live.Limit,
	}
	if !live.Since.IsZero() {
		f.Since = storage.FormatLogTime(live.Since)
	}
	if !live.Until.IsZero() {
		f.Until = storage.FormatLogTime(live.Until)
	}
	return f, nil
}

func validateSurface(s reqlog.Surface) error {
	switch s {
	case "", reqlog.SurfaceAPI, reqlog.SurfaceAIAPI:
		return nil
	}
	return httpx.Errorf(http.StatusBadRequest, "unknown surface %q", s).WithHint(
		"bad_surface", `surface must be "api" (devhub's own pages) or "ai-api" (local agents and CLIs).`)
}

func firstValue(q map[string][]string, k string) string {
	if v := q[k]; len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

// parseTime accepts RFC3339, or a relative "-15m" / "-2h" offset from now,
// which is what a filter for "the last few minutes" actually wants to say.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if strings.HasPrefix(s, "-") {
		d, err := time.ParseDuration(s)
		if err != nil {
			return time.Time{}, httpx.Errorf(http.StatusBadRequest, "bad relative time %q", s).WithHint(
				"bad_time", `A relative time looks like "-15m", "-2h" or "-1h30m".`)
		}
		return time.Now().Add(d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, httpx.Errorf(http.StatusBadRequest, "bad time %q", s).WithHint(
			"bad_time", `Use RFC3339 (2026-08-05T12:00:00Z) or a relative offset like "-15m".`)
	}
	return t, nil
}

func parseInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, httpx.Errorf(http.StatusBadRequest, "not a number: %q", s).WithHint(
			"bad_number", "This parameter takes a plain integer.")
	}
	return n, nil
}
