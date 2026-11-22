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
