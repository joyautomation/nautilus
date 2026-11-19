package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Until #708's roles, authorize is the write policy, whatever the
// permission and target: the token when one is set, else a same-origin
// request addressed to a Host no page can rebind.
func TestAuthorizeIsTheWritePolicy(t *testing.T) {
	open := New(newTestRuntime(t))
	tok := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	for _, tc := range []struct {
		what   string
		srv    *Server
		host   string
		header map[string]string
		want   int
	}{
		{"no token set, no Origin", open, "localhost", nil, 0},
		{"no token set, same-origin page on an IP", open, "192.0.2.5:8080", map[string]string{"Origin": "http://192.0.2.5:8080"}, 0},
		{"no token set, cross-origin page", open, "localhost", map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		{"no token set, rebindable Host", open, "plc.example", map[string]string{"Origin": "http://plc.example"}, http.StatusForbidden},
		{"token set, none sent", tok, "localhost", nil, http.StatusUnauthorized},
		{"token set, wrong one", tok, "localhost", map[string]string{"X-Nautilus-Token": "guess"}, http.StatusUnauthorized},
		{"token set, sent", tok, "localhost", map[string]string{"Authorization": "Bearer s3cret"}, 0},
		{"token set, sent from another origin", tok, "localhost", map[string]string{"Authorization": "Bearer s3cret", "Origin": "http://hmi.example"}, 0},
	} {
		for _, perm := range []string{PermWrite, PermAck, PermShelve, PermSubstitute, PermForce, PermSFC, PermEdit, PermActivate, PermDeploy, PermConfig} {
			r := httptest.NewRequest("POST", "http://"+tc.host+"/api/x", nil)
			r.Host = tc.host
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}
			code, msg := tc.srv.authorize(r, perm, "SP")
			if wcode, wmsg := tc.srv.authorizeWrite(r); code != tc.want || code != wcode || msg != wmsg {
				t.Errorf("%s, %s: authorize = %d %q, want %d (authorizeWrite says %d %q)", tc.what, perm, code, msg, tc.want, wcode, wmsg)
			}
		}
	}
}
