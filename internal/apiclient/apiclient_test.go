package apiclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// A 401 or 403 says which credential the tool sent: none (set
// NAUTILUS_TOKEN) or NAUTILUS_TOKEN (refused); any other status adds
// nothing.
func TestRefusalHint(t *testing.T) {
	t.Setenv("NAUTILUS_TOKEN", "")
	for _, st := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		if h := RefusalHint(st); !strings.Contains(h, "set NAUTILUS_TOKEN") || !strings.Contains(h, "naut users token") {
			t.Errorf("%d with no token: %q", st, h)
		}
	}
	for _, st := range []int{http.StatusOK, http.StatusNotFound, http.StatusBadRequest, http.StatusInternalServerError} {
		if h := RefusalHint(st); h != "" {
			t.Errorf("%d: %q, want none", st, h)
		}
	}
	t.Setenv("NAUTILUS_TOKEN", "tok")
	if h := RefusalHint(http.StatusUnauthorized); !strings.Contains(h, "sent NAUTILUS_TOKEN") || strings.Contains(h, "tok)") {
		t.Errorf("401 with a token: %q", h)
	}
}
