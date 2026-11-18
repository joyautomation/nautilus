package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// #819: naut pull --host by name reaches a token controller only with its
// token, so it sends NAUTILUS_TOKEN when it is set.
func TestFetchProgramSendsTheToken(t *testing.T) {
	var got string
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"task":"Main","pou":"Main","source":"","language":"st"}`))
	}))
	defer src.Close()
	host, portStr, _ := net.SplitHostPort(src.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	t.Setenv("NAUTILUS_TOKEN", "s3cret")
	if _, err := fetchProgram(host, port, ""); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want the token", got)
	}
}
