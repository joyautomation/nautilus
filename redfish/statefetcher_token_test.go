package redfish

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #819: a controller with a token answers a host name it does not trust
// only when the request carries the token, so serve --from sends
// NAUTILUS_TOKEN when it is set.
func TestStateFetcherSendsTheToken(t *testing.T) {
	var got string
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"tags":{}}`))
	}))
	defer src.Close()
	t.Setenv("NAUTILUS_TOKEN", "s3cret")
	if _, err := StateFetcher(src.URL, nil)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want the token", got)
	}
}
