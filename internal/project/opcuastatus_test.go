package project

import (
	"strings"
	"testing"

	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
)

// The opcua-server row carries the server certificate's thumbprint and
// expiry, and puts the 30-day expiry warning in its message.
func TestOPCUAStatusCarriesTheCertificate(t *testing.T) {
	row := opcuaStatus(opcuaserver.Status{Endpoint: "opc.tcp://h:4840", Listening: true, CertThumbprint: "90b7", CertExpiresMs: 42})
	if row.Extra["certThumbprint"] != "90b7" || row.Extra["certExpiresMs"] != int64(42) {
		t.Errorf("extra %v", row.Extra)
	}
	if _, ok := row.Extra["certWarning"]; ok {
		t.Error("a warning with nothing to warn about")
	}
	row = opcuaStatus(opcuaserver.Status{Endpoint: "opc.tcp://h:4840", Listening: true, CertThumbprint: "90b7", CertWarning: "the OPC UA server certificate expires in 9 days"})
	if !strings.Contains(row.Message, "expires in 9 days") || row.Extra["certWarning"] == nil {
		t.Errorf("message %q, extra %v", row.Message, row.Extra)
	}
	row = opcuaStatus(opcuaserver.Status{Endpoint: "opc.tcp://h:4840", Listening: true})
	if _, ok := row.Extra["certThumbprint"]; ok {
		t.Error("a thumbprint without a certificate")
	}
}

// The row shows the write counters, whether the server is writable at all
// (opcua.writable), and a standby replica.
func TestOPCUAStatusCarriesTheWrites(t *testing.T) {
	row := opcuaStatus(opcuaserver.Status{Endpoint: "opc.tcp://h:4840", Listening: true, Writable: []string{"*_SP"}, Writes: 3, WritesRefused: 2, Standby: true})
	got := map[string]float64{}
	for _, m := range row.Metrics {
		got[m.Label] = m.Value
	}
	if got["writes"] != 3 || got["writes refused"] != 2 {
		t.Errorf("metrics %v", row.Metrics)
	}
	if row.Extra["access"] != "writable by trusted clients" || row.Extra["writes"] != uint64(3) || row.Extra["writesRefused"] != uint64(2) || row.Extra["standby"] != true {
		t.Errorf("extra %v", row.Extra)
	}
	row = opcuaStatus(opcuaserver.Status{Endpoint: "opc.tcp://h:4840", Listening: true})
	if row.Extra["access"] != "read-only" || row.Extra["writable"] != nil || row.Extra["standby"] != nil {
		t.Errorf("a read-only server's extra %v", row.Extra)
	}
}

// opcua.anonymous-writes shows on the row: who may write, and the switch
// itself, so a dashboard can say a lab server is open to anyone.
func TestOPCUAStatusCarriesAnonymousWrites(t *testing.T) {
	row := opcuaStatus(opcuaserver.Status{Endpoint: "opc.tcp://h:4840", Listening: true, Writable: []string{"*_SP"}, AnonymousWrites: true})
	if row.Extra["access"] != "writable by anonymous clients" || row.Extra["anonymousWrites"] != true {
		t.Errorf("extra %v", row.Extra)
	}
	// On, with nothing writable, the server is still read-only; the row
	// says so, and still shows the switch.
	row = opcuaStatus(opcuaserver.Status{Endpoint: "opc.tcp://h:4840", Listening: true, AnonymousWrites: true})
	if row.Extra["access"] != "read-only" || row.Extra["anonymousWrites"] != true {
		t.Errorf("extra %v", row.Extra)
	}
	row = opcuaStatus(opcuaserver.Status{Endpoint: "opc.tcp://h:4840", Listening: true, Writable: []string{"*_SP"}})
	if _, ok := row.Extra["anonymousWrites"]; ok {
		t.Errorf("anonymousWrites on a server without the switch: %v", row.Extra)
	}
}
