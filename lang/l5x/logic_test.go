package l5x

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A setpoint change is not logic drift; a rung change is.
func TestLogicIgnoresValues(t *testing.T) {
	a, b := LogicOf(load(t, "demoline.L5X")), LogicOf(load(t, "demoline.v80.L5X"))
	if d := LogicDiff(a, b); len(d) != 0 {
		t.Errorf("a setpoint change reported as logic drift: %v", d)
	}
}

func TestLogicDiffNamesTheChange(t *testing.T) {
	src := string(mustRead(t, "demoline.L5X"))
	edited := strings.Replace(src, "XIO(StopPB)OTE(RunCmd);", "XIO(StopPB)XIO(HiLevelAlm)OTE(RunCmd);", 1)
	edited = strings.Replace(edited, `<Tag Name="StartPB" TagType="Base" DataType="BOOL"`, `<Tag Name="StartPB" TagType="Base" DataType="DINT"`, 1)
	f, err := Parse([]byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	d := LogicDiff(LogicOf(load(t, "demoline.L5X")), LogicOf(f))
	joined := strings.Join(d, "\n")
	for _, want := range []string{"tag MainProgram.StartPB: BOOL vs DINT", "routine MainProgram/MainRoutine rung 0:", "XIO(HiLevelAlm)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diff lacks %q:\n%s", want, joined)
		}
	}
	if len(d) != 2 {
		t.Errorf("%d differences, want 2:\n%s", len(d), joined)
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
