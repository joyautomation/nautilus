package tiadtest

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The CPU's PLC access control (cpu.accessControl, siemens-ide.md §3.3,
// #789 decision of 2026-11-20). The fakes model TIA as the TIA machine
// showed it: a new CPU has access control on and fails a hardware compile
// until a user holds the runtime right Full access; users and roles are
// the project's and outlive any device delete; a user's password meets
// the project's password policy.

func contractAccessControl(t *testing.T, start Starter) {
	t.Run("Password", func(t *testing.T) { contractAccessPassword(t, start) })
	t.Run("NewStation", func(t *testing.T) { contractAccessNewStation(t, start) })
	t.Run("Adopted", func(t *testing.T) { contractAccessAdopted(t, start) })
	t.Run("UploadRemovesWhatTheSessionMade", func(t *testing.T) { contractAccessUpload(t, start) })
}

func accessStation(mode string) tiad.Station {
	st := BenchStation()
	st.CPU.AccessControl = mode
	return st
}

// adoptedStation is the spec an adopted station takes after an upload:
// the uploaded CPU, keeping its protection and its access control.
func adoptedStation() tiad.Station {
	st := BenchStation()
	st.Profinet = nil
	st.CPU.ConfigurationProtection = tiad.ProtectKeep
	st.CPU.AccessControl = tiad.AccessKeep
	return st
}

