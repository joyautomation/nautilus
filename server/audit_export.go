package server

import (
	"bytes"
	"net/http"
)

// AuditTarget is what a request acts on, for a route another package
// registers through Audited: the facades' program routes.
type AuditTarget struct {
	Target, Before, After string
	Detail                map[string]any
}

func adaptTarget(f func(*http.Request, []byte) AuditTarget) targetFunc {
	if f == nil {
		return nil
	}
	return func(r *http.Request, body []byte) auditTarget {
		t := f(r, body)
		at := auditTarget{target: t.Target, detail: t.Detail}
		if t.Before != "" {
			at.before = t.Before
		}
		if t.After != "" {
			at.after = t.After
		}
		return at
	}
}

// Audited wraps h as a journalled write route named action: authorized
// by the seam, one entry (accepted, refused or failed), held until
// recorded. action must be classed (actionClass) and mapped (actionPerm).
func (s *Server) Audited(action string, tf func(*http.Request, []byte) AuditTarget, h http.HandlerFunc) http.Handler {
	return s.audited(action, adaptTarget(tf), h)
}

// MarkOutcomeUnknown marks a failed answer of an Audited or AuditedStream
// route whose outcome is not known (a deploy that failed after its write
// to a controller began): the entry is journalled as "unknown", not
// "error". Call it before the handler returns; on a stream, the headers
// on the wire are not changed, only the entry.
func MarkOutcomeUnknown(w http.ResponseWriter) {
	w.Header().Set(outcomeHeader, "unknown")
}

// JournalWarning puts a warning on the entry of an Audited or
// AuditedStream route's answer (its detail "warning"), whatever its
// result: a deploy that succeeded but could not read the controller's mode
// after it. Call it before the handler returns; it never reaches the
// client.
func JournalWarning(w http.ResponseWriter, text string) {
	w.Header().Set(journalWarningHeader, text)
}

// journalWarningHeader carries JournalWarning's text to the seam.
const journalWarningHeader = "X-Nautilus-Journal-Warning"

// AuditedStream is Audited for a route that streams its answer as NDJSON
// (a deploy's progress): bytes reach the client as written, the entry is
// recorded when h returns, and a last line {"event":"error"} records a
// failure although the status was 200.
func (s *Server) AuditedStream(action string, tf func(*http.Request, []byte) AuditTarget, h http.HandlerFunc) http.Handler {
	a := s.audited(action, adaptTarget(tf), h)
	a.stream = true
	return a
}

// auditStreamWriter passes a streamed answer through and keeps its tail, so
// the seam can read how it ended.
type auditStreamWriter struct {
	http.ResponseWriter
	buf *bufferedResponse
	// The stream's end is its last terminal event: {"event":"done"…} is
	// success, {"event":"error"…} a failure, and a stream with neither
	// ended without a result. A terminal line is found by its marker
	// wherever it sits in the line (JSON keys come sorted, so "event" can
	// follow a very long "error"); lineTerm is the current line's, term the
	// last seen, termLine that line's start (capped), for the reason.
	line, termLine []byte
	lineTerm, term string
	carry          []byte
}

var terminalMarkers = []struct{ marker, event string }{
	{`"event":"done"`, "done"},
	{`"event":"error"`, "error"},
}

// auditLineMax caps what is kept of one line.
const auditLineMax = 16 << 10

func (w *auditStreamWriter) WriteHeader(code int) {
	if w.buf.status == 0 {
		w.buf.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *auditStreamWriter) Write(p []byte) (int, error) {
	if w.buf.status == 0 {
		w.buf.status = http.StatusOK
	}
	for _, c := range p {
		w.see(c)
	}
	return w.ResponseWriter.Write(p)
}

// see takes one byte of the stream: it keeps the line (capped), looks for
// a terminal marker at the line's tail as it grows, and closes the line.
func (w *auditStreamWriter) see(c byte) {
	if c == '\n' {
		w.endLine()
		return
	}
	w.carry = append(w.carry, c)
	for _, m := range terminalMarkers {
		if bytes.HasSuffix(w.carry, []byte(m.marker)) {
			w.lineTerm = m.event
		}
	}
	if len(w.carry) > 32 {
		w.carry = w.carry[len(w.carry)-32:]
	}
	if len(w.line) < auditLineMax {
		w.line = append(w.line, c)
	}
}

func (w *auditStreamWriter) endLine() {
	if w.lineTerm != "" {
		w.term = w.lineTerm
		w.termLine = append(w.termLine[:0], w.line...)
	}
	w.line, w.lineTerm, w.carry = w.line[:0], "", w.carry[:0]
}

// Unwrap lets http.NewResponseController reach the real writer's Flush.
func (w *auditStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ended is how the stream ended: "done", "error", or "" when it ended
// without a terminal event. A last line written without its newline
// counts. It leaves the terminal line as the body the entry's reason is
// read from.
func (w *auditStreamWriter) ended() string {
	w.endLine()
	w.buf.body.Reset()
	w.buf.body.Write(bytes.TrimSpace(w.termLine))
	return w.term
}
