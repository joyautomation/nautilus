package tiad

import (
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
		{"stop yes", []Question{stop}, Decisions{StopCPU: Yes()}, false, false, "", ""},
		{"stop no", []Question{stop}, Decisions{StopCPU: No()}, false, false, "", "stop-cpu"},
		{"start-after no is stay in STOP", []Question{{ID: QuestionStartAfter}}, Decisions{StartAfter: No()}, false, false, "", ""},
		{"start-after unanswered", []Question{{ID: QuestionStartAfter}}, Decisions{}, false, false, "start-after", ""},
		{"reinit all agreed, any case", []Question{reinit}, Decisions{Reinitialize: []string{"tags", "PROG_DB"}}, false, false, "", ""},
		{"reinit partly agreed", []Question{reinit}, Decisions{Reinitialize: []string{"Tags"}}, false, false, "reinitialize", ""},
		{"reinit unanswered", []Question{reinit}, Decisions{}, false, false, "reinitialize", ""},
		{"reinit naming nothing is never answered", []Question{{ID: QuestionReinitialize}}, Decisions{Reinitialize: []string{"Tags"}}, false, false, "reinitialize", ""},
		{"cert matches", []Question{cert}, Decisions{TrustCertificate: "ab12"}, false, false, "", ""},
		{"cert differs", []Question{cert}, Decisions{TrustCertificate: "FFFF"}, false, false, "", "certificate"},
		{"password from secret", []Question{pw}, Decisions{}, true, false, "", ""},
		{"password missing", []Question{pw}, Decisions{}, false, false, "password", ""},
		{"configuration password from its secret", []Question{cfg}, Decisions{}, false, true, "", ""},
		{"configuration password missing", []Question{cfg}, Decisions{}, false, false, "configuration-password", ""},
		{"protection password never answers configuration-password", []Question{cfg}, Decisions{}, true, false, "configuration-password", ""},
		{"configuration password never answers password", []Question{pw}, Decisions{}, false, true, "password", ""},
		{"other never answered", []Question{other}, Decisions{StopCPU: Yes(), Overwrite: Yes(), StartAfter: Yes()}, true, false, "other", ""},
		{"unknown id is other", []Question{{ID: "new-in-v22"}}, Decisions{StopCPU: Yes()}, true, false, "new-in-v22", ""},
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
	all := Decisions{StopCPU: Yes(), Overwrite: Yes(), ConsistentDownload: Yes(), StartAfter: Yes()}
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

func TestResolveNamesTheMissingDB(t *testing.T) {
	q := Question{ID: QuestionReinitialize, Message: "Reinitialise", Objects: []string{"Tags", "Prog_DB"}}
	u, _ := Resolve([]Question{q}, Decisions{Reinitialize: []string{"Tags"}}, false, false, false, ResetGate{})
	if len(u) != 1 || !strings.Contains(u[0].Message, "not agreed: Prog_DB") {
		t.Fatalf("got %+v", u)
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
