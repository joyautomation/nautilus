// pki.go is the OPC UA server's certificate store: its own certificate and
// key, and the client certificates it trusts or has refused. The layout is
// the one OPC UA stacks use, so an operator who knows UaExpert's or the
// .NET stack's directories knows this one:
//
//	<pki>/own/cert.der            the server's certificate (DER)
//	<pki>/own/private/key.pem     its private key (PKCS #8 PEM, mode 0600)
//	<pki>/trusted/certs/          client certificates a channel may open with
//	<pki>/rejected/certs/         client certificates refused, kept for approval
//
// A client's certificate that is not trusted is written to rejected/ named
// by its thumbprint, and its channel refused (Bad_CertificateUntrusted).
// `naut opcua trust <thumbprint>` moves it to trusted/; the trust list is
// read again on every OpenSecureChannel, so approval needs no restart. There
// is no auto-accept (design: opcua-server-writes §2.2).
package server

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gopcua/opcua/ua"
)

// DefaultPKI is the certificate store's directory when the manifest names
// none: relative to the working directory, as bdseq and retain files are.
// `naut run` from the project directory puts it in the project, so naut new
// ignores /pki/ in git and naut build skips any certificate store.
const DefaultPKI = "pki"

// certValidity is how long a generated server certificate is valid.
const certValidity = 5 * 365 * 24 * time.Hour

// PKI is a certificate store directory.
type PKI struct{ Dir string }

func (p PKI) ownCert() string     { return filepath.Join(p.Dir, "own", "cert.der") }
func (p PKI) ownKey() string      { return filepath.Join(p.Dir, "own", "private", "key.pem") }
func (p PKI) trustedDir() string  { return filepath.Join(p.Dir, "trusted", "certs") }
func (p PKI) rejectedDir() string { return filepath.Join(p.Dir, "rejected", "certs") }

// Thumbprint is a certificate's OPC UA thumbprint: the hex SHA-1 of its DER
// encoding (Part 6 §6.2.2), the name clients and other stacks show for it.
func Thumbprint(der []byte) string {
	sum := sha1.Sum(der)
	return hex.EncodeToString(sum[:])
}

// CertInfo describes one certificate in the store.
type CertInfo struct {
	Thumbprint     string
	Subject        string
	ApplicationURI string
	NotBefore      time.Time
	NotAfter       time.Time
	DNSNames       []string
	IPAddresses    []string
	File           string
}

func describeCert(der []byte, file string) (CertInfo, error) {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return CertInfo{}, err
	}
	info := CertInfo{
		Thumbprint: Thumbprint(der),
		Subject:    c.Subject.String(),
		NotBefore:  c.NotBefore,
		NotAfter:   c.NotAfter,
		DNSNames:   c.DNSNames,
		File:       file,
	}
	if len(c.URIs) > 0 {
		info.ApplicationURI = c.URIs[0].String()
	}
	for _, ip := range c.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	return info, nil
}

// Own returns the server's certificate and key, generating them on first use:
// RSA-2048, SHA-256, self-signed, valid five years, with appURI as the URI
// SAN and each of hosts as a DNS or IP SAN. A certificate already in own/
// wins and is never regenerated, since a new one breaks every client's trust
// in this server: its ApplicationURI is the one to announce, and stale lists
// the hosts it does not name (a host name that changed), for the caller to
// log. created says whether this call generated the pair.
func (p PKI) Own(appURI, name string, hosts []string) (der []byte, key *rsa.PrivateKey, created bool, stale []string, err error) {
	_, certErr := os.Stat(p.ownCert())
	_, keyErr := os.Stat(p.ownKey())
	switch {
	case certErr == nil && keyErr == nil:
		der, key, err = p.loadOwn()
		if err != nil {
			return nil, nil, false, nil, err
		}
		c, _ := x509.ParseCertificate(der)
		return der, key, false, missingHosts(c, hosts), nil
	case certErr == nil || keyErr == nil:
		return nil, nil, false, nil, fmt.Errorf("opcua: %s has a certificate or a key but not both; restore the missing one or empty own/ to generate a new pair (every client will have to trust the new certificate)", filepath.Join(p.Dir, "own"))
	}
	der, key, err = newServerCert(appURI, name, hosts)
	if err != nil {
		return nil, nil, false, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p.ownKey()), 0o700); err != nil {
		return nil, nil, false, nil, fmt.Errorf("opcua: create %s: %w", p.Dir, err)
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, false, nil, err
	}
	if err := os.WriteFile(p.ownKey(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0o600); err != nil {
		return nil, nil, false, nil, fmt.Errorf("opcua: write server key: %w", err)
	}
	if err := os.WriteFile(p.ownCert(), der, 0o644); err != nil {
		return nil, nil, false, nil, fmt.Errorf("opcua: write server certificate: %w", err)
	}
	for _, d := range []string{p.trustedDir(), p.rejectedDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, nil, false, nil, fmt.Errorf("opcua: create %s: %w", d, err)
		}
	}
	return der, key, true, nil, nil
}

