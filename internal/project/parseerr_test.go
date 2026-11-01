package project

import (
	"strings"
	"testing"
)

// F-19 (#427): a parse error in one source file must be reported with that
// file's name and position, not as "this project declares no TYPE" against a
// tag typed with a TYPE another (parsing) file declares.
func TestParseErrorIsNamedNotBlamedOnTypes(t *testing.T) {
	err := newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Mode1, role: state, type: Mode }\n",
		"types.st":      "TYPE Mode : (Idle, Run); END_TYPE\n",
		"main.st":       "PROGRAM Main\nVAR\n    Constant : INT;\nEND_VAR\nEND_PROGRAM\n",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "declares no TYPE") {
		t.Errorf("parse failure reported as a missing TYPE: %v", err)
	}
	if !strings.Contains(msg, "main.st:3:") {
		t.Errorf("error does not name main.st and the line: %v", err)
	}
}

// The same for a library file.
func TestLibraryParseErrorIsNamed(t *testing.T) {
	err := newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\n",
		"lib.st":        "FUNCTION_BLOCK F\nVAR\n    Constant : INT;\nEND_VAR\nEND_FUNCTION_BLOCK\n",
		"main.st":       "PROGRAM Main\nEND_PROGRAM\n",
	})
	if err == nil || !strings.Contains(err.Error(), "lib.st:3:") {
		t.Errorf("err = %v, want it to name lib.st:3:", err)
	}
}
