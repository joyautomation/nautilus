package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A Mount's routes exist only on a server given it, on the mux Handler builds
// each time, and the core routes are untouched.
func TestMountAddsItsRoutesOnlyWhereItIsGiven(t *testing.T) {
	hello := Mount(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/mounted-test", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) })
	})
	with := New(newTestRuntime(t), Options{Mounts: []Mount{hello}})
	without := New(newTestRuntime(t))
	get := func(s *Server, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost"+path, nil))
		return rec
	}
	for i := 0; i < 2; i++ { // Handler builds a new mux per call: the Mount runs again
		if rec := get(with, "/api/mounted-test"); rec.Code != 200 || rec.Body.String() != "hello" {
			t.Errorf("mounted route, call %d: %d %q", i, rec.Code, rec.Body)
		}
	}
	if rec := get(without, "/api/mounted-test"); rec.Code != http.StatusNotFound {
		t.Errorf("the route on a server with no Mount: %d, want 404", rec.Code)
	}
	if rec := get(with, "/api/state"); rec.Code != 200 {
		t.Errorf("a core route beside the Mount: %d", rec.Code)
	}
}
