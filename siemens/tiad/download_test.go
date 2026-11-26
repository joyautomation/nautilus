package tiad

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func ids(qs []Question) string {
	var s []string
	for _, q := range qs {
		s = append(s, q.ID)
	}
	return strings.Join(s, ",")
}

func TestResolve(t *testing.T) {
	stop := Question{ID: QuestionStopCPU, Message: "The CPU must stop"}
	reinit := Question{ID: QuestionReinitialize, Message: "Reinitialise", Objects: []string{"Tags", "Prog_DB"}}
	cert := Question{ID: QuestionCertificate, Detail: "AB12"}
	pw := Question{ID: QuestionPassword}
	cfg := Question{ID: QuestionConfigurationPassword}
	other := Question{ID: QuestionOther, Message: "Something new"}

	cases := []struct {
		name                 string
		qs                   []Question
		d                    Decisions
		protection           bool
		configPassword       bool
		unanswered, declined string
	}{
		{"nothing asked", nil, Decisions{}, false, false, "", ""},
		{"stop unanswered", []Question{stop}, Decisions{}, false, false, "stop-cpu", ""},
		{"stop yes", []Question{stop}, Decisions{StopCPU: Yes(), ResetData: Yes()}, false, false, "", ""},
		{"stop no", []Question{stop}, Decisions{StopCPU: No()}, false, false, "", "stop-cpu"},
		{"start-after no is stay in STOP", []Question{{ID: QuestionStartAfter}}, Decisions{StartAfter: No()}, false, false, "", ""},
		{"start-after unanswered", []Question{{ID: QuestionStartAfter}}, Decisions{}, false, false, "start-after", ""},
		// Only the consent to the reset answers TIA's reinitialize check,
		// which names no data block (#1341).
		{"reinit with the consent and the stop", []Question{reinit}, Decisions{ResetData: Yes(), StopCPU: Yes()}, false, false, "", ""},
		{"reinit naming nothing, with the consent and the stop", []Question{{ID: QuestionReinitialize}}, Decisions{ResetData: Yes(), StopCPU: Yes()}, false, false, "", ""},
		{"reinit with the consent, no stop (its yes stops the CPU)", []Question{reinit}, Decisions{ResetData: Yes()}, false, false, "reinitialize", ""},
		{"reinit without the consent", []Question{reinit}, Decisions{}, false, false, "reinitialize", ""},
		{"reinit, consent no", []Question{reinit}, Decisions{ResetData: No()}, false, false, "reinitialize", ""},
		{"cert matches", []Question{cert}, Decisions{TrustCertificate: "ab12"}, false, false, "", ""},
		{"cert differs", []Question{cert}, Decisions{TrustCertificate: "FFFF"}, false, false, "", "certificate"},
		{"password from secret", []Question{pw}, Decisions{}, true, false, "", ""},
		{"password missing", []Question{pw}, Decisions{}, false, false, "password", ""},
		{"configuration password from its secret", []Question{cfg}, Decisions{}, false, true, "", ""},
		{"configuration password missing", []Question{cfg}, Decisions{}, false, false, "configuration-password", ""},
		{"protection password never answers configuration-password", []Question{cfg}, Decisions{}, true, false, "configuration-password", ""},
		{"configuration password never answers password", []Question{pw}, Decisions{}, false, true, "password", ""},
		{"other never answered", []Question{other}, Decisions{StopCPU: Yes(), ResetData: Yes(), Overwrite: Yes(), StartAfter: Yes()}, true, false, "other", ""},
		{"unknown id is other", []Question{{ID: "new-in-v22"}}, Decisions{StopCPU: Yes(), ResetData: Yes()}, true, false, "new-in-v22", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, d := Resolve(c.qs, c.d, c.protection, c.configPassword, false, ResetGate{})
			if ids(u) != c.unanswered || ids(d) != c.declined {
				t.Fatalf("Resolve = unanswered %q declined %q, want %q %q", ids(u), ids(d), c.unanswered, c.declined)
			}
		})
	}
}

