package opcua

import (
	"context"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/opcua/server"
	"github.com/joyautomation/nautilus/runtime"
)

// A nautilus server's enumeration tag has its enumeration's own DataType
// (ns=1;s=type:Mode, a subtype of Enumeration); the import follows the
// supertypes and binds it as the Int32 it is encoded as, rather than
// skipping it. A Duration (a subtype of Double) binds as Double.
func TestBrowseResolvesSubtypesOfBuiltins(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: `TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE
TYPE Pump : STRUCT State : Mode; END_STRUCT END_TYPE
PROGRAM Main
VAR_EXTERNAL State : Mode; P1 : Pump; Delay : TIME; Count : DINT; END_VAR
END_PROGRAM`,
		Driver: nio.NewMemory(),
		Scan:   time.Second,
		Tags: []runtime.TagDef{
			runtime.Typed("State", runtime.RoleSetpoint, "Mode"),
			runtime.Typed("P1", runtime.RoleSetpoint, "Pump"),
			runtime.Typed("Delay", runtime.RoleSetpoint, "TIME"),
			runtime.Typed("Count", runtime.RoleSetpoint, "DINT"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(rt, server.Options{Endpoint: "localhost:0", Name: "plant", Sweep: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	sess, err := Dial(context.Background(), srv.URL(), Connect{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	br, err := Browse(context.Background(), sess.Client, ObjectsFolder, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Variable{}
	for _, v := range br.Variables {
		byName[strings.Join(v.Path, "/")] = v
	}
	for name, want := range map[string]string{"plant/State": "Int32", "plant/P1/State": "Int32", "plant/Delay": "Double", "plant/Count": "Int32"} {
		v, ok := byName[name]
		if !ok {
			t.Errorf("browse missed %s (found %v)", name, keys(byName))
			continue
		}
		if v.Type != want || !v.Scalar {
			t.Errorf("%s: type %q scalar %v, want %s", name, v.Type, v.Scalar, want)
		}
	}
}
