package logixd

// Reading the FactoryTalk Linx topology, so nobody has to transcribe a comm
// path out of a GUI tree.
//
// A comm path is what `SetCommunicationsPath` takes, and getting one wrong
// does not fail cleanly — it fails as "cannot go online", which reads like a
// controller problem. The path is visible in the FT Linx Network Browser,
// and a person copies it by hand, backslashes and all.
//
// It is also fully recoverable from FT Linx's own configuration:
//
//	<bus name="AB_ETH-1">                        the driver
//	  <port address="10.0.0.5">                  the device on it
//	    <device name="Emulate 5580 Controller">
//	      <port name="Backplane">                how you reach its backplane
//	        <bus name="Emulate 1756 Backplane">
//	          <port address="0">                 the slot
//	            <device name="PlantCtl"/>        the controller sitting there
//
//	=> AB_ETH-1\10.0.0.5\Backplane\0   is  PlantCtl
//
// Parsing lives here rather than in the agent because the agent runs on the
// machine that is hardest to test on. logixd returns the file; this turns it
// into paths, and a committed fixture keeps it honest.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// CommPath is one controller FT Linx can reach, and the path that reaches it.
type CommPath struct {
	// Path is the string SetCommunicationsPath wants, e.g.
	// `AB_ETH-1\10.0.0.5\Backplane\0`.
	Path string `json:"path"`
	// Controller is the name of the controller in that slot, when FT Linx
	// has browsed one. This is the field that makes the listing useful:
	// it says which path is the controller you meant.
	Controller string `json:"controller,omitempty"`
	// Device is the module the path traverses (the Ethernet bridge or the
	// emulator), and Catalog its catalogue number.
	Device  string `json:"device,omitempty"`
	Catalog string `json:"catalog,omitempty"`
	// Driver is the FT Linx driver the path starts with.
	Driver string `json:"driver"`
}

// linxNode mirrors the parts of the FT Linx config we walk. The file nests
// bus → port → device → port → bus, so one recursive type covers it.
type linxNode struct {
	XMLName xml.Name
	Name    string     `xml:"name,attr"`
	Address string     `xml:"address,attr"`
	Catalog string     `xml:"CatalogNumber,attr"`
	Nodes   []linxNode `xml:",any"`
}

// CommPaths asks the agent for the FactoryTalk Linx configuration and
// returns every controller it can reach.
func (c *Client) CommPaths(ctx context.Context) ([]CommPath, error) {
	var r struct {
		Dir   string `json:"dir"`
		Files []struct {
			Name string `json:"name"`
			XML  string `json:"xml"`
		} `json:"files"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/v1/linx-config", nil, &r); err != nil {
		return nil, err
	}
	var out []CommPath
	var firstErr error
	parsed := 0
	seen := map[string]bool{}
	for _, f := range r.Files {
		paths, err := ParseLinxConfig([]byte(f.XML))
		if err != nil {
			// One unreadable instance must not hide the other: FT Linx
			// routinely has two, and only one may be configured. But if
			// NONE of them parse, that is a bug, not an empty site — say
			// so instead of reporting "no controllers found".
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", f.Name, err)
			}
			continue
		}
		parsed++
		for _, p := range paths {
			if seen[p.Path] {
				continue
			}
			seen[p.Path] = true
			out = append(out, p)
		}
	}
	if parsed == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// ParseLinxConfig extracts every reachable controller from one FT Linx
// configuration file.
func ParseLinxConfig(raw []byte) ([]CommPath, error) {
	doc, err := decodeUTF16(raw)
	if err != nil {
		return nil, err
	}
	var root linxNode
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = false
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("logixd: parsing FactoryTalk Linx config: %w", err)
	}
	var out []CommPath
	walkLinx(&root, nil, &out)
	return out, nil
}

// walkLinx descends the topology carrying the path built so far.
//
// Which attribute a <port> contributes depends on what it hangs off, and
// that is the whole subtlety of the format:
//
//   - a port under a BUS is a node ON that bus, so it contributes its
//     ADDRESS   (…\10.0.0.5\…)
//   - a port under a DEVICE is a way OUT of that device, so it contributes
//     its NAME   (…\Backplane\…)
//
// Get it backwards and you produce `AB_ETH-1\10.0.0.5\0\0`, which looks
// plausible and does not work.
//
// A path is only emitted where a DEVICE sits in a slot: a driver with no
// device browsed on it is configured, not reachable, and listing it would
// send someone to a dead end.
func walkLinx(n *linxNode, trail []string, out *[]CommPath) {
	parent := n.XMLName.Local
	for i := range n.Nodes {
		child := &n.Nodes[i]
		switch child.XMLName.Local {
		case "bus":
			// The outermost bus names the driver (AB_ETH-1). A deeper one
			// is a backplane, which the port above it has already named.
			next := trail
			if len(trail) == 0 && child.Name != "" {
				next = []string{child.Name}
			}
			walkLinx(child, next, out)
		case "port":
			next := trail
			if len(trail) > 0 {
				var seg string
				if parent == "device" {
					seg = child.Name
				} else {
					seg = child.Address
				}
				if seg != "" {
					next = append(append([]string{}, trail...), seg)
				}
			}
			walkLinx(child, next, out)
		case "device":
			// trail is driver\address\portName\slot — four segments — by
			// the time a device is a controller in a slot.
			if len(trail) >= 4 && child.Name != "" {
				*out = append(*out, CommPath{
					Path:       strings.Join(trail, `\`),
					Controller: child.Name,
					Catalog:    child.Catalog,
					Driver:     trail[0],
				})
			}
			walkLinx(child, trail, out)
		default:
			walkLinx(child, trail, out)
		}
	}
}

// decodeUTF16 converts the config to UTF-8. FT Linx writes UTF-16LE with a
// BOM, which Go's XML decoder will not read; a file that is already UTF-8
// passes through untouched.
func decodeUTF16(raw []byte) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == 0xff && raw[1] == 0xfe {
		if len(raw)%2 != 0 {
			return nil, fmt.Errorf("logixd: truncated UTF-16 configuration")
		}
		u := make([]uint16, 0, len(raw)/2-1)
		for i := 2; i+1 < len(raw); i += 2 {
			u = append(u, uint16(raw[i])|uint16(raw[i+1])<<8)
		}
		var b bytes.Buffer
		for _, r := range utf16.Decode(u) {
			b.WriteRune(r)
		}
		// The declared encoding is now a lie, and Go's decoder believes it.
		return fixDeclaredEncoding(b.Bytes()), nil
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("logixd: FactoryTalk Linx config is neither UTF-8 nor UTF-16")
	}
	// Already UTF-8 — but very likely still DECLARING utf-16, because the
	// agent read the file into a string and handed it over as JSON. Go's
	// decoder believes the declaration and refuses.
	return fixDeclaredEncoding(raw), nil
}

// fixDeclaredEncoding rewrites an XML declaration that claims UTF-16 on
// bytes that are now UTF-8. Both paths into the parser need it: the file
// read straight off disk, and the same file after a round trip through the
// agent's JSON.
func fixDeclaredEncoding(b []byte) []byte {
	for _, enc := range [][]byte{[]byte(`encoding="utf-16"`), []byte(`encoding="UTF-16"`)} {
		b = bytes.Replace(b, enc, []byte(`encoding="utf-8"`), 1)
	}
	return b
}
