package opcua

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

// secureOpts is a Basic256Sha256/SignAndEncrypt server with its own
// certificate, generated into dir.
func secureOpts(t *testing.T, dir string) []server.Option {
	t.Helper()
	certFile, keyFile, err := EnsureCertificate(dir, "urn:nautilus:test:server")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return []server.Option{
		server.EnableSecurity("Basic256Sha256", ua.MessageSecurityModeSignAndEncrypt),
		server.PrivateKey(pair.PrivateKey.(*rsa.PrivateKey)),
		server.Certificate(pair.Certificate[0]),
	}
}

// The first secured session records the server certificate's thumbprint
// in pki-dir; a different certificate on the same endpoint is refused with
// an error naming the pin file; deleting the pin trusts the new one.
func TestServerCertificatePinnedOnFirstUse(t *testing.T) {
	port := freePort(t)
	pki := t.TempDir()
	conn := Connect{Security: Security{Policy: "Basic256Sha256", Mode: "SignAndEncrypt"}, PKIDir: pki, Timeout: 3 * time.Second}

	ts := startServerWith(t, port, "urn:nautilus:test", 1, secureOpts(t, t.TempDir())...)
	sess, err := Dial(context.Background(), ts.url, conn)
	if err != nil {
		t.Fatalf("first secured dial: %v", err)
	}
	if sess.Endpoint.SecurityMode != ua.MessageSecurityModeSignAndEncrypt {
		t.Errorf("session mode = %v", sess.Endpoint.SecurityMode)
	}
	sess.Close()
	pin := PinFile(pki, ts.url)
	if _, err := os.Stat(pin); err != nil {
		t.Fatalf("no pin recorded: %v", err)
	}
	// Same certificate again: accepted.
	sess, err = Dial(context.Background(), ts.url, conn)
	if err != nil {
		t.Fatalf("second dial, same certificate: %v", err)
	}
	sess.Close()
	_ = ts.srv.Close()

	// Another certificate at the same address: refused.
	ts2 := startServerWith(t, port, "urn:nautilus:test", 1, secureOpts(t, t.TempDir())...)
	if _, err := Dial(context.Background(), ts2.url, conn); err == nil || !strings.Contains(err.Error(), "certificate changed") || !strings.Contains(err.Error(), pin) {
		t.Fatalf("a substituted certificate: err = %v, want a refusal naming %s", err, pin)
	}
	// Re-trust by deleting the pin.
	if err := os.Remove(pin); err != nil {
		t.Fatal(err)
	}
	sess, err = Dial(context.Background(), ts2.url, conn)
	if err != nil {
		t.Fatalf("dial after re-trust: %v", err)
	}
	sess.Close()
}

// A UserName token is never sent on an unsecured channel: a server that
// offers only None (or a list stripped down to None) is refused before any
// credential leaves.
func TestUsernameRefusedOnUnsecuredEndpoint(t *testing.T) {
	ts := startServer(t, freePort(t))
	_, err := Dial(context.Background(), ts.url, Connect{Username: "op", Password: "secret", Timeout: 2 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "refusing to send username") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}
