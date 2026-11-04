package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// opcua.anonymous-writes against asyncua (testdata/foreign/asyncua_anonymous.py),
// connected the way a lab simulator connects: None, no certificate,
// anonymous. On, it writes what opcua.writable opens with asyncua's plain
// write_value, is refused what is not open or does not fit, and reads
// UserAccessLevel CurrentRead|CurrentWrite on a writable tag. Off, the
// same client is refused every write with Bad_UserAccessDenied and reads
// CurrentRead. Gated on NAUTILUS_OPCUA_PYTHON like the rest of the foreign
// suite.
func TestForeignAsyncuaAnonymousWrites(t *testing.T) {
	python := os.Getenv("NAUTILUS_OPCUA_PYTHON")
	if python == "" {
		t.Skip("set NAUTILUS_OPCUA_PYTHON=/path/to/python (with asyncua installed) to run the foreign-stack test")
	}
	script, err := filepath.Abs(filepath.Join("testdata", "foreign", "asyncua_anonymous.py"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, anonymous bool) (map[string]any, *Server) {
		t.Helper()
		rt, _ := newRuntime(t)
		// No pki: the lab manifest, None alone.
		s, err := New(rt, Options{Endpoint: "localhost:0", Name: "lab", Sweep: 20 * time.Millisecond,
			Writable: []string{"Running", "Count", "Level", "P101.Drive", "Levels"}, AnonymousWrites: anonymous})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Stop)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, python, "-I", script, s.URL())
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("asyncua client: %v; stderr:\n%s", err, stderr.String())
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("asyncua client printed %q: %v", out, err)
		}
		if m["ok"] != true {
			t.Fatalf("asyncua client: %v", m)
		}
		return m, s
	}
	access := func(m map[string]any, addr, attr string) any {
		a, _ := m["access"].(map[string]any)
		n, _ := a[addr].(map[string]any)
		return n[attr]
	}

	t.Run("on", func(t *testing.T) {
		m, s := run(t, true)
		got, _ := m["writes"].(map[string]any)
		for name, want := range map[string]any{"Level": 4.25, "Count": float64(7), "Running": false, "P101.Drive.Speed": 1450.0} {
			r, _ := got[name].(map[string]any)
			if r["ok"] != true || r["value"] != want {
				t.Errorf("anonymous write %s: %v, want it to land as %v", name, r, want)
			}
		}
		for name, code := range map[string]string{"Unmatched": "BadNotWritable", "CountTooBig": "BadOutOfRange", "Array": "BadWriteNotSupported"} {
			r, _ := got[name].(map[string]any)
			if r["ok"] == true || !strings.Contains(fmt.Sprint(r["error"]), code) {
				t.Errorf("anonymous write %s: %v, want %s", name, r, code)
			}
		}
		if v, _ := s.rt.Tags().ReadPath("P101.Drive.Speed"); v != 1450.0 {
			t.Errorf("the store holds P101.Drive.Speed = %v", v)
		}
		if a, u := access(m, "Level", "accessLevel"), access(m, "Level", "userAccessLevel"); a != float64(3) || u != float64(3) {
			t.Errorf("Level AccessLevel %v UserAccessLevel %v, want 3 and 3", a, u)
		}
		if u := access(m, "P101.Running", "userAccessLevel"); u != float64(1) {
			t.Errorf("P101.Running UserAccessLevel %v, want 1", u)
		}
	})
	t.Run("off", func(t *testing.T) {
		m, s := run(t, false)
		got, _ := m["writes"].(map[string]any)
		for _, name := range []string{"Level", "Count", "Running", "P101.Drive.Speed", "Unmatched"} {
			r, _ := got[name].(map[string]any)
			if r["ok"] == true || !strings.Contains(fmt.Sprint(r["error"]), "BadUserAccessDenied") {
				t.Errorf("anonymous write %s with the switch off: %v, want BadUserAccessDenied", name, r)
			}
		}
		if v, _ := s.rt.Tags().ReadPath("P101.Drive.Speed"); v == 1450.0 {
			t.Error("a refused write landed")
		}
		if a, u := access(m, "Level", "accessLevel"), access(m, "Level", "userAccessLevel"); a != float64(3) || u != float64(1) {
			t.Errorf("Level AccessLevel %v UserAccessLevel %v, want 3 and 1", a, u)
		}
	})
}
