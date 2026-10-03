package facade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/logix/deploy"
	lw "github.com/joyautomation/nautilus/logix/writer"
)

const ladderA = "PROGRAM MainProgram\nVAR A : BOOL; B : BOOL; END_VAR\nLD\n  RUNG r A ( B )\nEND_LD\nEND_PROGRAM\n"
const ladderB = "PROGRAM MainProgram\nVAR A : BOOL; B : BOOL; END_VAR\nLD\n  RUNG r /A ( B )\nEND_LD\nEND_PROGRAM\n"

// startPlane serves a facade whose program plane deploys through fn.
func startPlane(t *testing.T, fn func(ctx context.Context, src string) (*deploy.Report, error)) string {
	t.Helper()
	port, _ := startController(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f, err := New(ctx, Options{Host: "127.0.0.1", Port: port, Poll: 30 * time.Millisecond,
		Program: &ProgramPlane{Source: ladderA, Deploy: fn}})
	if err != nil {
		t.Fatal(err)
	}
	go f.Run(ctx)
	api := httptest.NewServer(f.Handler())
	t.Cleanup(api.Close)
	return api.URL
}

func getProgram(t *testing.T, url string) map[string]any {
	t.Helper()
	res, err := http.Get(url + "/api/program")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	if res.StatusCode != 200 {
		t.Fatalf("GET /api/program: %d %v", res.StatusCode, m)
	}
	return m
}

func putProgram(t *testing.T, url, src, base string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"source": src, "baseHash": base})
	req, _ := http.NewRequest(http.MethodPut, url+"/api/program", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

// The editor's flow: GET for the hash, PUT with it, GET again and see the
// new source. A stale base is refused the way the runtime refuses it.
func TestProgramPlaneDeploysAnOnlineEdit(t *testing.T) {
	var got string
	url := startPlane(t, func(_ context.Context, src string) (*deploy.Report, error) {
		got = src
		return &deploy.Report{Applied: deploy.Online, Replaced: 1, Verified: true, ModeAfter: "Run"}, nil
	})
	info := getProgram(t, url)
	if info["source"] != ladderA || info["editable"] != true || info["language"] != "ld" || info["pou"] != "MainProgram" {
		t.Fatalf("info = %v", info)
	}
	hash := info["hash"].(string)

	if code, body := putProgram(t, url, ladderB, "deadbeef0000"); code != 409 || !strings.Contains(body["error"].(string), "changed since your base") {
		t.Fatalf("stale base: %d %v", code, body)
	}
	code, body := putProgram(t, url, ladderB, hash)
	if code != 200 || body["applied"] != "online edit" || body["verified"] != true {
		t.Fatalf("PUT: %d %v", code, body)
	}
	if got != ladderB {
		t.Errorf("deploy received %q", got)
	}
	after := getProgram(t, url)
	if after["source"] != ladderB || after["hash"] == hash || after["hash"] != body["hash"] {
		t.Errorf("after = %v", after)
	}
}

// A refused deploy reaches the author verbatim, naming the command for a
// download — never doing one.
func TestProgramPlaneRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		want string
	}{
		{"needs download", &deploy.NeedsDownloadError{Diffs: []string{"tag X: only on the second side"}}, 422, "naut logix deploy --download --yes"},
		{"outside subset", &deploy.DiagError{Diags: []lw.Diag{{Rule: "logix/fb", Line: 4, Message: "tp:TP: not in the Logix v1 subset"}}}, 422, "tp:TP"},
		{"verify failed", &deploy.VerifyError{Diffs: []string{"routine MainProgram/MainRoutine rung 0:"}}, 502, "verification failed"},
		{"agent down", errors.New("logixd: unreachable"), 502, "unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := startPlane(t, func(context.Context, string) (*deploy.Report, error) { return &deploy.Report{}, c.err })
			code, body := putProgram(t, url, ladderB, "")
			if code != c.code || !strings.Contains(body["error"].(string), c.want) {
				t.Fatalf("%d %v", code, body)
			}
			// Nothing changed.
			if getProgram(t, url)["source"] != ladderA {
				t.Error("a refused deploy changed the served source")
			}
		})
	}
}
