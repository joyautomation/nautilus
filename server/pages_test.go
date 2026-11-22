package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A controller (New) answers the sign-in page as the HTML page; the
// playground (NewPlayground) answers the same route with its message as
// plain text and no markup, because it carries no templates (pages.go).
func TestPlaygroundServesNoHTMLPages(t *testing.T) {
	ctl := call(t, New(newTestRuntime(t)), "GET", "/signin", "")
	if ct := ctl.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") ||
		!strings.Contains(ctl.Body.String(), "<!doctype html>") {
		t.Errorf("controller sign-in page: %s %q", ct, ctl.Body)
	}

	pg := call(t, NewPlayground(newTestRuntime(t)), "GET", "/signin", "")
	if pg.Code != ctl.Code {
		t.Errorf("playground status %d, controller %d: only the body may differ", pg.Code, ctl.Code)
	}
	body := pg.Body.String()
	if ct := pg.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("playground sign-in page Content-Type %q, want text/plain", ct)
	}
	if pg.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("playground page: no nosniff")
	}
	if strings.Contains(body, "<") || !strings.Contains(body, "not set up") {
		t.Errorf("playground sign-in page %q: want the notice, no markup", body)
	}
}

// writePage with no pages: a message that is empty says the status.
func TestPlainPageNamesItsStatus(t *testing.T) {
	srv := NewPlayground(newTestRuntime(t))
	rec := httptest.NewRecorder()
	srv.writePage(rec, http.StatusServiceUnavailable, pageAckLink, linkPageData{}, "  ")
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "Service Unavailable\n" {
		t.Errorf("empty message: %d %q", rec.Code, rec.Body)
	}
}

// The other three pages, rendered by their own functions: HTML under New,
// their message as plain text under NewPlayground.
func TestEveryPageHasAPlainForm(t *testing.T) {
	for _, c := range []struct {
		name   string
		render func(s *Server, w http.ResponseWriter)
		msg    string
	}{
		{"oidc hop", func(s *Server, w http.ResponseWriter) { s.oidcHop(w, "Signed in as akhan.", "/hmi") }, "Signed in as akhan."},
		{"elevate", func(s *Server, w http.ResponseWriter) {
			s.elevatePage(w, http.StatusForbidden, "Not confirmed.", "/hmi")
		}, "Not confirmed."},
		{"ack link", func(s *Server, w http.ResponseWriter) {
			s.linkPage(w, http.StatusGone, linkPageData{Title: "Not acknowledged", Error: "This link was used."})
		}, "Not acknowledged: This link was used."},
	} {
		ctl := httptest.NewRecorder()
		c.render(New(newTestRuntime(t)), ctl)
		if !strings.HasPrefix(ctl.Header().Get("Content-Type"), "text/html") || !strings.Contains(ctl.Body.String(), "<!doctype html>") {
			t.Errorf("%s under New: %q %q", c.name, ctl.Header().Get("Content-Type"), ctl.Body)
		}
		pg := httptest.NewRecorder()
		c.render(NewPlayground(newTestRuntime(t)), pg)
		if pg.Code != ctl.Code || !strings.HasPrefix(pg.Header().Get("Content-Type"), "text/plain") || pg.Body.String() != c.msg+"\n" {
			t.Errorf("%s under NewPlayground: %d (controller %d) %q %q", c.name, pg.Code, ctl.Code, pg.Header().Get("Content-Type"), pg.Body)
		}
	}
}
