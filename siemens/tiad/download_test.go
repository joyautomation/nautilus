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
	other := Question{ID: QuestionOther, Message: "Something new"}

	cases := []struct {
		name                 string
		qs                   []Question
		d                    Decisions
		protection           bool
		unanswered, declined string
	}{
		{"nothing asked", nil, Decisions{}, false, "", ""},
		{"stop unanswered", []Question{stop}, Decisions{}, false, "stop-cpu", ""},
		{"stop yes", []Question{stop}, Decisions{StopCPU: Yes()}, false, "", ""},
		{"stop no", []Question{stop}, Decisions{StopCPU: No()}, false, "", "stop-cpu"},
		{"start-after no is stay in STOP", []Question{{ID: QuestionStartAfter}}, Decisions{StartAfter: No()}, false, "", ""},
		{"start-after unanswered", []Question{{ID: QuestionStartAfter}}, Decisions{}, false, "start-after", ""},
		{"reinit all agreed, any case", []Question{reinit}, Decisions{Reinitialize: []string{"tags", "PROG_DB"}}, false, "", ""},
		{"reinit partly agreed", []Question{reinit}, Decisions{Reinitialize: []string{"Tags"}}, false, "reinitialize", ""},
		{"reinit unanswered", []Question{reinit}, Decisions{}, false, "reinitialize", ""},
		{"reinit naming nothing is never answered", []Question{{ID: QuestionReinitialize}}, Decisions{Reinitialize: []string{"Tags"}}, false, "reinitialize", ""},
		{"cert matches", []Question{cert}, Decisions{TrustCertificate: "ab12"}, false, "", ""},
		{"cert differs", []Question{cert}, Decisions{TrustCertificate: "FFFF"}, false, "", "certificate"},
		{"password from secret", []Question{pw}, Decisions{}, true, "", ""},
		{"password missing", []Question{pw}, Decisions{}, false, "password", ""},
		{"other never answered", []Question{other}, Decisions{StopCPU: Yes(), Overwrite: Yes(), StartAfter: Yes()}, true, "other", ""},
		{"unknown id is other", []Question{{ID: "new-in-v22"}}, Decisions{StopCPU: Yes()}, true, "new-in-v22", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, d := Resolve(c.qs, c.d, c.protection)
			if ids(u) != c.unanswered || ids(d) != c.declined {
				t.Fatalf("Resolve = unanswered %q declined %q, want %q %q", ids(u), ids(d), c.unanswered, c.declined)
			}
		})
	}
}

func TestResolveNamesTheMissingDB(t *testing.T) {
	q := Question{ID: QuestionReinitialize, Message: "Reinitialise", Objects: []string{"Tags", "Prog_DB"}}
	u, _ := Resolve([]Question{q}, Decisions{Reinitialize: []string{"Tags"}}, false)
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
