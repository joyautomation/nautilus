package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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
	if code != 0 || !strings.Contains(out, "this machine has no contacts or secrets file") ||
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
	_, err = openAlarmContacts(proj, "", func(k string) string {
		if k == "INVOCATION_ID" {
			return "x" // under systemd, with no StateDirectory=
		}
		return ""
	}, &errb, false)
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

// On a deployed box there is no auth.state-dir in the manifest and no
// checkout: naut alarms contacts check, given the state directory (as the
// built controller is), makes naut check's findings there and exits 1 on
// a gap. naut run says the same gaps at start, as warnings.
func TestContactsCheckOnTheBox(t *testing.T) {
	dir, _ := contactsProject(t)
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(strings.ReplaceAll(contactsManifest, "auth:\n  state-dir: STATEDIR\n", "")), 0o644)
	sd := filepath.Join(t.TempDir(), "var-lib-nautilus")
	cli := func(stdin string, args ...string) (string, int) {
		var out, errb bytes.Buffer
		code := contactsMain(args[0], append([]string{"--project", dir, "--state-dir", sd}, args[1:]...), func(d string) (*project.Manifest, string, error) {
			m, err := project.ReadManifest(os.DirFS(d), "")
			return m, "", err // the built controller: no checkout
		}, false, func(k string) string {
			if k == "INVOCATION_ID" {
				return "x"
			}
			return ""
		}, strings.NewReader(stdin), &out, &errb)
		return out.String() + errb.String(), code
	}
	cli("", "contacts", "set", "alice", "--email", "alice@example.com")
	cli("", "contacts", "set", "bob", "--sms", "+15550101")
	out, code := cli("", "contacts", "check")
	if code != 1 || !strings.Contains(out, "erin has no sms address") || !strings.Contains(out, "sms.token") {
		t.Fatalf("check with gaps: exit %d\n%s", code, out)
	}

	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	proj.AuthStateDir = sd
	var errb bytes.Buffer
	if _, err := openAlarmContacts(proj, "", func(string) string { return "" }, &errb, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errb.String(), "naut run: warning:") || !strings.Contains(errb.String(), "erin has no sms address") {
		t.Errorf("naut run did not say the gaps at start:\n%s", errb.String())
	}

	cli("", "contacts", "set", "erin", "--sms", "+15550102")
	cli("tok-1\n", "secrets", "set", "sms.token")
	cli("pw-1\n", "secrets", "set", "email.password")
	if out, code := cli("", "contacts", "check"); code != 0 {
		t.Fatalf("check with none: exit %d\n%s", code, out)
	}
}

// On the box an absent contacts or secrets file is an empty one: naut
// alarms contacts check fails on a fresh state directory (it does not
// pass with a warning), and a webhook-only manifest's missing secret is
// still found. A user name is looked up ignoring case, as a send does.
func TestContactsCheckFreshBoxAndCase(t *testing.T) {
	dir, _ := contactsProject(t)
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(t.TempDir(), "fresh")
	errs, _ := checkContacts(m.Alarms, fresh, true)
	all := strings.Join(errs, "\n")
	for _, want := range []string{"pages alice by email", "pages bob by sms", "needs the secret sms.token", "needs the secret email.password"} {
		if !strings.Contains(all, want) {
			t.Errorf("fresh box: want %q in\n%s", want, all)
		}
	}
	var out, errb bytes.Buffer
	if code := contactsCheck(m.Alarms, fresh, &out, &errb); code != 1 {
		t.Errorf("contacts check on a fresh box exited %d:\n%s%s", code, out.String(), errb.String())
	}

	hook := &project.AlarmsConfig{}
	// A passing check says so with a verb (#616): "ok: every person ...".
	pass := &project.AlarmsConfig{}
	if err := yamlInto("channels: {}\nescalation: []\n", pass); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := contactsCheck(pass, fresh, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "ok: every person the escalation levels page has an address") || !strings.Contains(out.String(), fresh) {
		t.Errorf("a passing contacts check: exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
	if err := yamlInto(`
channels:
  webhook: { url: "https://hooks.example.com/page", header: Authorization }
escalation:
  - id: w
    match: { area: wetwell }
    levels:
      - { after: 0s, notify: [Alice], via: [webhook] }
`, hook); err != nil {
		t.Fatal(err)
	}
	if errs, _ := checkContacts(hook, fresh, true); len(errs) != 1 || !strings.Contains(errs[0], "webhook.header") {
		t.Errorf("webhook only, no secrets file: %v", errs)
	}

	sd := t.TempDir()
	os.Chmod(sd, 0o700)
	oncall.UpdateContacts(oncall.ContactsPath(sd), "x", func(c *oncall.Contacts) error {
		c.Contacts["alice"] = &oncall.Contact{Email: "alice@example.com"}
		return nil
	})
	mixed := &project.AlarmsConfig{}
	if err := yamlInto(`
escalation:
  - id: w
    match: { area: wetwell }
    levels:
      - { after: 0s, notify: [Alice], via: [log] }
channels:
  email: { smtp: "mail.example.com:587", from: alarms@example.com }
`, mixed); err != nil {
		t.Fatal(err)
	}
	mixed.Escalation[0].Levels[0].Via = []string{"email"}
	if errs, _ := checkContacts(mixed, sd, true); len(errs) != 0 {
		t.Errorf("Alice in the manifest, alice in the contacts: %v", errs)
	}
}

func yamlInto(src string, v any) error { return yaml.Unmarshal([]byte(src), v) }

// naut alarms contacts help prints the usage, even where no state
// directory resolves (a built controller run by hand outside systemd).
func TestContactsHelpNeedsNoStateDir(t *testing.T) {
	var out, errb bytes.Buffer
	code := contactsMain("contacts", []string{"help"}, func(string) (*project.Manifest, string, error) {
		return &project.Manifest{}, "", nil
	}, false, func(k string) string {
		if k == "INVOCATION_ID" {
			return "x"
		}
		return ""
	}, strings.NewReader(""), &out, &errb)
	if code != 0 || !strings.Contains(out.String(), "naut alarms contacts") {
		t.Fatalf("help = %d\n%s%s", code, out.String(), errb.String())
	}
}
