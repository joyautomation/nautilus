package main

// server.tls: self-signed (#708): HTTPS with no certificate to make by
// hand. The controller makes one (ECDSA P-256, its key and certificate in
// one PEM file, mode 0600) in its state directory on its first start,
// keeps it across restarts so a browser that accepted it once keeps
// accepting it, and makes a new one at a start within 30 days of its
// expiry. Browsers warn about a certificate no authority signed: the
// fingerprint it prints is what to compare the browser's with.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// selfSignedName is the file in <state dir>/tls/.
	selfSignedName = "self-signed.pem"
	// selfSignedValidity stays under the 398 days browsers accept for a
	// server certificate; selfSignedRenew is how close to its expiry a
	// start makes a new one.
	selfSignedValidity = 397 * 24 * time.Hour
	selfSignedRenew    = 30 * 24 * time.Hour
)

// selfSignedCert is the certificate ensureSelfSigned found or made.
type selfSignedCert struct {
	Path        string
	Fingerprint string // SHA-256 of the DER, AB:CD:... as browsers show it
	NotAfter    time.Time
	Made        bool // made now: there was none
	Renewed     bool // made now: the old one was within selfSignedRenew of its expiry
}

// ensureSelfSigned loads <stateDir>/tls/self-signed.pem, or makes it.
// First made with a hard link from a temporary file, so two starts at
// once both end up with the one that got there first, never half of
// each; renewed with a rename over it. A file that does not hold a
// certificate and its key is refused, never replaced: it is someone's.
func ensureSelfSigned(stateDir, cn string, hosts []string, now time.Time) (selfSignedCert, error) {
	dir := filepath.Join(stateDir, "tls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return selfSignedCert{}, fmt.Errorf("tls: self-signed: %w", err)
	}
	path := filepath.Join(dir, selfSignedName)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		pemBytes, err := makeSelfSigned(cn, hosts, now)
		if err != nil {
			return selfSignedCert{}, err
		}
		perr := placeFile(path, pemBytes, false)
		if perr != nil && !errors.Is(perr, os.ErrExist) {
			return selfSignedCert{}, perr
		}
		c, err := readSelfSigned(path) // ours, or the one another start linked in first
		c.Made = err == nil && perr == nil
		return c, err
	case err != nil:
		return selfSignedCert{}, fmt.Errorf("tls: self-signed: %w", err)
	}
	c, err := parseSelfSigned(path, raw)
	if err != nil {
		return c, err
	}
	if c.NotAfter.Sub(now) > selfSignedRenew {
		return c, nil
	}
	pemBytes, err := makeSelfSigned(cn, hosts, now)
	if err != nil {
		return selfSignedCert{}, err
	}
	if err := placeFile(path, pemBytes, true); err != nil {
		return selfSignedCert{}, err
	}
	c, err = readSelfSigned(path)
	c.Renewed = err == nil
	return c, err
}

func readSelfSigned(path string) (selfSignedCert, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return selfSignedCert{}, fmt.Errorf("tls: self-signed: %w", err)
	}
	return parseSelfSigned(path, raw)
}

func parseSelfSigned(path string, raw []byte) (selfSignedCert, error) {
	pair, err := tls.X509KeyPair(raw, raw)
	if err != nil || len(pair.Certificate) == 0 {
		return selfSignedCert{}, fmt.Errorf("tls: %s does not hold a certificate and its key (%v): delete it, and the next start makes a new one", path, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return selfSignedCert{}, fmt.Errorf("tls: %s: %w", path, err)
	}
	return selfSignedCert{Path: path, Fingerprint: certFingerprint(leaf.Raw), NotAfter: leaf.NotAfter}, nil
}

// placeFile writes data (mode 0600) to path through a temporary file
// beside it: replace false links it in, failing with os.ErrExist when
// path is there already; replace true renames it over.
func placeFile(path string, data []byte, replace bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".self-signed-*")
	if err != nil {
		return fmt.Errorf("tls: self-signed: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("tls: self-signed: %w", err)
	}
	if replace {
		return os.Rename(tmp.Name(), path)
	}
	return os.Link(tmp.Name(), path)
}

// makeSelfSigned is a new key and a certificate for hosts, as one PEM.
func makeSelfSigned(cn string, hosts []string, now time.Time) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"nautilus (self-signed)"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	pem.Encode(&b, &pem.Block{Type: "PRIVATE KEY", Bytes: kder})
	return b.Bytes(), nil
}

// certFingerprint is the SHA-256 of der as browsers show it: AB:CD:...
func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// selfSignedHosts are the names a self-signed certificate is made for:
// localhost and the loopback addresses, this machine's host name, the
// listen address's host when it names one, and this machine's addresses.
func selfSignedHosts(listenAddr string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	if h, _, err := net.SplitHostPort(listenAddr); err == nil && h != "" {
		if ip := net.ParseIP(h); ip == nil || !ip.IsUnspecified() {
			hosts = append(hosts, h)
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				hosts = append(hosts, n.IP.String())
			}
		}
	}
	seen := map[string]bool{}
	out := hosts[:0]
	for _, h := range hosts {
		if k := strings.ToLower(h); !seen[k] {
			seen[k] = true
			out = append(out, h)
		}
	}
	return out
}

// selfSignedNotice is what naut run (and the serve commands) say about the
// certificate they serve.
func selfSignedNotice(c selfSignedCert) string {
	what := "a self-signed certificate"
	switch {
	case c.Made:
		what = "a self-signed certificate it just made"
	case c.Renewed:
		what = "a self-signed certificate it just renewed (a new fingerprint)"
	}
	return fmt.Sprintf("HTTPS with %s (%s, until %s).\n"+
		"  SHA-256 fingerprint: %s\n"+
		"  Browsers warn until you accept it: compare this fingerprint with the one the browser shows (the warning page, then the certificate's details).\n"+
		"  To serve a real certificate instead: server.tls: {cert: /abs/cert.pem, key: /abs/key.pem}.\n",
		what, c.Path, c.NotAfter.Format("2006-01-02"), c.Fingerprint)
}
