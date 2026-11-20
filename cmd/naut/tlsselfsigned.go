package main

// server.tls: self-signed (#708): HTTPS with no certificate to make by
// hand. The controller makes one (ECDSA P-256, its key and certificate in
// one PEM file, mode 0600) in its state directory on its first start,
// keeps it across restarts so a browser that accepted it once keeps
// accepting it. Only naut run renews it: at its start and once a day while
// it runs, from 30 days before its expiry (naut logix serve, naut siemens
// serve and naut auth init make one when there is none, never replace
// one). Browsers warn about a certificate no authority signed: the
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
	"runtime"
	"runtime/debug"
	"slices"
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
	// Names are the names and addresses it is valid for (its SANs), fixed
	// when it was made; Missing are those of hosts it lacks (this machine
	// was re-addressed since).
	Names, Missing []string
	Made           bool   // made now: there was none
	Renewed        bool   // made now: the old one was within selfSignedRenew of its expiry
	Replaced       string // when Renewed, the old one's fingerprint
	// Due: within selfSignedRenew of its expiry (or past it) and not
	// renewed, since this caller does not renew.
	Due bool
	// BuiltAfter is set when it was made now by a clock that reads earlier
	// than this binary was built (a controller whose clock was never set):
	// the certificate is not valid yet for any client whose clock is right.
	BuiltAfter time.Time
}

// clockFloor is a date this code was written after: a clock that reads
// earlier is certainly wrong, even in a build that carries no vcs.time (a
// worktree, a vendored or -buildvcs=false build).
var clockFloor = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

// binaryBuilt is when this binary's source was committed (vcs.time, which
// go build records in a plain git checkout), or clockFloor when that is
// later or unknown.
var binaryBuilt = func() time.Time {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.time" {
				if t, err := time.Parse(time.RFC3339, s.Value); err == nil && t.After(clockFloor) {
					return t
				}
			}
		}
	}
	return clockFloor
}

// ensureSelfSigned loads <stateDir>/tls/self-signed.pem, or makes it.
// First made with a hard link from a temporary file, so two starts at
// once both end up with the one that got there first, never half of
// each; with renew (naut run only), replaced by a rename over it within
// selfSignedRenew of its expiry. A file that does not hold a certificate
// and its key is refused, never replaced: it is someone's. The tls
// directory and the file are given the state directory's owner, so one
// made by a root run by hand is still the service's to read.
func ensureSelfSigned(stateDir, cn string, hosts []string, now time.Time, renew bool) (selfSignedCert, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return selfSignedCert{}, fmt.Errorf("tls: self-signed: %w", err)
	}
	owner, err := os.Stat(stateDir)
	if err != nil {
		return selfSignedCert{}, fmt.Errorf("tls: self-signed: %w", err)
	}
	dir := filepath.Join(stateDir, "tls")
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return selfSignedCert{}, fmt.Errorf("tls: self-signed: %w", err)
	}
	if err := ownLike(dir, owner); err != nil {
		return selfSignedCert{}, err
	}
	path := filepath.Join(dir, selfSignedName)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		pemBytes, err := makeSelfSigned(cn, hosts, now)
		if err != nil {
			return selfSignedCert{}, err
		}
		perr := placeFile(path, pemBytes, false, owner)
		if perr != nil && !errors.Is(perr, os.ErrExist) {
			return selfSignedCert{}, perr
		}
		// Ours, or the one another start linked in first: that start made
		// it, so this one says Made only when its own link won.
		c, err := readSelfSigned(path, hosts)
		c.Made = err == nil && perr == nil
		if c.Made {
			c.BuiltAfter = clockBehind(now)
		}
		return c, err
	case err != nil:
		return selfSignedCert{}, fmt.Errorf("tls: self-signed: %w", err)
	}
	c, err := parseSelfSigned(path, raw, hosts)
	if err != nil {
		return c, err
	}
	if c.NotAfter.Sub(now) > selfSignedRenew {
		return c, nil
	}
	if !renew {
		c.Due = true
		return c, nil
	}
	pemBytes, err := makeSelfSigned(cn, hosts, now)
	if err != nil {
		return selfSignedCert{}, err
	}
	if err := placeFile(path, pemBytes, true, owner); err != nil {
		return selfSignedCert{}, err
	}
	old := c.Fingerprint
	c, err = readSelfSigned(path, hosts)
	if err == nil {
		c.Renewed, c.Replaced, c.BuiltAfter = true, old, clockBehind(now)
	}
	return c, err
}

