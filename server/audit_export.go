package server

import (
	"bytes"
	"encoding/json"
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
	// line is the line being written; last the latest complete one (each
	// capped), so how a stream ended is known however long its lines are.
	line, last []byte
	// isErr, lastErr: the line (or the last complete one) held the event
	// marker, wherever in a long line it sat (JSON keys come sorted, so
	// "event" can follow a very long "error"); carry holds the bytes that
	// may start a marker split across writes.
	isErr, lastErr bool
	carry          []byte
}

var errorMarker = []byte(`"event":"error"`)

// auditLineMax caps what is kept of one line: an error event's "event"
// comes first, so a cut line still says what it was.
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
	// The marker may sit anywhere in a line, however long.
	scan := append(w.carry, p...)
	if bytes.Contains(scan, errorMarker) {
		w.isErr = true
	}
	w.carry = append(w.carry[:0], scan[max(0, len(scan)-len(errorMarker)+1):]...)
	for _, c := range p {
		if c == '\n' {
			if len(bytes.TrimSpace(w.line)) > 0 {
				w.last = append(w.last[:0], w.line...)
				w.lastErr = w.isErr
			}
			w.line, w.isErr, w.carry = w.line[:0], false, w.carry[:0]
		} else if len(w.line) < auditLineMax {
			w.line = append(w.line, c)
		}
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.NewResponseController reach the real writer's Flush.
func (w *auditStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// lastEvent is the "event" of the last line written (complete or not),
// and leaves that line as the body the entry's reason is read from. A cut
// line is not valid JSON, so its event is read from its start.
func (w *auditStreamWriter) lastEvent() string {
	b, isErr := bytes.TrimSpace(w.line), w.isErr
	if len(b) == 0 {
		b, isErr = bytes.TrimSpace(w.last), w.lastErr
	}
	w.buf.body.Reset()
	w.buf.body.Write(b)
	var j struct {
		Event string `json:"event"`
	}
	if isErr {
		return "error"
	}
	if json.Unmarshal(b, &j) == nil {
		return j.Event
	}
	return ""
}
