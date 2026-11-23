package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/joyautomation/nautilus/alarm"
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

// A page carries its ack link inside the payload (#715 PR 6), and always
// fits one push: long words are cut on a character boundary, then
// dropped, then the link; the page itself is never dropped.
func TestPushPageCarriesItsLinkAndFits(t *testing.T) {
	link := "https://hmi.example/alarm-ack/" + strings.Repeat("t", 200)
	m := escalate.Message{Alarm: alarm.Record{Def: alarm.Def{ID: "Hi", Name: "Wet well high", Area: "wetwell"}, State: alarm.UnackActive}, Level: 1, AckURL: link}
	var p pushPayload
	if err := json.Unmarshal(pageBody(m), &p); err != nil || p.Ack != link || p.Name != "Wet well high" {
		t.Fatalf("page %+v %v", p, err)
	}
	m.AckURL = ""
	if b := pageBody(m); strings.Contains(string(b), `"ack"`) {
		t.Fatalf("a page with no link names one: %s", b)
	}

	// Long words: cut, never through a character, and the link stays.
	m.AckURL = link
	m.Alarm.Name = "x" + strings.Repeat("é", 2000) // 2 bytes each, off by one: a byte cut lands mid-character
	m.Alarm.Area = strings.Repeat("x", 5000)
	b := pageBody(m)
	p = pushPayload{} // a field the page omits must not keep the last one's
	if len(b) > webpush.MaxPayload {
		t.Fatalf("a page of %d bytes; a push takes %d", len(b), webpush.MaxPayload)
	}
	if err := json.Unmarshal(b, &p); err != nil || p.Ack != link || p.Name == "" || strings.ContainsRune(p.Name, utf8.RuneError) || len(p.Area) > pageFieldMax {
		t.Fatalf("long words: ack %q name %d bytes (broken character %v) area %d: %v", p.Ack, len(p.Name), strings.ContainsRune(p.Name, utf8.RuneError), len(p.Area), err)
	}

	// A link too long to fit beside the words: the words go first.
	m.AckURL = "https://hmi.example/alarm-ack/" + strings.Repeat("t", webpush.MaxPayload-400)
	p = pushPayload{}
	if err := json.Unmarshal(pageBody(m), &p); err != nil || p.Ack != m.AckURL || p.Name != "" {
		t.Fatalf("a long link: kept %v, name %q: %v", p.Ack == m.AckURL, p.Name, err)
	}
	// A name of characters JSON would escape for HTML is not six times
	// its length: it is cut, not dropped.
	m.AckURL = link
	m.Alarm.Name, m.Alarm.Area = strings.Repeat("<", 2000), strings.Repeat("&", 2000)
	p = pushPayload{}
	if err := json.Unmarshal(pageBody(m), &p); err != nil || len(p.Name) != pageFieldMax || len(p.Area) != pageFieldMax || p.Ack != link {
		t.Fatalf("escaped words: name %d area %d bytes, link kept %v: %v", len(p.Name), len(p.Area), p.Ack == link, err)
	}
	// One that cannot fit at all: the page goes without it.
	m.AckURL = "https://hmi.example/alarm-ack/" + strings.Repeat("t", webpush.MaxPayload)
	b = pageBody(m)
	p = pushPayload{}
	if err := json.Unmarshal(b, &p); err != nil || len(b) > webpush.MaxPayload || p.Ack != "" || p.Alarm != "Hi" {
		t.Fatalf("an oversize link: %d bytes, ack kept %v: %v", len(b), p.Ack != "", err)
	}
	// An id that alone is too long: the page still goes, its id cut.
	m.Alarm.ID = strings.Repeat("<", webpush.MaxPayload)
	b = pageBody(m)
	p = pushPayload{}
	if err := json.Unmarshal(b, &p); err != nil || len(b) > webpush.MaxPayload || p.Alarm == "" {
		t.Fatalf("an oversize id: %d bytes, id %d: %v", len(b), len(p.Alarm), err)
	}
}

// A chatter notice and a grouped flood page (#1249) carry no ack link and
// open the right screen: the alarm, or the flood's focus view. A grouped
// page fits one push whatever it lists.
func TestPushChatterAndFloodPages(t *testing.T) {
	m := escalate.Message{Alarm: alarm.Record{Def: alarm.Def{ID: "P 1", Name: "Seal leak"}, State: alarm.UnackActive}, Level: 1,
		AckURL: "https://hmi.example/alarm-ack/x", Chatter: &alarm.Chatter{Activations: 7, WindowMs: 60000}}
	var p pushPayload
	if err := json.Unmarshal(pageBody(m), &p); err != nil || p.Ack != "" || p.Path != "/alarms?alarm=P+1" || !strings.Contains(p.State, "chattering 7") {
		t.Fatalf("chatter page %+v %v", p, err)
	}
	m.Alarm.ID = strings.Repeat("é", 400)
	p = pushPayload{}
	if err := json.Unmarshal(pageBody(m), &p); err != nil || p.Path != "/alarms" {
		t.Fatalf("an id too long for a path: %q %v", p.Path, err)
	}
	g := &escalate.Group{Flood: alarm.FloodBrief{ID: "f1", Count: 40, Threshold: 10, SinceMs: 1}}
	for i := range 500 {
		g.Alarms = append(g.Alarms, escalate.GroupAlarm{ID: fmt.Sprint("a", i), Name: strings.Repeat("n", 300)})
	}
	b := pageBody(escalate.Message{Group: g, Level: 2})
	p = pushPayload{}
	if err := json.Unmarshal(b, &p); err != nil || len(b) > webpush.MaxPayload || p.Ack != "" || p.Path != "/alarms?focus=1" ||
		!strings.Contains(p.Name, "500 waiting") || p.Priority != "flood" {
		t.Fatalf("flood page (%d bytes) %+v %v", len(b), p, err)
	}
}