// clockBehind is binaryBuilt when now is before it, else zero.
func clockBehind(now time.Time) time.Time {
	if b := binaryBuilt(); !b.IsZero() && now.Before(b) {
		return b
	}
	return time.Time{}
}

// chownLike gives an open file ref's owner (keepOwner; a test swaps it).
var chownLike = keepOwner

// ownLike gives path ref's owner (unix), when it has another.
func ownLike(path string, ref os.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("tls: self-signed: %w", err)
	}
	defer f.Close()
	if err := chownLike(f, ref); err != nil {
		return fmt.Errorf("tls: self-signed: %s cannot be given its state directory's owner (%w): run as the controller's user", path, err)
	}
	return nil
}

func readSelfSigned(path string, hosts []string) (selfSignedCert, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return selfSignedCert{}, fmt.Errorf("tls: self-signed: %w", err)
	}
	return parseSelfSigned(path, raw, hosts)
}

func parseSelfSigned(path string, raw []byte, hosts []string) (selfSignedCert, error) {
	pair, err := tls.X509KeyPair(raw, raw)
	if err != nil || len(pair.Certificate) == 0 {
		return selfSignedCert{}, fmt.Errorf("tls: %s does not hold a certificate and its key (%v): delete it, and the next start makes a new one", path, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return selfSignedCert{}, fmt.Errorf("tls: %s: %w", path, err)
	}
	c := selfSignedCert{Path: path, Fingerprint: certFingerprint(leaf.Raw), NotAfter: leaf.NotAfter}
	c.Names = append(c.Names, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		c.Names = append(c.Names, ip.String())
	}
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			h = ip.String()
		}
		if !slices.ContainsFunc(c.Names, func(n string) bool { return strings.EqualFold(n, h) }) {
			c.Missing = append(c.Missing, h)
		}
	}
	return c, nil
}

// placeFile writes data (mode 0600, owner's owner) to path through a
// temporary file beside it: replace false links it in, failing with
// os.ErrExist when path is there already; replace true renames it over.
// The directory is synced after, so the new name survives a power cut.
func placeFile(path string, data []byte, replace bool, owner os.FileInfo) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".self-signed-*")
	if err != nil {
		return fmt.Errorf("tls: self-signed: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil && owner != nil {
		err = chownLike(tmp, owner)
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
		err = os.Rename(tmp.Name(), path)
	} else {
		err = os.Link(tmp.Name(), path)
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir fsyncs a directory (a no-op where directories cannot be).
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("tls: self-signed: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("tls: self-signed: %w", err)
	}
	return nil
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
	var b strings.Builder
	fmt.Fprintf(&b, "HTTPS with %s (%s, until %s).\n"+
		"  SHA-256 fingerprint: %s\n", what, c.Path, c.NotAfter.Format("2006-01-02"), c.Fingerprint)
	if c.Renewed {
		fmt.Fprintf(&b, "  It replaces %s: browsers that accepted that one warn again.\n", c.Replaced)
	}
	fmt.Fprintf(&b, "  Valid for: %s.\n", strings.Join(c.Names, ", "))
	if len(c.Missing) > 0 {
		fmt.Fprintf(&b, "  Not valid for %s, an address this machine has now. Only if clients reach it by that address: delete %s and restart naut run, which makes one for every current address (a new fingerprint).\n", strings.Join(c.Missing, ", "), c.Path)
	}
	if c.Due {
		fmt.Fprintf(&b, "  WARNING: it expires on %s: naut run renews it (at its start, and daily while it runs).\n", c.NotAfter.Format("2006-01-02"))
	}
	if !c.BuiltAfter.IsZero() {
		fmt.Fprintf(&b, "  WARNING: this machine's clock reads earlier than this naut's build (%s): the certificate starts from that clock, so clients whose clock is right refuse it as not yet valid. Set the clock (NTP), delete %s and restart.\n", c.BuiltAfter.Format("2006-01-02"), c.Path)
	}
	b.WriteString("  Browsers warn until you accept it: compare this fingerprint with the one the browser shows (the warning page, then the certificate's details).\n" +
		"  To serve a real certificate instead: server.tls: {cert: /abs/cert.pem, key: /abs/key.pem}.\n")
	return b.String()
}