// TestResolveSoftwareLoadFamily: #789. Each check of the software-load
// family is answered only by its own decision, never by another's, and
// user-management only when the spec keeps access control.
func TestResolveSoftwareLoadFamily(t *testing.T) {
	all := Decisions{StopCPU: Yes(), ResetData: Yes(), Overwrite: Yes(), ConsistentDownload: Yes(), StartAfter: Yes()}
	cases := []struct {
		name                 string
		q                    string
		d                    Decisions
		kept                 bool
		unanswered, declined string
	}{
		{"alarm texts not answered by the others", QuestionAlarmTexts, all, true, "alarm-texts", ""},
		{"alarm texts yes", QuestionAlarmTexts, Decisions{AlarmTexts: Yes()}, false, "", ""},
		{"alarm texts no", QuestionAlarmTexts, Decisions{AlarmTexts: No()}, false, "", "alarm-texts"},
		{"all blocks not answered by the others", QuestionAllBlocks, all, true, "all-blocks", ""},
		{"all blocks yes", QuestionAllBlocks, Decisions{AllBlocks: Yes()}, false, "", ""},
		{"all blocks no", QuestionAllBlocks, Decisions{AllBlocks: No()}, false, "", "all-blocks"},
		{"identification data not answered by the others", QuestionIdentificationData, all, true, "identification-data", ""},
		{"identification data yes", QuestionIdentificationData, Decisions{IdentificationDataLoadNothing: Yes()}, false, "", ""},
		{"identification data no", QuestionIdentificationData, Decisions{IdentificationDataLoadNothing: No()}, false, "", "identification-data"},
		{"user management not answered by the others", QuestionUserManagement, all, true, "user-management", ""},
		{"user management keep, access control kept", QuestionUserManagement, Decisions{UserManagementKeepOnline: Yes()}, true, "", ""},
		{"user management keep, access control set by the spec", QuestionUserManagement, Decisions{UserManagementKeepOnline: Yes()}, false, "user-management", ""},
		{"user management no", QuestionUserManagement, Decisions{UserManagementKeepOnline: No()}, true, "", "user-management"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, d := Resolve([]Question{{ID: c.q}}, c.d, true, true, c.kept, ResetGate{})
			if ids(u) != c.unanswered || ids(d) != c.declined {
				t.Fatalf("Resolve = unanswered %q declined %q, want %q %q", ids(u), ids(d), c.unanswered, c.declined)
			}
		})
	}
	u, _ := Resolve([]Question{{ID: QuestionUserManagement}}, Decisions{UserManagementKeepOnline: Yes()}, false, false, false, ResetGate{})
	if len(u) != 1 || !strings.Contains(u[0].Message, "a keep spec on a session that never set access control") {
		t.Fatalf("a refused keep must name why: %+v", u)
	}
	for _, id := range []string{QuestionAlarmTexts, QuestionAllBlocks} {
		if !Acting(id) {
			t.Errorf("%s must be acting", id)
		}
	}
	for _, id := range []string{QuestionUserManagement, QuestionIdentificationData} {
		if Acting(id) {
			t.Errorf("%s must be passive", id)
		}
	}
}

