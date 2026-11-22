package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A controller (New) answers the sign-in page as the HTML page. The
// playground (NewPlayground) has no sign-in route at all: it is in the auth
// mount, which only New adds (mounts_controller_test.go), so the browser
// build carries neither the route nor the templates (pages.go).
func TestPlaygroundServesNoHTMLPages(t *testing.T) {
	ctl := call(t, New(newTestRuntime(t)), "GET", "/signin", "")
	if ct := ctl.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") ||
		!strings.Contains(ctl.Body.String(), "<!doctype html>") {
		t.Errorf("controller sign-in page: %s %q", ct, ctl.Body)
	}
	pg := call(t, NewPlayground(newTestRuntime(t)), "GET", "/signin", "")
	if strings.Contains(pg.Body.String(), "<!doctype html>") || strings.Contains(pg.Body.String(), "not set up") {
		t.Errorf("playground answered the sign-in page: %d %q", pg.Code, pg.Body)
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
