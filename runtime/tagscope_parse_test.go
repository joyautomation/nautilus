package runtime

import (
	"strings"
	"testing"
)

// F-19 (#427): when the program source does not parse, the TYPE table is empty
// and a tag typed with a TYPE that source declares cannot resolve. New must
// report the parse error, not "this project declares no TYPE".
func TestTagScopeParseErrorNotBlamedOnTypes(t *testing.T) {
	src := `TYPE Mode : (Idle, Run); END_TYPE
PROGRAM Main
VAR
    Constant : INT;
END_VAR
    Constant := 7;
END_PROGRAM
`
	_, err := tagScopeFor(Options{
		Program: src,
		Tags:    []TagDef{{Name: "Mode1", Role: RoleSetpoint, Type: "Mode"}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "declares no TYPE") {
		t.Errorf("the parse failure was reported as a missing TYPE: %v", err)
	}
	// Exactly the parser's own message: nothing about tags or types.
	if want := `var decl: line 4: expected an identifier, got ":"`; msg != want {
		t.Errorf("error = %q, want %q", msg, want)
	}
}
