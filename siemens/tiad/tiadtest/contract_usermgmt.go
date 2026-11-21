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

// ResetLoad is a hardware load at address that answers the bench's
// checks (and the device an upload keeps) and asks a user-management
// reset.
func ResetLoad(address string) tiad.DownloadRequest {
	return AcceptHardware(tiad.DownloadRequest{Address: address, Hardware: true, Software: true, Announced: true,
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
		on := ResetLoad(benchCPU)
		on.Attestation.CPUAccessControl = tiad.AttestCPUAccessEnabled
		_, err = s.Download(ctx, on)
		wantResetRefused(t, "attested enabled", err, "never resets a CPU whose access control is on")
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
		// (Disabled): the reset goes, by the upload route, without an
		// attestation of the CPU's access control.
		res, err := s.Download(ctx, ResetLoad(benchCPU))
		if err != nil || !res.Downloaded || res.UserManagementReset != tiad.ResetRouteUpload || questionIDs(res.Questions) != "stop-cpu,overwrite,consistent-download,user-management" {
			t.Fatalf("reset by the upload route: %+v %v", res, err)
		}
		if res.Attestation == nil || res.Attestation.CPUAccessControl != "" {
			t.Fatalf("the attestation said nothing of access control: %+v", res.Attestation)
		}
		// A reset TIA never asks is no reset.
		a.SetQuestions(t, []tiad.Question{qStop})
		if res, err := s.Download(ctx, ResetLoad(benchCPU)); err != nil || res.UserManagementReset != "" {
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
		a.SetQuestions(t, later)
		_, err := s.Download(ctx, keep)
		if tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != tiad.QuestionOverwrite || len(tiad.ActingAnswers(err)) != 0 {
			t.Fatalf("keep, then overwrite unanswered: %v (acting %v), want a clean decision_required", err, tiad.ActingAnswers(err))
		}
		reset := ResetLoad(benchCPU)
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
		wantResetRefused(t, "uploaded from another address, no attestation", err, `nothing shows the CPU's access control is off: upload from `+otherCPU+` in this session first, or give attestation.cpuAccessControl "disabled"`)
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

	t.Run("SpecSetsAccessControl", func(t *testing.T) {
		// The bench spec sets access control (fullAccessUser): no reset,
		// whatever the attestation says.
		a, s := compiledBench(t, start)
		a.SetQuestions(t, benchHardwareChecks)
		att := ResetLoad(benchCPU)
		att.Decisions.KeptDevices = nil
		att.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
		_, err := s.Download(ctx, att)
		wantResetRefused(t, "spec sets access control", err, "does not keep access control")
	})
}
