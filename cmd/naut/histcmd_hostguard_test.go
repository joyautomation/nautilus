package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// #831: naut historian's own API answers by the controller's Host rule: a
// page on another site that rebinds a name to the historian cannot read the
// archive. localhost, an IP, a NAUTILUS_ALLOWED_HOSTS name, or the token
// (NAUTILUS_TOKEN, the one its poll sends) is answered.
func TestHistorianAnswersOnlyATrustedHostOrTheToken(t *testing.T) {
	get := func(h http.Handler, host, path, token string) int {
		req := httptest.NewRequest("GET", "http://"+host+path, nil)
		req.Host = host
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	paths := []string{"/history?tags=pv", "/history/span", "/history/agg?tags=pv&fn=avg", "/history/at?tags=pv", "/healthz", "/nope"}

	t.Setenv("NAUTILUS_ALLOWED_HOSTS", "historian")
	t.Setenv("NAUTILUS_TOKEN", "")
	open := (&historianServer{store: &fakeHistStore{}}).guarded()
	for _, p := range paths {
		if got := get(open, "evil.example:9100", p, ""); got != http.StatusForbidden {
			t.Errorf("GET %s by a rebound name: %d, want 403", p, got)
		}
		for _, host := range []string{"localhost:9100", "10.0.0.7:9100", "historian:9100"} {
			if got := get(open, host, p, ""); got == http.StatusForbidden {
				t.Errorf("GET %s by %s: 403, want it answered", p, host)
			}
		}
	}

	t.Setenv("NAUTILUS_TOKEN", "s3cret")
	locked := (&historianServer{store: &fakeHistStore{}}).guarded()
	if got := get(locked, "evil.example:9100", "/history/span", ""); got != http.StatusForbidden {
		t.Errorf("token historian, rebound name, no token: %d, want 403", got)
	}
	if got := get(locked, "hist.plant.lan:9100", "/history/span", "s3cret"); got == http.StatusForbidden {
		t.Errorf("token historian, any name with the token: 403")
	}
	if got := get(locked, "hist.plant.lan:9100", "/history/span", "wrong"); got != http.StatusForbidden {
		t.Errorf("token historian, wrong token: %d, want 403", got)
	}
}

// #831: the controller's /api/history proxy addresses the historian by the
// name it was configured as, not by the controller's own Host. A controller
// reached as an allowed name it trusts ("plc.lan") would otherwise hand
// "plc.lan" to the historian, which does not allow it, and every trend
// would come back 403.
func TestControllerProxyReachesAGuardedHistorian(t *testing.T) {
	t.Setenv("NAUTILUS_TOKEN", "")
	t.Setenv("NAUTILUS_ALLOWED_HOSTS", "")
	hist := httptest.NewServer((&historianServer{store: &fakeHistStore{}}).guarded())
	defer hist.Close()
	rt, err := runtime.New(runtime.Options{Program: "PROGRAM P\nVAR x : REAL; END_VAR\nx := 1.0;\nEND_PROGRAM\n"})
	if err != nil {
		t.Fatal(err)
	}
	ctl := server.New(rt, server.Options{HistorianURL: hist.URL, AllowedHosts: []string{"plc.lan"}}).Handler()
	req := httptest.NewRequest("GET", "http://plc.lan/api/history/span", nil)
	req.Host = "plc.lan:8080"
	rec := httptest.NewRecorder()
	ctl.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/history/span through the controller: %d %q, want 200", rec.Code, rec.Body.String())
	}
}
