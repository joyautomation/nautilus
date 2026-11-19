package iolink

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/iolink/iodd"
)

// A port device is the struct a sensor on a master's port is delivered as
// (docs/design/iolink.md §4.1): one member per process data in item,
// typed by the IODD, plus PdValid when the master says whether its image
// is valid. Its process data out items are written through command tags.

// Member is one member of a port device's struct.
type Member struct {
	Name string
	// Type is the IEC type name (BOOL, USINT … LREAL, STRING).
	Type string
	Unit string
	Desc string
	// Item indexes the process data in image's items; -1 for PdValid.
	Item int
}

// Command is one process data out item's command tag (without the port
// device's name): <Port device>_<Name>.
type Command struct {
	Name string
	Type string
	Unit string
	Desc string
	// Item indexes the process data out image's items.
	Item int
}

// Device is what an IODD derives for one port.
type Device struct {
	// TypeName is the struct type's name, from the IODD's file name.
	TypeName string
	IODD     *iodd.IODD
	In, Out  *iodd.Image
	Members  []Member
	Commands []Command
}

var (
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
	notIdent   = regexp.MustCompile(`[^A-Za-z0-9_]+`)
)

// TypeNameOf is the struct type's name for an IODD file: the standard name
// <Vendor>-<Device>-<date>-IODD<version>.xml loses its date and version
// and is upper-cased with every non-identifier run an underscore
// (acme-level100-20261118-IODD1.1.xml is ACME_LEVEL100).
func TypeNameOf(file string) (string, error) {
	base := path.Base(file)
	base = strings.TrimSuffix(base, path.Ext(base))
	if i := strings.LastIndex(strings.ToUpper(base), "-IODD"); i >= 0 {
		base = base[:i]
	}
	base = dateSuffix.ReplaceAllString(base, "")
	name := strings.Trim(notIdent.ReplaceAllString(strings.ToUpper(base), "_"), "_")
	if !devprofile.IsIdent(name) {
		return "", fmt.Errorf("the IODD file name %s gives %q, which is not an IEC identifier", file, name)
	}
	return name, nil
}

// itemName is an identifier made from an item's text, or Item<subindex>
// when the text is empty, makes no identifier, or collides.
func itemName(it iodd.Item, taken map[string]bool) string {
	name := strings.Trim(notIdent.ReplaceAllString(it.Name, "_"), "_")
	if !devprofile.IsIdent(name) || taken[strings.ToLower(name)] {
		name = fmt.Sprintf("Item%d", it.Subindex)
	}
	taken[strings.ToLower(name)] = true
	return name
}

// Derive derives a port device from an IODD (file is its name in iodd/).
// pdValid says the master profile has a pd-valid point, which adds PdValid.
// An IODD whose process data layout depends on a parameter, or has an item
// the codec does not carry as a member (an octet string or a type it does
// not decode), is refused naming the item.
func Derive(file string, d *iodd.IODD, pdValid bool) (*Device, error) {
	tn, err := TypeNameOf(file)
	if err != nil {
		return nil, err
	}
	pd, err := d.Layout("")
	if err != nil {
		return nil, err
	}
	dev := &Device{TypeName: tn, IODD: d, In: pd.In, Out: pd.Out}
	if pd.In == nil {
		return nil, fmt.Errorf("the IODD declares no process data in")
	}
	if err := pd.In.Check(); err != nil {
		return nil, fmt.Errorf("process data in: %w", err)
	}
	taken := map[string]bool{"pdvalid": true}
	for i, it := range pd.In.Items {
		typ := it.IECType()
		if typ == "" || strings.HasPrefix(typ, "ARRAY") {
			return nil, fmt.Errorf("process data in item %q (%s): not carried as a member yet", it.Name, it.Type.Name)
		}
		dev.Members = append(dev.Members, Member{Name: itemName(it, taken), Type: typ, Unit: it.Unit, Desc: it.Name, Item: i})
	}
	if pdValid {
		dev.Members = append(dev.Members, Member{Name: "PdValid", Type: "BOOL", Desc: "the master holds valid process data in", Item: -1})
	}
	if pd.Out != nil {
		if err := pd.Out.Check(); err != nil {
			return nil, fmt.Errorf("process data out: %w", err)
		}
		taken := map[string]bool{}
		for i, it := range pd.Out.Items {
			typ := it.IECType()
			if typ == "" || strings.HasPrefix(typ, "ARRAY") {
				return nil, fmt.Errorf("process data out item %q (%s): not carried as a command tag yet", it.Name, it.Type.Name)
			}
			dev.Commands = append(dev.Commands, Command{Name: itemName(it, taken), Type: typ, Unit: it.Unit, Desc: it.Name, Item: i})
		}
	}
	return dev, nil
}

// Decode decodes the carrier value a master delivers for a port's process
// data in (in the carrier's form) into the struct's members, in Members
// order: bool, int64, float64 or string by member type. pdValid is the
// master's flag, used only when the device has the PdValid member.
func (dev *Device) Decode(form string, carrier any, pdValid bool) ([]any, error) {
	raw, err := Image(form, carrier, dev.In.Octets())
	if err != nil {
		return nil, err
	}
	vals, err := dev.In.Decode(raw)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(dev.Members))
	for i, m := range dev.Members {
		if m.Item < 0 {
			out[i] = pdValid
			continue
		}
		switch x := vals[m.Item].(type) {
		case uint64:
			out[i] = int64(x)
		case float32:
			out[i] = float64(x)
		default:
			out[i] = x
		}
	}
	return out, nil
}
