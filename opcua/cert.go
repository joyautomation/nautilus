// cert.go makes the application instance certificate a secured endpoint
// needs. Any policy above None signs (and may encrypt) the channel with an
// RSA key pair the client presents in an X.509 certificate; the server
// trusts it — or, as Ignition does, parks it in a quarantine list for an
// administrator to approve. The certificate must carry the application URI
// as a URI SAN (Part 6 §6.2.2), and it is the same URI the client announces
// in CreateSession, or Ignition rejects the session with
// Bad_CertificateUriInvalid. Ported from tentacle-opcua-go's cert.go.
package opcua

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// DefaultApplicationURI is the URI nautilus announces and puts in its
// certificate when a project sets none.
const DefaultApplicationURI = "urn:joyautomation:nautilus:opcua"

// EnsureCertificate loads the client certificate and key from pkiDir
// (cert.pem, key.pem), generating a self-signed pair on first use. appURI
// becomes the certificate's URI SAN; the local hostname its DNS SAN.
func EnsureCertificate(pkiDir, appURI string) (certFile, keyFile string, err error) {
	certFile = filepath.Join(pkiDir, "cert.pem")
	keyFile = filepath.Join(pkiDir, "key.pem")
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			return certFile, keyFile, nil
		}
	}
	if err := os.MkdirAll(pkiDir, 0o700); err != nil {
		return "", "", fmt.Errorf("opcua-client: create pki dir: %w", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("opcua-client: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("opcua-client: serial: %w", err)
	}
	uri, err := url.Parse(appURI)
	if err != nil {
		return "", "", fmt.Errorf("opcua-client: application uri %q: %w", appURI, err)
	}
	hostname, _ := os.Hostname()
	dns := []string{"localhost"}
	if hostname != "" && hostname != "localhost" {
		dns = append(dns, hostname)
	}
	// SubjectKeyIdentifier per RFC 5280 method 1 — some servers insist.
	ski := sha1.Sum(x509.MarshalPKCS1PublicKey(&key.PublicKey))
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "nautilus", Organization: []string{"Joy Automation"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment |
			x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		SubjectKeyId:          ski[:],
		DNSNames:              dns,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		URIs:                  []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("opcua-client: create certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("opcua-client: write key: %w", err)
	}
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return "", "", fmt.Errorf("opcua-client: write certificate: %w", err)
	}
	return certFile, keyFile, nil
}
