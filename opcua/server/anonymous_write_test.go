package server

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// startAnonymousWrites is a server with opcua.writable and
// opcua.anonymous-writes as given, and an anonymous client on None. With
// pki it also offers the secure endpoints and a trusted writer connects
// on one (f.writer); without, it offers None alone, as a lab manifest
// whose author never touched certificates still gets (the store is made,
// but nothing need be trusted).
func startAnonymousWrites(t *testing.T, anonymous, pki bool, patterns []string) *writeFixture {
	t.Helper()
	f := &writeFixture{rt: writeRuntime(t), log: &syncBuffer{}}
	o := Options{Endpoint: "localhost:0", Name: "lab", Sweep: 20 * time.Millisecond,
		Writable: patterns, AnonymousWrites: anonymous, Logger: slog.New(slog.NewTextHandler(f.log, nil))}
	if pki {
		f.dir = filepath.Join(t.TempDir(), "pki")
		o.PKI = f.dir
	}
	s, err := New(f.rt, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f.s = s
	if pki {
		f.der, f.key = clientCert(t, writerURI, time.Now().Add(-time.Hour), 24*time.Hour)
		trust(t, f.dir, f.der)
		f.writer = f.connect(t)
	}
	f.anon = f.connectAnonymous(t)
	return f
}

// Off, the default: anonymous on None is refused every Value write with
// Bad_UserAccessDenied and reads UserAccessLevel CurrentRead, with or
// without secure endpoints, exactly as before the switch existed.
func TestAnonymousWritesOffRefusesAnonymous(t *testing.T) {
	for _, pki := range []bool{false, true} {
		f := startAnonymousWrites(t, false, pki, writePatterns)
		if got := write(t, f.anon, wv("Speed", 9.0))[0]; got != ua.StatusBadUserAccessDenied {
			t.Errorf("pki %v: anonymous write with the switch off: %s, want Bad_UserAccessDenied", pki, statusName(got))
		}
		if v := stored(t, f.rt, "Speed"); v != 0.0 {
			t.Errorf("pki %v: Speed = %v after a refused write", pki, v)
		}
		if got := read(t, f.anon, ua.NewStringNodeID(1, "Speed"), ua.AttributeIDUserAccessLevel).Value.Value(); got != uint8(ua.AccessLevelTypeCurrentRead) {
			t.Errorf("pki %v: anonymous UserAccessLevel %v with the switch off, want CurrentRead", pki, got)
		}
		if st := f.s.Status(); st.AnonymousWrites {
			t.Errorf("pki %v: Status says anonymous writes are on", pki)
		}
	}
}

// On, an anonymous session on None is a writer: it writes what
// opcua.writable opens, through the same checks and the same front door
// as a trusted client, and nothing else. A tag no pattern opens is still
// Bad_NotWritable, an input too, an array Bad_WriteNotSupported, a forced
// address Bad_Locked, a value that does not fit refused.
func TestAnonymousWritesOpenTheNoneEndpoint(t *testing.T) {
	for _, pki := range []bool{false, true} {
		f := startAnonymousWrites(t, true, pki, writePatterns)
		if err := f.rt.Tags().Force("Locked", 7.0); err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name  string
			item  *ua.WriteValue
			want  ua.StatusCode
			path  string
			value any
		}{
			{"matched setpoint", wv("Speed", 9.5), ua.StatusGood, "Speed", 9.5},
			{"member under a matched ancestor", wv("P101.Drive.Speed", 1450.0), ua.StatusGood, "P101.Drive.Speed", 1450.0},
			{"BYTE", wv("V_BYTE", uint8(17)), ua.StatusGood, "V_BYTE", int64(17)},
			{"Boolean", wv("Flag", true), ua.StatusGood, "Flag", true},
			{"not matched", wv("Closed", 1.0), ua.StatusBadNotWritable, "Closed", 0.0},
			{"member not matched", wv("P101.Running", true), ua.StatusBadNotWritable, "P101.Running", false},
			{"input, matched", wv("Level", 1.0), ua.StatusBadNotWritable, "", nil},
			{"an Object", wv("P101", 1.0), ua.StatusBadNotWritable, "", nil},
			{"array", wv("Levels", []float64{9, 9, 9}), ua.StatusBadWriteNotSupported, "Levels[0]", 1.0},
			{"forced", wv("Locked", 1.0), ua.StatusBadLocked, "Locked", 7.0},
			{"past SINT", wv("V_SINT", int64(128)), ua.StatusBadOutOfRange, "V_SINT", int64(0)},
			{"Double to INT", wv("V_INT", 5.0), ua.StatusBadTypeMismatch, "V_INT", int64(0)},
		}
		for _, c := range cases {
			got := write(t, f.anon, c.item)[0]
			if got != c.want {
				t.Errorf("pki %v, %s: %s, want %s", pki, c.name, statusName(got), statusName(c.want))
			}
			if c.path != "" {
				if v := stored(t, f.rt, c.path); v != c.value {
					t.Errorf("pki %v, %s: %s holds %v (%T), want %v (%T)", pki, c.name, c.path, v, v, c.value, c.value)
				}
			}
		}
		// A trusted writer on a secure endpoint is unchanged by the switch.
		if pki {
			if got := write(t, f.writer, wv("Speed", 3.0))[0]; got != ua.StatusGood {
				t.Errorf("trusted writer with the switch on: %s", statusName(got))
			}
		}
		st := f.s.Status()
		if !st.AnonymousWrites {
			t.Errorf("pki %v: Status does not say anonymous writes are on", pki)
		}
		if st.Writes == 0 || st.WritesRefused == 0 {
			t.Errorf("pki %v: Status writes %d refused %d", pki, st.Writes, st.WritesRefused)
		}
		// The audit line marks the writer anonymous: it has no certificate
		// to name.
		var anon bool
		for _, line := range strings.Split(f.log.String(), "\n") {
			if strings.Contains(line, `msg="opcua: write"`) && strings.Contains(line, `node="ns=1;s=Speed"`) && strings.Contains(line, "new=9.5") {
				anon = strings.Contains(line, "anonymous=true") && strings.Contains(line, "remote=127.0.0.1:")
			}
		}
		if !anon {
			t.Errorf("pki %v: no anonymous audit line for the Speed write:\n%s", pki, f.log.String())
		}
	}
}

