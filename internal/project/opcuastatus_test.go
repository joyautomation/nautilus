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
