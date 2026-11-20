package tiadtest

import (
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

func benchSecrets(configurationPassword string) map[string]any {
	return map[string]any{"webUsers": BenchSecrets.WebUsers, "configurationPassword": configurationPassword}
}

// contractPasswords runs PasswordCases past the client, on a project and
// on a download: no TIA call sees a password TIA would refuse, and no
// refusal echoes it.
func contractPasswords(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	for i, pc := range PasswordCases {
		name := "pw" + string(rune('a'+i))
		status, b := rawJSON(t, c, "POST", "/v1/projects", map[string]any{"name": name,
			"station": BenchStation(), "secrets": benchSecrets(pc.Password)})
		if pc.Want == "" {
			if status != http.StatusOK {
				t.Errorf("%s: HTTP %d %s, want a project", pc.Name, status, b)
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
		// A download checks it before anything else reaches TIA.
		status, b = rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/download", map[string]any{"address": benchCPU,
			"software": true, "announced": true, "secrets": map[string]string{"configurationPassword": pc.Password}})
		if msg := envelopeMessage(b); status != http.StatusBadRequest || !strings.Contains(msg, "download: secrets.configurationPassword breaks") || !strings.Contains(msg, pc.Want) {
			t.Errorf("%s: download HTTP %d %s, want 400 naming %q", pc.Name, status, b, pc.Want)
		}
		if n := len(a.Loaded(t)); n != 0 {
			t.Fatalf("%s: a refused password loaded %d downloads", pc.Name, n)
		}
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
	if r, err := s.ApplyStation(ctx, st, sec); err != nil || len(r.Changes) != 0 {
		t.Fatalf("applying the protected spec again: %+v %v, want no change", r, err)
	}

	path := "/v1/sessions/" + s.ID + "/download"
	body := func(secrets map[string]string) map[string]any {
		return map[string]any{"address": benchCPU, "hardware": true, "software": true, "announced": true,
			"decisions": map[string]any{"startAfter": true}, "secrets": secrets}
	}
	for _, sec := range []map[string]string{{}, {"protection": BenchConfigurationPassword}} {
		status, b := rawJSON(t, c, "POST", path, body(sec))
		if status != http.StatusConflict || envelopeKind(b) != tiad.KindDecisionRequired || !strings.Contains(string(b), `"id":"configuration-password"`) {
			t.Errorf("secrets %v: HTTP %d %s, want decision_required configuration-password", sec, status, b)
		}
	}
	if n := len(a.Loaded(t)); n != 0 {
		t.Fatalf("loaded %d without the configuration password", n)
	}
	status, b = rawJSON(t, c, "POST", path, body(map[string]string{"configurationPassword": BenchConfigurationPassword}))
	if status != http.StatusOK || len(a.Loaded(t)) != 1 {
		t.Fatalf("with the configuration password: HTTP %d %s", status, b)
	}
}

// contractProtectionModes: keep never sets up a new CPU, none turns
// protection off only with its password, and an upload followed by an
// apply makes the CPU anew, so keep refuses there too. So does any apply
// after a failed one: the session no longer holds a spec, and the CPU is
// made anew (#789: the CPU is kept in the next change). Each refusal
// below therefore gets a session of its own.
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
	if _, err := protected("nopw").ApplyStation(ctx, none, BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "turning that off needs it in secrets.configurationPassword") {
		t.Fatalf("none on a protected CPU without its password: %v", err)
	}
	wrong := BenchSecrets
	wrong.ConfigurationPassword = "Wrong-pass-2026"
	if _, err := protected("wrong").ApplyStation(ctx, none, wrong); err == nil {
		t.Fatal("none with the wrong password went through")
	}
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

	if err := s.Upload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStation(ctx, protectedStation(tiad.ProtectKeep), BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "keep has nothing to keep") {
		t.Fatalf("keep after an upload (the CPU is made anew): %v", err)
	}
	// That new CPU is TIA's unconfigured default, which never compiles.
	if _, err := s.Compile(ctx, true, true); tiad.Kind(err) != tiad.KindCompileFailed || !strings.Contains(err.Error(), "1 errors") {
		t.Fatalf("compile of an unconfigured CPU: %v", err)
	}
	if _, err := s.Compile(ctx, false, true); err != nil {
		t.Fatalf("a software-only compile does not touch the hardware: %v", err)
	}
	if _, err := s.ApplyStation(ctx, none, BenchSecrets); err != nil {
		t.Fatalf("none after an upload: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("compile after none: %v", err)
	}
}
