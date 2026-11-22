package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/alarm/escalate"
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
