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

// Memory's own QualityReporter implementation (SetQuality/Quality) is not
// ported here — this branch's Memory does not yet carry the `q` field
// st-struct-pins' io.go adds alongside BatchReader; see the NOTE at the
// bottom of quality.go. TestQualityTokensAreStableWireFormat and
// TestQualityIsGood above are the byte-identical half of
// st-struct-pins/io/quality_test.go that does not depend on it.
