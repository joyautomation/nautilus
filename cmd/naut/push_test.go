package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/internal/webpush"
	"github.com/joyautomation/nautilus/server"
)

func stateDir(t *testing.T) string {
	d := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

// A controller that is not half of a pair makes the VAPID key at its
// first start and keeps it; a half of a pair refuses to, because the other
// box would have another and every phone would break at a takeover.
func TestPushKeyIsMadeOnceAndNeverOnAPair(t *testing.T) {
	sd := stateDir(t)
	var errb bytes.Buffer
	_, err := loadPushKey(sd, "test", false, &errb)
	if err == nil || !strings.Contains(err.Error(), "naut alarms secrets generate push.vapid-key") || !strings.Contains(err.Error(), "scp -p") {
		t.Fatalf("a pair member made a key or did not say the fix: %v", err)
	}
	if s, _ := oncall.ReadSecrets(oncall.SecretsPath(sd)); s.Secrets["push.vapid-key"] != "" {
		t.Fatal("a pair member wrote a key")
	}
	k1, err := loadPushKey(sd, "test", true, &errb)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := loadPushKey(sd, "test", false, &errb) // now there is one: a pair member reads it
	if err != nil || k2.Public() != k1.Public() {
		t.Fatalf("second read: %v", err)
	}
	if !strings.Contains(errb.String(), k1.Fingerprint()) || strings.Contains(errb.String(), k1.Private()) {
		t.Errorf("start line: %q", errb.String())
	}
	if _, err := generatePushKey(oncall.SecretsPath(sd), "test"); err == nil || !strings.Contains(err.Error(), "already set") {
		t.Errorf("generate replaced the key: %v", err)
	}
	// A secrets file holding something else under the key is refused, not replaced.
	sd2 := stateDir(t)
	oncall.UpdateSecrets(oncall.SecretsPath(sd2), "t", func(s *oncall.Secrets) error { s.Secrets["push.vapid-key"] = "garbage"; return nil })
	if _, err := loadPushKey(sd2, "test", true, &errb); err == nil || !strings.Contains(err.Error(), "does not hold a VAPID key") {
		t.Errorf("garbage key: %v", err)
	}
	if _, err := webpush.ParseKey(k1.Private()); err != nil {
		t.Error(err)
	}
}

func TestPushOutcomeSaysClassNeverTheEndpoint(t *testing.T) {
	class := func(err error) string {
		var se *escalate.SendError
		if !errors.As(err, &se) {
			t.Fatalf("%v is not a SendError", err)
		}
		return se.Class
	}
	for _, c := range []struct {
		res  server.PushResult
		err  error
		want string // "" is success
	}{
		{server.PushResult{Sent: 2, Failed: 1}, nil, ""},
		{server.PushResult{NoSubscription: true}, nil, escalate.FailNoContact},
		{server.PushResult{Pruned: 1}, nil, escalate.FailNoContact},
		{server.PushResult{Failed: 1, Class: escalate.FailProvider}, nil, escalate.FailProvider},
		{server.PushResult{Sealed: 1}, nil, escalate.FailConfig},
		{server.PushResult{Skipped: 1}, nil, escalate.FailConfig},
		{server.PushResult{}, server.ErrPushOff, escalate.FailConfig},
		{server.PushResult{}, server.ErrPushNotLeading, escalate.FailConfig},
		{server.PushResult{}, server.ErrPushUsers, escalate.FailConfig},
		{server.PushResult{}, errors.New("boom"), escalate.FailUnknown},
	} {
		err := pushOutcome(c.res, c.err)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%+v: %v", c.res, err)
		case c.want != "" && (err == nil || class(err) != c.want):
			t.Errorf("%+v %v: %v, want %s", c.res, c.err, err, c.want)
		}
	}
}

// naut check says so when alarms.channels.push has no one who could ever
// subscribe: a phone belongs to a signed-in person.
func TestCheckWarnsOfPushWithNoSignIn(t *testing.T) {
	manifest := func(auth string) string {
		return "name: p\ntasks:\n  - program: main.st\ntags:\n  - { name: Hi, type: BOOL, role: state }\n" + auth +
			"alarms:\n  defs:\n    - { id: Hi, tag: Hi, priority: high }\n  channels:\n    push: { subject: \"mailto:ops@example.com\" }\n"
	}
	for _, c := range []struct {
		name, auth string
		warns      bool
	}{
		{"no auth block", "", true},
		{"auth with no users", "auth:\n  anonymous: viewer\n", true},
		{"auth with a user", "auth:\n  users:\n    alice: [engineer]\n", false},
		{"auth with OIDC", "auth:\n  oidc:\n    issuer: https://login.example/v2.0\n    client-id: abc\n    users:\n      jsmith@corp.example: engineer\n", false},
	} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nVAR_EXTERNAL\n  Hi : BOOL;\nEND_VAR\nEND_PROGRAM\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest(c.auth)), 0o644)
		out, _ := checkInDir(t, dir)
		if got := strings.Contains(out, "alarms.channels.push: nobody can sign in"); got != c.warns {
			t.Errorf("%s: warned %v, want %v\n%s", c.name, got, c.warns, out)
		}
	}
}