// With the switch on and nothing under opcua.writable, nothing is
// writable: the switch says who may write, never what.
func TestAnonymousWritesOpenNothingWithoutAWritableList(t *testing.T) {
	f := startAnonymousWrites(t, true, false, nil)
	if got := write(t, f.anon, wv("Speed", 1.0))[0]; got != ua.StatusBadNotWritable {
		t.Errorf("anonymous write with no writable list: %s, want Bad_NotWritable", statusName(got))
	}
	if got := read(t, f.anon, ua.NewStringNodeID(1, "Speed"), ua.AttributeIDUserAccessLevel).Value.Value(); got != uint8(ua.AccessLevelTypeCurrentRead) {
		t.Errorf("UserAccessLevel %v with no writable list", got)
	}
	if !strings.Contains(f.log.String(), "anonymous-writes is on, but opcua.writable opens nothing") {
		t.Errorf("no start-up line saying the switch opens nothing:\n%s", f.log.String())
	}
}

// AccessLevel is the node's, unchanged by the switch. UserAccessLevel for
// an anonymous session follows it with the switch on, since a client
// decides from it whether to offer a write; off, it stays CurrentRead.
func TestAnonymousWritesUserAccessLevel(t *testing.T) {
	const r, rw = uint8(ua.AccessLevelTypeCurrentRead), uint8(ua.AccessLevelTypeCurrentRead | ua.AccessLevelTypeCurrentWrite)
	on := startAnonymousWrites(t, true, false, writePatterns)
	off := startAnonymousWrites(t, false, false, writePatterns)
	cases := []struct {
		addr      string
		node, use uint8
	}{
		{"Speed", rw, rw},
		{"P101.Drive.Speed", rw, rw},
		{"P101.Running", r, r},
		{"Closed", r, r},
		{"Level", r, r},  // an input, though matched
		{"Levels", r, r}, // an array: not writable until slice 4
		{"Pump1", rw, rw},
	}
	for _, c := range cases {
		nid := ua.NewStringNodeID(1, c.addr)
		for who, f := range map[string]*writeFixture{"on": on, "off": off} {
			if got := read(t, f.anon, nid, ua.AttributeIDAccessLevel).Value.Value(); got != c.node {
				t.Errorf("%s AccessLevel (switch %s) = %v, want %d", c.addr, who, got, c.node)
			}
		}
		if got := read(t, on.anon, nid, ua.AttributeIDUserAccessLevel).Value.Value(); got != c.use {
			t.Errorf("%s UserAccessLevel (switch on) = %v, want %d", c.addr, got, c.use)
		}
		if got := read(t, off.anon, nid, ua.AttributeIDUserAccessLevel).Value.Value(); got != r {
			t.Errorf("%s UserAccessLevel (switch off) = %v, want CurrentRead", c.addr, got)
		}
	}
}

