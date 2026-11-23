package tiadtest

import (
	"slices"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// accessControlTurnOn (K, naut-lead 2026-11-23): an apply turns a kept
// CPU's access control on only when it reads exactly "Disabled" and the
// request asks it; never from the spec's default, and never over an
// active anonymous user holding more than read rights. The project as
// turned on loads only by a hardware download that resets the CPU's user
// management with attestation.cpuAccessControl "disabled", and is not
// saved before that load.

func contractAccessTurnOn(t *testing.T, start Starter) {
	t.Run("Refusals", func(t *testing.T) { contractTurnOnRefusals(t, start) })
	t.Run("Anonymous", func(t *testing.T) { contractTurnOnAnonymous(t, start) })
	t.Run("LoadsOnlyByTheAttestationRoute", func(t *testing.T) { contractTurnOnLoads(t, start) })
	t.Run("PendingSurvivesReopenAndAdopt", func(t *testing.T) { contractTurnOnPending(t, start) })
	t.Run("PendingMarkGoesOverAnOffProject", func(t *testing.T) { contractTurnOnMarkGoes(t, start) })
}

var turnOn = tiad.ApplyOptions{AccessControlTurnOn: true}

// turnOnStation is the adopted station with a Full access user.
func turnOnStation() tiad.Station {
	st := adoptedStation()
	st.CPU.AccessControl = tiad.AccessFullAccessUser
	return st
}

// uploadedSession is a session that uploaded the bench CPU (access control
// as a.SetAccess scripts it, "Disabled" by default) and applied nothing.
func uploadedSession(t *testing.T, a Target, name string) *tiad.Session {
	t.Helper()
	c := a.Client()
	create := BenchStation()
	create.CPU.AccessControl = tiad.AccessKeep
	s, err := c.CreateProject(ctx, name, create, BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil {
		t.Fatal(err)
	}
	return s
}

func wantBadRequest(t *testing.T, name string, err error, says string) {
	t.Helper()
	if tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), says) {
		t.Fatalf("%s: %v, want bad_request saying %q", name, err, says)
	}
}

