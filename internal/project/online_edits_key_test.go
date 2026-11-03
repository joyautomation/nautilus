package project

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// The 403 a controller gives a pushed program when online edits are off
// tells the user what to change. It must name a manifest key, not a Go
// field, and that key must be one that, set in nautilus.yaml, actually
// lets the same push through.
func TestOnlineEditsRefusalNamesTheManifestKey(t *testing.T) {
	const body = "tasks:\n  - program: main.st\ntags:\n  - { name: Out, role: state, init: 0.0 }\n"
	put := func(manifest string) *httptest.ResponseRecorder {
		t.Helper()
		proj, err := Load(mapProject(map[string]string{
			"nautilus.yaml": manifest,
			"main.st":       "PROGRAM Main\nOut := 1.0;\nEND_PROGRAM\n",
		}), "")
		if err != nil {
			t.Fatal(err)
		}
		rt, err := runtime.New(proj.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		server.New(rt, proj.Server).Handler().ServeHTTP(rec, httptest.NewRequest("PUT", "/api/program",
			bytes.NewBufferString(`{"source": "PROGRAM Main\nOut := 2.0;\nEND_PROGRAM\n"}`)))
		return rec
	}

	rec := put(body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT with online edits off = %d %s, want 403", rec.Code, rec.Body)
	}
	var refusal struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(refusal.Error, "Options") {
		t.Errorf("refusal names a Go field: %q", refusal.Error)
	}
	m := regexp.MustCompile(`set (server)\.([a-z-]+): true in nautilus\.yaml`).FindStringSubmatch(refusal.Error)
	if m == nil {
		t.Fatalf("refusal does not say which manifest key to set: %q", refusal.Error)
	}

	// Set exactly the key the message names, and the push is let through.
	rec = put(m[1] + ":\n  " + m[2] + ": true\n" + body)
	if rec.Code != http.StatusOK {
		t.Errorf("with %s.%s: true the PUT = %d %s, want 200", m[1], m[2], rec.Code, rec.Body)
	}
}
