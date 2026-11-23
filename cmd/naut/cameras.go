package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/alarm"
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
func cameraOptions(proj *project.Project, dir string, rt *runtime.Runtime, leads func() bool, getenv func(string) string, stderr io.Writer) (*server.CameraOptions, func()) {
	if len(proj.Cameras) == 0 {
		return nil, func() {}
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
	closeFn := func() {}
	// pictures kept when an alarm activates, if any camera asks for them
	wantSnaps := false
	for _, c := range proj.Cameras {
		if c.SnapshotOnAlarm || len(c.SnapshotAlarms) > 0 {
			wantSnaps = true
		}
	}
	if wantSnaps {
		sd, err := controllerStateDir("the camera snapshots", proj.AuthStateDir, proj.Name, dir, dir != "", getenv)
		if err != nil {
			fmt.Fprintf(stderr, "naut run: cameras: alarm pictures are off: %v\n", err)
		} else {
			o := camera.SnapOptions{}
			if p := proj.CameraSnapshots; p != nil {
				o.KeepDays, o.MaxCount, o.MaxBytes = p.KeepDays, p.MaxCount, int64(p.MaxMB)<<20
			}
			st, err := camera.OpenSnapStore(filepath.Join(sd, SnapshotsDir), o)
			if err != nil {
				fmt.Fprintf(stderr, "naut run: cameras: alarm pictures are off: %v\n", err)
			} else {
				co.Snaps, co.Leads = st, leads
				closeFn = func() { st.Close() }
				// alarm id -> equipment, read from the project's definitions when
				// an alarm first needs it and again a minute later (or at once if the
				// first read failed or found nothing): never fixed by one failure
				var mu sync.Mutex
				var eq map[string]string
				var built time.Time
				co.Equipment = func(id string) string {
					mu.Lock()
					defer mu.Unlock()
					if eq == nil || len(eq) == 0 || time.Since(built) > time.Minute {
						m := map[string]string{}
						if defs, err := proj.AlarmDefs(rt); err == nil {
							for _, d := range defs {
								m[d.ID] = d.Equipment
							}
						}
						if len(m) > 0 || eq == nil {
							eq = m
						}
						built = time.Now()
					}
					return eq[id]
				}
			}
		}
	}
	return co, closeFn
}

// SnapshotsDir is the name of the alarm pictures' directory in the state directory.
const SnapshotsDir = "snapshots"

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

// checkCameraAlarms is what `naut check` says about the pictures kept when an
// alarm activates: an alarm id a camera names must exist, and a camera that asks
// for pictures of its asset's alarms should have alarms on that equipment.
func checkCameraAlarms(proj *project.Project, defs []alarm.Def) (errs, warns []string) {
	ids := map[string]bool{}
	equip := map[string]bool{}
	for _, d := range defs {
		ids[d.ID] = true
		equip[strings.ToLower(d.Equipment)] = true
	}
	for _, c := range proj.Cameras {
		for _, id := range c.SnapshotAlarms {
			if !ids[id] {
				errs = append(errs, fmt.Sprintf("camera %q: snapshot-alarms: the project has no alarm %q", c.ID, id))
			}
		}
		if c.SnapshotOnAlarm && !equip[strings.ToLower(c.Asset)] {
			warns = append(warns, fmt.Sprintf("camera %q: snapshot-on-alarm: no alarm is on equipment %q (an alarm's equipment: key), so no picture will be kept", c.ID, c.Asset))
		}
	}
	return errs, warns
}