func contractTurnOnRefusals(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s := uploadedSession(t, a, "turnref")
	// Only with fullAccessUser.
	_, err := s.ApplyStationWith(ctx, adoptedStation(), BenchSecrets, turnOn)
	wantBadRequest(t, "turn-on with keep", err, TurnOnNeedsFullAccessUser)
	// Never from the default: without the opt-in the refusal names it.
	_, err = s.ApplyStation(ctx, turnOnStation(), BenchSecrets)
	wantBadRequest(t, "fullAccessUser over Disabled, no opt-in", err, TurnOnNotAsked)
	// Never over a web server judged against the state before it.
	web := turnOnStation()
	web.WebServer = &tiad.WebServer{Enabled: true}
	_, err = s.ApplyStation(ctx, web, BenchSecrets)
	wantBadRequest(t, "web server over Disabled, no opt-in", err, TurnOnNotAsked)
	// The refusals wrote nothing: keep still applies and compiles, and
	// the project's access control is still off (no Full access user is
	// needed to compile).
	if r, err := s.ApplyStation(ctx, adoptedStation(), BenchSecrets); err != nil || noteWith(r.Notes, "TURNED ON") {
		t.Fatalf("keep after the refusals: %+v %v", r, err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("compile after the refusals: %v", err)
	}
	closeFirst(t, s)

	// Another state than "Disabled" is never turned on.
	a.SetAccess(t, AccessScript{UploadAccessControl: "EnabledWithAccessControlViaAccessLevel"})
	s = uploadedSession(t, a, "turnlevel")
	_, err = s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn)
	wantBadRequest(t, "turn-on over another state", err, `or one it keeps whose access control reads "Disabled" (accessControlTurnOn)`)
	closeFirst(t, s)
	a.SetAccess(t, AccessScript{})

	// A CPU the apply makes, or one already on: the opt-in turns nothing
	// on, says so, and leaves the session's downloads and saves alone.
	m, err := c.CreateProject(ctx, "turnmade", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ApplyStationWith(ctx, BenchStation(), BenchSecrets, turnOn)
	if err != nil || !noteWith(r.Notes, TurnOnNothingNote) || noteWith(r.Notes, "TURNED ON") {
		t.Fatalf("turn-on on a made CPU: %+v %v", r, err)
	}
	if _, err := m.Save(ctx, false); err != nil {
		t.Fatalf("save after a turn-on that turned nothing on: %v", err)
	}
	closeFirst(t, m)
}

func contractTurnOnAnonymous(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	installGSD(t, a.Client())
	for _, w := range []string{"ProtectionLevelFullAccess", "ProtectionLevelReadAccess", "ProtectionLevelHMIAccess", "WebWriteTags", "WebChangeOperatingMode", "WebWriteWatchTables", "SomethingNew"} {
		a.SetAccess(t, AccessScript{UploadAnonymous: true, UploadAnonymousRoles: []AnonymousRoleScript{{Name: "viewers", Rights: []string{"WebReadTags"}}, {Name: "ops", Rights: []string{"WebReadTags", w}}}})
		s := uploadedSession(t, a, "anon")
		_, err := s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn)
		wantBadRequest(t, "anonymous with "+w, err, `the project's anonymous user is active and holds rights beyond reading (role "ops": `+w+`)`)
		wantBadRequest(t, "anonymous with "+w+" names the fix", err, "remove the anonymous user's write rights in the project, or do not turn access control on")
		// Refused before any write: the session saves (no turn-on held).
		if _, err := s.Save(ctx, false); err != nil {
			t.Fatalf("save after a refused turn-on (%s): %v", w, err)
		}
		closeFirst(t, s)
	}
	// A role whose rights tiad cannot read refuses too.
	a.SetAccess(t, AccessScript{UploadAnonymous: true, UploadAnonymousRoles: []AnonymousRoleScript{{Name: "Engineer", System: true}}})
	s := uploadedSession(t, a, "anonsys")
	_, err := s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn)
	wantBadRequest(t, "anonymous with a system role", err, `role "Engineer": rights tiad cannot read`)
	closeFirst(t, s)
	// An inactive anonymous user's rights never count.
	a.SetAccess(t, AccessScript{UploadAnonymousRoles: []AnonymousRoleScript{{Name: "all", Rights: []string{"ProtectionLevelFullAccess"}}}})
	s = uploadedSession(t, a, "anonoff")
	if r, err := s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn); err != nil || !noteWith(r.Notes, "the project's anonymous user is not active") {
		t.Fatalf("an inactive anonymous user: %+v %v", r, err)
	}
	closeFirst(t, s)
	// A TIA that reads a role's rights only with access control on: the
	// read after the turn-on refuses, and the session is left unclean
	// (closed unsaved).
	a.SetAccess(t, AccessScript{UploadAnonymous: true, RightsOnlyWhenOn: true, UploadAnonymousRoles: []AnonymousRoleScript{{Name: "ops", Rights: []string{"WebWriteTags"}}}})
	s = uploadedSession(t, a, "anonlate")
	_, err = s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn)
	wantBadRequest(t, "anonymous rights read only once on", err, `holds rights beyond reading (role "ops": WebWriteTags)`)
	wantBadRequest(t, "anonymous rights read only once on, says unclean", err, TurnOnLateRefusal)
	if _, err := s.Save(ctx, false); err == nil || !strings.Contains(err.Error(), "unclean") && !strings.Contains(err.Error(), "part-way") {
		t.Fatalf("save after the late refusal: %v, want the session unclean", err)
	}
	closeFirst(t, s)
	// Read rights only: a warning naming them.
	a.SetAccess(t, AccessScript{UploadAnonymous: true, UploadAnonymousRoles: []AnonymousRoleScript{{Name: "viewers", Rights: []string{"WebReadTags", "WebAllowDiagnostics"}}}})
	s = uploadedSession(t, a, "anonread")
	if r, err := s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn); err != nil ||
		!noteWith(r.Notes, "WARNING: the project's anonymous user is active: once access control is on anyone who reaches the CPU gets, without a password, WebAllowDiagnostics, WebReadTags") {
		t.Fatalf("an anonymous user with read rights: %+v %v", r, err)
	}
	closeFirst(t, s)
	a.SetAccess(t, AccessScript{})
}

