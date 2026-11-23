package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/camera"
	"github.com/joyautomation/nautilus/internal/project"
)

func camProject(cams ...camera.Camera) *project.Project { return &project.Project{Cameras: cams} }

func secureDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCameraCredentialsAreCheckedAgainstTheSecretsFile(t *testing.T) {
	proj := camProject(
		camera.Camera{ID: "dock", Asset: "A", Kind: camera.KindSnapshot, URL: "http://10.0.0.1/s", Credentials: true},
		camera.Camera{ID: "belt", Asset: "A", Kind: camera.KindSnapshot, URL: "http://10.0.0.2/s", Credentials: true},
		camera.Camera{ID: "open", Asset: "A", Kind: camera.KindVirtual},
	)
	dir := secureDir(t)
	// on the controller: an absent file is an empty one, so the gaps are errors
	errs, warns := checkCameraSecrets(proj, dir, true)
	if len(errs) != 2 || len(warns) != 0 || !strings.Contains(errs[0], "camera.belt") || !strings.Contains(errs[1], "camera.dock") {
		t.Errorf("on the box, no file: errors %v warnings %v", errs, warns)
	}
	// off it, the file living elsewhere is one warning, not an error
	if errs, warns := checkCameraSecrets(proj, dir, false); len(errs) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "dock, belt") && !strings.Contains(warns[0], "belt, dock") {
		t.Errorf("off the box, no file: errors %v warnings %v", errs, warns)
	}
	if errs, warns := checkCameraSecrets(proj, "", false); len(errs) != 0 || len(warns) != 1 {
		t.Errorf("no state directory: errors %v warnings %v", errs, warns)
	}
	// one set, one set wrongly (no user:password), one not needed
	path := oncall.SecretsPath(dir)
	if err := oncall.UpdateSecrets(path, "test", func(s *oncall.Secrets) error {
		s.Secrets["camera.dock"] = "viewer:hunter2"
		s.Secrets["camera.belt"] = "nocolonhere"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	errs, warns = checkCameraSecrets(proj, dir, false)
	if len(errs) != 1 || !strings.Contains(errs[0], `camera "belt"`) || !strings.Contains(errs[0], "user:password") || len(warns) != 0 {
		t.Errorf("a bad secret: errors %v warnings %v", errs, warns)
	}
	for _, m := range append(errs, warns...) {
		if strings.Contains(m, "hunter2") || strings.Contains(m, "nocolonhere") {
			t.Errorf("the check repeats a credential: %s", m)
		}
	}
	// a project with no cameras says nothing
	if errs, warns := checkCameraSecrets(camProject(), dir, true); len(errs)+len(warns) != 0 {
		t.Errorf("no cameras: %v %v", errs, warns)
	}
}

func TestACameraProjectGetsOptionsAndOthersGetNone(t *testing.T) {
	if cameraOptions(camProject(), "", nil, os.Getenv, io.Discard) != nil {
		t.Error("a project with no cameras got camera options")
	}
	dir := secureDir(t)
	proj := camProject(camera.Camera{ID: "dock", Asset: "A", Kind: camera.KindSnapshot, URL: "http://10.0.0.1/s", Credentials: true})
	proj.AuthStateDir = dir
	co := cameraOptions(proj, "", nil, os.Getenv, io.Discard)
	if co == nil || len(co.Cameras) != 1 {
		t.Fatalf("options: %+v", co)
	}
	if _, ok := co.Fetcher.Secret("camera.dock"); ok {
		t.Error("a secret that is not set was found")
	}
	if err := oncall.UpdateSecrets(oncall.SecretsPath(dir), "test", func(s *oncall.Secrets) error { s.Secrets["camera.dock"] = "viewer:pw"; return nil }); err != nil {
		t.Fatal(err)
	}
	// read when a picture is asked for: a password changed on the box needs no restart
	if v, ok := co.Fetcher.Secret("camera.dock"); !ok || v != "viewer:pw" {
		t.Errorf("the secret set after start: %q %v", v, ok)
	}
}
