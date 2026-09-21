package logixd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAgent stands in for logixd. It records what the client sent, which is
// most of what these tests are checking: the wire shape is a contract with
// a program on another machine, and a renamed field is a silent failure at
// the worst possible moment.
type fakeAgent struct {
	t        *testing.T
	handler  func(r *http.Request, body map[string]any) (status int, reply any)
	lastAuth string
	lastPath string
}

func (f *fakeAgent) start() (*Client, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		f.lastPath = r.URL.Path
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		status, reply := f.handler(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	}))
	c := New(srv.URL, "tok")
	return c, srv.Close
}

func okEnvelope(data any, events ...Event) map[string]any {
	return map[string]any{"ok": true, "data": data, "events": events}
}

func TestClientSendsTokenAndDecodesData(t *testing.T) {
	f := &fakeAgent{t: t, handler: func(r *http.Request, _ map[string]any) (int, any) {
		return 200, okEnvelope(map[string]any{
			"service": "logixd", "sdkClient": "2.2.1109.0", "sessions": 2,
		})
	}}
	c, stop := f.start()
	defer stop()

	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.lastAuth != "Bearer tok" {
		t.Errorf("Authorization = %q", f.lastAuth)
	}
	if h.SDKClient != "2.2.1109.0" || h.Sessions != 2 {
		t.Errorf("health = %+v", h)
	}
}

// The agent's error kinds are the SDK's own distinction, and a caller acts
// on it: OperationFailed is "fix your request", OperationNotPerformed is
// "this session is dead". Flattening them would be a real bug.
func TestClientClassifiesErrors(t *testing.T) {
	cases := []struct {
		status    int
		kind      string
		fatal     bool
		wantFatal bool
	}{
		{409, "operation_failed", false, false},
		{502, "operation_not_performed", true, true},
		{403, "forbidden", false, false},
	}
	for _, tc := range cases {
		f := &fakeAgent{t: t, handler: func(r *http.Request, _ map[string]any) (int, any) {
			return tc.status, map[string]any{
				"ok": false,
				"error": map[string]any{
					"kind": tc.kind, "message": "boom", "type": "X", "fatal": tc.fatal,
				},
				"events": []Event{{Kind: "error", Message: "the real reason"}},
			}
		}}
		c, stop := f.start()
		_, err := c.Health(context.Background())
		stop()
		if err == nil {
			t.Fatalf("%s: want an error", tc.kind)
		}
		if got := Kind(err); got != tc.kind {
			t.Errorf("Kind = %q, want %q", got, tc.kind)
		}
		if got := IsFatal(err); got != tc.wantFatal {
			t.Errorf("%s: IsFatal = %v, want %v", tc.kind, got, tc.wantFatal)
		}
		var e *Error
		if !asError(err, &e) || len(e.Events) != 1 {
			t.Errorf("%s: the SDK events must survive onto the error — that is where the diagnosis is", tc.kind)
		}
		if e.Status != tc.status {
			t.Errorf("Status = %d, want %d", e.Status, tc.status)
		}
	}
}

// An unreachable agent is fatal and must name where it looked — a wrong
// --agent URL is the usual cause and the message should say so.
func TestClientUnreachableIsFatalAndNamesTheURL(t *testing.T) {
	c := New("http://127.0.0.1:1", "")
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !IsFatal(err) {
		t.Error("a dead agent is fatal")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("error should name the URL it tried: %v", err)
	}
}

func TestClientDefaultsFromEnvironment(t *testing.T) {
	t.Setenv("NAUTILUS_LOGIXD_URL", "http://agent.example:9000/")
	t.Setenv("NAUTILUS_LOGIXD_TOKEN", "envtok")
	c := New("", "")
	if c.BaseURL != "http://agent.example:9000" {
		t.Errorf("BaseURL = %q (a trailing slash would double the slash in every path)", c.BaseURL)
	}
	if c.Token != "envtok" {
		t.Errorf("Token = %q", c.Token)
	}
	if explicit := New("http://other", "t"); explicit.BaseURL != "http://other" || explicit.Token != "t" {
		t.Error("an explicit URL and token must win over the environment")
	}
}

// The online-edit options are the capability that makes a warm change to a
// running controller possible. Their spelling is the SDK's, and a typo here
// would silently degrade FinalizeEdits to the default LeaveEdits — the
// change would land as pending edits nobody accepted.
func TestImportRungsSendsTheOnlineOptionVerbatim(t *testing.T) {
	for _, opt := range []ImportOption{LeaveEdits, AcceptEdits, FinalizeEdits} {
		var got map[string]any
		f := &fakeAgent{t: t, handler: func(r *http.Request, body map[string]any) (int, any) {
			got = body
			return 200, okEnvelope(map[string]any{
				"xpath": body["xpath"], "onlineOption": body["onlineOption"], "elapsedMs": 12,
			})
		}}
		c, stop := f.start()
		s := &Session{ID: "sess1", c: c}
		res, _, err := s.ImportRungs(context.Background(),
			RoutinePath("MainProgram", "MainRoutine"), 3, 2, `C:\tmp\r.L5X`, opt)
		stop()
		if err != nil {
			t.Fatal(err)
		}
		if got["onlineOption"] != string(opt) {
			t.Errorf("onlineOption = %v, want %q", got["onlineOption"], opt)
		}
		if got["insertPosition"] != float64(3) || got["replaceCount"] != float64(2) {
			t.Errorf("position/replace = %v/%v", got["insertPosition"], got["replaceCount"])
		}
		if res.OnlineOption != string(opt) {
			t.Errorf("result option = %q", res.OnlineOption)
		}
		if f.lastPath != "/v1/sessions/sess1/import-rungs" {
			t.Errorf("path = %q", f.lastPath)
		}
	}
}