func TestDownloadRequestValidate(t *testing.T) {
	ok := DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	dry := DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true}
	if err := dry.Validate(); err != nil {
		t.Fatalf("an announced dry run: %v", err)
	}
	// Until S2 proves a cancel loads nothing, a dry run reaches the CPU
	// and is announced like a download.
	if err := (DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true}).Validate(); err == nil || !strings.Contains(err.Error(), "announce it first") {
		t.Fatalf("an unannounced dry run: %v", err)
	}
	for name, r := range map[string]DownloadRequest{
		"not announced": {Address: "192.0.2.10", Software: true},
		"nothing":       {Address: "192.0.2.10", Announced: true},
		"bad address":   {Address: "plc", Software: true, Announced: true},
	} {
		if r.Validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// TestCheckKept is the agent's KeptRule: a download's yes names exactly
// the kept devices; no kept devices, no question.
func TestCheckKept(t *testing.T) {
	io := func(names ...string) []KeptDevice {
		var ks []KeptDevice
		for _, n := range names {
			ks = append(ks, KeptDevice{Name: n})
		}
		return ks
	}
	for _, c := range []struct {
		name   string
		kept   []KeptDevice
		answer []string
		want   string
	}{
		{"nothing kept", nil, nil, ""},
		{"named", io("IO1"), []string{"IO1"}, ""},
		{"any order", io("IO2", "IO1"), []string{"IO1", "IO2"}, ""},
		{"twice is once", io("IO2", "IO1"), []string{"IO2", "IO1", "IO1"}, ""},
		{"unanswered", io("IO2", "IO1"), nil, `the project keeps device(s) ["IO1", "IO2"] that a download loads too: name them in decisions.keptDevices`},
		{"a smaller set", io("IO1", "IO2"), []string{"IO1"}, `decisions.keptDevices names ["IO1"], but the project keeps ["IO1", "IO2"] now`},
		{"a larger set", io("IO1"), []string{"IO1", "IO2"}, `but the project keeps ["IO1"] now`},
		{"nothing kept, a yes", nil, []string{"IO1"}, "but the project keeps none now"},
		{"exact names", io("IO1"), []string{"io1"}, "a yes for another set does not carry over"},
	} {
		got := CheckKept(c.kept, c.answer)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if q := KeptQuestion(io("IO2", "IO1")); q.ID != QuestionKeptDevices || strings.Join(q.Objects, ",") != "IO1,IO2" {
		t.Errorf("question: %+v", q)
	}
}

// resetRequest is a request every conjunct of CheckUserManagementReset
// lets by the upload route.
func resetRequest() DownloadRequest {
	return DownloadRequest{Address: "192.0.2.10", Hardware: true, Software: true, Announced: true,
		Decisions:   Decisions{UserManagementResetToProject: Yes()},
		Attestation: &Attestation{By: "op", Text: "as found"}}
}

// TestCheckUserManagementReset: #789 stage 4. From a request every
// conjunct lets, each one flipped on its own refuses (a security
// property: each is mutation-checked); the agent's UserManagementRule
// has the same table.
func TestCheckUserManagementReset(t *testing.T) {
	const addr = "192.0.2.10"
	project, readable, reads := ProjectAccessDisabled, true, 0
	read := func() (string, bool) { reads++; return project, readable }
	if g := CheckUserManagementReset(resetRequest(), true, addr, true, read); !g.Allowed || g.Route != ResetRouteUpload || reads != 1 {
		t.Fatalf("the baseline: %+v (reads %d)", g, reads)
	}
	refuse := func(name string, g ResetGate, says string) {
		t.Helper()
		if g.Allowed || !strings.Contains(g.Why, says) {
			t.Errorf("%s: %+v, want refused saying %q", name, g, says)
		}
	}
	r := resetRequest()
	r.Decisions.UserManagementResetToProject = nil
	refuse("not asked", CheckUserManagementReset(r, true, addr, true, read), "not asked")
	r.Decisions.UserManagementResetToProject = No()
	refuse("asked no", CheckUserManagementReset(r, true, addr, true, read), "not asked")
	reads = 0
	r = resetRequest()
	r.Hardware = false
	refuse("software only", CheckUserManagementReset(r, true, addr, true, read), "loads no hardware")
	refuse("access control not kept", CheckUserManagementReset(resetRequest(), false, addr, true, read), "does not keep access control")
	if reads != 0 {
		t.Errorf("the cheap conjuncts must refuse before the project is read (%d reads)", reads)
	}
	r = resetRequest()
	r.Attestation.CPUAccessControl = AttestCPUAccessEnabled
	// An attestation that says enabled takes the logon route, the upload
	// notwithstanding: it needs the password to log on.
	refuse("attested enabled, no password", CheckUserManagementReset(r, true, addr, true, read), ResetNoPassword)
	r.Secrets = &Secrets{FullAccessPassword: "Bench-pass-2026"}
	if g := CheckUserManagementReset(r, true, addr, true, read); !g.Allowed || g.Route != ResetRouteLogon {
		t.Errorf("attested enabled with a password: %+v, want the logon route over the upload's", g)
	}
	for _, st := range []string{"EnabledWithAccessControl", "", "disabled", "Disabled ", "EnabledWithAccessControlViaAccessLevel"} {
		project = st
		refuse("project "+strconv.Quote(st), CheckUserManagementReset(resetRequest(), true, addr, true, read), "not Disabled")
	}
	project, readable = ProjectAccessDisabled, false
	refuse("project unreadable", CheckUserManagementReset(resetRequest(), true, addr, true, read), "unreadable, not Disabled")
	readable = true
	refuse("no reader", CheckUserManagementReset(resetRequest(), true, addr, true, nil), "unreadable")
	for _, up := range []string{"", "192.0.2.11"} {
		refuse("uploaded from "+strconv.Quote(up), CheckUserManagementReset(resetRequest(), true, up, true, read), "upload from 192.0.2.10 in this session first")
		r := resetRequest()
		r.Attestation.CPUAccessControl = AttestCPUAccessDisabled
		if g := CheckUserManagementReset(r, true, up, true, read); !g.Allowed || g.Route != ResetRouteAttestation {
			t.Errorf("attested disabled, uploaded from %q: %+v", up, g)
		}
		r.Attestation.By = " "
		refuse("attested without who", CheckUserManagementReset(r, true, up, true, read), "nothing shows")
		r.Attestation = &Attestation{By: "op", CPUAccessControl: AttestCPUAccessDisabled}
		refuse("attested without what", CheckUserManagementReset(r, true, up, true, read), "nothing shows")
		r.Attestation = nil
		refuse("no attestation", CheckUserManagementReset(r, true, up, true, read), "nothing shows")
	}
	if (ResetGate{}).Allowed {
		t.Fatal("the zero gate must refuse")
	}
	// With the upload route off (UploadRouteEnabled), an upload from this
	// very CPU shows nothing: only the attestation lets a reset, and the
	// refusal says the upload route is not yet available.
	refuse("upload route off", CheckUserManagementReset(resetRequest(), true, addr, false, read), "the upload route is not yet available")
	refuse("upload route off, software only", func() ResetGate {
		r := resetRequest()
		r.Hardware = false
		return CheckUserManagementReset(r, true, addr, false, read)
	}(), "the upload route is not yet available")
	r = resetRequest()
	r.Attestation.CPUAccessControl = AttestCPUAccessDisabled
	if g := CheckUserManagementReset(r, true, addr, false, read); !g.Allowed || g.Route != ResetRouteAttestation {
		t.Fatalf("upload route off, attested disabled: %+v", g)
	}
}

// TestResetFromTheAttestationRoute: the conjuncts flipped one at a time
// from the baseline production runs: the upload route off, the operator
// attesting disabled, no upload.
func TestResetFromTheAttestationRoute(t *testing.T) {
	read := func() (string, bool) { return ProjectAccessDisabled, true }
	base := func() DownloadRequest {
		r := resetRequest()
		r.Attestation.CPUAccessControl = AttestCPUAccessDisabled
		return r
	}
	if g := CheckUserManagementReset(base(), true, "", false, read); !g.Allowed || g.Route != ResetRouteAttestation {
		t.Fatalf("the baseline: %+v", g)
	}
	for name, g := range map[string]ResetGate{
		"software only": func() ResetGate {
			r := base()
			r.Hardware = false
			return CheckUserManagementReset(r, true, "", false, read)
		}(),
		"access control set": CheckUserManagementReset(base(), false, "", false, read),
		"project enabled":    CheckUserManagementReset(base(), true, "", false, func() (string, bool) { return "EnabledWithAccessControl", true }),
		"project unreadable": CheckUserManagementReset(base(), true, "", false, func() (string, bool) { return "", false }),
		"no who": func() ResetGate {
			r := base()
			r.Attestation.By = ""
			return CheckUserManagementReset(r, true, "", false, read)
		}(),
		"no what": func() ResetGate {
			r := base()
			r.Attestation.Text = " "
			return CheckUserManagementReset(r, true, "", false, read)
		}(),
		"attested enabled": func() ResetGate {
			r := base()
			r.Attestation.CPUAccessControl = AttestCPUAccessEnabled
			return CheckUserManagementReset(r, true, "", false, read)
		}(),
		"attested nothing": func() ResetGate {
			r := base()
			r.Attestation.CPUAccessControl = ""
			return CheckUserManagementReset(r, true, "192.0.2.10", false, read)
		}(),
		"reset not asked": func() ResetGate {
			r := base()
			r.Decisions.UserManagementResetToProject = nil
			return CheckUserManagementReset(r, true, "", false, read)
		}(),
	} {
		if g.Allowed {
			t.Errorf("%s: let a reset", name)
		}
	}
}

// resetFacts are a session's facts for CheckReset: keep (spec keep, never
// set) or tiad's user (fullAccessUser set on the session), with the
// project's access control and whether its record is of the request's
// password, each read counted.
type resetFacts struct {
	project            string
	projectOK          bool
	recorded, recordOK bool
	projectReads       int
	recordReads        int
}

func (f *resetFacts) make(keep, set bool) ResetFacts {
	return ResetFacts{AccessControlKept: keep, FullAccessUserSet: set,
		ProjectAccess:    func() (string, bool) { f.projectReads++; return f.project, f.projectOK },
		PasswordRecorded: func() (bool, bool) { f.recordReads++; return f.recorded, f.recordOK }}
}

func newFacts(keep bool) *resetFacts {
	p := ProjectAccessDisabled
	if !keep {
		p = ProjectAccessEnabled
	}
	return &resetFacts{project: p, projectOK: true, recorded: true, recordOK: true}
}

func attested(cpu, password string) DownloadRequest {
	r := resetRequest()
	r.Attestation.CPUAccessControl = cpu
	r.Secrets = &Secrets{FullAccessPassword: password}
	return r
}

// TestResetMatrix: the closed set of resets CheckReset lets (#789 option
// 1), each by its own route, and each conjunct flipped on its own
// refuses; the agent's UserManagementResetMatrix has the same table.
func TestResetMatrix(t *testing.T) {
	const pw = "Bench-pass-2026"
	for _, keep := range []bool{true, false} {
		for _, cpu := range []string{AttestCPUAccessDisabled, AttestCPUAccessEnabled} {
			name := fmt.Sprintf("keep %v, attested %s", keep, cpu)
			f := newFacts(keep)
			g := CheckReset(attested(cpu, pw), f.make(keep, !keep))
			want := ResetRouteAttestation
			if cpu == AttestCPUAccessEnabled {
				want = ResetRouteLogon
			}
			wantRecordReads := 1
			if keep {
				wantRecordReads = 0
			}
			if !g.Allowed || g.Route != want || f.projectReads != 1 || f.recordReads != wantRecordReads {
				t.Errorf("%s: %+v (reads %d/%d), want route %s", name, g, f.projectReads, f.recordReads, want)
			}
			other := newFacts(!keep)
			if g := CheckReset(attested(cpu, pw), other.make(keep, !keep)); g.Allowed || !strings.Contains(g.Why, "not ") {
				t.Errorf("%s: project %s: %+v", name, other.project, g)
			}
			for _, st := range []string{"EnabledWithAccessControlViaAccessLevel", "", "enabledwithaccesscontrol"} {
				odd := newFacts(keep)
				odd.project = st
				if g := CheckReset(attested(cpu, pw), odd.make(keep, !keep)); g.Allowed {
					t.Errorf("%s: project %q let a reset", name, st)
				}
			}
			unread := newFacts(keep)
			unread.projectOK = false
			if g := CheckReset(attested(cpu, pw), unread.make(keep, !keep)); g.Allowed {
				t.Errorf("%s: project unreadable let a reset", name)
			}
			for what, r := range map[string]DownloadRequest{
				"no who":        func() DownloadRequest { r := attested(cpu, pw); r.Attestation.By = ""; return r }(),
				"no what":       func() DownloadRequest { r := attested(cpu, pw); r.Attestation.Text = " "; return r }(),
				"software only": func() DownloadRequest { r := attested(cpu, pw); r.Hardware = false; return r }(),
			} {
				if g := CheckReset(r, newFacts(keep).make(keep, !keep)); g.Allowed {
					t.Errorf("%s: %s let a reset", name, what)
				}
			}
		}
	}
	n := newFacts(false)
	if g := CheckReset(attested(AttestCPUAccessEnabled, pw), n.make(false, false)); g.Allowed || !strings.Contains(g.Why, "does not keep access control as found") || n.projectReads+n.recordReads != 0 {
		t.Errorf("neither keep nor tiad's user: %+v, want refused before any read", g)
	}
	for _, keep := range []bool{true, false} {
		if g := CheckReset(attested("", pw), newFacts(keep).make(keep, !keep)); g.Allowed || !strings.Contains(g.Why, `give attestation.cpuAccessControl "disabled" or "enabled"`) {
			t.Errorf("keep %v, nothing attested: %+v", keep, g)
		}
		f := newFacts(keep)
		if g := CheckReset(attested(AttestCPUAccessEnabled, ""), f.make(keep, !keep)); g.Why != resetRefused+ResetNoPassword || f.projectReads != 0 {
			t.Errorf("keep %v, the logon route without a password: %+v (reads %d)", keep, g, f.projectReads)
		}
		r := attested(AttestCPUAccessEnabled, pw)
		r.Secrets = nil
		if g := CheckReset(r, newFacts(keep).make(keep, !keep)); g.Why != resetRefused+ResetNoPassword {
			t.Errorf("keep %v, the logon route without secrets: %+v", keep, g)
		}
	}
	for _, cpu := range []string{AttestCPUAccessDisabled, AttestCPUAccessEnabled} {
		f := newFacts(false)
		f.recorded = false
		if g := CheckReset(attested(cpu, pw), f.make(false, true)); g.Why != resetRefused+ResetPasswordNotRecorded {
			t.Errorf("attested %s, another password: %+v", cpu, g)
		}
		f = newFacts(false)
		f.recordOK = false
		if g := CheckReset(attested(cpu, pw), f.make(false, true)); g.Allowed || !strings.Contains(g.Why, "could not read the project's record") {
			t.Errorf("attested %s, record unreadable: %+v", cpu, g)
		}
		facts := newFacts(false).make(false, true)
		facts.PasswordRecorded = nil
		if g := CheckReset(attested(cpu, pw), facts); g.Allowed {
			t.Errorf("attested %s, no record reader: %+v", cpu, g)
		}
	}
	// A session that applied fullAccessRecordOnly: refused for tiad's
	// user before any read; keep is not concerned.
	for _, cpu := range []string{AttestCPUAccessDisabled, AttestCPUAccessEnabled} {
		ro := newFacts(false)
		rf := ro.make(false, true)
		rf.RecordOnly = true
		if g := CheckReset(attested(cpu, pw), rf); g.Why != resetRefused+ResetRecordOnly || ro.projectReads+ro.recordReads != 0 {
			t.Errorf("attested %s, record only: %+v (reads %d)", cpu, g, ro.projectReads+ro.recordReads)
		}
		kf := newFacts(true).make(true, false)
		kf.RecordOnly = true
		if g := CheckReset(attested(cpu, pw), kf); !g.Allowed {
			t.Errorf("attested %s, keep, record only elsewhere: %+v", cpu, g)
		}
	}
	up := newFacts(false).make(false, true)
	up.UploadRoute, up.UploadedFrom = true, "192.0.2.10"
	if g := CheckReset(attested("", pw), up); g.Allowed {
		t.Errorf("the upload route let a reset to tiad's user: %+v", g)
	}
}

// TestResetAt: the verdict when TIA asks. The logon route needs the
// download logged on; the others a CPU that asked no credentials; a
// refused gate stays refused.
func TestResetAt(t *testing.T) {
	logon := ResetGate{Allowed: true, Route: ResetRouteLogon}
	for _, inst := range []bool{false, true} {
		if g := logon.At(true, true, inst, false); g != logon {
			t.Errorf("logon, logged on (instance %v): %+v", inst, g)
		}
	}
	for _, asked := range []bool{false, true} {
		if g := logon.At(false, asked, false, false); g.Allowed || g.Why != resetRefused+ResetNoLogon {
			t.Errorf("logon, not logged on (asked %v): %+v", asked, g)
		}
	}
	// The instance-logon route: the instance's evidence stands in for the
	// download's logon only when the download asked no credentials.
	if g := logon.At(false, false, true, false); !g.Allowed || g.Route != ResetRouteInstanceLogon {
		t.Errorf("logon, nothing asked, instance evidence: %+v", g)
	}
	if g := logon.At(false, true, true, false); g.Allowed || g.Why != resetRefused+ResetNoLogon {
		t.Errorf("logon, asked and not logged on, instance evidence: %+v", g)
	}
	if strings.Contains(ResetNoLogon, "close every session") || !strings.Contains(ResetNoLogon, "first, then download again") {
		t.Errorf("ResetNoLogon names a fix that does nothing on an instance with no blind trust, or not the compare: %s", ResetNoLogon)
	}
	for _, route := range []string{ResetRouteAttestation, ResetRouteUpload} {
		off := ResetGate{Allowed: true, Route: route}
		for _, inst := range []bool{false, true} {
			if g := off.At(false, false, inst, false); g != off {
				t.Errorf("%s, no credentials asked (instance %v): %+v", route, inst, g)
			}
			// Asked and answered, or asked and refused (TIA may go on
			// after a no): either shows the CPU's access control on.
			for _, loggedOn := range []bool{true, false} {
				if g := off.At(loggedOn, true, inst, false); g.Allowed || !strings.Contains(g.Why, "so its access control is on, but the "+route+" route rests on it being off") {
					t.Errorf("%s, the CPU asked credentials (logged on %v, instance %v): %+v", route, loggedOn, inst, g)
				}
			}
		}
	}
	// A logon earlier in the instance refuses the off routes: TIA may
	// ride on it and ask nothing.
	for _, route := range []string{ResetRouteAttestation, ResetRouteUpload} {
		off := ResetGate{Allowed: true, Route: route}
		if g := off.At(false, false, false, true); g.Allowed || g.Why != resetRefused+ResetLoggedOnBefore(route) {
			t.Errorf("%s, logged on earlier in the instance: %+v", route, g)
		}
	}
	if g := logon.At(false, false, true, true); g.Route != ResetRouteInstanceLogon || logon.At(true, true, false, true) != logon {
		t.Errorf("the logon routes do not care about an earlier logon: %+v", g)
	}
	for _, g := range []ResetGate{{Why: "no"}, {Route: ResetRouteLogon}} {
		if g.At(true, true, true, false).Allowed || g.At(false, false, true, false).Allowed {
			t.Errorf("a refused gate let: %+v", g)
		}
	}
}

// TestUploadRouteOff pins the upload route off (naut-lead 2026-11-21):
// turn it on only with a bench upload of a CPU with access control on
// that reads back as not Disabled, and the agent's flag with it.
func TestUploadRouteOff(t *testing.T) {
	if UploadRouteEnabled {
		t.Fatal("UploadRouteEnabled is on: it needs a bench upload of a CPU with access control on that reads back as not Disabled (siemens-ide.md §3.5)")
	}
}

// TestResolveKeepOrReset: the keep x reset table, with the rule's verdict
// let and refused (the zero gate).
func TestResolveKeepOrReset(t *testing.T) {
	let := ResetGate{Allowed: true, Route: ResetRouteUpload}
	refused := ResetGate{Why: "refused because"}
	vals := []*bool{nil, No(), Yes()}
	show := func(b *bool) string {
		if b == nil {
			return "nil"
		}
		return strconv.FormatBool(*b)
	}
	for _, keep := range vals {
		for _, reset := range vals {
			for _, gate := range []ResetGate{let, refused, {}} {
				d := Decisions{UserManagementKeepOnline: keep, UserManagementResetToProject: reset}
				u, dd := Resolve([]Question{{ID: QuestionUserManagement, Message: "m"}}, d, false, false, true, gate)
				var want string
				switch {
				case isYes(keep) && isYes(reset):
					want = "unanswered"
				case isYes(reset):
					want = map[bool]string{true: "answered", false: "unanswered"}[gate.Allowed]
				case isYes(keep):
					want = "answered"
				case keep != nil || reset != nil:
					want = "declined"
				default:
					want = "unanswered"
				}
				got := "answered"
				if len(u) > 0 {
					got = "unanswered"
				} else if len(dd) > 0 {
					got = "declined"
				}
				what := fmt.Sprintf("keep=%s reset=%s gate=%+v", show(keep), show(reset), gate)
				if got != want {
					t.Errorf("%s: %s, want %s", what, got, want)
				}
				if got == "unanswered" && isYes(reset) && !isYes(keep) && gate.Why != "" && !strings.Contains(u[0].Message, gate.Why) {
					t.Errorf("%s: %q must name the rule's why", what, u[0].Message)
				}
				if got == "answered" {
					want := AnswerKeep
					if isYes(reset) {
						want = AnswerReset
					}
					if c := UserManagementChoice(d); c != want {
						t.Errorf("%s: choice %q, want %q", what, c, want)
					}
				}
			}
		}
	}
	bad := DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: Decisions{UserManagementKeepOnline: Yes(), UserManagementResetToProject: Yes()}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "both yes") {
		t.Fatalf("keep and reset both yes: %v", err)
	}
	bad.Decisions.UserManagementKeepOnline = No()
	if err := bad.Validate(); err != nil {
		t.Fatalf("keep no, reset yes: %v", err)
	}
	for _, ok := range []string{"", AttestCPUAccessDisabled, AttestCPUAccessEnabled} {
		r := DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Attestation: &Attestation{CPUAccessControl: ok}}
		if err := r.Validate(); err != nil {
			t.Errorf("cpuAccessControl %q: %v", ok, err)
		}
	}
	for _, no := range []string{"Disabled", "off", "disabled "} {
		r := DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Attestation: &Attestation{CPUAccessControl: no}}
		if err := r.Validate(); err == nil || err.Error() != "download: attestation.cpuAccessControl is not one of disabled, enabled" {
			t.Errorf("cpuAccessControl %q: %v", no, err)
		}
	}
}