func (p PKI) loadOwn() ([]byte, *rsa.PrivateKey, error) {
	der, err := os.ReadFile(p.ownCert())
	if err != nil {
		return nil, nil, fmt.Errorf("opcua: read server certificate: %w", err)
	}
	if b, _ := pem.Decode(der); b != nil { // a PEM certificate put there by hand
		der = b.Bytes
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("opcua: %s: %w", p.ownCert(), err)
	}
	raw, err := os.ReadFile(p.ownKey())
	if err != nil {
		return nil, nil, fmt.Errorf("opcua: read server key: %w", err)
	}
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, nil, fmt.Errorf("opcua: %s is not a PEM key", p.ownKey())
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, nil, fmt.Errorf("opcua: %s is not an RSA key", p.ownKey())
		}
		key = rk
	} else if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		key = k
	} else {
		return nil, nil, fmt.Errorf("opcua: %s: %w", p.ownKey(), err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || pub.N.Cmp(key.N) != 0 {
		return nil, nil, fmt.Errorf("opcua: %s does not belong to %s", p.ownKey(), p.ownCert())
	}
	return der, key, nil
}

// newServerCert makes a self-signed application instance certificate as
// Part 6 §6.2.2 has it: the ApplicationURI as the URI SAN, the host names
// and addresses it is reached by, and the key usages a UA server signs and
// encrypts with (digitalSignature, nonRepudiation, keyEncipherment,
// dataEncipherment, and keyCertSign since it signs itself), for both
// serverAuth and clientAuth.
func newServerCert(appURI, name string, hosts []string) ([]byte, *rsa.PrivateKey, error) {
	uri, err := url.Parse(appURI)
	if err != nil || uri.Scheme == "" {
		return nil, nil, fmt.Errorf("opcua: application uri %q is not an absolute URI", appURI)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("opcua: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Nautilus " + name,
			Organization: []string{"Joy Automation"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{uri},
	}
	for _, h := range hosts {
		h = strings.Trim(h, "[]")
		if ip := net.ParseIP(h); ip != nil {
			if !ip.IsUnspecified() {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			}
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("opcua: create certificate: %w", err)
	}
	return der, key, nil
}

// missingHosts are the hosts c names in no SAN.
func missingHosts(c *x509.Certificate, hosts []string) []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, h := range hosts {
		h = strings.Trim(h, "[]")
		if ip := net.ParseIP(h); ip != nil {
			if ip.IsUnspecified() {
				continue
			}
			found := false
			for _, have := range c.IPAddresses {
				if have.Equal(ip) {
					found = true
					break
				}
			}
			if !found {
				out = append(out, h)
			}
			continue
		}
		found := false
		for _, have := range c.DNSNames {
			if strings.EqualFold(have, h) {
				found = true
				break
			}
		}
		if !found && h != "" {
			out = append(out, h)
		}
	}
	return out
}

// Verify is the trust check a secured channel's client certificate passes
// when the channel opens and at each renewal (uasc.Config.VerifyPeer). It
// reads trusted/ afresh each time, so `naut opcua trust` takes effect on the
// client's next attempt. A certificate that does not parse is
// Bad_CertificateInvalid; one outside its validity period
// Bad_CertificateTimeInvalid; one not in trusted/ is written to rejected/
// (once) and refused with Bad_CertificateUntrusted.
func (p PKI) Verify(der []byte) error {
	// A client may send its certificate followed by its issuers (Part 6
	// §6.7.2.3); the leaf is the one trusted, filed and named.
	chain, err := x509.ParseCertificates(der)
	if err != nil || len(chain) == 0 {
		return ua.StatusBadCertificateInvalid
	}
	c := chain[0]
	der = c.Raw
	if _, ok := c.PublicKey.(*rsa.PublicKey); !ok {
		return ua.StatusBadCertificateInvalid
	}
	now := time.Now()
	if now.Before(c.NotBefore) || now.After(c.NotAfter) {
		return ua.StatusBadCertificateTimeInvalid
	}
	if p.trusted(der) {
		return nil
	}
	p.reject(der)
	return ua.StatusBadCertificateUntrusted
}

// trusted reports whether der is in trusted/, by content: a file there may
// be named anything, DER or PEM.
func (p PKI) trusted(der []byte) bool {
	if raw, err := os.ReadFile(filepath.Join(p.trustedDir(), Thumbprint(der)+".der")); err == nil && bytes.Equal(raw, der) {
		return true
	}
	ents, err := os.ReadDir(p.trustedDir())
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p.trustedDir(), e.Name()))
		if err != nil {
			continue
		}
		if b, _ := pem.Decode(raw); b != nil {
			raw = b.Bytes
		}
		if bytes.Equal(raw, der) {
			return true
		}
	}
	return false
}

