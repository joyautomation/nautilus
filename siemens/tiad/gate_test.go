package tiad

import "testing"

// TestGateBlocks: only ok and unknown let TIA calls through; failed, no
// state and a state this client does not know all block (fail closed).
func TestGateBlocks(t *testing.T) {
	for state, want := range map[string]bool{
		GateOK:      false,
		GateUnknown: false,
		GateFailed:  true,
		"":          true,
		"bogus":     true,
	} {
		if got := (Gate{State: state}).Blocks(); got != want {
			t.Errorf("state %q: Blocks() = %v, want %v", state, got, want)
		}
	}
}