// TestActingAnswer: acting by what the answer writes.
func TestActingAnswer(t *testing.T) {
	for _, c := range []struct {
		id     string
		yes    bool
		choice string
		want   bool
	}{
		{QuestionUserManagement, true, AnswerKeep, false},
		{QuestionUserManagement, true, AnswerReset, true},
		{QuestionUserManagement, true, "", false},
		{QuestionStartAfter, false, "", false},
		{QuestionStartAfter, true, "", true},
		{QuestionStopCPU, true, "", true},
		{QuestionPassword, true, "", false},
		{"new-in-v22", true, "", true},
	} {
		if got := ActingAnswer(c.id, c.yes, c.choice); got != c.want {
			t.Errorf("ActingAnswer(%s, %v, %q) = %v, want %v", c.id, c.yes, c.choice, got, c.want)
		}
	}
}

// TestCompileMessageBlock reads the block and body line from a message as
// TIA V21 gave it (#789), and refuses what is not a block.
func TestCompileMessageBlock(t *testing.T) {
	path := []string{"PLC_1", "Program blocks", "nautilus", "naut_Main (FB1)"}
	for _, c := range []struct {
		m    CompileMessage
		name string
		line int
		ok   bool
	}{
		{CompileMessage{Object: "4", Path: path}, "naut_Main", 4, true},
		{CompileMessage{Object: "", Path: path}, "naut_Main", 0, true},
		{CompileMessage{Object: "x", Path: path}, "naut_Main", 0, true},
		{CompileMessage{Object: "7", Path: []string{"PLC_1", "Program blocks", "naut OB (x) (OB30)"}}, "naut OB (x)", 7, true},
		{CompileMessage{Object: "naut_Tags (DB2)", Path: []string{"PLC_1", "Program blocks"}}, "", 0, false},
		{CompileMessage{Object: "3", Path: []string{"PLC_1", "naut_Main (FB1)", "Network (x)"}}, "naut_Main", 0, true},
		{CompileMessage{Object: "3", Path: []string{"PLC_1", "Program blocks"}}, "", 0, false},
		{CompileMessage{Object: "3", Path: []string{"Tags (FBX)"}}, "", 0, false},
		{CompileMessage{Object: "3"}, "", 0, false},
	} {
		name, line, ok := c.m.Block()
		if name != c.name || line != c.line || ok != c.ok {
			t.Errorf("%+v: got %q %d %v, want %q %d %v", c.m, name, line, ok, c.name, c.line, c.ok)
		}
	}
}