func noteWith(notes []string, part string) bool {
	for _, n := range notes {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

const (
	madeUser  = `made the user "nautilus" with the runtime right Full access`
	foundUser = FoundUserNote
)

// contractAccessPassword: a Full access user needs its own password,
// which meets TIA's rules and the project's own policy before any write,
// and no refusal echoes it.
func contractAccessPassword(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	create := func(name string, secrets map[string]any) (int, []byte) {
		return rawJSON(t, c, "POST", "/v1/projects", map[string]any{"name": name, "station": BenchStation(), "secrets": secrets})
	}
	status, b := create("nopw", map[string]any{"webUsers": BenchSecrets.WebUsers})
	if status != http.StatusBadRequest || !strings.Contains(envelopeMessage(b), "cpu.accessControl is fullAccessUser, and secrets.fullAccessPassword is empty") {
		t.Fatalf("a Full access user without its password: HTTP %d %s", status, b)
	}
	// Another secret is never taken for it.
	status, b = create("crossed", map[string]any{"webUsers": BenchSecrets.WebUsers,
		"protection": BenchFullAccessPassword, "configurationPassword": BenchFullAccessPassword})
	if status != http.StatusBadRequest || !strings.Contains(envelopeMessage(b), "secrets.fullAccessPassword is empty") {
		t.Fatalf("a Full access user with only the other passwords: HTTP %d %s", status, b)
	}
	weak := "weakpass1x"
	status, b = create("weak", map[string]any{"webUsers": BenchSecrets.WebUsers, "fullAccessPassword": weak})
	if msg := envelopeMessage(b); status != http.StatusBadRequest || !strings.Contains(msg, "secrets.fullAccessPassword breaks TIA's password rules") ||
		!strings.Contains(msg, "no upper-case letter (A-Z)") || strings.Contains(msg, weak) {
		t.Fatalf("a weak Full access password: HTTP %d %s", status, b)
	}
	// A project whose own policy is stricter than TIA's default refuses
	// before any write, naming its rules.
	a.SetAccess(t, PasswordPolicy{MinLength: 12, MinDigits: 1, MinSpecial: 3, UpperAndLower: true}, "")
	status, b = create("strict", map[string]any{"webUsers": BenchSecrets.WebUsers, "fullAccessPassword": BenchFullAccessPassword})
	if msg := envelopeMessage(b); status != http.StatusBadRequest ||
		!strings.Contains(msg, "secrets.fullAccessPassword breaks the project's password policy for local users (at least 12 characters, 1 digit(s), 3 special character(s), upper and lower case): fewer than 3 special character(s)") ||
		strings.Contains(msg, BenchFullAccessPassword) {
		t.Fatalf("a password the project's policy refuses: HTTP %d %s", status, b)
	}
	if _, err := c.CreateProject(ctx, "strict", BenchStation(), tiad.Secrets{WebUsers: BenchSecrets.WebUsers, FullAccessPassword: "Bench-access-2026!"}); err != nil {
		t.Fatalf("a password the project's policy takes: %v", err)
	}
}

// contractAccessNewStation: a new CPU with keep fails its compile, as TIA
// does, and with fullAccessUser compiles; applying it again finds the user
// and sets its password, and never makes a second.
func contractAccessNewStation(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "keep", accessStation(tiad.AccessKeep), BenchSecrets)
	if err != nil {
		t.Fatalf("keep on a new CPU: %v", err)
	}
	if status, b := rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/compile", map[string]bool{"hardware": true, "software": true}); status == http.StatusOK ||
		envelopeKind(b) != tiad.KindCompileFailed || !strings.Contains(string(b), NoFullAccess) {
		t.Fatalf("a new CPU without a Full access user: HTTP %d %s, want compile_failed naming Full access", status, b)
	}
	// The same CPU, now with its user.
	r, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets)
	if err != nil || !noteWith(r.Notes, madeUser) {
		t.Fatalf("fullAccessUser on the new CPU: %+v %v", r, err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("a new CPU with its Full access user: %v", err)
	}
	rotated := BenchSecrets
	rotated.FullAccessPassword = "Rotated-access-2027"
	if r, err := s.ApplyStation(ctx, BenchStation(), rotated); err != nil || !noteWith(r.Notes, foundUser) || noteWith(r.Notes, madeUser) {
		t.Fatalf("fullAccessUser again: %+v %v", r, err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
}

// contractAccessAdopted: tiad never turns access control on for a CPU it
// did not make: fullAccessUser on an uploaded CPU without access control
// refuses before any write, naming keep; on one with it on, it adds the
// user.
func contractAccessAdopted(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "adopt", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{}); err != nil {
		t.Fatal(err)
	}
	full := adoptedStation()
	full.CPU.AccessControl = tiad.AccessFullAccessUser
	if _, err := s.ApplyStation(ctx, full, BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), `the project's CPU has its access control set to "Disabled", and tiad turns access control on only for a CPU it makes: to keep the CPU as found, set cpu.accessControl to keep`) {
		t.Fatalf("fullAccessUser on a CPU without access control: %v", err)
	}
	// Refused before any write: the session is as it was.
	if r, err := s.ApplyStation(ctx, adoptedStation(), BenchSecrets); err != nil || noteWith(r.Notes, "nautilus") {
		t.Fatalf("keep after the refusal: %+v %v", r, err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("an adopted CPU without access control compiles: %v", err)
	}

	a.SetAccess(t, DefaultPasswordPolicy(), AccessEnabled)
	s2, err := c.CreateProject(ctx, "adopt2", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.UploadWith(ctx, tiad.UploadRequest{}); err != nil {
		t.Fatal(err)
	}
	// The project's own policy is checked before any write on an apply too.
	a.SetAccess(t, PasswordPolicy{MinLength: 12, MinDigits: 1, MinSpecial: 3, UpperAndLower: true}, AccessEnabled)
	if _, err := s2.ApplyStation(ctx, full, BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "secrets.fullAccessPassword breaks the project's password policy for local users") {
		t.Fatalf("a password the project's policy refuses, on an apply: %v", err)
	}
	a.SetAccess(t, DefaultPasswordPolicy(), AccessEnabled)
	if r, err := s2.ApplyStation(ctx, full, BenchSecrets); err != nil || !noteWith(r.Notes, madeUser) {
		t.Fatalf("fullAccessUser on an adopted CPU with access control on (the policy refusal left the session as it was): %+v %v", r, err)
	}
	if _, err := s2.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
}

// contractAccessUpload: an upload removes what applies on its session
// made in the users and roles, and nothing else: a user the project held
// when it was opened stays (#789: a project may hold a "nautilus" user a
// previous deploy made on purpose).
func contractAccessUpload(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	// Uploads bring a CPU with access control on, so an apply after one
	// shows whether the user is still there.
	a.SetAccess(t, DefaultPasswordPolicy(), AccessEnabled)
	full := adoptedStation()
	full.CPU.AccessControl = tiad.AccessFullAccessUser

	s, err := c.CreateProject(ctx, "same", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	up, err := s.UploadWith(ctx, tiad.UploadRequest{})
	if want := []string{`user "nautilus"`, `role "nautilus full access"`}; err != nil || !reflect.DeepEqual(up.RemovedAccess, want) {
		t.Fatalf("an upload on the session that made the user: %+v %v, want removedAccess %q", up, err, want)
	}
	// An upload made nothing to remove the next time, unless an apply
	// makes it again.
	if _, err := s.ApplyStation(ctx, adoptedStation(), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if up, err := s.UploadWith(ctx, tiad.UploadRequest{}); err != nil || len(up.RemovedAccess) != 0 {
		t.Fatalf("an upload after one that removed what the session made: %+v %v", up, err)
	}
	if r, err := s.ApplyStation(ctx, full, BenchSecrets); err != nil || !noteWith(r.Notes, madeUser) {
		t.Fatalf("the user after the upload removed it: %+v %v", r, err)
	}
	// Made on this session again, so the next upload removes it again.
	if up, err := s.UploadWith(ctx, tiad.UploadRequest{}); err != nil || len(up.RemovedAccess) != 2 {
		t.Fatalf("a second upload: %+v %v", up, err)
	}
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatal(err)
	}

	// A project saved with its user, opened again: the user is the
	// project's, not this session's.
	s3, err := c.CreateProject(ctx, "kept", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Save(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := s3.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s4, err := c.Open(ctx, "projects/kept/kept.ap21")
	if err != nil {
		t.Fatal(err)
	}
	// keep names the CPU (an upload goes to the address the last spec
	// gave) and makes nothing.
	keep := BenchStation()
	keep.Profinet = nil
	keep.CPU.AccessControl = tiad.AccessKeep
	if r, err := s4.ApplyStation(ctx, keep, BenchSecrets); err != nil || noteWith(r.Notes, "nautilus") {
		t.Fatalf("keep on the opened project: %+v %v", r, err)
	}
	up, err = s4.UploadWith(ctx, tiad.UploadRequest{})
	if err != nil || len(up.RemovedAccess) != 0 {
		t.Fatalf("an upload on a session that made nothing: %+v %v", up, err)
	}
	if r, err := s4.ApplyStation(ctx, full, BenchSecrets); err != nil || !noteWith(r.Notes, foundUser) {
		t.Fatalf("the project's own user after an upload: %+v %v", r, err)
	}
}
