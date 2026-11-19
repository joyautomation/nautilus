package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// fakeParams is a ParamHook over one port device, LEVEL1, with two
// parameters: V_Setpoint (rw, 0..1000) and V_SerialNumber (ro). Its
// identity is the state it is set to; writes are refused unless it is "Ok".
type fakeParams struct {
	mu       sync.Mutex
	identity string
	values   map[string]any
	writes   []any
}

func newFakeParams() *fakeParams {
	return &fakeParams{identity: "Ok", values: map[string]any{"V_Setpoint": int64(100), "V_SerialNumber": "SN1"}}
}

func (f *fakeParams) hook() *ParamHook {
	return &ParamHook{
		List: func(port string) (ParamList, bool) {
			if port != "LEVEL1" {
				return ParamList{}, false
			}
			return ParamList{Port: "LEVEL1", Master: "M", Params: []ParamInfo{
				{ID: "V_Setpoint", Index: 64, Ref: "64", Type: "UIntegerT", Access: "rw", Octets: 2, Range: []string{"0..1000"}, Default: "100", Carried: true},
				{ID: "V_SerialNumber", Index: 21, Ref: "21", Type: "StringT", Access: "ro", Octets: 16, Carried: true},
			}}, true
		},
		Read: func(_ context.Context, port, ref string) (ParamAnswer, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			v, ok := f.values[ref]
			if port != "LEVEL1" || !ok {
				return ParamAnswer{}, fmt.Errorf("%w: no %s on %s", ErrParamUnknown, ref, port)
			}
			return ParamAnswer{Port: port, Variable: ref, Index: 64, Value: v, Identity: f.identity, IdentityChecked: true}, nil
		},
		Write: func(_ context.Context, port, ref string, value any) (ParamAnswer, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, ok := f.values[ref]; port != "LEVEL1" || !ok {
				return ParamAnswer{}, fmt.Errorf("%w: no %s on %s", ErrParamUnknown, ref, port)
			}
			if f.identity != "Ok" {
				return ParamAnswer{}, fmt.Errorf("%w: the device on the port is not the one expected (%s)", ErrParamRefused, f.identity)
			}
			if ref == "V_SerialNumber" {
				return ParamAnswer{}, fmt.Errorf("%w: V_SerialNumber is read-only", ErrParamInvalid)
			}
			if n, ok := value.(int64); ok && n > 1000 {
				return ParamAnswer{}, fmt.Errorf("%w: %d is outside the IODD's range", ErrParamInvalid, n)
			}
			if ref == "boom" {
				return ParamAnswer{}, errors.New("the master timed out")
			}
			f.writes = append(f.writes, value)
			f.values[ref] = value
			return ParamAnswer{Port: port, Variable: ref, Index: 64, Value: value, Identity: f.identity, IdentityChecked: true}, nil
		},
	}
}

func paramServer(t *testing.T, f *fakeParams, o Options) *Server {
	t.Helper()
	o.Params = f.hook()
	return auditServer(t, o)
}

func TestParamRoutes(t *testing.T) {
	f := newFakeParams()
	s := paramServer(t, f, Options{})
	// The listing and a read.
	if rec := call(t, s, "GET", "/api/iolink/LEVEL1/params", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"V_Setpoint"`) || !strings.Contains(rec.Body.String(), `"0..1000"`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, "GET", "/api/iolink/NOPE/params", ""); rec.Code != 404 {
		t.Errorf("list of an unknown port device: %d", rec.Code)
	}
	if rec := call(t, s, "GET", "/api/iolink/LEVEL1/params/V_Setpoint", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"value":100`) || !strings.Contains(rec.Body.String(), `"identity":"Ok"`) {
		t.Errorf("read: %d %s", rec.Code, rec.Body)
	}
	// A read from the wrong device still answers, and says so.
	f.identity = "WrongDevice"
	if rec := call(t, s, "GET", "/api/iolink/LEVEL1/params/V_Setpoint", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"identity":"WrongDevice"`) {
		t.Errorf("read from the wrong device: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, "GET", "/api/iolink/LEVEL1/params/V_Nothing", ""); rec.Code != 404 {
		t.Errorf("read of an unknown parameter: %d", rec.Code)
	}
	// Writes: refused for the wrong device, accepted once it is right.
	if rec := call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 500}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "WrongDevice") {
		t.Errorf("write to the wrong device: %d %s", rec.Code, rec.Body)
	}
	if len(f.writes) != 0 {
		t.Errorf("a refused write reached the hook's store: %v", f.writes)
	}
	f.identity = "Ok"
	if rec := call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 500}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"value":500`) {
		t.Errorf("write: %d %s", rec.Code, rec.Body)
	}
	if len(f.writes) != 1 || f.writes[0] != int64(500) {
		t.Errorf("the hook saw %#v, want the integer 500", f.writes)
	}
	// What the IODD forbids is 422, a master's failure 502, a bad body 400.
	if rec := call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 5000}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a value outside the range: %d", rec.Code)
	}
	if rec := call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_SerialNumber", `{"value": "x"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a read-only variable: %d", rec.Code)
	}
	if rec := call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": [1]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a value that is not a scalar: %d", rec.Code)
	}
	if rec := call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no value: %d", rec.Code)
	}
	if rec := call(t, s, "PUT", "/api/iolink/NOPE/params/V_Setpoint", `{"value": 1}`); rec.Code != 404 {
		t.Errorf("a write to an unknown port device: %d", rec.Code)
	}
	f.values["boom"] = int64(0)
	if rec := call(t, s, "PUT", "/api/iolink/LEVEL1/params/boom", `{"value": 1}`); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "timed out") {
		t.Errorf("a master failure: %d %s", rec.Code, rec.Body)
	}
	// A fraction and text reach the hook as such.
	call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 2.5}`)
	call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": "Pump"}`)
	if n := len(f.writes); n < 3 || f.writes[n-2] != 2.5 || f.writes[n-1] != "Pump" {
		t.Errorf("the hook saw %#v, want 2.5 then Pump", f.writes)
	}
}

