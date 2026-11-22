package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/internal/project"
)

const contactsManifest = `
name: lift
tasks:
  - program: main.st
tags:
  - { name: Hi, role: state, init: false }
auth:
  state-dir: STATEDIR
alarms:
  defs:
    - { id: Hi, tag: Hi, priority: high, area: wetwell }
  channels:
    email: { smtp: "mail.example.com:587", from: alarms@example.com, username: alarms }
    sms: { provider: "https://sms.example.com", account: AC0001, from: "+15550100", insecure: true }
  oncall:
    rosters:
      - { id: standby, members: [bob, erin] }
  escalation:
    - id: wet
      match: { area: wetwell }
      levels:
        - { after: 0s, notify: [alice], via: [email, log] }
        - { after: 10m, notify: [roster:standby], via: [sms] }
`

func contactsProject(t *testing.T) (dir, sd string) {
	t.Helper()
	dir = t.TempDir()
	sd = filepath.Join(t.TempDir(), "state")
	os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nVAR_EXTERNAL\n  Hi : BOOL;\nEND_VAR\nEND_PROGRAM\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(strings.ReplaceAll(contactsManifest, "STATEDIR", sd)), 0o644)
	return dir, sd
}

func contactsCLI(t *testing.T, dir, which, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := contactsMain(which, append([]string{"--project", dir}, args...), func(d string) (*project.Manifest, string, error) {
		m, err := project.ReadManifest(os.DirFS(d), "")
		return m, d, err
	}, true, func(string) string { return "" }, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), code
}

// naut check, with no contacts file on this machine, warns once where
// they are checked; with one (on the box), every person a level pages by
// email, sms or voice without an address for it is an error naming the
// fix, roster members included, and so is a missing channel secret.
// insecure: true is warned either way.
func TestCheckContacts(t *testing.T) {
	dir, sd := contactsProject(t)
	out, code := checkInDir(t, dir)
	if code != 0 || !strings.Contains(out, "this machine has no contacts file") ||
		!strings.Contains(out, "alarms.channels.sms: insecure: true") {
		t.Fatalf("no contacts file: code %d\n%s", code, out)
	}

	if _, e, c := contactsCLI(t, dir, "contacts", "", "set", "alice", "--email", "alice@example.com"); c != 0 {
		t.Fatal(e)
	}
	if _, e, c := contactsCLI(t, dir, "contacts", "", "set", "bob", "--sms", "+15550101"); c != 0 {
		t.Fatal(e)
	}
	out, code = checkInDir(t, dir)
	for _, want := range []string{
		"level 2 pages erin by sms, but erin has no sms address",
		"naut alarms contacts set erin --sms <address>",
		"alarms.channels.email needs the secret email.password",
		"alarms.channels.sms needs the secret sms.token",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q:\n%s", want, out)
		}
	}
	if code == 0 || strings.Contains(out, "pages alice") || strings.Contains(out, "pages bob") {
		t.Errorf("code %d (want an error), and alice and bob have their addresses:\n%s", code, out)
	}

	contactsCLI(t, dir, "contacts", "", "set", "erin", "--sms", "+15550102")
	for _, k := range []string{"email.password", "sms.token"} {
		if _, e, c := contactsCLI(t, dir, "secrets", "s3cret-"+k+"\n", "set", k); c != 0 {
			t.Fatal(e)
		}
	}
	if out, code = checkInDir(t, dir); code != 0 {
		t.Fatalf("everyone has an address and every secret is set (%d):\n%s", code, out)
	}
	_ = sd
}

