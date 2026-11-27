package quorum

import "testing"

// releasedWire is the vote protocol version of the last release that
// shipped it: 0 while none has (v0.22.0 has no vote protocol).
const releasedWire = 0

// pinnedWire is WireVersion as this test last saw it.
const pinnedWire = 9

// A change of WireVersion trips this test, so whoever makes it reads the
// release rule (docs/design/redundancy.md, "Wire"): from the first
// release that ships the vote protocol, a version must also read its
// predecessor's frames, or a rolling upgrade loses its leader and the
// upgraded standby pages the no-leader alarm (#1436). Before that release
// (releasedWire 0) a bump needs only pinnedWire moved; after it, a bump
// needs a reader of releasedWire's frames and a test of it, then both
// constants moved.
func TestWireVersionPinned(t *testing.T) {
	if WireVersion == pinnedWire {
		return
	}
	if releasedWire == 0 {
		t.Fatalf("WireVersion is %d, pinned %d. No release has shipped the vote protocol yet, so no reader of the old frames is owed: "+
			"move pinnedWire. Once one has, set releasedWire to the version it shipped", WireVersion, pinnedWire)
	}
	t.Fatalf("WireVersion is %d, pinned %d, and release frames are version %d: a rolling upgrade must keep a leader, "+
		"so this version must read version %d's frames (design: the release rule). Add that reader and its test, then move pinnedWire",
		WireVersion, pinnedWire, releasedWire, releasedWire)
}
