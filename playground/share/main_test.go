package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	sv := &server{store: &store{dir: t.TempDir()}, admin: "secret"}
	mux := http.NewServeMux()
	sv.routes(mux)
	ts := httptest.NewServer(headers(mux))
	t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, ts *httptest.Server, body string) (int, map[string]string) {
	t.Helper()
	r, err := http.Post(ts.URL+"/playground/api/p", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var out map[string]string
	json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out
}

const sample = `{"title":"t","open":["main.st","missing.st"],"files":{"nautilus.yaml":"name: t\n","main.st":"PROGRAM main END_PROGRAM"},"extra":1}`

func TestSaveLoadTakeDown(t *testing.T) {
	ts := newServer(t)
	code, out := post(t, ts, sample)
	if code != http.StatusCreated || !validID.MatchString(out["id"]) {
		t.Fatalf("save: %d %v", code, out)
	}
	id := out["id"]

	// The same project, keys in another order: the same id.
	_, again := post(t, ts, `{"files":{"main.st":"PROGRAM main END_PROGRAM","nautilus.yaml":"name: t\n"},"open":["main.st"],"title":"t"}`)
	if again["id"] != id {
		t.Fatalf("same project, new id: %s vs %s", again["id"], id)
	}

	r, _ := http.Get(ts.URL + "/playground/api/p/" + id)
	var p project
	json.NewDecoder(r.Body).Decode(&p)
	r.Body.Close()
	if r.StatusCode != 200 || p.Title != "t" || len(p.Files) != 2 || len(p.Open) != 1 {
		t.Fatalf("load: %d %+v", r.StatusCode, p)
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("no nosniff")
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/playground/api/p/"+id, nil)
	req.Header.Set("If-None-Match", r.Header.Get("ETag"))
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidate: %d", r.StatusCode)
	}

	del := func(token string) int {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/playground/api/p/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if c := del("wrong"); c != http.StatusUnauthorized {
		t.Fatalf("take down without the token: %d", c)
	}
	if c := del("secret"); c != 200 {
		t.Fatalf("take down: %d", c)
	}
	if r, _ := http.Get(ts.URL + "/playground/api/p/" + id); r.StatusCode != http.StatusGone {
		t.Fatalf("after take down: %d", r.StatusCode)
	}
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusGone {
		t.Fatalf("revalidate after take down: %d", r.StatusCode)
	}
	if c, _ := post(t, ts, sample); c != http.StatusGone {
		t.Fatalf("saved again after take down: %d", c)
	}
}

func TestRefused(t *testing.T) {
	ts := newServer(t)
	for name, body := range map[string]string{
		"no nautilus.yaml": `{"files":{"a.st":""}}`,
		"parent path":      `{"files":{"nautilus.yaml":"","../x":""}}`,
		"absolute path":    `{"files":{"nautilus.yaml":"","/etc/x":""}}`,
		"dot segment":      `{"files":{"nautilus.yaml":"","a/./b":""}}`,
		"not a project":    `[1,2]`,
		"too big":          `{"files":{"nautilus.yaml":"` + strings.Repeat("x", maxContent+1) + `"}}`,
	} {
		if c, _ := post(t, ts, body); c < 400 || c >= 500 {
			t.Errorf("%s: %d", name, c)
		}
	}
	if r, _ := http.Get(ts.URL + "/playground/api/p/../../etc"); r.StatusCode < 400 {
		t.Errorf("odd id: %d", r.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	var l limiter
	n := 0
	for range 50 {
		if l.allow("1.2.3.4") {
			n++
		}
	}
	if n != savesBurst {
		t.Fatalf("burst of %d, want %d", n, savesBurst)
	}
	if !l.keep(bytesPerDay/4) || l.keep(1<<20) {
		t.Fatal("the bytes kept a day are not limited")
	}
	if !l.allow("5.6.7.8") {
		t.Fatal("another visitor is limited too")
	}
}
