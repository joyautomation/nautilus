package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/camera"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// The cameras (#724): the manifest's rows, the controller's relay of them, and
// the check that their credentials are in the secrets file. Credentials are the
// secret camera.<id> of the alarm secrets file, so a controller has one file of
// secrets; it is read when a picture is asked for, so a changed password needs
// no restart.

// cameraOptions is the server's cameras for a project, nil without any.
func cameraOptions(proj *project.Project, dir string, rt *runtime.Runtime, getenv func(string) string, stderr io.Writer) *server.CameraOptions {
	if len(proj.Cameras) == 0 {
		return nil
	}
	secrets := func(name string) (string, bool) {
		sd, err := controllerStateDir("the camera credentials", proj.AuthStateDir, proj.Name, dir, dir != "", getenv)
		if err != nil {
			return "", false
		}
		s, err := oncall.ReadSecrets(oncall.SecretsPath(sd))
		if err != nil {
			fmt.Fprintf(stderr, "naut run: cameras: the secrets file cannot be read: %v\n", err)
			return "", false
		}
		v, ok := s.Secrets[name]
		return v, ok
	}
	co := &server.CameraOptions{Cameras: proj.Cameras, Fetcher: &camera.Fetcher{Secret: secrets}}
	// a virtual camera draws its asset's own reading
	co.Lines = func(c camera.Camera) []string {
		if proj.Plant == nil || rt == nil {
			return nil
		}
		e, ok := proj.Plant.Find(c.Asset)
		if !ok || e.Tag == "" {
			return nil
		}
		v, ok := rt.Tags().ReadPath(e.Tag)
		if !ok {
			return nil
		}
		return []string{fmt.Sprintf("%s %v", e.ID, v)}
	}
	return co
}

// checkCameraSecrets says which cameras need credentials the secrets file does
// not hold: an error on the controller, a warning off it, where the file may
// simply live elsewhere (as for the alarm contacts).
func checkCameraSecrets(proj *project.Project, stateDir string, onBox bool) (errs, warns []string) {
	var need []string
	for _, c := range proj.Cameras {
		if c.Credentials {
			need = append(need, c.ID)
		}
	}
	if len(need) == 0 {
		return nil, nil
	}
	sort.Strings(need)
	const where = "on the controller: naut alarms secrets list"
	if stateDir == "" {
		return nil, []string{"cameras: no state directory here, so the credentials of " + strings.Join(need, ", ") + " were not checked; " + where}
	}
	path := oncall.SecretsPath(stateDir)
	if _, err := os.Stat(path); err != nil && !onBox {
		return nil, []string{fmt.Sprintf("cameras: this machine has no secrets file in %s, so the credentials of %s were not checked; %s", stateDir, strings.Join(need, ", "), where)}
	}
	s, err := oncall.ReadSecrets(path)
	if err != nil {
		return []string{fmt.Sprintf("cameras: %v", err)}, nil
	}
	for _, id := range need {
		key := camera.SecretKey(id)
		v, ok := s.Secrets[key]
		switch {
		case !ok:
			errs = append(errs, fmt.Sprintf("camera %q needs credentials and %s has no %s: naut alarms secrets set %s (user:password on stdin)", id, path, key, key))
		default:
			if _, _, err := camera.SplitCredentials(v); err != nil {
				errs = append(errs, fmt.Sprintf("camera %q: secret %s: %v", id, key, err))
			}
		}
	}
	return errs, nil
}
