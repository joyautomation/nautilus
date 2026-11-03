package runtime_test

import (
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// Two tasks declaring one enumeration name with different members are
// refused: a tag of that type could not say which Mode it meant, and the
// OPC UA server serves one EnumDefinition per name (#520 review). The same
// declaration in both is one type.
func TestTasksDeclaringOneEnumerationDifferentlyAreRefused(t *testing.T) {
	mainProg := `TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE
PROGRAM Main
VAR_EXTERNAL AMode : Mode; END_VAR
END_PROGRAM`
	opts := func(lib string) runtime.Options {
		return runtime.Options{
			Program: mainProg,
			Driver:  nio.NewMemory(),
			Tags:    []runtime.TagDef{runtime.Typed("AMode", runtime.RoleState, "Mode")},
			Tasks: []runtime.Task{{Name: "b", Libraries: []string{lib}, Scan: time.Second, Program: `PROGRAM B
VAR_GLOBAL BMode : Mode; END_VAR
END_PROGRAM`}},
		}
	}
	for name, lib := range map[string]string{
		"other names":  `TYPE Mode : (Off, On, Tripped); END_TYPE`,
		"other values": `TYPE Mode : (Idle, Run := 11, Fault); END_TYPE`,
		"fewer":        `TYPE Mode : (Idle, Run := 10); END_TYPE`,
	} {
		_, err := runtime.New(opts(lib))
		if err == nil || !strings.Contains(err.Error(), "TYPE Mode") {
			t.Errorf("%s: New = %v, want the TYPE Mode collision refused", name, err)
		}
	}
	if _, err := runtime.New(opts(`TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE`)); err != nil {
		t.Errorf("the same declaration twice: %v", err)
	}
}
