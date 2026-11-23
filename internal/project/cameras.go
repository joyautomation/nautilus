//go:build !js

package project

import (
	"errors"
	"fmt"

	"github.com/joyautomation/nautilus/camera"
)

// CameraRows are the manifest's cameras: rows. The browser build keeps them as
// YAML nodes and reads none (cameras_js.go): the playground draws its own
// virtual camera, and the camera package's checks stay out of its wasm.
type CameraRows = []camera.Camera

// validateCameras checks the rows on their own (the plant is CheckCameras').
func validateCameras(rows CameraRows) error { return errors.Join(camera.Validate(rows)...) }

// CheckCameras is what `naut check` says about the cameras (#724): each names
// a piece of equipment the plant has, and a camera the asset page cannot show a
// picture of is a warning. Credentials are checked where the secrets file is
// (cmd/naut): this knows no state directory.
func (p *Project) CheckCameras() (errs, warns []string) {
	if len(p.Cameras) == 0 {
		return nil, nil
	}
	for _, c := range p.Cameras {
		switch {
		case p.Plant == nil:
			errs = append(errs, fmt.Sprintf("camera %q: asset %s: the project has no plant: (plant.yaml), so there is no equipment for a camera to show", c.ID, c.Asset))
			continue
		default:
			if _, ok := p.Plant.Find(c.Asset); !ok {
				errs = append(errs, fmt.Sprintf("camera %q: asset %s: plant.yaml has no equipment with that id", c.ID, c.Asset))
			}
		}
		switch c.Kind {
		case camera.KindHLS, camera.KindRTSP, camera.KindONVIF:
			if c.SnapshotURL == "" {
				warns = append(warns, fmt.Sprintf("camera %q (%s): live video of this kind is not built in yet and it has no snapshot-url, so the asset page has no picture to show", c.ID, c.Kind))
			}
		}
	}
	return errs, warns
}
