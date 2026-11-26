package tiadtest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The FW 3.1 configuration protection (cpu.configurationProtection,
// siemens-ide.md §3.3) and its password. The fakes model TIA's
// configurator as the offline check on the TIA machine found it: a new
// CPU is WithoutPassword and fails a hardware compile, Protect moves it
// to WithPassword, Unprotect with the password to None. A bare Unprotect
// of a new CPU is the aim, unproven (tiad.Openness reads the state back
// and refuses if TIA did otherwise).

// PasswordCases are TIA's password rules. Want is part of the refusal
// both copies (Go and C#) give, word for word, or "" for a password TIA
// takes. Lengths count UTF-16 code units, as .NET does.
var PasswordCases = []struct {
	Name, Password, Want string
}{
	{"good", BenchConfigurationPassword, ""},
	{"exactly 10", "Abcdefgh1x", ""},
	{"9 is short", "Abcdefg1x", ": shorter than 10 characters"},
	{"exactly 120", "Aa1" + strings.Repeat("x", 117), ""},
	{"121 is long", "Aa1" + strings.Repeat("x", 118), ": longer than 120 characters"},
	{"no digit", "Abcdefghijk", ": no digit"},
	{"no upper", "abcdefghij1", ": no upper-case letter (A-Z)"},
	{"no lower", "ABCDEFGHIJ1", ": no lower-case letter (a-z)"},
	{"every rule", "a", "breaks TIA's password rules (10 to 120 characters, at least one digit, one upper-case and one lower-case letter): shorter than 10 characters, no digit, no upper-case letter (A-Z)"},
	// 9 runes in 15 bytes: short, however bytes would count.
	{"bytes do not count", "Aa1" + strings.Repeat("é", 6), ": shorter than 10 characters"},
	// 7 runes in 11 UTF-16 code units: long enough as .NET counts.
	{"utf-16 units count", "Aa1" + strings.Repeat("😀", 4), ""},
	// A non-ASCII capital is not an upper-case letter here.
	{"ascii classes", "äääääääää1Ä", ": no upper-case letter (A-Z)"},
}

func contractProtection(t *testing.T, start Starter) {
	t.Run("Passwords", func(t *testing.T) { contractPasswords(t, start) })
	t.Run("Protected", func(t *testing.T) { contractProtected(t, start) })
	t.Run("Modes", func(t *testing.T) { contractProtectionModes(t, start) })
}

func protectedStation(mode string) tiad.Station {
	st := BenchStation()
	st.CPU.ConfigurationProtection = mode
	return st
}

// uploadedStation is protectedStation for the CPU an upload brought: the
// bench CPU has no access control (the S2a upload), so the spec keeps it.
func uploadedStation(mode string) tiad.Station {
	st := protectedStation(mode)
	st.CPU.AccessControl = tiad.AccessKeep
	// The uploaded CPU's access control is off, so its web server is left
	// as found: tiad refuses to turn it on there.
	st.WebServer = nil
	return st
}

// protectionNotes are the notes about the configuration protection: the
// Full access user's and the web server's are other fields'.
func protectionNotes(notes []string) []string {
	var out []string
	for _, n := range notes {
		if !strings.Contains(n, `"nautilus"`) && !strings.Contains(n, "the web user ") && n != WebOnNote {
			out = append(out, n)
		}
	}
	return out
}

func benchSecrets(configurationPassword string) map[string]any {
	return map[string]any{"webUsers": BenchSecrets.WebUsers, "configurationPassword": configurationPassword, "fullAccessPassword": BenchFullAccessPassword}
}

// contractPasswords runs PasswordCases past the client wherever a
// password may be set (password mode): no TIA call sees one TIA would
// refuse, and no refusal echoes it. A password that only unlocks (none, a
// download) was set under whatever rules TIA had then, and is never
// checked, so an adopted station is never locked out.
func contractPasswords(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	// One project at a time: each case's project is closed before the
	// next, and the bench one is made again after.
	closeFirst(t, s)
	for i, pc := range PasswordCases {
		name := "pw" + string(rune('a'+i))
		status, b := rawJSON(t, c, "POST", "/v1/projects", map[string]any{"name": name,
			"station": protectedStation(tiad.ProtectPassword), "secrets": benchSecrets(pc.Password)})
		if pc.Want == "" {
			var made struct {
				Data struct {
					Session string `json:"session"`
				} `json:"data"`
			}
			if status != http.StatusOK || json.Unmarshal(b, &made) != nil || made.Data.Session == "" {
				t.Errorf("%s: HTTP %d %s, want a project", pc.Name, status, b)
				continue
			}
			if st, b := raw(t, c, "DELETE", "/v1/sessions/"+made.Data.Session, nil); st != http.StatusOK {
				t.Fatalf("%s: closing its project: HTTP %d %s", pc.Name, st, b)
			}
			continue
		}
		msg := envelopeMessage(b)
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest ||
			!strings.Contains(msg, "secrets.configurationPassword breaks TIA's password rules") || !strings.Contains(msg, pc.Want) {
			t.Errorf("%s: HTTP %d %s, want 400 naming %q", pc.Name, status, b, pc.Want)
		}
		if len(pc.Password) >= 8 && strings.Contains(msg, pc.Password) {
			t.Errorf("%s: the refusal echoes the password: %s", pc.Name, b)
		}
	}
	s, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	// Unlock-only passwords are not TIA's rules' business.
	old := "old pw"
	if _, err := s.ApplyStation(ctx, BenchStation(), tiad.Secrets{WebUsers: BenchSecrets.WebUsers, ConfigurationPassword: old, FullAccessPassword: BenchFullAccessPassword}); err != nil {
		t.Errorf("none with an old-rules password: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	status, b := rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/download", map[string]any{"address": benchCPU,
		"software": true, "announced": true, "decisions": map[string]any{"resetData": true, "startAfter": false},
		"secrets": map[string]string{"configurationPassword": old, "fullAccessPassword": BenchFullAccessPassword}})
	if status != http.StatusOK {
		t.Errorf("a download carrying an old-rules password: HTTP %d %s", status, b)
	}
}

// contractProtected: a protected station needs its password at create,
// compiles, and loads only with the configuration password, never with
// the protection password; a second apply leaves it protected.
func contractProtected(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	st := protectedStation(tiad.ProtectPassword)

	status, b := rawJSON(t, c, "POST", "/v1/projects", map[string]any{"name": "bench", "station": st, "secrets": benchSecrets("")})
	if status != http.StatusBadRequest || !strings.Contains(envelopeMessage(b), "cpu.configurationProtection is password, and secrets.configurationPassword is empty") {
		t.Fatalf("a protected station without its password: HTTP %d %s", status, b)
	}
	sec := BenchSecrets
	sec.ConfigurationPassword = BenchConfigurationPassword
	s, err := c.CreateProject(ctx, "bench", st, sec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("a protected station compiles: %v", err)
	}
	// A rotated secret does not reach a protected project: the apply
	// says so, and how to change it.
	rotated := sec
	rotated.ConfigurationPassword = "Rotated-config-2027"
	r, err := s.ApplyStation(ctx, st, rotated)
	if n := protectionNotes(r.Notes); err != nil || len(r.Changes) != 0 || len(n) != 1 ||
		!strings.Contains(n[0], "apply none with the old password, then password with the new one") {
		t.Fatalf("applying the protected spec again with another password: %+v %v, want no change and one note", r, err)
	}
	// keep with a password sent changes nothing: the CPU stays
	// protected, and a download still asks for the password.
	if r, err := s.ApplyStation(ctx, protectedStation(tiad.ProtectKeep), sec); err != nil || len(protectionNotes(r.Notes)) != 0 {
		t.Fatalf("keep with a password: %+v %v", r, err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}

	path := "/v1/sessions/" + s.ID + "/download"
	body := func(secrets map[string]string) map[string]any {
		return map[string]any{"address": benchCPU, "hardware": true, "software": true, "announced": true,
			"decisions": map[string]any{"resetData": true, "startAfter": true, "acceptUnverifiedHardware": true}, "secrets": secrets,
			"attestation": map[string]string{"by": BenchAttestation.By, "text": BenchAttestation.Text}}
	}
	for _, sec := range []map[string]string{benchLoginJSON, {"protection": BenchConfigurationPassword, "fullAccessPassword": BenchFullAccessPassword}} {
		status, b := rawJSON(t, c, "POST", path, body(sec))
		if status != http.StatusConflict || envelopeKind(b) != tiad.KindDecisionRequired || !strings.Contains(string(b), `"id":"configuration-password"`) {
			t.Errorf("secrets %v: HTTP %d %s, want decision_required configuration-password", sec, status, b)
		}
	}
	if n := len(a.Loaded(t)); n != 0 {
		t.Fatalf("loaded %d without the configuration password", n)
	}
	status, b = rawJSON(t, c, "POST", path, body(map[string]string{"configurationPassword": BenchConfigurationPassword, "fullAccessPassword": BenchFullAccessPassword}))
	if status != http.StatusOK || len(a.Loaded(t)) != 1 {
		t.Fatalf("with the configuration password: HTTP %d %s", status, b)
	}
}

// contractProtectionModes: keep never sets up a new CPU, none turns
// protection off only with its password, and an upload followed by an
// apply keeps the uploaded CPU and its setting (#789), so keep keeps it.
// A refusal before any write leaves the session as it was; a wrong
// password fails part-way, and that session then only closes, unsaved.
func contractProtectionModes(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	sec := BenchSecrets
	sec.ConfigurationPassword = BenchConfigurationPassword

	if _, err := c.CreateProject(ctx, "keep", protectedStation(tiad.ProtectKeep), BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "keep has nothing to keep: set cpu.configurationProtection to password or none") {
		t.Fatalf("keep on a new CPU: %v", err)
	}

	protected := func(name string) *tiad.Session {
		t.Helper()
		s, err := c.CreateProject(ctx, name, protectedStation(tiad.ProtectPassword), sec)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	none := protectedStation(tiad.ProtectNone)
	nopw := protected("nopw")
	if _, err := nopw.ApplyStation(ctx, none, BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "turning that off needs it in secrets.configurationPassword") {
		t.Fatalf("none on a protected CPU without its password: %v", err)
	}
	// Refused before any write: the session is as it was.
	if _, err := nopw.Compile(ctx, true, true); err != nil {
		t.Fatalf("compile after a refusal before any write: %v", err)
	}
	if _, err := nopw.Save(ctx, false); err != nil {
		t.Fatalf("save after a refusal before any write: %v", err)
	}
	wrong := BenchSecrets
	wrong.ConfigurationPassword = "Wrong-pass-2026"
	closeFirst(t, nopw)
	w := protected("wrong")
	if _, err := w.ApplyStation(ctx, none, wrong); err == nil {
		t.Fatal("none with the wrong password went through")
	}
	// Failed part-way: never reused, never saved.
	if _, err := w.ApplyStation(ctx, none, sec); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("an apply after a failed one: %v", err)
	}
	if _, err := w.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after a failed apply: %v", err)
	}
	closeFirst(t, w)
	s := protected("modes")
	if r, err := s.ApplyStation(ctx, none, sec); err != nil || len(r.Changes) == 0 {
		t.Fatalf("none with the password: %+v %v", r, err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("an unprotected station compiles: %v", err)
	}
	// keep leaves an unprotected CPU as it is.
	if _, err := s.ApplyStation(ctx, protectedStation(tiad.ProtectKeep), BenchSecrets); err != nil {
		t.Fatalf("keep on a set-up CPU: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("compile after keep: %v", err)
	}

	// An upload brings the CPU's own setting (None, as the S2a upload
	// found), and an apply naming that CPU keeps it.
	if err := s.Upload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStation(ctx, uploadedStation(tiad.ProtectKeep), BenchSecrets); err != nil {
		t.Fatalf("keep after an upload: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("the uploaded CPU, kept, compiles: %v", err)
	}
	if _, err := s.ApplyStation(ctx, uploadedStation(tiad.ProtectPassword), sec); err != nil {
		t.Fatalf("password on the uploaded CPU: %v", err)
	}
	if _, err := s.ApplyStation(ctx, uploadedStation(tiad.ProtectNone), sec); err != nil {
		t.Fatalf("none with its password on the uploaded CPU: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("compile after none: %v", err)
	}
}
