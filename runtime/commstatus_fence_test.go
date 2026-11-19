package runtime

import (
	"fmt"
	"testing"

	"github.com/joyautomation/nautilus/internal/fence"
	"github.com/joyautomation/nautilus/lang/ir"
)

// A request the pair's gate refused mid-write (a fragment, fence.Check)
// reads as lost, as do() reports a write its gate refused, not as an
// unreachable peer (#1048 review).
func TestCommStatusGateRefusalIsLost(t *testing.T) {
	err := fmt.Errorf("logix: 127.0.0.1: cip: request not started: %w", fence.ErrGateClosed)
	if got := commStatus(err); got != ir.CommLost {
		t.Fatalf("commStatus(gate refusal) = %d; want CommLost (%d)", got, ir.CommLost)
	}
}