// TestCheckResetData (#1341): a stop needs the resetData consent; a
// download without it needs the layout check's digest, a dry run too;
// consent no is none, and a digest never stands in for it.
func TestCheckResetData(t *testing.T) {
	for _, c := range []struct {
		name string
		req  DownloadRequest
		want string
	}{
		{"consent", DownloadRequest{Decisions: Decisions{ResetData: Yes(), StopCPU: Yes()}}, ""},
		{"no consent, no digest", DownloadRequest{}, "a download without decisions.resetData needs layoutDigest"},
		{"consent no, no digest", DownloadRequest{Decisions: Decisions{ResetData: No()}}, "a download without decisions.resetData needs layoutDigest"},
		{"no consent, a digest", DownloadRequest{LayoutDigest: "d"}, ""},
		{"stop, no consent", DownloadRequest{Decisions: Decisions{StopCPU: Yes()}}, "decisions.stopCpu yes needs decisions.resetData yes"},
		{"stop, no consent, a digest", DownloadRequest{LayoutDigest: "d", Decisions: Decisions{StopCPU: Yes()}}, "decisions.stopCpu yes needs decisions.resetData yes"},
		{"dry run, stop, no consent", DownloadRequest{DryRun: true, Decisions: Decisions{StopCPU: Yes()}}, "decisions.stopCpu yes needs decisions.resetData yes"},
		{"dry run, no stop, no digest", DownloadRequest{DryRun: true, Decisions: Decisions{StopCPU: No()}}, "a download without decisions.resetData needs layoutDigest"},
		{"dry run, consent", DownloadRequest{DryRun: true, Decisions: Decisions{ResetData: Yes()}}, ""},
	} {
		got := CheckResetData(c.req)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