func contractTurnOnLoads(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	a.SetAccess(t, AccessScript{UploadUsers: []string{"op2", "op1"}})
	s := uploadedSession(t, a, "turnon")
	// A web server in the same apply is judged against the state after
	// the turn-on.
	st := turnOnStation()
	st.WebServer = &tiad.WebServer{Enabled: true}
	r, err := s.ApplyStationWith(ctx, st, BenchSecrets, turnOn)
	if err != nil {
		t.Fatalf("turn-on: %v", err)
	}
	for _, n := range []string{TurnedOnNote, madeUser, "the project's anonymous user is not active",
		`the project's local users besides "nautilus" may log on to the CPU with their own rights: "op1", "op2"`, WebOnNote} {
		if !noteWith(r.Notes, n) {
			t.Fatalf("turn-on notes lack %q: %q", n, r.Notes)
		}
	}
	if _, err := c.PutFile(ctx, "src/Main.scl", []byte(programSCL)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	// Not saved before it loads, also after a second apply without the
	// flag (the project now reads on).
	_, err = s.Save(ctx, false)
	wantBadRequest(t, "save before the load", err, TurnOnSaveRefused)
	if _, err := s.ApplyStation(ctx, st, BenchSecrets); err != nil {
		t.Fatalf("a second apply without the flag: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	_, err = s.Save(ctx, false)
	wantBadRequest(t, "save after a second apply", err, TurnOnSaveRefused)

	a.SetQuestions(t, benchHardwareChecks)
	load := func(cpu string) tiad.DownloadRequest {
		r := ResetLoad(benchCPU)
		r.Attestation.CPUAccessControl = cpu
		return r
	}
	software := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), ConsistentDownload: tiad.Yes(), StartAfter: tiad.Yes()}}
	noReset := load(tiad.AttestCPUAccessDisabled)
	noReset.Decisions.UserManagementResetToProject = nil
	noReset.Decisions.UserManagementKeepOnline = tiad.Yes()
	noWho := load(tiad.AttestCPUAccessDisabled)
	noWho.Attestation.By = ""
	dryRoute := load(tiad.AttestCPUAccessDisabled)
	dryRoute.DryRun = true
	for _, c := range []struct {
		name string
		r    tiad.DownloadRequest
		says string
	}{
		{"software only", software, "it loads no hardware"},
		{"attested enabled", load(tiad.AttestCPUAccessEnabled), `it does not attest attestation.cpuAccessControl "disabled"`},
		{"nothing attested", load(""), `it does not attest attestation.cpuAccessControl "disabled"`},
		{"no reset", noReset, "it does not answer decisions.userManagementResetToProject yes"},
		{"an attestation without who", noWho, `it does not attest attestation.cpuAccessControl "disabled"`},
		{"a dry run shaped as the route", dryRoute, "it is a dry run"},
	} {
		_, err := s.Download(ctx, c.r)
		wantBadRequest(t, c.name, err, tiad.TurnOnDownloadRefused)
		wantBadRequest(t, c.name, err, c.says)
		wantBadRequest(t, c.name, err, "; nothing was sent to the CPU")
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("refused turn-on downloads loaded %+v", l)
	}
	// The attestation route loads it; the CPU then asks credentials.
	res, err := s.Download(ctx, load(tiad.AttestCPUAccessDisabled))
	if err != nil || !res.Downloaded || !res.CompareEqual || res.UserManagementReset != tiad.ResetRouteAttestation {
		t.Fatalf("the turn-on load: %+v %v", res, err)
	}
	if _, err := s.Compare(ctx); tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), LoginNoPassword) {
		t.Fatalf("the CPU asks credentials after the turn-on load: compare without the password: %v", err)
	}
	// Loaded: saved now, and the gate is spent (a reload with the CPU's
	// access control on goes by the logon route).
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("save after the load: %v", err)
	}
	res, err = s.Download(ctx, load(tiad.AttestCPUAccessEnabled))
	if err != nil || !res.Downloaded || res.UserManagementReset != tiad.ResetRouteLogon {
		t.Fatalf("a reload after the turn-on load: %+v %v", res, err)
	}
	// An upload on the same session brings the CPU's project back
	// (scripted Disabled); a second turn-on there re-arms the spent gate.
	a.SetAccess(t, AccessScript{})
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil {
		t.Fatalf("upload after the turn-on load: %v", err)
	}
	if _, err := s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn); err != nil {
		t.Fatalf("a second turn-on after the upload: %v", err)
	}
	_, err = s.Save(ctx, false)
	wantBadRequest(t, "save after a second turn-on", err, TurnOnSaveRefused)
	closeFirst(t, s)
}

