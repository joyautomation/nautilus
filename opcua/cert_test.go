package opcua

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// A second driver's first use while the first is between writing its key
// and its certificate waits for it, and takes the pair it made: without
// the lock it would write its own key and certificate in between, and the
// first's certificate would land beside the second's key.
func TestCertificateFirstUseWaits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	inWindow, secondDone := make(chan struct{}), make(chan struct{})
	var entered atomic.Bool // the first caller only; a sync.Once would hold the second
	testHookKeyWritten = func() {
		if !entered.CompareAndSwap(false, true) {
			return
		}
		close(inWindow)
		select { // the second finishes here only if nothing holds it off
		case <-secondDone:
		case <-time.After(3 * time.Second): // RSA generation can take a second
		}
	}
	defer func() { testHookKeyWritten = nil }()
	first := make(chan error, 1)
	go func() { _, _, err := EnsureCertificate(dir, DefaultApplicationURI); first <- err }()
	<-inWindow
	go func() {
		defer close(secondDone)
		if _, _, err := EnsureCertificate(dir, DefaultApplicationURI); err != nil {
			t.Error(err)
		}
	}()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	<-secondDone
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")); err != nil {
		t.Fatalf("the key and the certificate do not match: %v", err)
	}
}

// Drivers that share a pki-dir and start at once (devices: rows naming one
// driver: section, #975) end up with one key and the certificate made for
// it, never a key of one generation beside another's certificate.
func TestCertificateConcurrentFirstUse(t *testing.T) {
	for round := 0; round < 3; round++ {
		dir := filepath.Join(t.TempDir(), "pki")
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, err := EnsureCertificate(dir, DefaultApplicationURI); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if _, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")); err != nil {
			t.Fatalf("round %d: the key and the certificate do not match: %v", round, err)
		}
		ents, _ := os.ReadDir(dir)
		if len(ents) != 2 {
			var names []string
			for _, e := range ents {
				names = append(names, e.Name())
			}
			t.Fatalf("round %d: pki-dir holds %v, want cert.pem and key.pem only", round, names)
		}
		if fi, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("round %d: key.pem mode %v, want 0600", round, fi.Mode().Perm())
		}
	}
}
