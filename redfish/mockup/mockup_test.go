package mockup

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDirBothForms(t *testing.T) {
	short := t.TempDir()
	tall := t.TempDir()
	tr := Tree{
		Root:                     json.RawMessage(`{"Chassis":{"@odata.id":"/redfish/v1/Chassis"}}`),
		Root + "/Chassis":        json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1"}]}`),
		Root + "/Chassis/1":      json.RawMessage(`{"Id":"1","Big":18446744073709551615}`),
		Root + "/Chassis/1/Deep": json.RawMessage(`{"Id":"Deep"}`),
	}
	if err := tr.WriteDir(short); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteDir(filepath.Join(tall, "redfish", "v1")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{short, tall} {
		got, err := LoadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got.URIs(), ",") != strings.Join(tr.URIs(), ",") {
			t.Fatalf("%s: %v", dir, got.URIs())
		}
		doc, err := got.Get("/redfish/v1/Chassis/1/")
		if err != nil || doc["Big"].(json.Number).String() != "18446744073709551615" {
			t.Fatalf("a big integer must survive: %v %v", doc, err)
		}
	}
	if _, err := LoadDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a Redfish mockup") {
		t.Fatalf("empty dir: %v", err)
	}
	if links := Links(map[string]any{"b": map[string]any{"@odata.id": "/x"}, "a": []any{map[string]any{"@odata.id": "/y"}}}); strings.Join(links, ",") != "/y,/x" {
		t.Fatalf("Links = %v", links)
	}
}

func TestRelPathRefusesEscapes(t *testing.T) {
	for uri, ok := range map[string]bool{
		"/redfish/v1":              true,
		"/redfish/v1/Chassis/1":    true,
		"/redfish/v1/../etc":       false,
		"/redfish/v1/Chassis/#/0":  false,
		"/redfish/v2/Chassis":      false,
		"/redfish/v1//Chassis":     false,
		"/redfish/v1/Chassis/..":   false,
		"/redfish/v1/Chassis?x=1":  false,
		"/redfish/v1/Chassis\\..":  false,
		"/redfish/v1/Chassis/1/./": false,
	} {
		if _, got := RelPath(uri); got != ok {
			t.Errorf("RelPath(%q) ok = %v, want %v", uri, got, ok)
		}
	}
	// A recording that would write outside its directory is refused.
	bad := Tree{Root: json.RawMessage(`{}`), "/redfish/v1/../../x": json.RawMessage(`{}`)}
	dir := t.TempDir()
	if err := bad.WriteDir(dir); err == nil {
		t.Fatal("an escaping URI must be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "x")); err == nil {
		t.Fatal("wrote outside the directory")
	}
}

func TestServerKnobs(t *testing.T) {
	srv := New(Tree{
		Root:                    json.RawMessage(`{}`),
		Root + "/Systems/1":     json.RawMessage(`{"PowerState":"On","Fans":[{"R":1},{"R":2}],"Actions":{"#ComputerSystem.Reset":{"target":"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"}}}`),
		Root + "/Chassis/1/Fan": json.RawMessage(`{"Reading":100}`),
	}, Options{Auth: AuthBasic, User: "u", Password: "p"})
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	do := func(method, path, body string, auth bool) (int, string) {
		req, _ := http.NewRequest(method, srv.URL()+path, strings.NewReader(body))
		if auth {
			req.SetBasicAuth("u", "p")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := do("GET", "/redfish/v1", "", false); code != 200 {
		t.Fatalf("service root must be open: %d", code)
	}
	if code, _ := do("GET", "/redfish/v1/Systems/1", "", false); code != 401 {
		t.Fatalf("unauthenticated GET: %d", code)
	}
	if code, _ := do("POST", "/redfish/v1/SessionService/Sessions", `{"UserName":"u","Password":"p"}`, false); code != 405 {
		t.Fatalf("a basic-only BMC refuses sessions: %d", code)
	}
	if err := srv.Patch("/redfish/v1/Systems/1", "Fans.1.R", 7); err != nil {
		t.Fatal(err)
	}
	if _, body := do("GET", "/redfish/v1/Systems/1", "", true); !strings.Contains(body, `{"R":7}`) {
		t.Fatalf("patched body = %s", body)
	}
	srv.Fail("/redfish/v1/Chassis/1/Fan", 503)
	if code, _ := do("GET", "/redfish/v1/Chassis/1/Fan", "", true); code != 503 {
		t.Fatalf("injected status: %d", code)
	}
	srv.Fail("/redfish/v1/Chassis/1/Fan", 0)
	if code, _ := do("GET", "/redfish/v1/Chassis/1/Fan", "", true); code != 200 || srv.Gets("/redfish/v1/Chassis/1/Fan") != 2 {
		t.Fatalf("restored: %d, %d GETs", code, srv.Gets("/redfish/v1/Chassis/1/Fan"))
	}
	if code, _ := do("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`, true); code != 204 {
		t.Fatalf("reset: %d", code)
	}
	if _, body := do("GET", "/redfish/v1/Systems/1", "", true); !strings.Contains(body, `"PowerState":"Off"`) {
		t.Fatalf("reset did not reflect: %s", body)
	}
	if code, _ := do("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Explode", `{}`, true); code != 404 {
		t.Fatalf("an action the resource does not advertise: %d", code)
	}
	if code, _ := do("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"Nmi"}`, true); code != 400 {
		t.Fatalf("a reset type we do not model: %d", code)
	}
	if a := srv.Actions(); len(a) != 1 || a[0].Body["ResetType"] != "ForceOff" {
		t.Fatalf("actions = %+v", a)
	}
}
