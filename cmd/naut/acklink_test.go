package main

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/internal/pairkey"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// linkProject is contactsProject's manifest with alarms.oncall's ack-url
// (and extra lines under oncall:).
func linkProject(t *testing.T, oncall string) *project.Project {
	t.Helper()
	dir, sd := contactsProject(t)
	m := strings.ReplaceAll(contactsManifest, "STATEDIR", sd)
	m = strings.Replace(m, "  oncall:\n", "  oncall:\n"+oncall, 1)
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	return proj
}

func mintCheck(t *testing.T, a, b *acklink.Keyring) bool {
	t.Helper()
	tok, err := a.Mint("Hi", "0123456789abcdef", "bob", "sms", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Verify(tok)
	return err == nil
}

// A pair's members derive the same key from the peer token (a link one
// sends checks on the other after a takeover); a controller alone keeps
// its own, made once, 0600, in the state directory, and reads it back.
func TestAckLinkKeyring(t *testing.T) {
	proj := linkProject(t, "    ack-url: https://hmi.example\n")
	var out bytes.Buffer
	a, err := ackLinkKeyring(proj, "", "peer-token", &out)
	if err != nil || a == nil {
		t.Fatalf("pair: %v %v", a, err)
	}
	b, _ := ackLinkKeyring(proj, "", "peer-token", &out)
	other, _ := ackLinkKeyring(proj, "", "another-token", &out)
	if !mintCheck(t, a, b) || mintCheck(t, a, other) {
		t.Fatal("the pair's members disagree, or two pairs agree")
	}
	// The key is pairkey's for ack links (one purpose, one key), never
	// the peer token itself.
	want, _ := pairkey.Derive([]byte("peer-token"), pairkey.AckLink)
	pinned, _ := acklink.New(want)
	if !mintCheck(t, a, pinned) {
		t.Fatal("the pair's key is not pairkey.Derive(peer token, alarm-ack-link)")
	}

	sd := t.TempDir()
	os.Chmod(sd, 0o700)
	c, err := ackLinkKeyring(proj, sd, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(sd, AckLinkKeyName))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", st, err)
	}
	d, _ := ackLinkKeyring(proj, sd, "", &out)
	if !mintCheck(t, c, d) || mintCheck(t, c, a) {
		t.Fatal("a restart made a new key, or the box's key is the pair's")
	}
	os.Chmod(filepath.Join(sd, AckLinkKeyName), 0o644)
	if _, err := ackLinkKeyring(proj, sd, "", &out); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a readable key file: %v", err)
	}
	os.WriteFile(filepath.Join(sd, AckLinkKeyName), []byte("not hex"), 0o600)
	os.Chmod(filepath.Join(sd, AckLinkKeyName), 0o600)
	if _, err := ackLinkKeyring(proj, sd, "", &out); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Fatalf("a bad key file: %v", err)
	}
	if _, err := ackLinkKeyring(proj, "", "", &out); err == nil || !strings.Contains(err.Error(), "state directory") {
		t.Fatalf("no state dir: %v", err)
	}
	// No ack-url: no links, nothing made.
	none, err := ackLinkKeyring(linkProject(t, ""), sd, "", &out)
	if none != nil || err != nil {
		t.Fatalf("without ack-url: %v %v", none, err)
	}
}

