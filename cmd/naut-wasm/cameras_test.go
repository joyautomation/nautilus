//go:build js && wasm

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func cameraRows(t *testing.T, src string) []yaml.Node {
	t.Helper()
	var rows []yaml.Node
	if err := yaml.Unmarshal([]byte(src), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// The playground shows only a virtual camera, drawn by the kit; a real camera's row
// (an address on a network the browser cannot reach) is never shown, and a project
// with no virtual camera has no camera route at all.
func TestThePlaygroundListsOnlyVirtualCamerasAndMarksThemLocal(t *testing.T) {
	mux := http.NewServeMux()
	camerasMount(cameraRows(t, `
- { id: tank-top, label: Tank top, asset: TK101, kind: virtual }
- { id: dock, asset: TK101, kind: rtsp, url: "rtsp://10.0.0.5/x" }
- { id: bare, asset: TK101, kind: virtual }
- { kind: virtual }
- { id: tank-top, label: Again, asset: TK101, kind: virtual }
`))(nil, mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/cameras", nil))
	var got struct {
		Cameras []struct {
			ID, Label, Asset, Kind string
			Local, Live, Snapshot  bool
		} `json:"cameras"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if len(got.Cameras) != 2 || got.Cameras[0].ID != "tank-top" || got.Cameras[0].Label != "Tank top" || got.Cameras[1].Label != "bare" {
		t.Fatalf("cameras: %+v", got.Cameras)
	}
	for _, c := range got.Cameras {
		if !c.Local || c.Live || c.Snapshot || c.Kind != "virtual" || c.Asset != "TK101" {
			t.Errorf("%+v: the kit must draw it itself and ask for no still or stream", c)
		}
	}
	if body := rec.Body.String(); strings.Contains(body, "10.0.0.5") || strings.Contains(body, "rtsp") {
		t.Errorf("a real camera's row reached the page: %s", body)
	}

	none := http.NewServeMux()
	camerasMount(cameraRows(t, `- { id: dock, asset: TK101, kind: rtsp, url: "rtsp://10.0.0.5/x" }`))(nil, none)
	rec = httptest.NewRecorder()
	none.ServeHTTP(rec, httptest.NewRequest("GET", "/api/cameras", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a project with no virtual camera answered %d", rec.Code)
	}
}
