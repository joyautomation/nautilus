// foreign_test.go is the foreign-implementation suite: the real server
// against asyncua (testdata/foreign/asyncua_client.py), the Python OPC UA
// stack, instead of gopcua's own client — so the address space, the
// encodings, the status codes and the subscription path are checked by
// somebody else's decoder.
//
// Gated on NAUTILUS_OPCUA_PYTHON=/path/to/python (one with asyncua
// installed; the opcua-foreign CI job, or `python3 -m venv v && v/bin/pip
// install asyncua` locally) exactly like the modbus suite's pymodbus gate —
// normal `go test ./...` skips it.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

func TestForeignAsyncuaClient(t *testing.T) {
	python := os.Getenv("NAUTILUS_OPCUA_PYTHON")
	if python == "" {
		t.Skip("set NAUTILUS_OPCUA_PYTHON=/path/to/python (with asyncua installed) to run the foreign-stack test")
	}
	rt, _ := newRuntime(t)
	rt.Tags().Set("Elapsed", ir.TimeVal(1500))
	rt.Tags().Set("Label", "")
	if err := rt.Tags().SetPath("P101.Drive.Speed", 1750.5); err != nil {
		t.Fatal(err)
	}
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "lift-station", Sweep: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)

	script, err := filepath.Abs(filepath.Join("testdata", "foreign", "asyncua_client.py"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-I", script, s.URL(), "lift-station")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	next := func() map[string]any {
		t.Helper()
		if !lines.Scan() {
			cmd.Wait()
			t.Fatalf("asyncua client ended early (%v); stderr:\n%s", cmd.ProcessState, stderr.String())
		}
		var m map[string]any
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			t.Fatalf("asyncua client printed %q: %v", lines.Text(), err)
		}
		return m
	}
	if ready := next(); ready["ready"] != true {
		t.Fatalf("expected the ready line, got %v", ready)
	}
	// The client is subscribed to Level: move it.
	rt.Tags().Set("Level", ir.RealVal(9.25))
	report := next()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("asyncua client: %v; stderr:\n%s", err, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Logf("asyncua stderr:\n%s", stderr.String())
	}

	get := func(path ...string) any {
		var cur any = report
		for _, k := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("report has no %v: %v", path, report)
			}
			cur = m[k]
		}
		return cur
	}
	has := func(list any, want string) bool {
		for _, v := range list.([]any) {
			if v == want {
				return true
			}
		}
		return false
	}

	// Discovery and browse.
	if !has(get("namespaces"), "urn:joyautomation:nautilus:lift-station") {
		t.Errorf("namespace array %v", get("namespaces"))
	}
	if !has(get("objects"), "lift-station") || get("folderClass") != "Object" {
		t.Errorf("Objects → %v (%v)", get("objects"), get("folderClass"))
	}
	for _, tag := range []string{"Running", "Count", "Level", "Label", "Elapsed", "State", "P101", "Levels", "Pumps"} {
		if !has(get("tags"), tag) {
			t.Errorf("folder lacks %s: %v", tag, get("tags"))
		}
	}
	for tag, class := range map[string]string{"Level": "Variable", "Levels": "Variable", "P101": "Object", "Pumps": "Object"} {
		if get("classes", tag) != class {
			t.Errorf("%s class %v, want %s", tag, get("classes", tag), class)
		}
	}
	if !has(get("P101"), "Drive") || !has(get("P101"), "Hours") || !has(get("P101"), "Running") {
		t.Errorf("P101 members %v", get("P101"))
	}
	if !has(get("P101.Drive"), "Speed") {
		t.Errorf("P101.Drive members %v", get("P101.Drive"))
	}
	if !has(get("Pumps"), "Pumps[1]") || !has(get("Pumps"), "Pumps[2]") {
		t.Errorf("Pumps elements %v", get("Pumps"))
	}

	// Reads: value, variant type, data type, status, timestamps.
	checks := []struct {
		node     string
		value    any
		variant  string
		dataType float64
	}{
		{"Level", 3.5, "Double", 11},
		{"Count", 42.0, "Int32", 6},
		{"Elapsed", 1500.0, "Double", 290},
	}
	for _, c := range checks {
		if get(c.node, "value") != c.value || get(c.node, "variant") != c.variant || get(c.node, "dataType") != c.dataType {
			t.Errorf("%s: %v", c.node, get(c.node))
		}
		if get(c.node, "status") != "Good" || get(c.node, "sourceTimestamp") != true || get(c.node, "serverTimestamp") != true {
			t.Errorf("%s status/timestamps: %v", c.node, get(c.node))
		}
	}
	if get("Level", "description") != "Wet well level [ft]" {
		t.Errorf("Level description %v", get("Level", "description"))
	}
	if al := get("Level", "accessLevel"); !has(al, "CurrentRead") || has(al, "CurrentWrite") {
		t.Errorf("Level access level %v", al)
	}
	if get("Speed", "value") != 1750.5 || get("Speed", "browseName") != "Speed" || get("Speed", "parent") != "P101.Drive" {
		t.Errorf("P101.Drive.Speed by node id: %v", get("Speed"))
	}
	if get("Levels", "isArray") != true || get("Levels", "valueRank") != 1.0 {
		t.Errorf("Levels: %v", get("Levels"))
	}
	if dims, ok := get("Levels", "arrayDimensions").([]any); !ok || len(dims) != 1 || dims[0] != 3.0 {
		t.Errorf("Levels dimensions %v", get("Levels", "arrayDimensions"))
	}
	if lv, ok := get("Levels", "value").([]any); !ok || len(lv) != 3 || lv[2] != 3.0 {
		t.Errorf("Levels value %v", get("Levels", "value"))
	}
	// An empty String is "" to a decoder that tells it from null (#434).
	if get("Label", "value") != "" || get("Label", "variant") != "String" {
		t.Errorf("Label holding \"\" read as %v", get("Label"))
	}
	if get("LabelNotified", "value") != "" {
		t.Errorf("Label holding \"\" notified as %v", get("LabelNotified"))
	}
	if get("unknown") != "BadNodeIdUnknown" {
		t.Errorf("unknown node read: %v", get("unknown"))
	}

	// Subscription: the initial value, then the change the test made.
	if get("subscription", "initial", "value") != 3.5 || get("subscription", "changed", "value") != 9.25 {
		t.Errorf("subscription %v", get("subscription"))
	}
	if get("subscription", "changed", "status") != "Good" || get("subscription", "changed", "sourceTimestamp") != true {
		t.Errorf("subscription change %v", get("subscription", "changed"))
	}

	// Write refused, value untouched.
	if get("write") != "BadUserAccessDenied" {
		t.Errorf("write: %v", get("write"))
	}
	if get("LevelAfterWrite") != 9.25 {
		t.Errorf("Level after the refused write: %v", get("LevelAfterWrite"))
	}
}