// The standby gate holds for an anonymous writer as for a trusted one.
func TestAnonymousWritesRespectTheStandbyGate(t *testing.T) {
	f := startAnonymousWrites(t, true, false, writePatterns)
	var mu sync.Mutex
	lead := false
	f.s.SetWriteGate(func() bool { mu.Lock(); defer mu.Unlock(); return lead })
	if got := write(t, f.anon, wv("Speed", 5.0))[0]; got != ua.StatusBadInvalidState {
		t.Errorf("standby: %s, want Bad_InvalidState", statusName(got))
	}
	mu.Lock()
	lead = true
	mu.Unlock()
	if got := write(t, f.anon, wv("Speed", 5.0))[0]; got != ua.StatusGood {
		t.Errorf("leader: %s", statusName(got))
	}
}

// A client certificate sent on None makes no difference with the switch
// on either: the session writes as an anonymous one, the certificate
// neither checked nor needed.
func TestAnonymousWritesIgnoreACertificateOnNone(t *testing.T) {
	f := startAnonymousWrites(t, true, true, writePatterns)
	der, key := clientCert(t, "urn:test:untrusted", time.Now().Add(-time.Hour), 24*time.Hour) // never trusted
	c := f.connectAnonymous(t, opcua.Certificate(der), opcua.PrivateKey(key), opcua.ApplicationURI("urn:test:untrusted"))
	if got := write(t, c, wv("Speed", 6.0))[0]; got != ua.StatusGood {
		t.Errorf("None with an untrusted certificate, switch on: %s", statusName(got))
	}
}

// Start says the switch is on, every time: a warning on loopback, an
// error beyond it, where anyone who can reach the port may write.
func TestAnonymousWritesAreLoggedAtStart(t *testing.T) {
	f := startAnonymousWrites(t, true, false, writePatterns)
	log := f.log.String()
	if !strings.Contains(log, "level=WARN") || !strings.Contains(log, "anonymous-writes is on: any client on this machine may write") {
		t.Errorf("no loopback warning at start:\n%s", log)
	}
	if strings.Contains(log, "beyond loopback") {
		t.Errorf("a loopback server logged the network error:\n%s", log)
	}

	buf := &syncBuffer{}
	s, err := New(writeRuntime(t), Options{Endpoint: "0.0.0.0:4840", Writable: writePatterns, AnonymousWrites: true, Logger: slog.New(slog.NewTextHandler(buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	s.warnAnonymousWrites("0.0.0.0")
	if got := buf.String(); !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "ANY client that can reach this port may write") {
		t.Errorf("no error for a server listening beyond loopback:\n%s", got)
	}

	for host, want := range map[string]bool{"127.0.0.1": true, "[::1]": true, "0.0.0.0": false, "192.0.2.7": false, "[2001:db8::1]": false, "plc.local": false} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// naut check's warning: the switch on with nothing under writable.
func TestAnonymousWritesWarning(t *testing.T) {
	if w := AnonymousWritesWarning(true, nil); !strings.Contains(w, "nothing is writable") {
		t.Errorf("on, no writable list: %q", w)
	}
	for _, c := range []struct {
		on       bool
		patterns []string
	}{{false, nil}, {true, []string{"*_SP"}}, {false, []string{"*_SP"}}} {
		if w := AnonymousWritesWarning(c.on, c.patterns); w != "" {
			t.Errorf("on %v, patterns %v: %q", c.on, c.patterns, w)
		}
	}
}