// contractTurnOnPending: the deploy record's archive saves the project
// before TIA's download (tiad.Openness archives the saved project), so a
// turn-on download that ends in doubt leaves a saved project with access
// control on. It is marked turn-on pending: reopening it, or adopting the
// deploy's archive, keeps the gate, which then lets "enabled" too (that
// download may have loaded it); a verified load clears the mark.
func contractTurnOnPending(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s := uploadedSession(t, a, "pending")
	if _, err := s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "src/Main.scl", []byte(programSCL)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	// The stop is answered, then a reinit nobody agreed to: in doubt.
	a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionReinitialize, Message: "reinit", Objects: []string{"naut_Main_DB"}}})
	load := ResetLoad(benchCPU)
	load.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
	if _, err := s.Download(ctx, load); tiad.Kind(err) != tiad.KindOutcomeUnknown {
		t.Fatalf("the turn-on download, in doubt: %v", err)
	}
	list, err := c.Deploys(ctx, benchCPU)
	if err != nil || len(list.Deploys) == 0 || !list.Deploys[0].AccessTurnOn || list.Deploys[0].Status != tiad.DeployInDoubt {
		t.Fatalf("the deploy record of the turn-on download: %+v %v", list, err)
	}
	id := list.Deploys[0].ID
	closeFirst(t, s)

	reopen := func(project string) *tiad.Session {
		t.Helper()
		s, err := c.Open(ctx, project)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	gated := func(name string, s *tiad.Session) {
		t.Helper()
		if !slices.Contains(s.Notes, tiad.TurnOnPendingNote) {
			t.Fatalf("%s: the open does not say turn-on pending: %q", name, s.Notes)
		}
		// The project reads on now: the default applies without the flag.
		if _, err := s.ApplyStation(ctx, turnOnStation(), BenchSecrets); err != nil {
			t.Fatalf("%s: apply: %v", name, err)
		}
		if _, err := s.Compile(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		_, err := s.Save(ctx, false)
		wantBadRequest(t, name+": save", err, TurnOnSaveRefused)
		software := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true,
			Decisions: tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), ConsistentDownload: tiad.Yes(), StartAfter: tiad.Yes()}}
		_, err = s.Download(ctx, software)
		wantBadRequest(t, name+": software only", err, tiad.TurnOnDownloadRefusedTried)
		_, err = s.Download(ctx, ResetLoad(benchCPU))
		wantBadRequest(t, name+": no attestation", err, `it does not attest attestation.cpuAccessControl "disabled" or "enabled"`)
	}
	// The file API neither deletes nor writes the mark.
	for _, p := range []string{"projects/pending/pending.ap21.turn-on-pending", "projects/pending/pending.ap21.TURN-ON-PENDING"} {
		if err := c.DeleteFile(ctx, p); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), TurnOnMarkerFileRefused) {
			t.Fatalf("delete %s: %v", p, err)
		}
		if _, err := c.PutFile(ctx, p, []byte("{}")); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), TurnOnMarkerFileRefused) {
			t.Fatalf("put %s: %v", p, err)
		}
	}
	s = reopen("projects/pending/pending.ap21")
	gated("reopened", s)
	closeFirst(t, s)

	// The in-doubt deploy's archive, adopted, is pending too.
	ad, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "pendad", Deploy: id, Address: benchCPU})
	if err != nil || !slices.Contains(ad.Notes, tiad.TurnOnPendingNote) {
		t.Fatalf("adopt of the in-doubt turn-on deploy: %+v %v", ad, err)
	}
	s = reopen(ad.Project)
	gated("adopted", s)
	closeFirst(t, s)

	// The verified load clears the mark.
	s = reopen("projects/pending/pending.ap21")
	if _, err := s.ApplyStation(ctx, turnOnStation(), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	a.SetQuestions(t, benchHardwareChecks)
	res, err := s.Download(ctx, load)
	if err != nil || !res.Downloaded || res.UserManagementReset != tiad.ResetRouteAttestation {
		t.Fatalf("the load after the reopen: %+v %v", res, err)
	}
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("save after the load: %v", err)
	}
	closeFirst(t, s)
	s = reopen("projects/pending/pending.ap21")
	if len(s.Notes) != 0 {
		t.Fatalf("still pending after the verified load: %q", s.Notes)
	}
	closeFirst(t, s)
}

