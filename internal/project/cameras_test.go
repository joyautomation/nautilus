package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

func cameraFS(cameras string) fstest.MapFS {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	return fstest.MapFS{
		"nautilus.yaml": file(`name: p
plant: plant.yaml
tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: TK101, type: REAL, role: input }
` + cameras),
		"main.st":                     file("PROGRAM main\nEND_PROGRAM\n"),
		"plant.yaml":                  file("areas: [{ id: Receiving }]\nequipment:\n  - { id: TK101, type: Tank, area: Receiving, tag: TK101 }\n"),
		"asset-types/Tank.asset.yaml": file("type: Tank\nreadings: [{ tag: \"{tag}\", unit: m }]\n"),
	}
}

// A project with no cameras: section has none, and checks say nothing.
func TestNoCamerasNoCheck(t *testing.T) {
	p, err := Load(cameraFS(""), ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	if errs, warns := p.CheckCameras(); len(p.Cameras) != 0 || len(errs)+len(warns) != 0 {
		t.Errorf("a project without cameras: %v %v %v", p.Cameras, errs, warns)
	}
}

func TestCamerasAreReadFromTheManifestAndCheckedAgainstThePlant(t *testing.T) {
	p, err := Load(cameraFS(`cameras:
  - { id: tank-top, label: Tank top, asset: TK101, kind: virtual }
  - { id: dock, asset: TK101, kind: snapshot, url: "http://10.0.0.5/snap.jpg", credentials: true, interval: 10, max-viewers: 1 }
  - { id: belt, asset: Nowhere, kind: virtual }
  - { id: gate, asset: TK101, kind: rtsp, url: "rtsp://10.0.0.7/s2" }
`), ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Cameras) != 4 || p.Cameras[1].Interval() != 10 || p.Cameras[1].Viewers() != 1 || !p.Cameras[1].Credentials {
		t.Fatalf("the cameras: %+v", p.Cameras)
	}
	errs, warns := p.CheckCameras()
	if len(errs) != 1 || !strings.Contains(errs[0], `camera "belt": asset Nowhere: plant.yaml has no equipment with that id`) {
		t.Errorf("errors: %v", errs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], `camera "gate" (rtsp)`) || !strings.Contains(warns[0], "no snapshot-url") {
		t.Errorf("warnings: %v", warns)
	}
}

// A row that cannot be right refuses the manifest, naming the row, and a URL
// with a password in it never gets as far as a check.
func TestACameraRowThatCannotBeRightRefusesTheManifest(t *testing.T) {
	for name, tc := range map[string]struct{ rows, want string }{
		"no kind":     {"  - { id: a, asset: TK101 }\n", "needs a kind"},
		"password":    {"  - { id: a, asset: TK101, kind: mjpeg, url: \"http://u:pw@10.0.0.1/v\" }\n", "secrets file"},
		"duplicate":   {"  - { id: a, asset: TK101, kind: virtual }\n  - { id: a, asset: TK101, kind: virtual }\n", "another camera has this id"},
		"unknown key": {"  - { id: a, asset: TK101, kind: virtual, pan: 1 }\n", "pan"},
	} {
		_, err := Load(cameraFS("cameras:\n"+tc.rows), ManifestName)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "pw@") {
			t.Errorf("%s: the error repeats the password: %v", name, err)
		}
	}
	// a project with no plant cannot bind a camera to anything
	fsys := cameraFS("cameras:\n  - { id: a, asset: TK101, kind: virtual }\n")
	fsys["nautilus.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(string(fsys["nautilus.yaml"].Data), "plant: plant.yaml\n", "", 1))}
	p, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	if errs, _ := p.CheckCameras(); len(errs) != 1 || !strings.Contains(errs[0], "no plant:") {
		t.Errorf("no plant: %v", errs)
	}
}
