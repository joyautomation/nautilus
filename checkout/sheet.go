// Package checkout is the I/O checkout and commissioning sign-off record
// (#719): a sheet of the project's I/O points, and runs of results against
// it, each result a person's pass, fail or note with what the controller
// read at that instant.
//
// The sheet is project data, committed with the project (checkout.yaml).
// A run is a record kept on the controller in an append-only file, fsynced
// per entry and chained by SHA-256, like the audit journal's lines: an
// entry is never changed or removed, a later result for a point supersedes
// an earlier one in the view and leaves it in the file.
//
// It imports only the standard library. Who may record a result, force an
// output for a step or seal a run is the server's to decide before an entry
// exists (docs/design/context-map.md §2).
package checkout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Limits.
const (
	MaxID     = 128
	MaxNote   = 2000 // characters
	MaxReason = 256
)

// Point kinds: digital or analog, in or out.
const (
	DI = "DI"
	DO = "DO"
	AI = "AI"
	AO = "AO"
)

// Methods of checking a point.
const (
	// Observe reads the live value while someone changes the field (or the
	// virtual device); nothing is forced.
	Observe = "observe"
	// Force drives an output through the force table, under the checkout's
	// interlock (a later slice).
	Force = "force"
)

// Point is one I/O point to check.
type Point struct {
	// ID names the point on the sheet, unique: the tag's name when the sheet
	// is written by `naut checkout init`.
	ID string `yaml:"id" json:"id"`
	// Tag is the tag (or member path) the point reads or drives.
	Tag string `yaml:"tag" json:"tag"`
	// Device, Address, Equipment and Area say where it is: the field device
	// or module, its hardware address, the equipment it belongs to and the
	// auth area it falls in. Informational; Equipment is what an output's
	// checkout takes out of service.
	Device    string `yaml:"device" json:"device,omitempty"`
	Address   string `yaml:"address" json:"address,omitempty"`
	Equipment string `yaml:"equipment" json:"equipment,omitempty"`
	Area      string `yaml:"area" json:"area,omitempty"`
	// Kind is DI, DO, AI or AO.
	Kind string `yaml:"kind" json:"kind"`
	// Expect says what a pass looks like: a state or a range, in words.
	Expect string `yaml:"expect" json:"expect,omitempty"`
	// Method is observe or force; an input is only ever observed.
	Method string `yaml:"method" json:"method"`
	Desc   string `yaml:"desc" json:"desc,omitempty"`
}

// Sheet is the list of points to check.
type Sheet struct {
	// Title names the checkout on the exported sheet.
	Title  string  `yaml:"title" json:"title,omitempty"`
	Points []Point `yaml:"points" json:"points"`
}

// Output reports whether the kind is driven by the controller.
func Output(kind string) bool { return kind == DO || kind == AO }

// Validate reports every fault in the sheet, so the engineer fixes them
// in one pass.
func (s *Sheet) Validate() error {
	var errs []string
	if len(s.Points) == 0 {
		return errors.New("the sheet lists no points")
	}
	seen := map[string]bool{}
	for i, p := range s.Points {
		at := fmt.Sprintf("points[%d]", i)
		if p.ID != "" {
			at = fmt.Sprintf("point %q", p.ID)
		}
		switch {
		case p.ID == "":
			errs = append(errs, at+": no id")
		case utf8.RuneCountInString(p.ID) > MaxID:
			errs = append(errs, at+": the id is longer than 128 characters")
		case seen[p.ID]:
			errs = append(errs, at+": the id is used twice")
		}
		seen[p.ID] = true
		if p.Tag == "" {
			errs = append(errs, at+": no tag")
		}
		switch p.Kind {
		case DI, DO, AI, AO:
		default:
			errs = append(errs, at+fmt.Sprintf(": kind %q: want DI, DO, AI or AO", p.Kind))
		}
		switch p.Method {
		case Observe:
		case Force:
			if !Output(p.Kind) {
				errs = append(errs, at+": method force: only an output (DO or AO) is forced; an input is observed")
			}
		default:
			errs = append(errs, at+fmt.Sprintf(": method %q: want observe or force", p.Method))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// Hash is the sheet's content hash: SHA-256 of its canonical JSON (points in
// the order the sheet lists them), as hex. A run records it, so a result is
// tied to the sheet it was recorded against.
func (s *Sheet) Hash() string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Point returns the point with the id.
func (s *Sheet) Point(id string) (Point, bool) {
	for _, p := range s.Points {
		if p.ID == id {
			return p, true
		}
	}
	return Point{}, false
}

// Progress counts the points by their latest result, for a view by device
// or by area.
type Progress struct {
	Total   int `json:"total"`
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Noted   int `json:"noted"`
	Pending int `json:"pending"`
}

// ProgressBy groups the sheet's points by key (a device or an area; "" is
// reported as "unassigned") and counts the run's latest results in each.
func (s *Sheet) ProgressBy(key func(Point) string, latest map[string]Result) map[string]Progress {
	out := map[string]Progress{}
	for _, p := range s.Points {
		k := key(p)
		if k == "" {
			k = "unassigned"
		}
		pr := out[k]
		pr.Total++
		switch latest[p.ID].Outcome {
		case Pass:
			pr.Pass++
		case Fail:
			pr.Fail++
		case Note:
			pr.Noted++
		default:
			pr.Pending++
		}
		out[k] = pr
	}
	return out
}

// Keys returns the sorted keys of a progress map.
func Keys(m map[string]Progress) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