// maxRejected is how many refused certificates rejected/ keeps: anyone who
// can reach the port can file one per connection attempt, so the oldest go
// once there are more.
const maxRejected = 100

// reject writes der to rejected/ under its thumbprint, unless it is there,
// and trims the directory to the newest maxRejected. A failure to write is
// not the client's business: the channel is refused either way.
func (p PKI) reject(der []byte) {
	if err := os.MkdirAll(p.rejectedDir(), 0o700); err != nil {
		return
	}
	file := filepath.Join(p.rejectedDir(), Thumbprint(der)+".der")
	if _, err := os.Stat(file); err == nil {
		return
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, der, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, file)
	p.trimRejected()
}

func (p PKI) trimRejected() {
	ents, err := os.ReadDir(p.rejectedDir())
	if err != nil || len(ents) <= maxRejected {
		return
	}
	type aged struct {
		name string
		at   time.Time
	}
	var files []aged
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && !e.IsDir() {
			files = append(files, aged{e.Name(), fi.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.After(files[j].at) })
	for _, f := range files[min(maxRejected, len(files)):] {
		_ = os.Remove(filepath.Join(p.rejectedDir(), f.name))
	}
}

// Certs lists the store: the server's own certificate (nil before the first
// start), and the trusted and rejected client certificates, by subject.
func (p PKI) Certs() (own *CertInfo, trusted, rejected []CertInfo, err error) {
	if der, err := os.ReadFile(p.ownCert()); err == nil {
		if b, _ := pem.Decode(der); b != nil {
			der = b.Bytes
		}
		if info, err := describeCert(der, p.ownCert()); err == nil {
			own = &info
		}
	}
	if trusted, err = listCerts(p.trustedDir()); err != nil {
		return nil, nil, nil, err
	}
	if rejected, err = listCerts(p.rejectedDir()); err != nil {
		return nil, nil, nil, err
	}
	return own, trusted, rejected, nil
}

func listCerts(dir string) ([]CertInfo, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []CertInfo
	for _, e := range ents {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		file := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if b, _ := pem.Decode(raw); b != nil {
			raw = b.Bytes
		}
		info, err := describeCert(raw, file)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].Thumbprint < out[j].Thumbprint
	})
	return out, nil
}

// minTrustPrefix is how many hex digits of a thumbprint Trust needs. Any
// client can file a certificate in rejected/, and the thumbprint of the one
// an operator means is visible on the wire (the OPN header is not
// encrypted), so a short prefix is a target: 8 digits is 2^32 tries to
// match, 16 is 2^64.
const minTrustPrefix = 16

// Trust approves the rejected certificate whose thumbprint starts with
// prefix (at least 16 hex digits, any case, colons allowed): it moves from
// rejected/ to trusted/. A prefix naming no rejected certificate, or more
// than one, is an error; one already trusted is reported as such.
func (p PKI) Trust(prefix string) (CertInfo, error) {
	prefix = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(prefix), ":", ""))
	if len(prefix) < minTrustPrefix {
		return CertInfo{}, fmt.Errorf("give at least %d hex digits of the thumbprint, ideally all 40 (naut opcua certs lists them): a shorter prefix is cheap for someone else to match", minTrustPrefix)
	}
	_, trusted, rejected, err := p.Certs()
	if err != nil {
		return CertInfo{}, err
	}
	var hits []CertInfo
	for _, c := range rejected {
		if strings.HasPrefix(c.Thumbprint, prefix) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 0:
		for _, c := range trusted {
			if strings.HasPrefix(c.Thumbprint, prefix) {
				return c, fmt.Errorf("%s (%s) is already trusted", c.Thumbprint, c.Subject)
			}
		}
		return CertInfo{}, fmt.Errorf("no rejected certificate has a thumbprint starting %s: a client's certificate lands in %s when it is refused", prefix, p.rejectedDir())
	case 1:
	default:
		return CertInfo{}, fmt.Errorf("%d rejected certificates have a thumbprint starting %s; give more digits", len(hits), prefix)
	}
	c := hits[0]
	if err := os.MkdirAll(p.trustedDir(), 0o700); err != nil {
		return CertInfo{}, err
	}
	dst := filepath.Join(p.trustedDir(), c.Thumbprint+".der")
	if err := os.Rename(c.File, dst); err != nil {
		return CertInfo{}, fmt.Errorf("trust %s: %w", c.Thumbprint, err)
	}
	c.File = dst
	return c, nil
}
