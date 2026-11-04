package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
	"github.com/joyautomation/nautilus/runtime"
)

// examples/plclab driven by a PLC-Lab-shaped client
// (testdata/foreign/asyncua_plclab.py): the example project as `naut run`
// builds it (its manifest, its programs, its opcua: section; only the
// endpoint moved to a free port), scanning in real time, and asyncua doing
// what MHJ PLC-Lab does with a CODESYS controller as far as its manual and
// a server PLC-Lab is known to accept show. It connects anonymously on
// None, finds PlcLabConnector by browse name, reads the SIZE_ words, writes
// the area tables and single input bytes by string node id, and polls
// Outputs; the tank logic must answer every step. Gated on
// NAUTILUS_OPCUA_PYTHON like the rest of the foreign suite.
func TestForeignPLCLabShapedClient(t *testing.T) {
	python := os.Getenv("NAUTILUS_OPCUA_PYTHON")
	if python == "" {
		t.Skip("set NAUTILUS_OPCUA_PYTHON=/path/to/python (with asyncua installed) to run the foreign-stack test")
	}
	script, err := filepath.Abs(filepath.Join("testdata", "foreign", "asyncua_plclab.py"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := project.Load(os.DirFS(filepath.Join("..", "..", "examples", "plclab")), "")
	if err != nil {
		t.Fatal(err)
	}
	if proj.OPCUA == nil || !proj.OPCUA.AnonymousWrites {
		t.Fatal("examples/plclab does not turn anonymous-writes on")
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { rt.Run(ctx); close(ran) }()
	t.Cleanup(func() { cancel(); <-ran })

	s, err := opcuaserver.New(rt, opcuaserver.Options{Endpoint: "localhost:0", Name: proj.Name, Sweep: 20 * time.Millisecond,
		Writable: proj.OPCUA.Writable, AnonymousWrites: proj.OPCUA.AnonymousWrites, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)

	cctx, ccancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer ccancel()
	cmd := exec.CommandContext(cctx, python, "-I", script, s.URL())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("asyncua client: %v; stderr:\n%s", err, stderr.String())
	}
	var got struct {
		OK         bool              `json:"ok"`
		Error      string            `json:"error"`
		Namespaces []string          `json:"namespaces"`
		Container  string            `json:"container"`
		Sizes      map[string][2]any `json:"sizes"`
		Access     map[string][2]int `json:"access"`
		OutWrite   string            `json:"outputsWrite"`
		OutLen     int               `json:"outputsLength"`
		Steps      []struct {
			Input    int `json:"input"`
			ReadBack int `json:"readBack"`
			Want     int `json:"want"`
			Got      int `json:"got"`
			Ms       int `json:"ms"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("asyncua client printed %q: %v", raw, err)
	}
	if !got.OK {
		t.Fatalf("asyncua client: %s\n%s", got.Error, raw)
	}
	t.Logf("asyncua client: %s", raw)

	// Where PLC-Lab finds the container: ns=1, the project's namespace.
	if got.Container != "ns=1;s=PlcLabConnector" {
		t.Errorf("PlcLabConnector found at %s, want ns=1;s=PlcLabConnector", got.Container)
	}
	if len(got.Namespaces) < 2 || got.Namespaces[1] != "urn:joyautomation:nautilus:plclab" {
		t.Errorf("NamespaceArray %v", got.Namespaces)
	}
	for name, want := range map[string]float64{"SIZE_AREA_I_ARRAY": 10, "SIZE_AREA_Q_ARRAY": 10, "SIZE_I_ARRAY": 1024, "SIZE_Q_ARRAY": 1024} {
		if v := got.Sizes[name]; v[0] != want || v[1] != "UInt16" {
			t.Errorf("%s = %v, want UInt16 %v", name, v, want)
		}
	}
	for path, want := range map[string][2]int{"Inputs[0]": {3, 3}, "AreaInputs[0].CntBytes": {3, 3}, "Outputs[0]": {1, 1}} {
		if got.Access[path] != want {
			t.Errorf("%s AccessLevel, UserAccessLevel = %v, want %v", path, got.Access[path], want)
		}
	}
	if got.OutWrite != "BadNotWritable" {
		t.Errorf("a write to Outputs[0]: %s, want BadNotWritable", got.OutWrite)
	}
	if got.OutLen != 1024 {
		t.Errorf("Outputs holds %d bytes, want 1024", got.OutLen)
	}
	if len(got.Steps) != 8 {
		t.Fatalf("%d steps ran, want 8: %s", len(got.Steps), raw)
	}
	for i, st := range got.Steps {
		if st.ReadBack != st.Input {
			t.Errorf("step %d: Inputs[0] reads back %d after a write of %d", i, st.ReadBack, st.Input)
		}
		if st.Got != st.Want {
			t.Errorf("step %d: input byte %d, Outputs[0] = %d after %d ms, want %d", i, st.Input, st.Got, st.Ms, st.Want)
		}
	}
	// The area tables PLC-Lab wrote are in the store, and the named tags
	// followed the last step (stop pressed).
	for path, want := range map[string]any{"PlcLabConnector.AreaOutputs[0].CntBytes": int64(1), "PlcLabMapped": true, "StopPB": false, "RunLamp": false} {
		if v, ok := rt.Tags().ReadPath(path); !ok || v != want {
			t.Errorf("%s = %v, want %v", path, v, want)
		}
	}
}