// ack-from-notification: sign-in where nobody can sign in is an error
// (naut check, and naut run sends no links); an http ack-url is warned.
func TestAckLinkChecks(t *testing.T) {
	proj := linkProject(t, "    ack-url: http://10.0.0.5:8080\n    ack-url-insecure: true\n    ack-from-notification: sign-in\n")
	errs, warns := ackLinkChecks(proj)
	if len(errs) != 1 || !strings.Contains(errs[0], "nobody can sign in") {
		t.Fatalf("errs %v", errs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "plaintext") {
		t.Fatalf("warns %v", warns)
	}
	if _, err := ackLinkKeyring(proj, t.TempDir(), "", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "nobody can sign in") {
		t.Fatalf("naut run with sign-in and no sign-in: %v", err)
	}
	var r systemRecorder
	journalInsecureChannels(proj, &r)
	if !strings.Contains(strings.Join(r.actions, ","), "alarm.ack-url-insecure") {
		t.Fatalf("journalled %v", r.actions)
	}
	if errs, warns := ackLinkChecks(linkProject(t, "    ack-url: https://10.0.0.5:8443\n")); len(errs)+len(warns) != 0 {
		t.Fatalf("a plain https ack-url by IP: %v %v", errs, warns)
	}
	// A name the manifest does not allow: the controller would refuse
	// the link's request by its Host.
	if _, warns := ackLinkChecks(linkProject(t, "    ack-url: https://hmi.example\n")); len(warns) != 1 || !strings.Contains(warns[0], "server.allowed-hosts") {
		t.Fatalf("a name not allowed: %v", warns)
	}
}

// The hook mints through the server for email and SMS pages only: a
// voice call cannot be tapped, a webhook goes to a system that must not
// act as the person, and before the server exists there is none.
func TestAckLinkHookKinds(t *testing.T) {
	proj := linkProject(t, "    ack-url: https://hmi.example\n")
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ring, _ := ackLinkKeyring(proj, "", "peer-token", &bytes.Buffer{})
	var ref atomic.Pointer[server.Server]
	hook := ackLinkHook(&ref)
	m := escalate.Message{Alarm: alarm.Record{Def: alarm.Def{ID: "Hi", AckRequired: true}, Occurrence: "0123456789abcdef"}, To: "bob", Level: 2}
	if hook(m, "email") != "" {
		t.Fatal("a link before the server exists")
	}
	ref.Store(server.New(rt, server.Options{AckLinks: ring, AckURL: "https://hmi.example"}))
	noAck := m
	noAck.Alarm.AckRequired = false
	if hook(noAck, "email") != "" {
		t.Fatal("a link for an alarm that needs no acknowledgement")
	}
	for via, want := range map[string]bool{"email": true, "sms": true, "voice": false, "webhook": false, "log": false} {
		u := hook(m, via)
		if (u != "") != want {
			t.Errorf("%s: %q", via, u)
		}
		if want {
			c, err := ring.Verify(strings.TrimPrefix(u, "https://hmi.example"+acklink.Path))
			if err != nil || c.Kind != via || c.Principal != "bob" || c.Level != 2 || c.Occ != m.Alarm.Occurrence {
				t.Errorf("%s: claim %+v %v", via, c, err)
			}
		}
	}
}

// A short peer token is warned about: every link is an offline check of
// a guess at it. A random one (naut redundancy new-pair) is not.
func TestAckLinkWarnsOfAShortPeerToken(t *testing.T) {
	proj := linkProject(t, "    ack-url: https://hmi.example\n")
	var out bytes.Buffer
	ackLinkKeyring(proj, "", "short", &out)
	if !strings.Contains(out.String(), "NAUTILUS_PEER_TOKEN is 5 characters") {
		t.Fatalf("no warning: %q", out.String())
	}
	out.Reset()
	ackLinkKeyring(proj, "", strings.Repeat("r", minPeerToken), &out)
	if out.Len() != 0 {
		t.Fatalf("a long token warned: %q", out.String())
	}
}

// Two starts at once on one state directory make one key, and both read
// it; a key file another user owns is refused (it could forge links).
func TestAckLinkKeyFileIsMadeOnceAndOwned(t *testing.T) {
	sd := t.TempDir()
	os.Chmod(sd, 0o700)
	path := filepath.Join(sd, AckLinkKeyName)
	const n = 8
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); keys[i], errs[i] = loadAckLinkKey(path) }()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || !bytes.Equal(keys[i], keys[0]) {
			t.Fatalf("start %d: %x %v; want everyone on %x", i, keys[i], errs[i], keys[0])
		}
	}
	if left, _ := filepath.Glob(filepath.Join(sd, ".alarm-link-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	old := ackLinkEuid
	ackLinkEuid = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { ackLinkEuid = old })
	if _, err := loadAckLinkKey(path); err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("another user's key file: %v", err)
	}
}

// inboundProject is linkProject with alarms.oncall.inbound taking SMS
// replies (the manifest has no voice channel).
func inboundProject(t *testing.T, extra string) *project.Project {
	t.Helper()
	return linkProject(t, "    inbound: { url: \"https://hmi.example:8443\", channels: [sms] }\n"+extra)
}

// Inbound replies need the keyring without an ack-url: the codes are
// made under it.
func TestInboundNeedsTheKeyring(t *testing.T) {
	proj := inboundProject(t, "")
	if proj.Alarms.Oncall.AckURL != "" {
		t.Fatal("fixture has an ack-url")
	}
	k, err := ackLinkKeyring(proj, "", "peer-token", &bytes.Buffer{})
	if err != nil || k == nil {
		t.Fatalf("inbound alone: %v %v", k, err)
	}
}

