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
			u, d := Resolve(c.qs, c.d, c.protection, c.configPassword)
			if ids(u) != c.unanswered || ids(d) != c.declined {
				t.Fatalf("Resolve = unanswered %q declined %q, want %q %q", ids(u), ids(d), c.unanswered, c.declined)
			}
		})
	}
}

func TestResolveNamesTheMissingDB(t *testing.T) {
	q := Question{ID: QuestionReinitialize, Message: "Reinitialise", Objects: []string{"Tags", "Prog_DB"}}
	u, _ := Resolve([]Question{q}, Decisions{Reinitialize: []string{"Tags"}}, false, false)
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
