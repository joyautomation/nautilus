package runtime

import (
	"testing"
	"time"
)

func TestLateUs(t *testing.T) {
	due := time.Unix(100, 0)
	if got := lateUs(due.Add(250*time.Microsecond), due, 0, time.Millisecond); got != 250 {
		t.Errorf("slot form: %g, want 250", got)
	}
	if got := lateUs(due.Add(-5*time.Microsecond), due, 0, time.Millisecond); got != 0 {
		t.Errorf("early against slot: %g, want 0", got)
	}
	if got := lateUs(due, time.Time{}, 0.00106, time.Millisecond); got < 59.9 || got > 60.1 {
		t.Errorf("period form: %g, want 60", got)
	}
}
