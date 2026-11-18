package opcua

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"
)

func loadCert(t *testing.T, file string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		t.Fatalf("%s: not PEM", file)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Two nautilus clients must be told apart by a server that finds a
// certificate's issuer by subject name (CODESYS does): each certificate
// gets a subject of its own and an AuthorityKeyIdentifier naming its own
// key. With one shared subject and no AKI, the lab's CODESYS runtime
// trusted only one of two trusted nautilus certificates at a time.
func TestCertificatesAreDistinguishable(t *testing.T) {
	var certs []*x509.Certificate
	for range 2 {
		certFile, _, err := EnsureCertificate(t.TempDir(), DefaultApplicationURI)
		if err != nil {
			t.Fatal(err)
		}
		certs = append(certs, loadCert(t, certFile))
	}
	a, b := certs[0], certs[1]
	if bytes.Equal(a.RawSubject, b.RawSubject) {
		t.Errorf("two certificates share the subject %q", a.Subject)
	}
	for _, c := range certs {
		if len(c.SubjectKeyId) == 0 || !bytes.Equal(c.AuthorityKeyId, c.SubjectKeyId) {
			t.Errorf("%s: AKI %x, SKI %x; want the AKI to name its own key", c.Subject, c.AuthorityKeyId, c.SubjectKeyId)
		}
		if err := c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature); err != nil {
			t.Errorf("%s: not a valid self-signed certificate: %v", c.Subject, err)
		}
		if len(c.URIs) != 1 || c.URIs[0].String() != DefaultApplicationURI {
			t.Errorf("%s: URI SANs %v", c.Subject, c.URIs)
		}
	}
}

// An existing certificate is kept as it is: servers have pinned or
// trusted it.
func TestCertificateIsKept(t *testing.T) {
	dir := t.TempDir()
	f1, _, err := EnsureCertificate(dir, DefaultApplicationURI)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(f1)
	f2, _, err := EnsureCertificate(dir, DefaultApplicationURI)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(f2)
	if !bytes.Equal(before, after) {
		t.Error("EnsureCertificate replaced an existing certificate")
	}
}
