package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// An instance's from: member is read-only to POST /api/tags (400, naming
// the member and why), while its memory and command members are written
// (#1458 slice 3).
func TestPostToAnInstanceFollowsMemberKinds(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\nEND_PROGRAM",
		Libraries: []string{"TYPE Pump : STRUCT Speed : REAL; RunHours : REAL; Start : BOOL; END_STRUCT; END_TYPE"},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{runtime.Instance("P101", "Pump", runtime.InstanceDef{Members: map[string]runtime.MemberDef{
			"Speed": {Kind: runtime.MemberFrom}, "Start": {Kind: runtime.MemberTo},
		}})},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	h := New(rt).Handler()
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "http://localhost/api/tags", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	if code, body := post(`{"name":"P101.Speed","value":1}`); code != 400 || !strings.Contains(body, "P101.Speed is read from the device") {
		t.Errorf("POST to a from: member = %d %q, want 400 naming it", code, body)
	}
	if code, body := post(`{"name":"P101","value":{"Speed":1,"RunHours":2}}`); code != 400 || !strings.Contains(body, "P101.Speed") {
		t.Errorf("POST of an object naming a from: member = %d %q, want 400", code, body)
	}
	for _, body := range []string{`{"name":"P101.RunHours","value":3}`, `{"name":"P101.Start","value":true}`} {
		if code, msg := post(body); code != 204 {
			t.Errorf("POST %s = %d %q, want 204", body, code, msg)
		}
	}
	if v, _ := rt.Tags().ReadPath("P101.RunHours"); v != 3.0 {
		t.Errorf("RunHours = %v, want 3", v)
	}
}