// With no port devices the routes say so; a write needs the write
// credential like any other; a standby does not write.
func TestParamRoutesNeedWhatWritesNeed(t *testing.T) {
	none := auditServer(t, Options{})
	if rec := call(t, none, "GET", "/api/iolink/LEVEL1/params", ""); rec.Code != 404 {
		t.Errorf("no hook, list: %d", rec.Code)
	}
	if rec := call(t, none, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 1}`); rec.Code != 404 {
		t.Errorf("no hook, write: %d", rec.Code)
	}
	f := newFakeParams()
	tok := paramServer(t, f, Options{AuthToken: "s3cret"})
	if rec := call(t, tok, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 500}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("a write without the token: %d", rec.Code)
	}
	if len(f.writes) != 0 {
		t.Error("an unauthorized write reached the hook")
	}
	if rec := call(t, tok, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 500}`, func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }); rec.Code != 200 {
		t.Errorf("a write with the token: %d %s", rec.Code, rec.Body)
	}
	// Reads need nothing, as GET /api/io needs nothing.
	if rec := call(t, tok, "GET", "/api/iolink/LEVEL1/params/V_Setpoint", ""); rec.Code != 200 {
		t.Errorf("a read with a token set: %d", rec.Code)
	}
}

func TestParamWriteIsRefusedOnAStandby(t *testing.T) {
	rt, err := runtime.New(runtime.Options{Program: testProgram, Driver: nio.NewMemory(), Coordinator: standbyCoord{}})
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeParams()
	s := New(rt, Options{Params: f.hook()})
	if rec := call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 500}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a write on a standby: %d %s", rec.Code, rec.Body)
	}
	if len(f.writes) != 0 {
		t.Error("a standby wrote a parameter")
	}
	if rec := call(t, s, "GET", "/api/iolink/LEVEL1/params/V_Setpoint", ""); rec.Code != 200 {
		t.Errorf("a read on a standby: %d", rec.Code)
	}
}

// Every write is journalled, accepted or refused, with the value the device
// held before, what was asked, and the identity.
func TestParamWriteIsJournalled(t *testing.T) {
	f := newFakeParams()
	s := paramServer(t, f, Options{})
	call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 500}`)
	f.identity = "WrongDevice"
	call(t, s, "PUT", "/api/iolink/LEVEL1/params/V_Setpoint", `{"value": 600}`)
	es := s.audit.ring.Recent()
	if len(es) != 2 {
		t.Fatalf("%d entries, want 2: %+v", len(es), es)
	}
	// Recent is newest first.
	ok, refused := es[1], es[0]
	if ok.Action != "param.write" || ok.Target != "LEVEL1/V_Setpoint" || ok.Result != "ok" || fmt.Sprint(ok.Before) != "100" || fmt.Sprint(ok.After) != "500" || ok.Detail["identity"] != "Ok" {
		t.Errorf("the accepted write: %+v", ok)
	}
	if refused.Result != "refused" || fmt.Sprint(refused.Before) != "500" || fmt.Sprint(refused.After) != "600" || refused.Detail["identity"] != "WrongDevice" || !strings.Contains(fmt.Sprint(refused.Detail["reason"]), "WrongDevice") {
		t.Errorf("the refused write: %+v", refused)
	}
}

func paramAuditServer(f *fakeParams) func(*testing.T) *Server {
	return func(t *testing.T) *Server { return paramServer(t, f, Options{}) }
}

func wrongDeviceParams() *fakeParams {
	f := newFakeParams()
	f.identity = "WrongDevice"
	return f
}
