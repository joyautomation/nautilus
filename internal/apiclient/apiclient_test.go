package apiclient

import (
	"net/http/httptest"
	"testing"
)

func TestAuthorizeSendsTheTokenOnlyWhenSet(t *testing.T) {
	t.Setenv("NAUTILUS_TOKEN", "s3cret")
	req := httptest.NewRequest("GET", "http://plc.lan/api/state", nil)
	Authorize(req)
	if got := req.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("with NAUTILUS_TOKEN: Authorization = %q", got)
	}
	t.Setenv("NAUTILUS_TOKEN", "")
	req = httptest.NewRequest("GET", "http://plc.lan/api/state", nil)
	Authorize(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("without NAUTILUS_TOKEN: Authorization = %q, want none", got)
	}
}
