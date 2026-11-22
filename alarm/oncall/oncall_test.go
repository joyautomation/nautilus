package oncall

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func stateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestContactsRoundTripAndStore(t *testing.T) {
	dir := stateDir(t)
	path := ContactsPath(dir)
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("no files yet: %v", err)
	}
	if a, err := st.Address("alice", SMS); a != "" || err != nil {
		t.Fatalf("an empty store: %q, %v", a, err)
	}
	err = UpdateContacts(path, "opsadmin", func(c *Contacts) error {
		c.Contacts["alice"] = &Contact{Email: "alice@example.com", SMS: "+15550101"}
		c.Contacts["oidc:carol"] = &Contact{Voice: "+15550102"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("contacts file is mode %04o, want 0600", fi.Mode().Perm())
		}
	}
	// The running store sees the change on its next lookup.
	if a, _ := st.Address("alice", SMS); a != "+15550101" {
		t.Errorf("alice sms = %q", a)
	}
	if a, _ := st.Address("oidc:carol", Voice); a != "+15550102" {
		t.Errorf("carol voice = %q", a)
	}
	if a, _ := st.Address("alice", Voice); a != "" {
		t.Errorf("alice has no voice number, got %q", a)
	}
	// Clearing every address removes the person.
	UpdateContacts(path, "opsadmin", func(c *Contacts) error { *c.Contacts["oidc:carol"] = Contact{}; return nil })
	c, _ := LoadContacts(path)
	if _, ok := c.Contacts["oidc:carol"]; ok || c.ChangedBy != "opsadmin" {
		t.Errorf("after clearing carol: %+v", c)
	}
}

func TestContactsRefuseMalformed(t *testing.T) {
	path := ContactsPath(stateDir(t))
	for _, c := range []Contact{{SMS: "5550101"}, {Voice: "+1 555 0101"}, {Email: "Alice <alice@example.com>"}, {Email: "nobody"}} {
		c := c
		if err := UpdateContacts(path, "x", func(f *Contacts) error { f.Contacts["alice"] = &c; return nil }); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	for _, p := range []string{"", "roster:ops", "al ice", "a\nb"} {
		if err := UpdateContacts(path, "x", func(f *Contacts) error { f.Contacts[p] = &Contact{SMS: "+15550101"}; return nil }); err == nil {
			t.Errorf("accepted principal %q", p)
		}
	}
	// A hand-edited file with a bad address is refused at load, and the
	// refusal does not repeat the address.
	os.WriteFile(path, []byte(`{"version":1,"contacts":{"alice":{"sms":"+1 555 0199 x"}}}`), 0o600)
	if _, err := LoadContacts(path); err == nil || strings.Contains(err.Error(), "0199") {
		t.Errorf("malformed file: %v", err)
	}
	os.WriteFile(path, []byte(`{"version":1,"contacts":{},"extra":1}`), 0o600)
	if _, err := LoadContacts(path); err == nil {
		t.Error("an unknown field was accepted")
	}
}

func TestSecrets(t *testing.T) {
	dir := stateDir(t)
	path := SecretsPath(dir)
	if err := UpdateSecrets(path, "x", func(s *Secrets) error { s.Secrets["sms.password"] = "x"; return nil }); err == nil {
		t.Error("an unknown key was accepted")
	}
	if err := UpdateSecrets(path, "x", func(s *Secrets) error { s.Secrets["sms.token"] = "a\nb"; return nil }); err == nil {
		t.Error("a line break was accepted")
	}
	if err := UpdateSecrets(path, "x", func(s *Secrets) error { s.Secrets["sms.token"] = "tok-123"; return nil }); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Secret("sms.token"); v != "tok-123" {
		t.Errorf("sms.token = %q", v)
	}
	if v, _ := st.Secret("voice.token"); v != "" {
		t.Errorf("voice.token = %q", v)
	}
}

// A manifest may spell a local user name in capitals; the contacts file
// stores it lowercase, as naut users does. An oidc: name is exact.
func TestStoreLooksUpUserNamesIgnoringCase(t *testing.T) {
	dir := stateDir(t)
	UpdateContacts(ContactsPath(dir), "x", func(c *Contacts) error {
		c.Contacts["alice"] = &Contact{SMS: "+15550101"}
		c.Contacts["oidc:Carol"] = &Contact{SMS: "+15550102"}
		return nil
	})
	st, _ := Open(dir)
	if a, _ := st.Address("Alice", SMS); a != "+15550101" {
		t.Errorf("Alice = %q", a)
	}
	if a, _ := st.Address("oidc:carol", SMS); a != "" {
		t.Errorf("oidc:carol matched oidc:Carol: %q", a)
	}
}
