package io

import "testing"

// The tokens are wire format: the HMI kit's Quality union type is these four
// strings, so a rename here is a breaking change to every screen.
func TestQualityTokensAreStableWireFormat(t *testing.T) {
	want := map[Quality]string{
		Good:         "good",
		Stale:        "stale",
		Bad:          "bad",
		NotConnected: "notConnected",
	}
	for q, s := range want {
		if got := q.String(); got != s {
			t.Errorf("Quality(%d).String() = %q, want %q", q, got, s)
		}
		back, ok := ParseQuality(s)
		if !ok || back != q {
			t.Errorf("ParseQuality(%q) = %v,%v want %v,true", s, back, ok, q)
		}
	}
	if q, ok := ParseQuality("uncertain"); ok || q != Good {
		t.Errorf("ParseQuality(unknown) = %v,%v want good,false", q, ok)
	}
	// An unmodelled value must never render as anything a client would
	// trust less than it should — unknown reads as good, same as absent.
	if got := Quality(200).String(); got != "good" {
		t.Errorf("out-of-range quality = %q, want good", got)
	}
}

func TestQualityIsGood(t *testing.T) {
	if !Good.IsGood() {
		t.Error("Good.IsGood() = false")
	}
	for _, q := range []Quality{Stale, Bad, NotConnected} {
		if q.IsGood() {
			t.Errorf("%v.IsGood() = true", q)
		}
	}
}

func TestMemoryReportsQuality(t *testing.T) {
	m := NewMemory()
	if q := m.Quality(); q != nil {
		t.Errorf("fresh Memory quality = %v, want nil", q)
	}

	m.SetQuality("RTU9_LVL", Stale)
	m.SetQuality("RTU9_FLOW", NotConnected)
	q := m.Quality()
	if len(q) != 2 || q["RTU9_LVL"] != Stale || q["RTU9_FLOW"] != NotConnected {
		t.Fatalf("quality = %v", q)
	}

	// The map is the caller's: mutating it must not reach back into the
	// driver, or a frame builder trimming Good entries would silently
	// rewrite the driver's own state.
	q["RTU9_LVL"] = Bad
	if again := m.Quality(); again["RTU9_LVL"] != Stale {
		t.Errorf("driver state leaked: %v", again)
	}

	// Setting Good CLEARS, so the report keeps its non-Good-only shape.
	m.SetQuality("RTU9_LVL", Good)
	if q := m.Quality(); len(q) != 1 || q["RTU9_FLOW"] != NotConnected {
		t.Errorf("after clearing = %v", q)
	}
	m.SetQuality("RTU9_FLOW", Good)
	if q := m.Quality(); q != nil {
		t.Errorf("all-good quality = %v, want nil", q)
	}
}

// Quality must not disturb the driver's actual job.
func TestMemoryQualityIsIndependentOfValues(t *testing.T) {
	m := NewMemory()
	m.SetQuality("A", Bad)
	if err := m.WriteOutputs(Values{"A": 1.0}); err != nil {
		t.Fatal(err)
	}
	in, err := m.ReadInputs()
	if err != nil || in["A"] != 1.0 {
		t.Fatalf("ReadInputs = %v, %v", in, err)
	}
	if m.Quality()["A"] != Bad {
		t.Error("quality lost across a write/read")
	}
}