// The CLI writes the files 0600 in the state directory, masks addresses
// in list unless asked, never prints a secret, and refuses a malformed
// address or an unknown key.
func TestContactsCLI(t *testing.T) {
	dir, sd := contactsProject(t)
	if _, e, c := contactsCLI(t, dir, "contacts", "", "set", "Alice", "--email", "alice@example.com", "--sms", "+15550101"); c != 0 {
		t.Fatal(e)
	}
	if _, _, c := contactsCLI(t, dir, "contacts", "", "set", "bob", "--sms", "5550101"); c == 0 {
		t.Error("a number that is not E.164 was accepted")
	}
	if _, _, c := contactsCLI(t, dir, "contacts", "", "set", "bob", "--fax", "+15550101"); c == 0 {
		t.Error("an unknown kind was accepted")
	}
	out, _, _ := contactsCLI(t, dir, "contacts", "", "list")
	if !strings.Contains(out, "alice") || strings.Contains(out, "alice@example.com") || strings.Contains(out, "+15550101") ||
		!strings.Contains(out, "a…@example.com") || !strings.Contains(out, "…01") {
		t.Errorf("list (masked):\n%s", out)
	}
	out, _, _ = contactsCLI(t, dir, "contacts", "", "list", "--show")
	if !strings.Contains(out, "alice@example.com") {
		t.Errorf("list --show:\n%s", out)
	}
	contactsCLI(t, dir, "contacts", "", "set", "alice", "--sms", "-")
	if c, _ := oncall.LoadContacts(oncall.ContactsPath(sd)); c.Contacts["alice"].SMS != "" || c.Contacts["alice"].Email == "" {
		t.Errorf(`"-" did not clear only the SMS number: %+v`, c.Contacts["alice"])
	}
	if _, _, c := contactsCLI(t, dir, "secrets", "x\n", "set", "sms.password"); c == 0 {
		t.Error("an unknown secret key was accepted")
	}
	contactsCLI(t, dir, "secrets", "tok-abc\n", "set", "sms.token")
	out, e, _ := contactsCLI(t, dir, "secrets", "", "list")
	if strings.Contains(out+e, "tok-abc") || !regexpMatch(`sms\.token\s+set`, out) || !regexpMatch(`voice\.token\s+not set`, out) {
		t.Errorf("secrets list:\n%s%s", out, e)
	}
	for _, f := range []string{oncall.ContactsPath(sd), oncall.SecretsPath(sd)} {
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v, want mode 0600", f, fi, err)
		}
	}
	if _, _, c := contactsCLI(t, dir, "contacts", "", "rm", "alice"); c != 0 {
		t.Error("rm alice failed")
	}
	if _, _, c := contactsCLI(t, dir, "contacts", "", "rm", "alice"); c == 0 {
		t.Error("removing someone who is not there succeeded")
	}
}

// naut run refuses a manifest whose channels need the contacts files when
// this controller has no state directory, naming the fix.
func TestRunNeedsAStateDirForChannels(t *testing.T) {
	dir, _ := contactsProject(t)
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(strings.ReplaceAll(contactsManifest, "  state-dir: STATEDIR\n", "  state-dir: \"\"\n")), 0o644)
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	err = openAlarmContacts(proj, "", func(k string) string {
		if k == "INVOCATION_ID" {
			return "x" // under systemd, with no StateDirectory=
		}
		return ""
	}, &errb)
	if err == nil || !strings.Contains(err.Error(), "auth.state-dir") {
		t.Fatalf("err = %v", err)
	}
}

// checkInDir runs naut check on dir and returns what it printed.
func checkInDir(t *testing.T, dir string) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		b.ReadFrom(r)
		done <- b.String()
	}()
	code := runCheck([]string{dir})
	w.Close()
	os.Stdout = old
	return <-done, code
}

func regexpMatch(pat, s string) bool { return regexp.MustCompile(pat).MatchString(s) }

type systemRecorder struct{ actions, targets []string }

func (r *systemRecorder) RecordSystem(action, target string, _ map[string]any) {
	r.actions, r.targets = append(r.actions, action), append(r.targets, target)
}

// naut run journals each channel with insecure: true at start.
func TestRunJournalsInsecureChannels(t *testing.T) {
	dir, _ := contactsProject(t)
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	var r systemRecorder
	journalInsecureChannels(proj, &r)
	if strings.Join(r.actions, ",") != "alarm.channel-insecure" || strings.Join(r.targets, ",") != "sms" {
		t.Fatalf("journalled %v %v, want alarm.channel-insecure for sms", r.actions, r.targets)
	}
}
