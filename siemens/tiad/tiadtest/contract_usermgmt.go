package tiadtest

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The user-management reset (siemens-ide.md §3.5, #789 stage 4): a
// hardware load asks UserManagementDownload, TIA refuses keep there, and
// its reset deletes the CPU's users and sets its access control as the
// project has it. tiad answers it reset only under
// tiad.CheckUserManagementReset, checked before anything reaches the
// CPU. The rule's conjuncts are each unit-tested (TestCheckUserManagement
// Reset, UserManagementResetRule); this is the wiring: the session facts
// the agent feeds the rule, the refusal's shape and place, the route it
// reports, and that a reset given acts.

// qUserManagement is TIA's UserManagementDownload check as a hardware
// load asks it (#789 7065368046).
var qUserManagement = tiad.Question{ID: tiad.QuestionUserManagement, Message: "Delete user management data and disable access control in runtime", Detail: "UserManagementDownload"}

// UploadedKeep makes a session on a that uploaded the bench CPU (access
// control Disabled, unless a.SetAccess says otherwise before), applied a
// spec that keeps it at address, and compiled: ready for a hardware load
// that may reset user management (ResetLoad).
func UploadedKeep(t *testing.T, a Target, address string) *tiad.Session {
	t.Helper()
	c := a.Client()
	installGSD(t, c)
	// Created with a keep spec: a session that ever applied
	// fullAccessUser never keeps access control.
	create := BenchStation()
	create.CPU.AccessControl = tiad.AccessKeep
	s, err := c.CreateProject(ctx, "um", create, BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(ctx); err != nil {
		t.Fatal(err)
	}
	st := adoptedStation()
	st.CPU.Address = address
	if _, err := s.ApplyStation(ctx, st, BenchSecrets); err != nil {
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
	return s
}

// restoreSession is a session holding the bench CPU's as-found project:
// created with a keep spec, uploaded from the CPU (whose access control may
// be on: the upload logs on), the spec kept, compiled. The project's
// access control is Disabled and it holds no users (the fakes' upload, as
// the bench's as-found archive).
func restoreSession(t *testing.T, a Target) *tiad.Session {
	t.Helper()
	c := a.Client()
	create := BenchStation()
	create.CPU.AccessControl = tiad.AccessKeep
	s, err := c.CreateProject(ctx, "restore", create, BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStation(ctx, adoptedStation(), BenchSecrets); err != nil {
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
	return s
}

// ResetLoad is a hardware load at address that answers the bench's
// checks (and the device an upload keeps) and asks a user-management
// reset.
func ResetLoad(address string) tiad.DownloadRequest {
	return AcceptHardware(tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: address, Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), ConsistentDownload: tiad.Yes(), StartAfter: tiad.Yes(),
			KeptDevices: []string{UploadedIODevice().Name}, UserManagementResetToProject: tiad.Yes()}})
}

// benchHardwareChecks are the checks the bench's hardware load asked, in
// its order (#789 7065368046).
var benchHardwareChecks = []tiad.Question{qStop, {ID: tiad.QuestionOverwrite}, {ID: tiad.QuestionConsistentDownload}, qUserManagement}

// wantResetRefused checks a reset refused before TIA: decision_required
// naming says, listing user-management, nothing sent, nothing acting.
func wantResetRefused(t *testing.T, name string, err error, says string) {
	t.Helper()
	if tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), says) || !strings.Contains(err.Error(), "nothing was sent to the CPU") ||
		questionIDs(tiad.Questions(err)) != tiad.QuestionUserManagement || len(tiad.ActingAnswers(err)) != 0 {
		t.Fatalf("%s: %v (questions %s)", name, err, questionIDs(tiad.Questions(err)))
	}
}

