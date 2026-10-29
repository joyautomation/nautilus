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
	raw, err := fs.ReadFile(fsys, ManifestName)
	if errors.Is(err, fs.ErrNotExist) {
		return onlineTagFor("", "eip"), "no " + ManifestName + "; the default driver name", nil
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

	type cand struct{ name, host, manifest string }
	var eips []cand
	for i, c := range configs {
		if strings.ToLower(c.Type) == "eip" {
			eips = append(eips, cand{names[i], c.Host, c.Manifest})
		}
	}
	pick := func(c cand, why string) (string, string, error) {
		return onlineTagFor(c.name, "eip"), fmt.Sprintf("%s's eip driver %q (%s)", ManifestName, c.name, why), nil
	}
	if host != "" {
		// An exact host first, so two emulators on one address told apart
		// by port resolve to the right one; then host without port.
		for _, exact := range []bool{true, false} {
			for _, c := range eips {
				if exact && strings.EqualFold(c.host, host) || !exact && sameHost(c.host, host) {
					return pick(c, "host "+c.host)
				}
			}
		}
	}
	if manifest != "" {
		for _, c := range eips {
			if c.manifest != "" && path.Clean(c.manifest) == path.Clean(manifest) {
				return pick(c, "manifest "+c.manifest)
			}
		}
	}
	switch len(eips) {
	case 0:
		return onlineTagFor("", "eip"), "no eip driver in " + ManifestName + "; the default driver name", nil
	case 1:
		return pick(eips[0], "the only one")
	}
	list := make([]string, len(eips))
	for i, c := range eips {
		list[i] = fmt.Sprintf("%s (host %s)", c.name, c.host)
	}
	return "", "", fmt.Errorf("%s has %d eip drivers and none has host %s: %s — pass --name with the one this controller is",
		ManifestName, len(eips), host, strings.Join(list, ", "))
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