// contractTurnOnMarkGoes: the archive fails after the mark was written, so
// the saved project still reads its access control off. The reopened
// session is gated; an apply that finds the project's access control off
// removes the mark, and the session saves again.
func contractTurnOnMarkGoes(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s := uploadedSession(t, a, "markoff")
	// Saved as uploaded (access control off), then turned on unsaved.
	if _, err := s.ApplyStation(ctx, adoptedStation(), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStationWith(ctx, turnOnStation(), BenchSecrets, turnOn); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "src/Main.scl", []byte(programSCL)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	a.SetArchiveFails(t, "the disk is full")
	load := ResetLoad(benchCPU)
	load.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
	if _, err := s.Download(ctx, load); tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "nothing was sent to the CPU") {
		t.Fatalf("the turn-on download, its archive failing: %v", err)
	}
	a.SetArchiveFails(t, "")
	// Nothing reached TIA: still only "disabled".
	on := ResetLoad(benchCPU)
	on.Attestation.CPUAccessControl = tiad.AttestCPUAccessEnabled
	_, err := s.Download(ctx, on)
	wantBadRequest(t, "attested enabled after a failed record", err, tiad.TurnOnDownloadRefused)
	closeFirst(t, s)

	s, err = c.Open(ctx, "projects/markoff/markoff.ap21")
	if err != nil || !slices.Contains(s.Notes, tiad.TurnOnPendingNote) {
		t.Fatalf("reopen: %+v %v", s, err)
	}
	_, err = s.Save(ctx, false)
	wantBadRequest(t, "save while pending", err, TurnOnSaveRefused)
	r, err := s.ApplyStation(ctx, adoptedStation(), BenchSecrets)
	if err != nil || !noteWith(r.Notes, TurnOnClearedNote) {
		t.Fatalf("keep over an off project marked pending: %+v %v", r, err)
	}
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("save once the mark went: %v", err)
	}
	closeFirst(t, s)
	if s, err = c.Open(ctx, "projects/markoff/markoff.ap21"); err != nil || len(s.Notes) != 0 {
		t.Fatalf("reopen after the mark went: %+v %v", s, err)
	}
	closeFirst(t, s)
}
