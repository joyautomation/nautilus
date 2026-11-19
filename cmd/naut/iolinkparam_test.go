package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// paramController is a real controller server over a fake port device whose
// V_Setpoint is 100 and whose identity is the state it is set to.
func paramController(t *testing.T, token string, identity *string) *httptest.Server {
	t.Helper()
	rt, err := runtime.New(runtime.Options{Program: "PROGRAM Main\nEND_PROGRAM\n", Driver: nio.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	value := any(int64(100))
	answer := func() server.ParamAnswer {
		return server.ParamAnswer{Port: "LEVEL1", Variable: "V_Setpoint", Index: 64, Value: value, Identity: *identity, IdentityChecked: true}
	}
	s := server.New(rt, server.Options{AuthToken: token, Params: &server.ParamHook{
		List: func(port string) (server.ParamList, bool) {
			if port != "LEVEL1" {
				return server.ParamList{}, false
			}
			return server.ParamList{Port: port, Master: "M", Params: []server.ParamInfo{
				{ID: "V_Setpoint", Index: 64, Ref: "64", Type: "UIntegerT", Access: "rw", Octets: 2, Range: []string{"0..1000"}, Default: "100", Carried: true},
			}}, true
		},
		Read: func(_ context.Context, port, ref string) (server.ParamAnswer, error) {
			if ref != "V_Setpoint" {
				return server.ParamAnswer{}, fmt.Errorf("%w: no %s", server.ErrParamUnknown, ref)
			}
			return answer(), nil
		},
		Write: func(_ context.Context, port, ref string, v any) (server.ParamAnswer, error) {
			if *identity != "Ok" {
				return server.ParamAnswer{}, fmt.Errorf("%w: the device on the port is not the one expected (%s)", server.ErrParamRefused, *identity)
			}
			if n, ok := v.(int64); ok && n > 1000 {
				return server.ParamAnswer{}, fmt.Errorf("%w: %d is outside the IODD's range", server.ErrParamInvalid, n)
			}
			if ref == "boom" {
				return server.ParamAnswer{}, errors.New("the master timed out")
			}
			value = v
			return answer(), nil
		},
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func param(t *testing.T, base, cmd string, rest ...string) (code int, out, errs string) {
	t.Helper()
	var o, e bytes.Buffer
	code = iolinkParamAt(base, cmd, rest, false, &o, &e)
	return code, o.String(), e.String()
}

func TestIOLinkParamAgainstAController(t *testing.T) {
	t.Setenv("NAUTILUS_TOKEN", "")
	identity := "Ok"
	srv := paramController(t, "", &identity)

	code, out, errs := param(t, srv.URL, "list", "LEVEL1")
	if code != 0 || !strings.Contains(out, "V_Setpoint") || !strings.Contains(out, "0..1000") || !strings.Contains(out, "on M") {
		t.Errorf("list: %d %q %q", code, out, errs)
	}
	code, out, errs = param(t, srv.URL, "get", "LEVEL1", "V_Setpoint")
	if code != 0 || !strings.Contains(out, "V_Setpoint = 100") || !strings.Contains(out, "identity Ok") || errs != "" {
		t.Errorf("get: %d %q %q", code, out, errs)
	}
	code, out, errs = param(t, srv.URL, "set", "LEVEL1", "V_Setpoint", "500")
	if code != 0 || !strings.Contains(out, "now holds 500") {
		t.Errorf("set: %d %q %q", code, out, errs)
	}
	// A read from the wrong device is answered and says so on stderr.
	identity = "WrongDevice"
	code, out, errs = param(t, srv.URL, "get", "LEVEL1", "V_Setpoint")
	if code != 0 || !strings.Contains(out, "identity WrongDevice") || !strings.Contains(errs, "not the one expected") {
		t.Errorf("get from the wrong device: %d %q %q", code, out, errs)
	}
	// A write to it is refused, with the controller's reason: exit 1.
	code, _, errs = param(t, srv.URL, "set", "LEVEL1", "V_Setpoint", "600")
	if code != 1 || !strings.Contains(errs, "409") || !strings.Contains(errs, "WrongDevice") {
		t.Errorf("set to the wrong device: %d %q", code, errs)
	}
	identity = "Ok"
	if code, _, errs = param(t, srv.URL, "set", "LEVEL1", "V_Setpoint", "5000"); code != 1 || !strings.Contains(errs, "422") || !strings.Contains(errs, "range") {
		t.Errorf("a value outside the range: %d %q", code, errs)
	}
	if code, _, errs = param(t, srv.URL, "get", "LEVEL1", "V_Nothing"); code != 1 || !strings.Contains(errs, "404") {
		t.Errorf("an unknown parameter: %d %q", code, errs)
	}
	if code, _, errs = param(t, srv.URL, "list", "NOPE"); code != 1 || !strings.Contains(errs, "404") {
		t.Errorf("an unknown port device: %d %q", code, errs)
	}
	// The master's own failure is the controller's 5xx: exit 2.
	if code, _, errs = param(t, srv.URL, "set", "LEVEL1", "boom", "1"); code != 2 || !strings.Contains(errs, "timed out") {
		t.Errorf("a master failure: %d %q", code, errs)
	}
}

// A write needs the controller's token, which the CLI sends from
// NAUTILUS_TOKEN; reads need none.
func TestIOLinkParamSendsTheToken(t *testing.T) {
	identity := "Ok"
	srv := paramController(t, "s3cret", &identity)
	t.Setenv("NAUTILUS_TOKEN", "")
	if code, _, errs := param(t, srv.URL, "set", "LEVEL1", "V_Setpoint", "500"); code != 1 || !strings.Contains(errs, "401") {
		t.Errorf("a write without the token: %d %q", code, errs)
	}
	if code, _, _ := param(t, srv.URL, "get", "LEVEL1", "V_Setpoint"); code != 0 {
		t.Errorf("a read without the token: %d", code)
	}
	t.Setenv("NAUTILUS_TOKEN", "s3cret")
	if code, out, errs := param(t, srv.URL, "set", "LEVEL1", "V_Setpoint", "500"); code != 0 || !strings.Contains(out, "now holds 500") {
		t.Errorf("a write with the token: %d %q %q", code, out, errs)
	}
}

func TestIOLinkParamUsageAndNoController(t *testing.T) {
	var o, e bytes.Buffer
	for name, args := range map[string][]string{
		"nothing":       {},
		"no host":       {"get", "LEVEL1", "V_Setpoint"},
		"unknown":       {"frob", "--host", "x"},
		"get no param":  {"get", "--host", "127.0.0.1", "LEVEL1"},
		"set no value":  {"set", "--host", "127.0.0.1", "LEVEL1", "V_Setpoint"},
		"list too many": {"list", "--host", "127.0.0.1", "LEVEL1", "extra"},
	} {
		if code := runIOLinkParam(args, &o, &e); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if code, _, errs := param(t, "http://127.0.0.1:1", "get", "LEVEL1", "V_Setpoint"); code != 2 || !strings.Contains(errs, "no controller at") {
		t.Errorf("no controller: %d %q", code, errs)
	}
}

func TestParamValueOf(t *testing.T) {
	for in, want := range map[string]any{
		"500": int64(500), "-3": int64(-3), "2.5": 2.5, "true": true, "false": false,
		"Pump": "Pump", "18446744073709551615": uint64(18446744073709551615), "0x10": "0x10",
	} {
		if got := paramValueOf(in, false); got != want {
			t.Errorf("%q = %#v, want %#v", in, got, want)
		}
	}
	if got := paramValueOf("500", true); got != "500" {
		t.Errorf("--string: %#v", got)
	}
}

// A write outwaits the controller's own two calls (the read before it, then
// the write): a slow master is not "no controller", and a write that was
// sent and got no answer says the outcome is unknown, is not retried, and
// never says the parameter now holds the value.
func TestIOLinkParamWriteTimeoutsAndUncertainty(t *testing.T) {
	t.Setenv("NAUTILUS_TOKEN", "")
	oldGet, oldPut := paramGetTimeout, paramPutTimeout
	t.Cleanup(func() { paramGetTimeout, paramPutTimeout = oldGet, oldPut })
	paramGetTimeout, paramPutTimeout = 150*time.Millisecond, 2*time.Second
	var puts atomic.Int32
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			time.Sleep(400 * time.Millisecond) // a slow master, longer than a read's limit here
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/slow"): // slower than a read may take, within a write's
			time.Sleep(400 * time.Millisecond)
			w.Write([]byte(`{"variable":"V","value":1,"identity":"Ok","identityChecked":true}`))
		case strings.HasSuffix(r.URL.Path, "/hang"):
			puts.Add(1)
			<-hang
		case strings.HasSuffix(r.URL.Path, "/uncertain"):
			w.Header().Set("X-Nautilus-Outcome", "unknown")
			http.Error(w, `{"error":"the device on the port changed during the write"}`, http.StatusBadGateway)
		}
	}))
	t.Cleanup(func() { close(hang); srv.Close() })

	if code, _, errs := param(t, srv.URL, "get", "LEVEL1", "V"); code != 2 || !strings.Contains(errs, "no controller at") {
		t.Errorf("a read slower than its limit: %d %q", code, errs)
	}
	if code, out, errs := param(t, srv.URL, "set", "LEVEL1", "slow", "1"); code != 0 || !strings.Contains(out, "now holds") {
		t.Errorf("a write that takes longer than a read may: %d %q %q (it must outwait the controller)", code, out, errs)
	}
	// Sent, never answered: outcome unknown, exit 2, no 'now holds', one request.
	paramPutTimeout = 200 * time.Millisecond
	code, out, errs := param(t, srv.URL, "set", "LEVEL1", "hang", "1")
	if code != 2 || !strings.Contains(errs, "outcome unknown") || strings.Contains(errs, "no controller") || strings.Contains(out, "now holds") || !strings.Contains(errs, "naut iolink param get") {
		t.Errorf("a write sent and not answered: %d %q %q", code, out, errs)
	}
	if n := puts.Load(); n != 1 {
		t.Errorf("the CLI sent the write %d times, want once: it never retries", n)
	}
	// The controller's own 'outcome unknown' answer: the same words, exit 2,
	// and not a word of 'now holds'.
	code, out, errs = param(t, srv.URL, "set", "LEVEL1", "uncertain", "1")
	if code != 2 || !strings.Contains(errs, "outcome unknown") || !strings.Contains(errs, "changed during the write") || strings.Contains(out, "now holds") {
		t.Errorf("an uncertain write: %d %q %q", code, out, errs)
	}
}