// The hooks: an SMS page gets a reply code and a call the keypress URL
// when inbound takes them; an alarm that needs no ack, another kind, or
// inbound off gets neither.
func TestInboundHooks(t *testing.T) {
	proj := inboundProject(t, "")
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ring, _ := ackLinkKeyring(proj, "", "peer-token", &bytes.Buffer{})
	var ref atomic.Pointer[server.Server]
	link, code := ackLinkHook(&ref), ackCodeHook(&ref)
	m := escalate.Message{Alarm: alarm.Record{Def: alarm.Def{ID: "Hi", AckRequired: true}, Occurrence: "0123456789abcdef"}, To: "bob", Level: 2}
	if code(m, "sms") != "" || link(m, "voice") != "" {
		t.Fatal("hooks before the server exists")
	}
	ref.Store(server.New(rt, server.Options{AckLinks: ring, Inbound: &server.Inbound{URL: "https://hmi.example:8443", SMS: true, Voice: true}}))
	c := code(m, "sms")
	if len(c) != acklink.CodeDigits {
		t.Fatalf("sms code %q", c)
	}
	if got, err := ring.MatchCode(c, "bob", []acklink.Standing{{Alarm: "Hi", Occ: m.Alarm.Occurrence}}); err != nil || got.Alarm != "Hi" {
		t.Fatalf("the code does not match: %+v %v", got, err)
	}
	for _, via := range []string{"email", "voice", "webhook", "push"} {
		if code(m, via) != "" {
			t.Errorf("a code for %s", via)
		}
	}
	u := link(m, "voice")
	tok, ok := strings.CutPrefix(u, "https://hmi.example:8443"+server.InboundVoicePath)
	if !ok {
		t.Fatalf("voice URL %q", u)
	}
	if cl, err := ring.Verify(tok); err != nil || cl.Kind != "voice" || cl.Principal != "bob" || cl.Level != 2 {
		t.Fatalf("voice claim %+v %v", cl, err)
	}
	noAck := m
	noAck.Alarm.AckRequired = false
	if code(noAck, "sms") != "" || link(noAck, "voice") != "" {
		t.Fatal("a code or call URL for an alarm that needs no acknowledgement")
	}
	ref.Store(server.New(rt, server.Options{AckLinks: ring}))
	if code(m, "sms") != "" || link(m, "voice") != "" {
		t.Fatal("inbound off, yet a code or call URL")
	}
}

// naut run's inbound options read the contacts and secrets files on the
// box: the provider's signature is checked under sms.token, a sender is
// found by number; inbound off gives none.
func TestInboundOptions(t *testing.T) {
	proj := inboundProject(t, "")
	if inboundOptions(linkProject(t, "    ack-url: https://hmi.example\n")) != nil {
		t.Fatal("inbound options with inbound off")
	}
	sd := proj.AuthStateDir
	os.MkdirAll(sd, 0o700)
	if err := oncall.UpdateContacts(oncall.ContactsPath(sd), "test", func(c *oncall.Contacts) error {
		c.Contacts = map[string]*oncall.Contact{"bob": {SMS: "+15550102"}, "erin": {SMS: "+15550103"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := oncall.UpdateSecrets(oncall.SecretsPath(sd), "test", func(s *oncall.Secrets) error {
		s.Secrets = map[string]string{channel.SecretSMSToken: "12345"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := oncall.Open(sd)
	if err != nil {
		t.Fatal(err)
	}
	proj.AlarmDirectory = st
	in := inboundOptions(proj)
	if in == nil || !in.SMS || in.Voice || in.URL != "https://hmi.example:8443" {
		t.Fatalf("options %+v", in)
	}
	if got := in.Callers("sms", "+15550103"); len(got) != 1 || got[0] != "erin" {
		t.Fatalf("callers %v", got)
	}
	// The wet-well policy pages the standby roster (bob, erin) by SMS and
	// alice by email only.
	rec := alarm.Record{Def: proj.Alarms.Defs[0], WaitMs: time.Now().UnixMilli()}
	if in.MayPage == nil || !in.MayPage(rec, "bob", "sms") || !in.MayPage(rec, "Erin", "sms") || in.MayPage(rec, "alice", "sms") || !in.MayPage(rec, "alice", "email") {
		t.Fatal("MayPage does not follow the escalation policy")
	}
	// The provider's published vector, under the token in the secrets file.
	form := url.Values{"CallSid": {"CA1234567890ABCDE"}, "Digits": {"1234"}, "From": {"+14158675309"}, "To": {"+18005551212"}, "Caller": {"+14158675309"}}
	const u, sig = "https://mycompany.com/myapp.php?foo=1&bar=2", "RSOYDt4T1cUTdK1PDd93/VVr8B8="
	if !in.Verify("sms", u, form, sig) {
		t.Fatal("signature under sms.token refused")
	}
	if in.Verify("voice", u, form, sig) || in.Verify("email", u, form, sig) {
		t.Fatal("checked under a token the secrets file does not hold")
	}
}

// naut check warns when the inbound URL's host is not one the controller
// answers.
func TestInboundChecks(t *testing.T) {
	if _, warns := ackLinkChecks(inboundProject(t, "")); len(warns) != 1 || !strings.Contains(warns[0], "alarms.oncall.inbound.url") || !strings.Contains(warns[0], "server.allowed-hosts") {
		t.Fatalf("warns %v", warns)
	}
	p := linkProject(t, "    inbound: { url: \"https://10.0.0.5:8443\", channels: [sms] }\n")
	if errs, warns := ackLinkChecks(p); len(errs)+len(warns) != 0 {
		t.Fatalf("by IP: %v %v", errs, warns)
	}
}
