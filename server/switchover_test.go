package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

// A change refused for a planned switchover (#710 d1c) answers 503 with
// Retry-After, as a moment's wait lands it on the new leader; any other
// error is left to the handler.
func TestRefuseSwitchingAnswers503(t *testing.T) {
	w := httptest.NewRecorder()
	if !refuseSwitching(w, fmt.Errorf("force Mode: %w", runtime.ErrSwitching)) {
		t.Fatal("a wrapped ErrSwitching was not answered")
	}
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("answer %d, Retry-After %q; want 503 and 1", w.Code, w.Header().Get("Retry-After"))
	}
	w = httptest.NewRecorder()
	if refuseSwitching(w, errors.New("out of range")) || refuseSwitching(w, nil) {
		t.Fatal("another error, or none, was answered as switching")
	}
}