func contractUserManagementReset(t *testing.T, start Starter) {
	t.Run("UploadRoute", func(t *testing.T) {
		a := start(t, Config{Targets: []string{benchCPU, otherCPU}})
		s := UploadedKeep(t, a, benchCPU)
		a.SetQuestions(t, benchHardwareChecks)
		// Refused before TIA, each named, nothing loaded: a software-only
		// download; and an attestation that says the CPU's access control
		// is on, the upload notwithstanding.
		sw := ResetLoad(benchCPU)
		sw.Hardware, sw.Decisions.AcceptUnverifiedHardware, sw.Attestation = false, nil, nil
		_, err := s.Download(ctx, sw)
		wantResetRefused(t, "software only", err, "loads no hardware")
		// An attestation that says enabled takes the logon route: without
		// the password, refused before TIA.
		on := ResetLoad(benchCPU)
		on.Attestation.CPUAccessControl = tiad.AttestCPUAccessEnabled
		on.Secrets = nil
		_, err = s.Download(ctx, on)
		wantResetRefused(t, "attested enabled, no password", err, tiad.ResetNoPassword)
		// Its place among tiad's own refusals: after the hardware gate
		// and the kept devices, before start-after and the device pin.
		hw := ResetLoad(benchCPU)
		hw.Hardware = false
		hw.Decisions.StartAfter = nil
		if _, err := s.Download(ctx, hw); !strings.Contains(err.Error(), "loads no hardware") {
			t.Fatalf("a refused reset before start-after: %v", err)
		}
		noHW := ResetLoad(benchCPU)
		noHW.Decisions.AcceptUnverifiedHardware = nil
		if _, err := s.Download(ctx, noHW); questionIDs(tiad.Questions(err)) != tiad.QuestionUnverifiedHardware {
			t.Fatalf("the hardware gate before a reset: %v", err)
		}
		noKept := ResetLoad(benchCPU)
		noKept.Hardware = false
		noKept.Decisions.KeptDevices = nil
		if _, err := s.Download(ctx, noKept); questionIDs(tiad.Questions(err)) != tiad.QuestionKeptDevices {
			t.Fatalf("the kept devices before a reset: %v", err)
		}
		if l := a.Loaded(t); len(l) != 0 {
			t.Fatalf("a refused reset loaded %+v", l)
		}
		// This session uploaded from this CPU and keeps its access control
		// (Disabled). While the upload route is off (tiad.UploadRouteEnabled)
		// that shows nothing, and the refusal says why; with it on, the
		// reset goes by the upload route without an attestation of the
		// CPU's access control.
		if tiad.UploadRouteEnabled {
			res, err := s.Download(ctx, ResetLoad(benchCPU))
			if err != nil || !res.Downloaded || res.UserManagementReset != tiad.ResetRouteUpload || questionIDs(res.Questions) != "stop-cpu,overwrite,consistent-download,user-management" {
				t.Fatalf("reset by the upload route: %+v %v", res, err)
			}
			if res.Attestation == nil || res.Attestation.CPUAccessControl != "" {
				t.Fatalf("the attestation said nothing of access control: %+v", res.Attestation)
			}
		} else {
			_, err := s.Download(ctx, ResetLoad(benchCPU))
			wantResetRefused(t, "the upload route, off", err, "the upload route is not yet available")
			if l := a.Loaded(t); len(l) != 0 {
				t.Fatalf("the upload route, off, loaded %+v", l)
			}
		}
		attested := ResetLoad(benchCPU)
		attested.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
		a.SetQuestions(t, benchHardwareChecks)
		res, err := s.Download(ctx, attested)
		want := tiad.ResetRouteAttestation
		if tiad.UploadRouteEnabled {
			want = tiad.ResetRouteUpload
		}
		if err != nil || !res.Downloaded || res.UserManagementReset != want || questionIDs(res.Questions) != "stop-cpu,overwrite,consistent-download,user-management" {
			t.Fatalf("reset, attested disabled: %+v %v, want route %s", res, err, want)
		}
		// A reset TIA never asks is no reset.
		a.SetQuestions(t, []tiad.Question{qStop})
		if res, err := s.Download(ctx, attested); err != nil || res.UserManagementReset != "" {
			t.Fatalf("a reset TIA never asked: %+v %v", res, err)
		}
		// Keep and reset both yes is a bad request, from the agent itself.
		both := ResetLoad(benchCPU)
		both.Decisions.UserManagementKeepOnline = tiad.Yes()
		if status, b := rawJSON(t, a.Client(), "POST", "/v1/sessions/"+s.ID+"/download", both); status != http.StatusBadRequest || !strings.Contains(string(b), "both yes") {
			t.Fatalf("keep and reset both yes: HTTP %d %s", status, b)
		}
		odd := ResetLoad(benchCPU)
		odd.Attestation.CPUAccessControl = "Disabled"
		if status, b := rawJSON(t, a.Client(), "POST", "/v1/sessions/"+s.ID+"/download", odd); status != http.StatusBadRequest || !strings.Contains(string(b), "attestation.cpuAccessControl is not one of disabled, enabled") {
			t.Fatalf("an attestation word outside the vocabulary: HTTP %d %s", status, b)
		}
	})

	t.Run("ResetActs", func(t *testing.T) {
		// A reset given is acting: a cancel at a later check is
		// outcome_unknown naming it, where keep leaves a clean refusal.
		a := start(t, Config{Targets: []string{benchCPU}})
		s := UploadedKeep(t, a, benchCPU)
		later := []tiad.Question{qUserManagement, {ID: tiad.QuestionOverwrite}}
		keep := ResetLoad(benchCPU)
		keep.Decisions.UserManagementResetToProject, keep.Decisions.UserManagementKeepOnline, keep.Decisions.Overwrite = nil, tiad.Yes(), nil
		// A software load, where TIA takes keep (#789 restart window);
		// a hardware load refuses it on the bench, which the fakes do not
		// model.
		keep.Hardware, keep.Decisions.AcceptUnverifiedHardware, keep.Attestation = false, nil, nil
		a.SetQuestions(t, later)
		_, err := s.Download(ctx, keep)
		if tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != tiad.QuestionOverwrite || len(tiad.ActingAnswers(err)) != 0 {
			t.Fatalf("keep, then overwrite unanswered: %v (acting %v), want a clean decision_required", err, tiad.ActingAnswers(err))
		}
		reset := ResetLoad(benchCPU)
		reset.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
		reset.Decisions.Overwrite = nil
		a.SetQuestions(t, later)
		_, err = s.Download(ctx, reset)
		// (The agent names start-after too: TIA's own cancel may come
		// after its after-download callback, which the Go fake does not
		// model.)
		if tiad.Kind(err) != tiad.KindOutcomeUnknown || !slices.Contains(tiad.ActingAnswers(err), tiad.QuestionUserManagement) ||
			!strings.Contains(err.Error(), "may already have reset the CPU's user management to the project's") {
			t.Fatalf("reset, then overwrite unanswered: %v (acting %v), want outcome_unknown naming user-management", err, tiad.ActingAnswers(err))
		}
	})

	t.Run("AttestationRoute", func(t *testing.T) {
		// The spec moves the uploaded CPU to another address: this
		// session never uploaded from there, so only the operator's
		// attestation can show its access control is off.
		a := start(t, Config{Targets: []string{benchCPU, otherCPU}})
		s := UploadedKeep(t, a, otherCPU)
		a.SetQuestions(t, benchHardwareChecks)
		_, err := s.Download(ctx, ResetLoad(otherCPU))
		says := `nothing shows the CPU's access control as it is now: give attestation.cpuAccessControl "disabled" or "enabled"`
		if tiad.UploadRouteEnabled {
			says = `nothing shows the CPU's access control as it is now: upload from ` + otherCPU + ` in this session first, or give attestation.cpuAccessControl "disabled" or "enabled"`
		}
		wantResetRefused(t, "uploaded from another address, no attestation", err, says)
		att := ResetLoad(otherCPU)
		att.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
		res, err := s.Download(ctx, att)
		if err != nil || !res.Downloaded || res.UserManagementReset != tiad.ResetRouteAttestation {
			t.Fatalf("reset by the attestation route: %+v %v", res, err)
		}
		if res.Attestation == nil || res.Attestation.CPUAccessControl != tiad.AttestCPUAccessDisabled || res.Attestation.At == "" {
			t.Fatalf("the attestation comes back with what it said of access control: %+v", res.Attestation)
		}
	})

	t.Run("LogonRouteNeedsALogon", func(t *testing.T) {
		// Attested enabled, with the password, on a CPU whose access
		// control is off: let before TIA, but the CPU asks no
		// credentials, so when TIA asks the check tiad refuses the reset.
		// After stop-cpu that is in doubt, naming why; nothing loads and
		// no reset is given.
		a := start(t, Config{Targets: []string{benchCPU}})
		s := UploadedKeep(t, a, benchCPU)
		a.SetQuestions(t, benchHardwareChecks)
		on := ResetLoad(benchCPU)
		on.Attestation.CPUAccessControl = tiad.AttestCPUAccessEnabled
		_, err := s.Download(ctx, on)
		if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), tiad.ResetNoLogon) || slices.Contains(tiad.ActingAnswers(err), tiad.QuestionUserManagement) {
			t.Fatalf("attested enabled, no logon: %v (acting %v), want outcome_unknown naming why, no reset given", err, tiad.ActingAnswers(err))
		}
		// With user-management asked first, the same refusal is clean.
		a.SetQuestions(t, []tiad.Question{qUserManagement, qStop})
		_, err = s.Download(ctx, on)
		if tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != tiad.QuestionUserManagement ||
			!strings.Contains(tiad.Questions(err)[0].Message, tiad.ResetNoLogon) {
			t.Fatalf("attested enabled, no logon, asked first: %v (questions %+v)", err, tiad.Questions(err))
		}
		if l := a.Loaded(t); len(l) != 0 {
			t.Fatalf("a reset refused when TIA asked loaded %+v", l)
		}
	})

	t.Run("AccessControlOn", func(t *testing.T) {
		// #789 option 1: the window's three loads through tiad. A, turning
		// access control on: a station tiad made (fullAccessUser), the
		// CPU's access control attested off. B, a reload of it with the
		// CPU's access control on: tiad logs on, then resets. C, the
		// restore: an as-found project with access control off and no
		// users, loaded over the CPU tiad turned on, logged on as
		// "nautilus", a user the project being loaded does not hold.
		a, s := compiledBench(t, start)
		c := a.Client()
		a.SetQuestions(t, benchHardwareChecks)
		load := func(cpu string, secrets *tiad.Secrets) tiad.DownloadRequest {
			r := AcceptHardware(tiad.DownloadRequest{Secrets: secrets, Address: benchCPU, Hardware: true, Software: true, Announced: true,
				Decisions: tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), ConsistentDownload: tiad.Yes(), StartAfter: tiad.Yes(), UserManagementResetToProject: tiad.Yes()}})
			r.Attestation.CPUAccessControl = cpu
			return r
		}
		// Refused before TIA: a password the project's record of
		// "nautilus" does not hold, and no word on the CPU's access
		// control.
		_, err := s.Download(ctx, load(tiad.AttestCPUAccessDisabled, &tiad.Secrets{FullAccessPassword: "Other-access-2026"}))
		wantResetRefused(t, "turning it on, another password", err, tiad.ResetPasswordNotRecorded)
		_, err = s.Download(ctx, load("", BenchDownloadSecrets()))
		wantResetRefused(t, "turning it on, nothing attested", err, `give attestation.cpuAccessControl "disabled" or "enabled"`)
		if l := a.Loaded(t); len(l) != 0 {
			t.Fatalf("refused resets loaded %+v", l)
		}
		// A.
		res, err := s.Download(ctx, load(tiad.AttestCPUAccessDisabled, BenchDownloadSecrets()))
		if err != nil || !res.Downloaded || !res.CompareEqual || res.UserManagementReset != tiad.ResetRouteAttestation {
			t.Fatalf("turning access control on: %+v %v", res, err)
		}
		if _, err := s.Compare(ctx); tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), LoginNoPassword) {
			t.Fatalf("the CPU asks credentials after A: compare without the password: %v", err)
		}
		// Attested disabled now: the CPU asks credentials and tiad logs
		// on, which contradicts the attestation: no reset, in doubt after
		// stop-cpu, nothing loaded.
		_, err = s.Download(ctx, load(tiad.AttestCPUAccessDisabled, BenchDownloadSecrets()))
		if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "so its access control is on, but the attestation route rests on it being off") ||
			slices.Contains(tiad.ActingAnswers(err), tiad.QuestionUserManagement) {
			t.Fatalf("attested disabled on a CPU that asks credentials: %v (acting %v)", err, tiad.ActingAnswers(err))
		}
		// B.
		res, err = s.Download(ctx, load(tiad.AttestCPUAccessEnabled, BenchDownloadSecrets()))
		if err != nil || !res.Downloaded || !res.CompareEqual || res.UserManagementReset != tiad.ResetRouteLogon {
			t.Fatalf("a reload with access control on: %+v %v", res, err)
		}
		if list, err := c.Deploys(ctx, benchCPU); err != nil || len(list.Deploys) == 0 || list.Deploys[0].Outcome == nil || list.Deploys[0].Outcome.UserManagementReset != tiad.ResetRouteLogon {
			t.Fatalf("the deploy record carries the logon route: %+v %v", list, err)
		}
		if l := a.Loaded(t); len(l) != 2 {
			t.Fatalf("loaded %d, want A and B", len(l))
		}
		closeFirst(t, s)

		// C. Refused before TIA: no password; a blind trust.
		r := restoreSession(t, a)
		a.SetQuestions(t, benchHardwareChecks)
		restore := ResetLoad(benchCPU)
		restore.Attestation.CPUAccessControl = tiad.AttestCPUAccessEnabled
		noPw := restore
		noPw.Secrets = nil
		_, err = r.Download(ctx, noPw)
		wantResetRefused(t, "the restore without a password", err, tiad.ResetNoPassword)
		a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
		if fp, err := r.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true}); err != nil || len(fp.TrustedBlindly) != 1 {
			t.Fatalf("a read that trusts blindly: %+v %v", fp, err)
		}
		a.SetFingerprint(t, FingerprintScript{})
		if _, err := r.Download(ctx, restore); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "has to log on to the CPU first, so nothing was sent to the CPU") {
			t.Fatalf("the restore after a blind trust: %v", err)
		}
		// Closing every session retires the instance.
		closeFirst(t, r)
		r = restoreSession(t, a)
		a.SetQuestions(t, benchHardwareChecks)
		// A wrong password: refused going online, in doubt, no reset
		// given, nothing loaded, and the deploy record says no reset.
		wrong := restore
		wrong.Secrets = &tiad.Secrets{FullAccessPassword: "Wrong-access-2026"}
		_, err = r.Download(ctx, wrong)
		if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), LoginAskedAgain) || slices.Contains(tiad.ActingAnswers(err), tiad.QuestionUserManagement) {
			t.Fatalf("the restore with a wrong password: %v (acting %v)", err, tiad.ActingAnswers(err))
		}
		if l := a.Loaded(t); len(l) != 2 {
			t.Fatalf("a refused logon loaded: %d loads", len(l))
		}
		list, err := c.Deploys(ctx, benchCPU)
		if err != nil || len(list.Deploys) == 0 || list.Deploys[0].Outcome == nil || list.Deploys[0].Outcome.UserManagementReset != "" {
			t.Fatalf("the refused restore's deploy record: %+v %v", list, err)
		}
		// The restore: logged on as "nautilus", reset to a project with
		// access control off and no users; the CPU then asks nothing.
		res, err = r.Download(ctx, restore)
		if err != nil || !res.Downloaded || !res.CompareEqual || res.UserManagementReset != tiad.ResetRouteLogon {
			t.Fatalf("the restore: %+v %v", res, err)
		}
		if cmp, err := r.Compare(ctx); err != nil || !cmp.Equal {
			t.Fatalf("a compare without a password after the restore: %+v %v", cmp, err)
		}
	})

	t.Run("ProjectNotDisabled", func(t *testing.T) {
		// An upload of a CPU whose access control is not Disabled (one
		// that compiles without a Full access user): neither route lets a
		// reset.
		a := start(t, Config{Targets: []string{benchCPU}})
		a.SetAccess(t, AccessScript{UploadAccessControl: "EnabledWithAccessControlViaAccessLevel"})
		s := UploadedKeep(t, a, benchCPU)
		a.SetQuestions(t, benchHardwareChecks)
		att := ResetLoad(benchCPU)
		att.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
		_, err := s.Download(ctx, att)
		wantResetRefused(t, "project access control on", err, `the project's access control is "EnabledWithAccessControlViaAccessLevel", not Disabled`)
		if l := a.Loaded(t); len(l) != 0 {
			t.Fatalf("a refused reset loaded %+v", l)
		}
	})

	t.Run("SessionSetAccessControl", func(t *testing.T) {
		// A session that once applied fullAccessUser (the bench spec, at
		// create), then uploaded the CPU (Disabled) and kept it: every
		// other conjunct holds, the attestation route included, yet keep
		// after fullAccessUser keeps what tiad set (#1159), so no reset.
		a := start(t, Config{Targets: []string{benchCPU}})
		c := a.Client()
		installGSD(t, c)
		s, err := c.CreateProject(ctx, "um", BenchStation(), BenchSecrets)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Upload(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApplyStation(ctx, adoptedStation(), BenchSecrets); err != nil {
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
		a.SetQuestions(t, benchHardwareChecks)
		att := ResetLoad(benchCPU)
		att.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
		_, err = s.Download(ctx, att)
		wantResetRefused(t, "the session set access control", err, "does not keep access control")
	})
}
