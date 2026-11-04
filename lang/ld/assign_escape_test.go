package ld

import "testing"

// A `$'` (an escaped quote) or `$$` inside a string does not end it, so a
// `;` after it stays inside the string instead of splitting the statement.
func TestParseAssignmentsStringEscapes(t *testing.T) {
	as, err := ParseAssignments("Msg := 'it$'s;x$$'; Count := 2; Msg := 'q$$;'")
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 3 {
		t.Fatalf("got %d assignments, want 3: %v", len(as), PrintAssignments(as))
	}
	if as[0].Target != "Msg" || as[1].Target != "Count" || as[2].Target != "Msg" {
		t.Fatalf("targets: %v", PrintAssignments(as))
	}
}
