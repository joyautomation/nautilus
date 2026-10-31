package project

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"path"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// EIPOnlineTag is the name Load will give an eip driver's online companion
// (<driver name>__Online, see onlineTagFor), worked out ahead of time for
// `naut eip import` and `naut eip tags`, which write that tag into the
// generated tag file so nobody declares it by hand.
//
// fsys is the project directory. The driver is the eip entry of its
// nautilus.yaml whose host: is host (a bare host matches "host:port" and the
// other way round), else whose manifest: is manifest. With no match, a
// project with exactly one eip driver uses it — the controller was most
// likely named differently (an IP against a DNS name) — and one with
// several is an error that lists them, because guessing would name the
// companion after the wrong controller. No nautilus.yaml, or no eip driver
// in it, gives the default "eip" — what a driver: section added later will
// be called. how says which of those happened, for the import to print.
//
// Only the driver sections are decoded, and loosely: an import runs before
// the tag file it writes exists, and a manifest that already names that
// file would fail ReadManifest's tag-file composition.
func EIPOnlineTag(fsys fs.FS, host, manifest string) (name, how string, err error) {
	return onlineTagInProject(fsys, "eip", "host", host, manifest,
		func(c DriverConfig) string { return c.Host },
		func(a, b string) bool { return strings.EqualFold(a, b) }, sameHost,
		"pass --name with the one this controller is")
}

// OPCUAOnlineTag is EIPOnlineTag for an opcua (client) driver, matched by
// endpoint: — exactly, then ignoring case and a trailing slash — else by
// manifest:, with the same fallbacks. `naut opcua import` and `naut opcua
// tags` write the companion it names into the generated tag file.
func OPCUAOnlineTag(fsys fs.FS, endpoint, manifest string) (name, how string, err error) {
	trim := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
	return onlineTagInProject(fsys, "opcua", "endpoint", endpoint, manifest,
		func(c DriverConfig) string { return c.Endpoint },
		func(a, b string) bool { return a == b },
		func(a, b string) bool { return strings.EqualFold(trim(a), trim(b)) },
		"pass --name with the one this server is")
}

// onlineTagInProject is the shared resolution behind EIPOnlineTag and
// OPCUAOnlineTag: find the driver of type typ in the project's
// nautilus.yaml that an import is for, by its address (exact, then loose)
// or its manifest, and name its companion as Load will.
func onlineTagInProject(fsys fs.FS, typ, key, addr, manifest string,
	addrOf func(DriverConfig) string, exactAddr, looseAddr func(a, b string) bool,
	hint string) (name, how string, err error) {
	raw, err := fs.ReadFile(fsys, ManifestName)
	if errors.Is(err, fs.ErrNotExist) {
		return onlineTagFor("", typ), "no " + ManifestName + "; the default driver name", nil
	}
	if err != nil {
		return "", "", err
	}
	var m struct {
		Driver  DriverConfig   `yaml:"driver"`
		Drivers []DriverConfig `yaml:"drivers"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return "", "", fmt.Errorf("%s: %w (pass --name to name the driver yourself)", ManifestName, err)
	}
	configs := m.Drivers
	if !reflect.DeepEqual(m.Driver, DriverConfig{}) {
		configs = append([]DriverConfig{m.Driver}, configs...)
	}
	names := driverNames(configs)

	type cand struct{ name, addr, manifest string }
	var found []cand
	for i, c := range configs {
		if strings.ToLower(c.Type) == typ {
			found = append(found, cand{names[i], addrOf(c), c.Manifest})
		}
	}
	pick := func(c cand, why string) (string, string, error) {
		return onlineTagFor(c.name, typ), fmt.Sprintf("%s's %s driver %q (%s)", ManifestName, typ, c.name, why), nil
	}
	if addr != "" {
		// An exact address first, so two emulators on one address told
		// apart by port resolve to the right one; then the loose match.
		for _, match := range []func(a, b string) bool{exactAddr, looseAddr} {
			for _, c := range found {
				if match(c.addr, addr) {
					return pick(c, key+" "+c.addr)
				}
			}
		}
	}
	if manifest != "" {
		for _, c := range found {
			if c.manifest != "" && path.Clean(c.manifest) == path.Clean(manifest) {
				return pick(c, "manifest "+c.manifest)
			}
		}
	}
	switch len(found) {
	case 0:
		return onlineTagFor("", typ), "no " + typ + " driver in " + ManifestName + "; the default driver name", nil
	case 1:
		return pick(found[0], "the only one")
	}
	list := make([]string, len(found))
	for i, c := range found {
		list[i] = fmt.Sprintf("%s (%s %s)", c.name, key, c.addr)
	}
	return "", "", fmt.Errorf("%s has %d %s drivers and none has %s %s: %s — %s",
		ManifestName, len(found), typ, key, addr, strings.Join(list, ", "), hint)
}

// sameHost compares two driver hosts, either of which may carry a port.
func sameHost(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	strip := func(h string) string {
		if hh, _, err := net.SplitHostPort(h); err == nil {
			return hh
		}
		return h
	}
	return strings.EqualFold(strip(a), strip(b))
}

// OnlineTagFor is onlineTagFor for a name given on the command line
// (`naut eip import --name`): the same reduction to an identifier Load
// applies, so the two cannot disagree.
func OnlineTagFor(driverName string) string { return onlineTagFor(driverName, "eip") }

// OPCUAOnlineTagFor is OnlineTagFor for `naut opcua import --name`.
func OPCUAOnlineTagFor(driverName string) string { return onlineTagFor(driverName, "opcua") }