// A download stops a controller. ensureProgramMode must be sent exactly as
// the caller set it — defaulting it on would stop lines nobody asked to
// stop, and defaulting it off silently would fail every download instead.
func TestDownloadSendsEnsureProgramModeExplicitly(t *testing.T) {
	for _, want := range []bool{false, true} {
		var got map[string]any
		f := &fakeAgent{t: t, handler: func(r *http.Request, body map[string]any) (int, any) {
			got = body
			return 200, okEnvelope(map[string]any{"downloaded": "x"})
		}}
		c, stop := f.start()
		s := &Session{ID: "s", c: c}
		_, err := s.Download(context.Background(), want)
		stop()
		if err != nil {
			t.Fatal(err)
		}
		if got["ensureProgramMode"] != want {
			t.Errorf("ensureProgramMode = %v, want %v", got["ensureProgramMode"], want)
		}
	}
}

// The lean export is the one that diffs well, so detailedL5x must travel as
// given rather than being quietly defaulted.
func TestConvertSendsDetailedFlag(t *testing.T) {
	var got map[string]any
	f := &fakeAgent{t: t, handler: func(r *http.Request, body map[string]any) (int, any) {
		got = body
		return 200, okEnvelope(map[string]any{
			"input": body["input"], "output": body["output"], "bytes": 1234, "detailedL5x": body["detailedL5x"],
		})
	}}
	c, stop := f.start()
	defer stop()
	r, _, err := c.Convert(context.Background(), `C:\a.ACD`, `C:\a.L5X`, false)
	if err != nil {
		t.Fatal(err)
	}
	if got["detailedL5x"] != false || got["force"] != true {
		t.Errorf("body = %v", got)
	}
	if r.Bytes != 1234 {
		t.Errorf("bytes = %d", r.Bytes)
	}
}

func TestSessionLifecycle(t *testing.T) {
	closed := ""
	f := &fakeAgent{t: t, handler: func(r *http.Request, body map[string]any) (int, any) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			return 200, okEnvelope(map[string]any{"session": "abc123", "project": body["project"]})
		case r.Method == http.MethodDelete:
			closed = strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
			return 200, okEnvelope(map[string]any{"closed": closed})
		}
		return 404, map[string]any{"ok": false, "error": map[string]any{"kind": "not_found", "message": "x"}}
	}}
	c, stop := f.start()
	defer stop()

	s, err := c.Open(context.Background(), `C:\proj.ACD`)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "abc123" || s.Project != `C:\proj.ACD` {
		t.Fatalf("session = %+v", s)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closed != "abc123" {
		t.Errorf("closed %q", closed)
	}
}

// Closing an already-reaped session is not an error. The agent expires idle
// sessions on its own, so a deferred Close routinely races the reaper and
// must not turn a successful operation into a failed one.
func TestCloseIsIdempotent(t *testing.T) {
	f := &fakeAgent{t: t, handler: func(r *http.Request, _ map[string]any) (int, any) {
		return 404, map[string]any{"ok": false, "error": map[string]any{"kind": "not_found", "message": "gone"}}
	}}
	c, stop := f.start()
	defer stop()
	s := &Session{ID: "reaped", c: c}
	if err := s.Close(context.Background()); err != nil {
		t.Errorf("closing a reaped session should be a no-op, got %v", err)
	}
}

// The SDK addresses tags and routines by XPath, not by name. Getting the
// quoting wrong reads as "not found" rather than as a syntax error, so the
// builders are worth pinning.
func TestXPathBuilders(t *testing.T) {
	cases := [][2]string{
		{TagPath("HiLevelSP"), `Controller/Tags/Tag[@Name='HiLevelSP']`},
		{ProgramTagPath("MainProgram", "StartPB"), `Controller/Programs/Program[@Name='MainProgram']/Tags/Tag[@Name='StartPB']`},
		{RoutinePath("MainProgram", "MainRoutine"), `Controller/Programs/Program[@Name='MainProgram']/Routines/Routine[@Name='MainRoutine']`},
		{ProgramPath("MainProgram"), `Controller/Programs/Program[@Name='MainProgram']`},
	}
	for _, c := range cases {
		if c[0] != c[1] {
			t.Errorf("got %s, want %s", c[0], c[1])
		}
	}
}

func TestEventString(t *testing.T) {
	cases := [][2]string{
		{Event{Kind: "progress", Source: "Download", Percent: 40}.String(), "Download: 40%"},
		{Event{Kind: "error", Source: "Import", Message: "bad xpath"}.String(), "error [Import]: bad xpath"},
		{Event{Kind: "status", Message: "opened"}.String(), "status: opened"},
	}
	for _, c := range cases {
		if c[0] != c[1] {
			t.Errorf("got %q, want %q", c[0], c[1])
		}
	}
}

// A reply that is not the agent's envelope (a proxy's HTML error page, say)
// must be a clear failure naming what came back, not a nil-data success.
func TestNonEnvelopeReplyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>Bad Gateway</html>"))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "").Health(context.Background())
	if Kind(err) != "bad_reply" || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Errorf("err = %v", err)
	}
}
