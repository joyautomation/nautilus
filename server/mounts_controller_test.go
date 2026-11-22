package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/webpush"
)

// controllerRoutes is every route of the controller-only groups
// (mounts_controller.go) with the path it is tried at.
var controllerRoutes = []struct{ pattern, path string }{
	{"GET /signin", "/signin"},
	{"POST /api/signin", "/api/signin"},
	{"POST /api/signout", "/api/signout"},
	{"POST /api/elevate", "/api/elevate"},
	{"GET /auth/oidc/start", "/auth/oidc/start"},
	{"GET /auth/oidc/callback", "/auth/oidc/callback"},
	{"GET /auth/oidc/elevated", "/auth/oidc/elevated"},
	{"GET /api/push/key", "/api/push/key"},
	{"GET /api/push/subscriptions", "/api/push/subscriptions"},
	{"GET /api/push/health", "/api/push/health"},
	{"POST /api/push/subscribe", "/api/push/subscribe"},
	{"POST /api/push/confirm-revocations", "/api/push/confirm-revocations"},
	{"DELETE /api/push/subscribe", "/api/push/subscribe"},
	{"GET /alarm-ack/{token}", "/alarm-ack/x"},
	{"POST /alarm-ack/{token}", "/alarm-ack/x"},
	{"POST /alarm-inbound/sms", "/alarm-inbound/sms"},
	{"POST /alarm-inbound/voice/{token}", "/alarm-inbound/voice/x"},
	{"POST /api/cluster/clear-duplicate", "/api/cluster/clear-duplicate"},
	{"POST /api/cluster/commission", "/api/cluster/commission"},
	{"POST /api/cluster/recommission", "/api/cluster/recommission"},
	{"POST /api/cluster/vouch", "/api/cluster/vouch"},
	{"GET /api/io", "/api/io"},
	{"GET /api/io/files", "/api/io/files"},
	{"POST /api/io/staged", "/api/io/staged"},
	{"GET /api/io/staged/{id}", "/api/io/staged/i"},
	{"POST /api/io/staged/{id}/validate", "/api/io/staged/i/validate"},
	{"POST /api/io/staged/{id}/apply", "/api/io/staged/i/apply"},
	{"POST /api/io/revert/cancel", "/api/io/revert/cancel"},
	{"GET /api/config/files", "/api/config/files"},
	{"GET /api/config/patch", "/api/config/patch"},
	{"POST /api/config/commit", "/api/config/commit"},
	{"GET /api/program/history", "/api/program/history"},
	{"POST /api/program/activate", "/api/program/activate"},
	{"POST /api/parameters/import", "/api/parameters/import"},
	{"GET /api/iolink/{port}/params", "/api/iolink/1/params"},
	{"GET /api/iolink/{port}/params/{ref}", "/api/iolink/1/params/r"},
	{"PUT /api/iolink/{port}/params/{ref}", "/api/iolink/1/params/r"},
	{"GET /api/history", "/api/history"},
	{"GET /api/history/", "/api/history/x"},
}

// The controller's own groups answer on New and on nothing else: each route is
// registered by controllerMounts, and NewPlayground has none of them, so a
// request for one is the API's JSON "no such endpoint" (the page routes fall
// to the playground's index).
func TestControllerRoutesAreMountedByNewOnly(t *testing.T) {
	mux := http.NewServeMux()
	for _, m := range controllerMounts() {
		m(New(newTestRuntime(t)), mux)
	}
	pg := NewPlayground(newTestRuntime(t))
	if len(pg.mounts) != 0 {
		t.Fatalf("NewPlayground has %d mounts, want none", len(pg.mounts))
	}
	ctl := New(newTestRuntime(t))
	if len(ctl.mounts) != len(controllerMounts()) {
		t.Fatalf("New has %d mounts, want the controller's %d", len(ctl.mounts), len(controllerMounts()))
	}
	for _, r := range controllerRoutes {
		method, _, _ := strings.Cut(r.pattern, " ")
		req := httptest.NewRequest(method, "http://127.0.0.1:8080"+r.path, nil)
		if _, got := mux.Handler(req); got != r.pattern {
			t.Errorf("%s %s: the controller's groups register %q, want %q", method, r.path, got, r.pattern)
		}
		if strings.HasPrefix(r.path, "/api/") {
			rec := httptest.NewRecorder()
			req.Host, req.RemoteAddr = "127.0.0.1:8080", "127.0.0.1:5555"
			pg.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such API endpoint") {
				t.Errorf("playground %s %s: %d %q, want the JSON 404", method, r.path, rec.Code, rec.Body)
			}
		}
	}
}

// A moved route keeps its refusal: on a controller with users and push set
// up and online edits on (so no route answers "not set up" before it asks who
// is calling), every audited write of the groups is refused with no session,
// before it does anything, as it was when Handler registered it.
func TestMountedWriteRoutesRefuseWithoutASession(t *testing.T) {
	key, err := webpush.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	srv, _, _ := patServerRT(t, newTestRuntime(t), Options{
		OnlineEdits: true,
		Push:        &PushOptions{Key: key, Subject: "mailto:ops@example.com"},
	})
	for _, r := range controllerRoutes {
		method, _, _ := strings.Cut(r.pattern, " ")
		if method == "GET" || selfJournalled[r.pattern] || strings.Contains(r.path, "alarm-ack") || strings.Contains(r.path, "alarm-inbound") {
			continue // reads; sign-in/out/elevate answer anonymous callers themselves; links and providers carry their own proof
		}
		rec := call(t, srv, method, r.path, `{}`)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no session: %d %s, want 401", method, r.path, rec.Code, rec.Body)
		}
	}
}
