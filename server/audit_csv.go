package server

import (
	stdcsv "encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// auditCSVColumns is the export's header: the entry's own fields, in the
// journal's order; before, after and detail as JSON text.
var auditCSVColumns = []string{"ts", "seq", "id", "user", "source", "action", "target", "result", "before", "after", "from", "session", "detail"}

// handleAuditCSV is GET /api/audit.csv: the journal as CSV, oldest first,
// with the same filters as /api/audit (from, to, user, action, target,
// result) and an optional limit (none by default: an export is the whole
// range). With a journal on disk it reads the files, one line at a time,
// so a range reaches past the ring's last entries and nothing is held in
// memory; without one (the playground, an unwritable directory) it is the
// ring. It needs what /api/audit needs.
func (s *Server) handleAuditCSV(w http.ResponseWriter, r *http.Request) {
	if !s.auditReadable(w, r) {
		return
	}
	q := r.URL.Query()
	keep, ok := s.auditFilterOf(w, q)
	if !ok {
		return
	}
	from, _ := parseTime(q.Get("from"), time.Time{}) // validated by auditFilterOf
	limit := 0
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit: want a positive integer"})
			return
		}
		limit = n
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit-`+s.audit.now().UTC().Format("20060102T150405Z")+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := stdcsv.NewWriter(w)
	_ = cw.Write(auditCSVColumns)
	rc := http.NewResponseController(w)
	n := 0
	emit := func(e audit.Entry) bool {
		if !keep(e) {
			return true
		}
		if cw.Write(auditCSVRow(e)) != nil {
			return false // the client went away
		}
		if n++; n%500 == 0 {
			cw.Flush()
			_ = rc.Flush()
		}
		return limit == 0 || n < limit
	}
	// The ring holds the newest entries, among them any the journal has
	// not taken yet (a full disk): they are in the export too, after the
	// files', so the CSV and /api/audit never disagree about them.
	ring := s.audit.ring.Recent() // newest first
	if d, ok := s.audit.rec.(journalDir); ok && d.Dir() != "" {
		w.Header().Set("X-Audit-Source", "files")
		onDisk := make(map[string]bool, len(ring))
		for _, e := range ring {
			onDisk[e.ID] = false
		}
		// The files are the record. A read error mid-stream cannot change
		// the status: the connection is aborted, so the download fails
		// rather than looking complete.
		err := audit.Scan(d.Dir(), from, func(e audit.Entry) bool {
			if _, inRing := onDisk[e.ID]; inRing {
				onDisk[e.ID] = true
			}
			return emit(e)
		})
		if err != nil {
			cw.Flush()
			panic(http.ErrAbortHandler)
		}
		if limit == 0 || n < limit {
			for i := len(ring) - 1; i >= 0; i-- {
				if !onDisk[ring[i].ID] && !emit(ring[i]) {
					break
				}
			}
		}
	} else {
		w.Header().Set("X-Audit-Source", "ring")
		for i := len(ring) - 1; i >= 0; i-- {
			if !emit(ring[i]) {
				break
			}
		}
	}
	cw.Flush()
}

// journalDir is what a journal on disk offers (audit.FileJournal): where
// its files are. A recorder that wraps one must pass it on, or the export
// falls back to the ring (and says so in X-Audit-Source).
type journalDir interface{ Dir() string }

func auditCSVRow(e audit.Entry) []string {
	return []string{
		e.TS.UTC().Format(time.RFC3339Nano), strconv.FormatUint(e.Seq, 10), e.ID,
		csvText(e.User), csvText(e.Source), csvText(e.Action), csvText(e.Target), csvText(e.Result),
		csvJSON(e.Before), csvJSON(e.After), csvText(e.From), csvText(e.Session), csvJSON(e.Detail),
	}
}

func csvJSON(v any) string {
	if v == nil {
		return ""
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	if s, ok := v.(string); ok {
		return csvText(s)
	}
	// A number, bool, object or array: JSON never starts with = + or @,
	// and a leading - is a negative number, not a formula.
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// csvText stops a spreadsheet taking a cell for a formula: a person or a
// tag name chooses some of these words, and a cell that starts with = + -
// @ (or a tab or return) is run by Excel and Sheets. A quote in front
// makes it text.
func csvText(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
