package main

// The PROFINET section of a Siemens CPU's station spec. In nautilus.yaml a
// nautilus PROFINET device in the CPU's IO system is a reference,
// {profinet-manifest: <path>, address, update-time}, not a copy
// (docs/design/siemens-ide.md §3.3). expandProfinet turns it into the
// explicit device tiad applies, from the same profinet_device.yaml the
// driver serves, so the PLC's hardware config, the GSDML and the driver
// cannot drift apart. The context map keeps siemens/* and profinet apart,
// so the CLI composes them here.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"time"

	"github.com/joyautomation/nautilus/profinet"
	"github.com/joyautomation/nautilus/profinet/gsdml"
	"github.com/joyautomation/nautilus/siemens/station"
	"github.com/joyautomation/nautilus/siemens/tiad"
)

// gsdFile is a GSDML file a deploy puts in tiad's work dir and installs
// (gsd/install) before it applies the station.
type gsdFile struct {
	Name string
	Body []byte
}

// profinetGSDML generates a manifest's GSDML and its file name for date.
// `naut profinet gsdml` and the station expansion both use it, so the file
// a person imports by hand and the one a deploy installs are the same.
func profinetGSDML(m profinet.DeviceManifest, date time.Time) (gsdFile, error) {
	d := gsdml.Device{Identity: m.Identity(), Subslots: m.Subslots(), MinCycle: time.Duration(m.MinCycle)}
	body, err := gsdml.Generate(d)
	if err != nil {
		return gsdFile{}, err
	}
	return gsdFile{Name: gsdml.Filename(d, date), Body: body}, nil
}

// expandProfinet returns the explicit IO system for tiad and the GSDML
// files its references need, one per referenced manifest. Explicit devices
// pass through unchanged. projectDir is the directory nautilus.yaml is in;
// a reference's path is relative to it and may not leave it. date names
// the generated GSDML files.
//
// The station's own rules (siemens/station.Problems) are checked first by
// the caller; this adds the rules only an expansion can see: the
// referenced manifest, its station name under the Siemens rules, the
// update time against the device's min-cycle, and names and addresses that
// collide once references are expanded. Every problem is reported at once.
func expandProfinet(projectDir string, cpu station.CPU, p *station.Profinet, date time.Time) (*tiad.Profinet, []gsdFile, error) {
	if p == nil {
		return nil, nil, nil
	}
	var errs []error
	bad := func(at, f string, a ...any) { errs = append(errs, fmt.Errorf("%s: %s", at, fmt.Sprintf(f, a...))) }

	out := &tiad.Profinet{UpdateTime: p.UpdateTime}
	var files []gsdFile
	names := map[string]string{cpu.ProfinetName: "the CPU"}
	addrs := map[string]string{cpu.Address: "the CPU"}
	claim := func(at, name, addr string) {
		if owner, used := names[name]; used {
			bad(at, "name of station %q is already %s's", name, owner)
		} else {
			names[name] = at
		}
		if owner, used := addrs[addr]; used {
			bad(at, "address %s is already %s's", addr, owner)
		} else {
			addrs[addr] = at
		}
	}

	for i, d := range p.Devices {
		at := fmt.Sprintf("profinet.devices[%d]", i)
		if !d.IsReference() {
			claim(at, d.Name, d.Address)
			out.Devices = append(out.Devices, explicitDevice(d))
			continue
		}
		m, err := readDeviceManifest(projectDir, d.ProfinetManifest)
		if err != nil {
			bad(at+".profinet-manifest", "%v", err)
			continue
		}
		if err := station.CheckStationName(m.Station); err != nil {
			bad(at, "%s: station %q: %v", d.ProfinetManifest, m.Station, err)
			continue
		}
		claim(at, m.Station, d.Address)

		update, err := referenceUpdateTime(d.UpdateTime, p.UpdateTime, time.Duration(m.MinCycle))
		if err != nil {
			bad(at+".update-time", "%v", err)
			continue
		}
		f, err := profinetGSDML(m, date)
		if err != nil {
			bad(at, "%s: %v", d.ProfinetManifest, err)
			continue
		}
		files = append(files, f)
		out.Devices = append(out.Devices, tiad.Device{
			GSDML:      f.Name,
			DAP:        gsdml.DAPID,
			Name:       m.Station,
			Address:    d.Address,
			UpdateTime: update,
			Modules:    referenceModules(m),
		})
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	return out, files, nil
}

// readDeviceManifest reads and parses a referenced profinet_device.yaml.
// os.Root keeps the read inside the project, symlinks included.
func readDeviceManifest(projectDir, ref string) (profinet.DeviceManifest, error) {
	rel := path.Clean(ref)
	if !fs.ValidPath(rel) {
		return profinet.DeviceManifest{}, fmt.Errorf("%q is outside the project", ref)
	}
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return profinet.DeviceManifest{}, err
	}
	defer root.Close()
	raw, err := root.ReadFile(rel)
	if err != nil {
		return profinet.DeviceManifest{}, err
	}
	m, err := profinet.ParseDeviceManifest(raw)
	if err != nil {
		return profinet.DeviceManifest{}, fmt.Errorf("%s: %w", ref, err)
	}
	return m, nil
}

// referenceUpdateTime is a reference's update time, as tiad receives it:
// the device's own, else the IO system's, else the slowest of the 16 ms
// minimum and the device's min-cycle. It is always explicit, so TIA never
// picks a faster automatic time for a software device. A time below the
// device's min-cycle is refused: the device would refuse that connection.
func referenceUpdateTime(own, system string, minCycle time.Duration) (string, error) {
	s := own
	if s == "" {
		s = system
	}
	if s == "" {
		return max(station.MinUpdateTime, minCycle).String(), nil
	}
	if err := station.CheckUpdateTime(s); err != nil {
		return "", err
	}
	d, _ := time.ParseDuration(s) // CheckUpdateTime parsed it
	if d < minCycle {
		return "", fmt.Errorf("%s is below the device's min-cycle %s", d, minCycle)
	}
	return s, nil
}

// referenceModules places one module per manifest slot, by the module ID
// the generated GSDML gives it, in the slot's lowest subslot: a module's
// subslots are fixed inside it (#814).
func referenceModules(m profinet.DeviceManifest) []tiad.DeviceModule {
	var out []tiad.DeviceModule
	for _, s := range m.Subslots() { // sorted by slot, then subslot
		if n := len(out); n > 0 && out[n-1].Slot == int(s.Slot) {
			continue
		}
		out = append(out, tiad.DeviceModule{Slot: int(s.Slot), Subslot: int(s.Subslot), Module: gsdml.ModuleID(s.Slot)})
	}
	return out
}

func explicitDevice(d station.Device) tiad.Device {
	out := tiad.Device{GSDML: d.GSDML, DAP: d.DAP, Name: d.Name, Address: d.Address, UpdateTime: d.UpdateTime}
	for _, m := range d.Modules {
		out.Modules = append(out.Modules, tiad.DeviceModule{
			Slot: m.Slot, Subslot: m.Subslot, Module: m.Module,
			InputStart: m.InputStart, OutputStart: m.OutputStart,
		})
	}
	return out
}
