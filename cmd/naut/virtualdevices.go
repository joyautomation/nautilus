package main

// A project's virtual devices: one per devices: row in nautilus.yaml and
// virtual-devices.yaml's own (docs/design/device-library.md §3.6). Every
// command that builds them (test, check, build, devices up/check) composes
// them here, so they agree on what a project's virtual devices are.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/virtdev"
)

// composeVirtualDevices builds dir's virtual devices from the project's
// devices (nil when it declares none) and virtual-devices.yaml. The error
// is os.ErrNotExist when the project has neither.
func composeVirtualDevices(dir string, devs *devbind.Set) (*virtdev.Set, error) {
	var declared []virtdev.Instance
	for _, r := range devs.Rows() {
		d := virtdev.Instance{Name: r.Name, Profile: r.Profile, Endpoint: r.Endpoint, Params: r.Params}
		if r.UnitID != nil {
			u := uint8(*r.UnitID) // devbind.Resolve checked 0-255
			d.UnitID = &u
		}
		declared = append(declared, d)
	}
	return virtdev.Compose(dir, declared)
}

// manifestDevices are the devices dir's nautilus.yaml declares: nil, with
// no error, when there is no nautilus.yaml.
func manifestDevices(dir string) (*devbind.Set, error) {
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if _, serr := os.Stat(filepath.Join(dir, project.ManifestName)); errors.Is(serr, fs.ErrNotExist) {
				return nil, nil
			}
		}
		return nil, err
	}
	return m.DeviceSet(), nil
}

// loadVirtualDevices is composeVirtualDevices for dir's own nautilus.yaml.
func loadVirtualDevices(dir string) (*virtdev.Set, error) {
	devs, err := manifestDevices(dir)
	if err != nil {
		return nil, err
	}
	return composeVirtualDevices(dir, devs)
}
