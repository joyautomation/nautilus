package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
)

// isolateState points every state directory a test's commands could use
// at a temporary one: never the developer's own ~/.local/state.
func isolateState(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("XDG_STATE_HOME", d)
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("INVOCATION_ID", "")
	return d
}

func leafOf(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(raw, raw)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// The first start makes the certificate: one file holding the key and the
// certificate, mode 0600, valid for the names given and under 398 days,
// a server certificate a client that trusts it verifies by name. Later
// starts keep it (same fingerprint: a browser that accepted it keeps
// accepting it), until one within 30 days of its expiry makes a new one.
func TestEnsureSelfSigned(t *testing.T) {
	sd := t.TempDir()
	now := time.Now()
	c, err := ensureSelfSigned(sd, "plant", []string{"localhost", "127.0.0.1", "plant-ipc"}, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Made || c.Renewed || c.Path != filepath.Join(sd, "tls", "self-signed.pem") {
		t.Errorf("first: %+v", c)
	}
	if st, _ := os.Stat(c.Path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	leaf := leafOf(t, c.Path)
	if certFingerprint(leaf.Raw) != c.Fingerprint || len(c.Fingerprint) != 95 {
		t.Errorf("fingerprint %q", c.Fingerprint)
	}
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d > 398*24*time.Hour {
		t.Errorf("valid for %v: browsers refuse a server certificate over 398 days", d)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	for _, h := range []string{"localhost", "127.0.0.1", "plant-ipc"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: h, Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			t.Errorf("not valid for %s: %v", h, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "elsewhere", Roots: roots}); err == nil {
		t.Error("valid for a name it was not made for")
	}
	again, err := ensureSelfSigned(sd, "plant", []string{"localhost"}, now.Add(300*24*time.Hour), true)
	if err != nil || again.Made || again.Renewed || again.Fingerprint != c.Fingerprint {
		t.Errorf("a later start did not keep it: %+v %v", again, err)
	}
	renewed, err := ensureSelfSigned(sd, "plant", []string{"localhost"}, c.NotAfter.Add(-29*24*time.Hour), true)
	if err != nil || !renewed.Renewed || renewed.Fingerprint == c.Fingerprint || renewed.Replaced != c.Fingerprint {
		t.Errorf("within 30 days of expiry: %+v %v", renewed, err)
	}
	if st, _ := os.Stat(c.Path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("renewed mode %v", st.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(sd, "tls", ".self-signed-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// A file that is not a certificate and its key is someone's: refused,
// naming the fix, never replaced. A second start that finds the file made
// between its look and its link uses that one (the link fails; it does not
// overwrite).
func TestEnsureSelfSignedKeepsWhatIsThere(t *testing.T) {
	sd := t.TempDir()
	os.MkdirAll(filepath.Join(sd, "tls"), 0o700)
	path := filepath.Join(sd, "tls", "self-signed.pem")
	os.WriteFile(path, []byte("not a certificate"), 0o600)
	if _, err := ensureSelfSigned(sd, "p", nil, time.Now(), true); err == nil || !strings.Contains(err.Error(), "delete it") {
		t.Errorf("a broken file: %v", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "not a certificate" {
		t.Error("a broken file was replaced")
	}
	os.Remove(path)
	first, _ := makeSelfSigned("p", nil, time.Now())
	if err := placeFile(path, first, false, nil); err != nil {
		t.Fatal(err)
	}
	second, _ := makeSelfSigned("p", nil, time.Now())
	if err := placeFile(path, second, false, nil); !os.IsExist(err) {
		t.Errorf("a second create: %v, want it to find the file there", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != string(first) {
		t.Error("a second create replaced the first")
	}
}

// tlsSource with self-signed: the certificate in the state directory,
// told with its fingerprint; NAUTILUS_TLS_CERT/_KEY win over it; under
// systemd with no state directory it refuses, naming the fix.
func TestTLSSourceSelfSigned(t *testing.T) {
	state := isolateState(t)
	proj := t.TempDir()
	var said string
	spec := tlsSpec{selfSigned: true, name: "plant", devFallback: true, listen: "127.0.0.1:8443", say: func(s string) { said += s }}
	src, err := tlsSource(spec, proj, os.Getenv)
	if err != nil || src == nil {
		t.Fatalf("%v", err)
	}
	path := filepath.Join(state, "nautilus", "plant", "tls", "self-signed.pem")
	leaf := leafOf(t, path)
	if src.Pin() != sha256.Sum256(leaf.Raw) {
		t.Error("the source serves another certificate than the file's")
	}
	if !strings.Contains(said, certFingerprint(leaf.Raw)) || !strings.Contains(said, "Browsers warn") || !strings.Contains(said, "server.tls: {cert:") {
		t.Errorf("notice:\n%s", said)
	}
	// The environment's pair wins.
	cert, key, _ := selfSigned(t, t.TempDir())
	env := func(k string) string {
		switch k {
		case "NAUTILUS_TLS_CERT":
			return cert
		case "NAUTILUS_TLS_KEY":
			return key
		}
		return os.Getenv(k)
	}
	said = ""
	if src, err := tlsSource(spec, proj, env); err != nil || src.Pin() == sha256.Sum256(leaf.Raw) || said != "" {
		t.Errorf("NAUTILUS_TLS_CERT/_KEY did not win: %v %q", err, said)
	}
	// Under systemd, no state directory: refused, naming it.
	sysd := func(k string) string {
		if k == "INVOCATION_ID" {
			return "x"
		}
		if k == "STATE_DIRECTORY" {
			return ""
		}
		return os.Getenv(k)
	}
	if _, err := tlsSource(spec, proj, sysd); err == nil || !strings.Contains(err.Error(), "StateDirectory=") {
		t.Errorf("no state directory under systemd: %v", err)
	}
}

// nearExpiry makes sd's certificate as a start 387 days ago would have:
// it expires in 10 days, inside the renewal window.
func nearExpiry(t *testing.T, sd string, hosts []string) selfSignedCert {
	t.Helper()
	c, err := ensureSelfSigned(sd, "plant", hosts, time.Now().Add(-387*24*time.Hour), true)
	if err != nil || !c.Made {
		t.Fatalf("%+v %v", c, err)
	}
	return c
}

// Only naut run renews (#1114 review): naut logix serve, naut siemens
// serve and naut auth init serve a certificate near its expiry as it is,
// saying naut run renews it, so no other process swaps the certificate a
// running controller serves; naut run's start renews it.
func TestOnlyNautRunRenews(t *testing.T) {
	sd := t.TempDir()
	old := nearExpiry(t, sd, []string{"localhost"})
	c, err := ensureSelfSigned(sd, "plant", []string{"localhost"}, time.Now(), false)
	if err != nil || c.Renewed || !c.Due || c.Fingerprint != old.Fingerprint {
		t.Fatalf("without renew: %+v %v", c, err)
	}
	if n := selfSignedNotice(c); !strings.Contains(n, "naut run renews it") {
		t.Errorf("notice:\n%s", n)
	}
	state := isolateState(t)
	proj := t.TempDir()
	path := filepath.Join(state, "nautilus", "plant", "tls", "self-signed.pem")
	os.MkdirAll(filepath.Dir(path), 0o700)
	raw, _ := os.ReadFile(c.Path)
	os.WriteFile(path, raw, 0o600)
	spec := tlsSpec{selfSigned: true, name: "plant", devFallback: true, listen: "127.0.0.1:8443", say: func(string) {}}
	for _, facade := range []tlsSpec{spec, tlsSpecOf(&project.TLSConfig{SelfSigned: true}, "", "plant", "127.0.0.1:8443", true, func(string) {})} {
		if _, err := tlsSource(facade, proj, os.Getenv); err != nil {
			t.Fatal(err)
		}
		if certFingerprint(leafOf(t, path).Raw) != old.Fingerprint {
			t.Fatal("a serve command renewed the certificate")
		}
	}
	spec.renew = true
	if _, err := tlsSource(spec, proj, os.Getenv); err != nil {
		t.Fatal(err)
	}
	if certFingerprint(leafOf(t, path).Raw) == old.Fingerprint {
		t.Error("naut run's start did not renew a certificate 10 days from its expiry")
	}
}

// naut run looks daily (#1114 review): a controller that runs past the
// renewal window renews while running, saying so with the old and the new
// fingerprint. It stops with its context.
func TestKeepSelfSignedFresh(t *testing.T) {
	state := isolateState(t)
	sd := filepath.Join(state, "nautilus", "plant")
	os.MkdirAll(sd, 0o700)
	old := nearExpiry(t, sd, []string{"localhost"})
	defer func(d time.Duration) { selfSignedCheckEvery = d }(selfSignedCheckEvery)
	selfSignedCheckEvery = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	said := make(chan string, 100)
	done := make(chan struct{})
	go func() {
		keepSelfSignedFresh(ctx, tlsSpec{selfSigned: true, name: "plant", devFallback: true, listen: "127.0.0.1:8443"}, t.TempDir(), os.Getenv, time.Now, func(s string) { said <- s })
		close(done)
	}()
	var msg string
	select {
	case msg = <-said:
	case <-time.After(10 * time.Second):
		t.Fatal("no renewal while running")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the check did not stop with its context")
	}
	now := certFingerprint(leafOf(t, old.Path).Raw)
	if now == old.Fingerprint || !strings.HasPrefix(msg, "RENEWED") || !strings.Contains(msg, now) || !strings.Contains(msg, "It replaces "+old.Fingerprint) {
		t.Errorf("renewal said:\n%s", msg)
	}
	// A certificate far from its expiry is left alone.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	keepSelfSignedFresh(ctx2, tlsSpec{selfSigned: true, name: "plant", devFallback: true}, t.TempDir(), os.Getenv, time.Now, func(s string) { t.Errorf("said %q", s) })
	if certFingerprint(leafOf(t, old.Path).Raw) != now {
		t.Error("a fresh certificate was renewed")
	}
}

// The tls directory and the file take the state directory's owner, so a
// root run by hand on a box whose service runs as another user leaves
// nothing that service cannot read (#1114 review).
func TestSelfSignedKeepsTheStateDirOwner(t *testing.T) {
	sd := t.TempDir()
	ref, _ := os.Stat(sd)
	defer func(f func(*os.File, os.FileInfo) error) { chownLike = f }(chownLike)
	var owned []string
	chownLike = func(f *os.File, st os.FileInfo) error {
		if !os.SameFile(st, ref) {
			t.Errorf("%s given the owner of %s, not the state directory's", f.Name(), st.Name())
		}
		owned = append(owned, filepath.Base(f.Name()))
		return nil
	}
	nearExpiry(t, sd, nil)
	if len(owned) != 2 || owned[0] != "tls" || !strings.HasPrefix(owned[1], ".self-signed-") {
		t.Errorf("made: owned %v", owned)
	}
	owned = nil
	if _, err := ensureSelfSigned(sd, "plant", nil, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if len(owned) != 2 || !strings.HasPrefix(owned[1], ".self-signed-") {
		t.Errorf("renewed: owned %v", owned)
	}
	chownLike = func(*os.File, os.FileInfo) error { return os.ErrPermission }
	if _, err := ensureSelfSigned(sd, "plant", nil, time.Now(), true); err == nil || !strings.Contains(err.Error(), "controller's user") {
		t.Errorf("an owner that cannot be given: %v", err)
	}
}

// A clock that reads earlier than the binary was built (never set) is
// warned about when a certificate is made, not refused (#1114 review).
func TestSelfSignedWarnsOfAClockBehind(t *testing.T) {
	defer func(f func() time.Time) { binaryBuilt = f }(binaryBuilt)
	built := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	binaryBuilt = func() time.Time { return built }
	sd := t.TempDir()
	c, err := ensureSelfSigned(sd, "p", nil, built.Add(-24*time.Hour), true)
	if err != nil || !c.Made || !c.BuiltAfter.Equal(built) || !strings.Contains(selfSignedNotice(c), "clock reads earlier") {
		t.Errorf("a clock behind: %+v %v", c, err)
	}
	sd = t.TempDir()
	c, err = ensureSelfSigned(sd, "p", nil, built.Add(time.Hour), true)
	if err != nil || !c.BuiltAfter.IsZero() || strings.Contains(selfSignedNotice(c), "clock reads earlier") {
		t.Errorf("a clock that is right: %+v %v", c, err)
	}
}

// The names are fixed when the certificate is made: the notice lists them,
// and names an address this machine has now that it lacks, with the fix.
func TestSelfSignedNoticeNamesItsSANs(t *testing.T) {
	sd := t.TempDir()
	if _, err := ensureSelfSigned(sd, "p", []string{"localhost", "127.0.0.1"}, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	c, err := ensureSelfSigned(sd, "p", []string{"LOCALHOST", "127.0.0.1", "10.9.8.7"}, time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	n := selfSignedNotice(c)
	if !strings.Contains(n, "Valid for: localhost, 127.0.0.1.") || !strings.Contains(n, "Not valid for 10.9.8.7") || !strings.Contains(n, "delete "+c.Path) {
		t.Errorf("notice:\n%s", n)
	}
	if len(c.Missing) != 1 {
		t.Errorf("missing %v", c.Missing)
	}
}

// naut run is the command that renews: its spec says so (the serve
// commands' tlsSpecOf does not, TestOnlyNautRunRenews).
func TestRunTLSRenews(t *testing.T) {
	proj := &project.Project{Name: "plant"}
	if spec := runTLS(proj, t.TempDir(), "127.0.0.1:8443"); !spec.renew || spec.name != "plant" {
		t.Errorf("naut run's spec: %+v", spec)
	}
}
