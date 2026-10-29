package hw

import "testing"

// InputType: a struct tag's contract type, BOOL for __Online, LINT for
// __LastPollMs — the seed type for a tag with no type: (runtime.TagDef
// DriverType).
func TestBaseInputType(t *testing.T) {
	b, err := NewBase(switchConfig(newFakeWire()))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"SW1_Port01": "SwitchPort", "SW1__Online": "BOOL", "SW1__LastPollMs": "LINT", "Nope": "",
	} {
		if got := b.InputType(name); got != want {
			t.Errorf("InputType(%s) = %q, want %q", name, got, want)
		}
	}
}
