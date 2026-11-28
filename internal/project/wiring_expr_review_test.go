package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

// Does a project reach a struct-typed expr: member, and an expr: member
// whose TYPE gives it an initial value?
func TestExprReviewProjectReach(t *testing.T) {
	lib := "FUNCTION MkSub : GaugeSub\nVAR_INPUT x : REAL; END_VAR\nMkSub.X := x; MkSub.Y := x;\nEND_FUNCTION\n"
	fsys := gaugeProject("      Sub: { expr: \"MkSub(A)\" }\n      Healthy: { expr: \"NOT Fault\" }\n", oneGauge, lib, "")
	if _, err := Load(fsys, ""); err != nil {
		t.Errorf("a struct-typed expr: member: %v", err)
	}
	fsys = gaugeProject("      Healthy: { expr: \"NOT Fault\" }\n", oneGauge, "", "")
	fsys["types.st"] = &fstest.MapFile{Data: []byte(strings.Replace(gaugeTypes, "Healthy : BOOL;", "Healthy : BOOL := TRUE;", 1))}
	if _, err := Load(fsys, ""); err != nil {
		t.Errorf("an expr: member with a TYPE initial value: %v", err)
	}
}
