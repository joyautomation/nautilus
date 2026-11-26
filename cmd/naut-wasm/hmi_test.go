//go:build js && wasm

package main

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/project"
)

func hmiTestProject(t *testing.T, plantLine string, files fstest.MapFS) *project.Project {
	t.Helper()
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	files["nautilus.yaml"] = file("name: p\n" + plantLine + `tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: P101, type: BOOL, role: state }
  - { name: P101_Flow, type: REAL, role: input }
`)
	files["main.st"] = file("PROGRAM main\nEND_PROGRAM\n")
	p, err := project.Load(files, project.ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A project with a plant model and no screens of its own gets the plant's
// asset and area pages in the playground (#714): the descriptors naut run
// builds go to the screen builder with the build request. Before, startHMI
// returned nothing for it and the asset pages (and the camera and notes
// panels on them) never showed.
func TestThePlaygroundBuildsThePlantsAssetPages(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	p := hmiTestProject(t, "plant: plant.yaml\n", fstest.MapFS{
		"plant.yaml": file(`areas: [{ id: Receiving, label: "Receiving dock" }]
equipment:
  - { id: P101, type: Pump, area: Receiving, tag: P101 }
`),
		"asset-types/Pump.asset.yaml": file(`type: Pump
readings:
  - { tag: "{tag}", label: State, on: Running, off: Stopped }
`),
	})
	b := startHMI(p, "/project")
	if b == nil {
		t.Fatal("a plant model with equipment builds no screens")
	}
	if len(b.req.Assets) != 1 || !strings.Contains(string(b.req.Assets[0]), `"id":"P101"`) {
		t.Errorf("assets handed to the builder: %s", b.req.Assets)
	}
	if len(b.req.Areas) != 1 || !strings.Contains(string(b.req.Areas[0]), "P101") {
		t.Errorf("areas handed to the builder: %s", b.req.Areas)
	}
}

// A project with neither screens nor a plant model still builds nothing.
func TestThePlaygroundBuildsNoScreensForABareProject(t *testing.T) {
	if b := startHMI(hmiTestProject(t, "", fstest.MapFS{}), "/project"); b != nil {
		t.Errorf("a bare project builds %q", b.src)
	}
}
