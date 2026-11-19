package facade

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/logix/deploy"
	"github.com/joyautomation/nautilus/server"
)

// With an auth: block, the facade's own program routes ask for what they
// do (#708): an online edit needs edit, a download deploy. An operator
// may do neither; an editor (an engineer without deploy) may edit and
// not deploy.
func TestProgramRoutesNeedEditAndDeploy(t *testing.T) {
	p, err := auth.Compile(auth.Settings{
		Roles: map[string]auth.RoleDef{"editor": {Inherits: auth.Engineer, Deny: []string{auth.Deploy}}},
		ServiceTokens: map[string][]auth.Grant{
			"op":     {{Role: auth.Operator}},
			"editor": {{Role: "editor"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	port, _ := startController(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f, err := New(ctx, Options{Host: "127.0.0.1", Port: port, Poll: 30 * time.Millisecond,
		Program: &ProgramPlane{Source: ladderA, Deploy: func(context.Context, string, func(string)) (*deploy.Report, error) {
			return &deploy.Report{}, nil
		}},
		Server: server.Options{Auth: p, ServiceTokens: map[string]string{"op": "op-secret", "editor": "ed-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	go f.Run(ctx)
	api := httptest.NewServer(f.Handler())
	t.Cleanup(api.Close)
	send := func(method, path, tok string) int {
		body, _ := json.Marshal(map[string]string{"source": ladderB})
		req, _ := http.NewRequest(method, api.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, c := range []struct {
		method, path, tok string
		want              int
	}{
		{"PUT", "/api/program", "op-secret", http.StatusForbidden},
		{"POST", "/api/logix/deploy", "op-secret", http.StatusForbidden},
		{"POST", "/api/logix/deploy", "ed-secret", http.StatusForbidden},
		{"PUT", "/api/program", "ed-secret", http.StatusOK},
	} {
		if got := send(c.method, c.path, c.tok); got != c.want {
			t.Errorf("%s %s with %s: %d, want %d", c.method, c.path, c.tok, got, c.want)
		}
	}
}
