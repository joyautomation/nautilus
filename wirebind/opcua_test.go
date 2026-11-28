package wirebind

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/opcua"
)

// SameNode: a node is its URI and its identifier, whatever index its id
// was written with (the driver re-targets the index from the URI); a
// binding with no URI is taken as the same node as any with its
// identifier, except across the standard namespace, which never moves.
func TestSameNode(t *testing.T) {
	b := func(node, uri string) opcua.TagBinding { return opcua.TagBinding{NodeID: node, Namespace: uri} }
	for _, tc := range []struct {
		a, b opcua.TagBinding
		same bool
	}{
		{b("ns=1;s=P/Start", "urn:x"), b("ns=1;s=P/Start", "urn:x"), true},
		{b("ns=1;s=P/Start", "urn:x"), b("ns=3;s=P/Start", "urn:x"), true}, // an import made before a renumbering
		{b("ns=1;s=P/Start", "urn:x"), b("ns=1;s=P/Start", ""), true},      // no URI: doubtful, so the same
		{b("ns=1;s=P/Start", "urn:x"), b("ns=1;s=P/Start", "urn:y"), false},
		{b("ns=1;s=P/Start", "urn:x"), b("ns=1;s=P/Stop", "urn:x"), false},
		{b("i=2259", ""), b("ns=2;i=2259", ""), false}, // the standard namespace never moves
		{b("ns=2;i=7", ""), b("ns=3;i=7", ""), true},
	} {
		if got := SameNode(tc.a, tc.b); got != tc.same {
			t.Errorf("SameNode(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}

// A shared session's command may not write a node the driver's own
// manifest already writes, however its id is spelled.
func TestMergeOPCUARefusesASecondWriter(t *testing.T) {
	own := opcua.Manifest{Tags: []opcua.TagBinding{
		{Name: "Imported", NodeID: "ns=3;s=P101/Start", Namespace: "urn:x:drives", Type: "Boolean", Writable: true},
		{Name: "Reading", NodeID: "ns=3;s=P101/Running", Namespace: "urn:x:drives", Type: "Boolean"},
	}}
	cmd := opcua.TagBinding{Name: "P101.Cmd.Start", NodeID: "ns=1;s=P101/Start", Namespace: "urn:x:drives", Type: "Boolean", Writable: true}
	if _, err := MergeOPCUA(own, []opcua.TagBinding{cmd}); err == nil || !strings.Contains(err.Error(), "Imported and P101.Cmd.Start both write node ns=1;s=P101/Start") {
		t.Errorf("MergeOPCUA = %v, want the second writer refused", err)
	}
	read := opcua.TagBinding{Name: "P101.Running", NodeID: "ns=1;s=P101/Running", Namespace: "urn:x:drives", Type: "Boolean"}
	if m, err := MergeOPCUA(own, []opcua.TagBinding{read}); err != nil || len(m.Tags) != 3 {
		t.Errorf("a second reader of one node is fine: %v %d", err, len(m.Tags))
	}
	if _, err := MergeOPCUA(own, []opcua.TagBinding{{Name: "Imported", NodeID: "i=1", Type: "Boolean"}}); err == nil || !strings.Contains(err.Error(), "already binds Imported") {
		t.Errorf("a name both bind: %v", err)
	}
}
