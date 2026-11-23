//go:build js

package main

import (
	"encoding/json"
	"net/http"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/server"
)

// The playground's cameras (#724): there is no camera relay in the browser build
// (it is controller-only code and none of it is linked here), so only a
// `kind: virtual` row is shown, as a camera the kit draws itself on a canvas
// (hmi/src/lib/components/VirtualCamera.svelte). This route is all the server
// says about it: the rows' id, label and asset, marked `local` so the kit does
// not ask for a still or a stream that does not exist.
func camerasMount(rows project.CameraRows) server.Mount {
	type view struct {
		ID         string `json:"id"`
		Label      string `json:"label"`
		Asset      string `json:"asset"`
		Kind       string `json:"kind"`
		Interval   int    `json:"interval"`
		MaxViewers int    `json:"maxViewers"`
		View       bool   `json:"view"`
		Live       bool   `json:"live"`
		Hls        bool   `json:"hls"`
		Ptz        bool   `json:"ptz"`
		Snapshot   bool   `json:"snapshot"`
		Watching   int    `json:"watching"`
		Bps        int    `json:"bps"`
		Local      bool   `json:"local"`
	}
	var out []view
	for i := range rows {
		var r struct {
			ID    string `yaml:"id"`
			Label string `yaml:"label"`
			Asset string `yaml:"asset"`
			Kind  string `yaml:"kind"`
		}
		if rows[i].Decode(&r) != nil || r.Kind != "virtual" || r.ID == "" {
			continue
		}
		if r.Label == "" {
			r.Label = r.ID
		}
		out = append(out, view{ID: r.ID, Label: r.Label, Asset: r.Asset, Kind: "virtual", Interval: 5, View: true, Local: true})
	}
	return func(s *server.Server, mux *http.ServeMux) {
		if len(out) == 0 {
			return // no virtual camera: no route, the kit shows nothing
		}
		mux.HandleFunc("GET /api/cameras", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(map[string]any{"cameras": out})
		})
	}
}
