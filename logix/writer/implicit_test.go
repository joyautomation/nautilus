package writer

import (
	"strings"
	"testing"
)

// A rung that names a manifest tag without a VAR_EXTERNAL (#177/#210) still
// gets its controller tag, typed from Options.Tags; a declared one is not
// written twice, and a tag no rung names is not written at all.
func TestImplicitTagsBecomeControllerTags(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL\n    Start : BOOL;\nEND_VAR\nLD\n  RUNG r1\n    Start CNT_OK ( Motor )\nEND_LD\nEND_PROGRAM\n"
	out, diags, err := Write(src, Options{Tags: map[string]string{"Start": "BOOL", "Motor": "BOOL", "cnt_ok": "BOOL", "Unused": "REAL"}})
	if err != nil || len(diags) > 0 {
		t.Fatalf("err %v diags %v", err, diags)
	}
	s := string(out)
	for _, want := range []string{`<Tag Name="Start"`, `<Tag Name="Motor"`, `<Tag Name="cnt_ok"`} {
		if strings.Count(s, want) != 1 {
			t.Errorf("want exactly one %s in:\n%s", want, s)
		}
	}
	if strings.Contains(s, `Name="Unused"`) {
		t.Error("a tag no rung names was written")
	}
}
