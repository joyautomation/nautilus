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
}

const auditStreamTail = 8 << 10

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
	w.buf.body.Write(p)
	if over := w.buf.body.Len() - auditStreamTail; over > 0 {
		w.buf.body.Next(over)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.NewResponseController reach the real writer's Flush.
func (w *auditStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// lastEvent is the "event" of the last complete NDJSON line, and leaves
// that line as the body the entry's reason is read from.
func (w *auditStreamWriter) lastEvent() string {
	b := bytes.TrimSpace(w.buf.body.Bytes())
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	var j struct {
		Event string `json:"event"`
	}
	_ = json.Unmarshal(b, &j)
	w.buf.body.Reset()
	w.buf.body.Write(b)
	return j.Event
}
