package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// captureOutErr runs fn with stdout and stderr redirected and returns both.
func captureOutErr(t *testing.T, fn func() int) (stdout, stderr string, code int) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	re, we, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wo, we
	outc, errc := make(chan string), make(chan string)
	go func() { b, _ := io.ReadAll(ro); outc <- string(b) }()
	go func() { b, _ := io.ReadAll(re); errc <- string(b) }()
	code = fn()
	wo.Close()
	we.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-outc, <-errc, code
}

// oosController is a real server with an alarm engine of two alarms on
// two pieces of equipment: the CLI meets the routes it will meet on a box.
func oosController(t *testing.T) (host, port string, eng *alarm.Engine, authz *[]string) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL x : REAL; END_VAR\nx := x;\nEND_PROGRAM\n",
		Seed:    map[string]any{"x": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	cond := map[string]any{"P101.Fault": true, "P102.Fault": false}
	eng, err = alarm.New(alarm.Options{
		Defs: []alarm.Def{
			{ID: "P101_Fault", Tag: "P101.Fault", Equipment: "P101", Name: "Pump 101 fault",
				Priority: alarm.High, AckRequired: true, AutoClear: true, Shelvable: true},
			{ID: "P102_Fault", Tag: "P102.Fault", Equipment: "P102", Name: "Pump 102 fault",
				Priority: alarm.High, AckRequired: true, AutoClear: true, Shelvable: true},
		},
		Read: func(p string) (any, bool) { v, ok := cond[p]; return v, ok },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	eng.Evaluate()
	h := server.New(rt, server.Options{Alarms: eng}).Handler()
	authz = new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*authz = append(*authz, r.Header.Get("Authorization"))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u.Hostname(), u.Port(), eng, authz
}

func oosState(t *testing.T, eng *alarm.Engine, id string) alarm.State {
	t.Helper()
	rec, ok := eng.Lookup(id)
	if !ok {
		t.Fatalf("no alarm %s", id)
	}
	return rec.State
}

// naut alarms out-of-service | oos-list | in-service against a running
// controller: the equipment's alarm is suppressed with the reason, the
// list shows it, and in-service puts it back (unacknowledged, since its
// condition is still present). The other pump is untouched.
func TestAlarmsOutOfServiceCLI(t *testing.T) {
	host, port, eng, authz := oosController(t)
	t.Setenv("NAUTILUS_TOKEN", "tok-1")
	run := func(args ...string) (string, string, int) {
		return captureOutErr(t, func() int { return runAlarms(args) })
	}

	out, errOut, code := run("out-of-service", "P101", "--host", host, "--port", port, "--for", "8h", "--reason", "seal change, WO 4471")
	if code != 0 || !strings.Contains(out, "P101 out of service until") || !strings.Contains(out, "1 alarm(s) hidden") {
		t.Fatalf("out-of-service: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	eng.Evaluate()
	if st := oosState(t, eng, "P101_Fault"); st != alarm.Suppressed {
		t.Fatalf("P101_Fault is %s after out-of-service, want suppressed", st)
	}
	if st := oosState(t, eng, "P102_Fault"); st == alarm.Suppressed {
		t.Fatalf("P102_Fault suppressed too")
	}

	out, errOut, code = run("oos-list", "--host", host, "--port", port)
	if code != 0 || !strings.Contains(out, "EQUIPMENT") || !strings.Contains(out, "P101") ||
		!strings.Contains(out, "seal change, WO 4471") || strings.Contains(out, "P102") {
		t.Fatalf("oos-list: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}

	out, errOut, code = run("in-service", "P101", "--host", host, "--port", port)
	if code != 0 || !strings.Contains(out, "P101 back in service: 1 alarm(s) evaluated again") {
		t.Fatalf("in-service: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	eng.Evaluate()
	if st := oosState(t, eng, "P101_Fault"); st != alarm.UnackActive {
		t.Fatalf("P101_Fault is %s after in-service, want unack-active", st)
	}
	out, _, code = run("oos-list", "--host", host, "--port", port)
	if code != 0 || !strings.Contains(out, "no equipment is out of service") {
		t.Fatalf("oos-list after in-service: exit %d: %s", code, out)
	}

	// The controller's refusal reaches the person, with exit 1.
	_, errOut, code = run("out-of-service", "NoSuchPump", "--host", host, "--port", port, "--for", "1h", "--reason", "x")
	if code != 1 || !strings.Contains(errOut, "404") {
		t.Fatalf("unknown equipment: exit %d, stderr %s", code, errOut)
	}
	_, errOut, code = run("in-service", "P102", "--host", host, "--port", port)
	if code != 1 || !strings.Contains(errOut, "400") {
		t.Fatalf("in-service of equipment that is not out: exit %d, stderr %s", code, errOut)
	}
	// Every call carried NAUTILUS_TOKEN as a bearer credential.
	for i, a := range *authz {
		if a != "Bearer tok-1" {
			t.Fatalf("request %d carried Authorization %q", i, a)
		}
	}
}

// The CLI refuses what the controller would, before sending: no
// duration (there is no permanent out of service), both --for and
// --until, no reason, no host, no equipment. Nothing reaches the server.
func TestAlarmsOutOfServiceCLIRefusesBeforeSending(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	h, p := u.Hostname(), u.Port()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"out-of-service", "P101", "--host", h, "--port", p, "--reason", "x"}, "say how long"},
		{[]string{"out-of-service", "P101", "--host", h, "--port", p, "--for", "1h", "--until", "2030-01-01T00:00:00Z", "--reason", "x"}, "not both"},
		{[]string{"out-of-service", "P101", "--host", h, "--port", p, "--for", "1h"}, "--reason is required"},
		{[]string{"out-of-service", "P101", "--host", h, "--port", p, "--for", "1h", "--reason", "  "}, "--reason is required"},
		{[]string{"out-of-service", "P101", "--for", "1h", "--reason", "x"}, "--host is required"},
		{[]string{"out-of-service", "--host", h, "--for", "1h", "--reason", "x"}, "name the equipment first"},
		{[]string{"in-service", "--host", h}, "name the equipment first"},
	} {
		_, errOut, code := captureOutErr(t, func() int { return runAlarms(c.args) })
		if code != 2 || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, stderr %q, want 2 and %q", c.args, code, errOut, c.want)
		}
	}
	if hits != 0 {
		t.Fatalf("%d requests reached the controller", hits)
	}
}

// naut alarms push confirm-revocations POSTs to the confirm route with
// the token as a bearer credential and prints what ended; a refusal
// (no config permission, no fresh password) prints the controller's
// reason and exits 1.
func TestAlarmsPushConfirmRevocationsCLI(t *testing.T) {
	var method, path, authz string
	status, body := http.StatusOK, `{"ended":3,"stillHeld":0}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, authz = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("NAUTILUS_TOKEN", "tok-1")
	args := []string{"push", "confirm-revocations", "--host", u.Hostname(), "--port", u.Port()}

	out, errOut, code := captureOutErr(t, func() int { return runAlarms(args) })
	if code != 0 || method != http.MethodPost || path != "/api/push/confirm-revocations" || authz != "Bearer tok-1" {
		t.Fatalf("exit %d, %s %s auth %q\nstderr: %s", code, method, path, authz, errOut)
	}
	if !strings.Contains(out, "ended 3 held subscription(s); 0 still held") {
		t.Fatalf("stdout: %s", out)
	}

	status, body = http.StatusForbidden, `{"error":"needs a fresh password"}`
	out, errOut, code = captureOutErr(t, func() int { return runAlarms(args) })
	if code != 1 || !strings.Contains(errOut, "403") || !strings.Contains(errOut, "needs a fresh password") ||
		strings.Contains(errOut, `{"error"`) || out != "" {
		t.Fatalf("refusal: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}

	_, errOut, code = captureOutErr(t, func() int { return runAlarms([]string{"push", "confirm-revocations"}) })
	if code != 2 || !strings.Contains(errOut, "--host is required") {
		t.Fatalf("no host: exit %d, stderr %s", code, errOut)
	}
}
