// Package rationalize is the master alarm database and the ISA-18.2
// performance reports (#715).
//
// The master is a flat table, one row per alarm, of the columns a
// rationalization team reviews: identity, priority, consequence, operator
// response and review state. It is exported from the manifest's expanded
// definitions (so a spreadsheet or a Git diff can carry it) and checked
// back against them, so a master the team signed off cannot drift from
// what the controller runs without someone seeing it.
//
// The reports read alarm journal events and compute the ISA-18.2 metrics:
// alarm rate and floods, chattering alarms, the most frequent ("bad
// actor") alarms, and stale alarms. Everything here is pure: the caller
// hands it definitions or events, and nothing is opened.
package rationalize

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/alarm"
)

// Row is one alarm in the master. Every column is text, so a CSV and a
// YAML master hold exactly the same thing.
type Row struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Tag         string `yaml:"tag"`
	Priority    string `yaml:"priority"`
	Class       string `yaml:"class,omitempty"`
	Site        string `yaml:"site,omitempty"`
	Area        string `yaml:"area,omitempty"`
	Consequence string `yaml:"consequence,omitempty"`
	Response    string `yaml:"response,omitempty"`
	State       string `yaml:"state"`
	By          string `yaml:"by,omitempty"`
	At          string `yaml:"at,omitempty"`
	// Source is where the definition is declared: the manifest, an alarm
	// file, or the rule that generated it. It tells a reviewer where to
	// make a change and is never compared.
	Source string `yaml:"source,omitempty"`
}

// columns is the CSV header, in order. Compared columns are every one but
// source.
var columns = []string{
	"id", "name", "tag", "priority", "class", "site", "area",
	"consequence", "response", "state", "by", "at", "source",
}

func (r Row) values() []string {
	return []string{
		r.ID, r.Name, r.Tag, r.Priority, r.Class, r.Site, r.Area,
		r.Consequence, r.Response, r.State, r.By, r.At, r.Source,
	}
}

func rowFrom(v []string) Row {
	return Row{
		ID: v[0], Name: v[1], Tag: v[2], Priority: v[3], Class: v[4], Site: v[5], Area: v[6],
		Consequence: v[7], Response: v[8], State: v[9], By: v[10], At: v[11], Source: v[12],
	}
}

// Rows is the master for a set of expanded definitions, sorted by id.
func Rows(defs []alarm.Def) []Row {
	out := make([]Row, 0, len(defs))
	for _, d := range defs {
		r := d.Rationalization
		out = append(out, Row{
			ID: d.ID, Name: d.Name, Tag: d.Tag, Priority: d.Priority.String(),
			Class: d.Class, Site: d.Site, Area: d.Area,
			Consequence: d.Consequence, Response: d.Response,
			State: string(r.Effective()), By: r.By, At: r.At,
			Source: d.Source,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// WriteCSV writes the master as CSV with a header row. encoding/csv quotes
// a field holding a comma, a quote or a newline, so a multi-line response
// survives a spreadsheet round trip.
func WriteCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(columns); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write(r.values()); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// ReadCSV reads a master written by WriteCSV, or edited in a spreadsheet.
// The header names the columns, so a spreadsheet that reordered them still
// reads; an unknown column or a missing id column is an error, since a
// renamed header would otherwise read as an empty column.
func ReadCSV(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err == io.EOF {
		return nil, errors.New("empty master: want a header row (" + strings.Join(columns, ",") + ")")
	}
	if err != nil {
		return nil, err
	}
	pos := make([]int, len(columns))
	for i := range pos {
		pos[i] = -1
	}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		k := indexOf(columns, h)
		if k < 0 {
			return nil, fmt.Errorf("master header: unknown column %q (known: %s)", h, strings.Join(columns, ", "))
		}
		if pos[k] >= 0 {
			return nil, fmt.Errorf("master header: column %q appears twice", h)
		}
		pos[k] = i
	}
	if pos[0] < 0 {
		return nil, errors.New("master header: no id column")
	}
	var out []Row
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		v := make([]string, len(columns))
		for k, p := range pos {
			if p >= 0 && p < len(rec) {
				v[k] = rec[p]
			}
		}
		row := rowFrom(v)
		if strings.TrimSpace(row.ID) == "" {
			if strings.TrimSpace(strings.Join(rec, "")) == "" {
				continue // a blank spreadsheet row
			}
			return nil, fmt.Errorf("master line %d: no id", line)
		}
		out = append(out, row)
	}
	return out, nil
}

func indexOf(set []string, v string) int {
	for i, s := range set {
		if s == v {
			return i
		}
	}
	return -1
}

// WriteYAML writes the master as a YAML list.
func WriteYAML(w io.Writer, rows []Row) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if rows == nil {
		rows = []Row{}
	}
	if err := enc.Encode(rows); err != nil {
		return err
	}
	return enc.Close()
}

// ReadYAML reads a master written by WriteYAML. Unknown keys are refused,
// for the same reason as ReadCSV's unknown columns.
func ReadYAML(r io.Reader) ([]Row, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var out []Row
	if err := dec.Decode(&out); err != nil && err != io.EOF {
		return nil, err
	}
	for i, row := range out {
		if strings.TrimSpace(row.ID) == "" {
			return nil, fmt.Errorf("master entry %d: no id", i)
		}
	}
	return out, nil
}

// Difference kinds.
const (
	OnlyInMaster   = "only-in-master"   // the master lists an alarm the manifest no longer declares
	OnlyInManifest = "only-in-manifest" // the manifest declares an alarm the master has never seen
	Differs        = "differs"          // one column differs
	Duplicate      = "duplicate"        // the master lists an id twice
)

// Difference is one way the master and the manifest disagree.
type Difference struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Field    string `json:"field,omitempty"`
	Master   string `json:"master,omitempty"`
	Manifest string `json:"manifest,omitempty"`
	// Source is where the manifest declares the alarm, so the reader knows
	// which file (or rule) to change.
	Source string `json:"source,omitempty"`
}

func (d Difference) String() string {
	switch d.Kind {
	case OnlyInMaster:
		return fmt.Sprintf("%s: in the master, not in the manifest", d.ID)
	case OnlyInManifest:
		return fmt.Sprintf("%s: in the manifest (%s), not in the master", d.ID, d.Source)
	case Duplicate:
		return fmt.Sprintf("%s: listed more than once in the master", d.ID)
	}
	return fmt.Sprintf("%s: %s: master %q, manifest %q (%s)", d.ID, d.Field, d.Master, d.Manifest, d.Source)
}

// Compare lists every difference between a master and the manifest's own
// rows (from Rows), sorted by id then column order.
//
// Text columns compare after trimming surrounding space and normalising
// line endings, which a spreadsheet changes on its own; priority and state
// compare case-insensitively, with an empty state read as pending.
// Source is never compared.
func Compare(master, manifest []Row) []Difference {
	var out []Difference
	byID := make(map[string]Row, len(master))
	for _, r := range master {
		id := strings.TrimSpace(r.ID)
		if _, dup := byID[id]; dup {
			out = append(out, Difference{ID: id, Kind: Duplicate})
			continue
		}
		byID[id] = r
	}
	seen := make(map[string]bool, len(manifest))
	for _, m := range manifest {
		seen[m.ID] = true
		r, ok := byID[m.ID]
		if !ok {
			out = append(out, Difference{ID: m.ID, Kind: OnlyInManifest, Source: m.Source})
			continue
		}
		mv, rv := m.values(), r.values()
		for k := 1; k < len(columns)-1; k++ { // id matched; source is not compared
			if !sameColumn(columns[k], rv[k], mv[k]) {
				out = append(out, Difference{
					ID: m.ID, Kind: Differs, Field: columns[k],
					Master: rv[k], Manifest: mv[k], Source: m.Source,
				})
			}
		}
	}
	for id := range byID {
		if !seen[id] {
			out = append(out, Difference{ID: id, Kind: OnlyInMaster})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sameColumn(col, a, b string) bool {
	a, b = normText(a), normText(b)
	switch col {
	case "priority":
		return strings.EqualFold(a, b)
	case "state":
		if a == "" {
			a = string(alarm.RationalizationPending)
		}
		if b == "" {
			b = string(alarm.RationalizationPending)
		}
		return strings.EqualFold(a, b)
	}
	return a == b
}

func normText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// Coverage counts the definitions in each rationalization state and names
// the high and critical ones still pending: the ones ISA-18.2 expects a
// team to have reviewed first.
type Coverage struct {
	Total    int `json:"total"`
	Pending  int `json:"pending"`
	Reviewed int `json:"reviewed"`
	Approved int `json:"approved"`
	// NoResponse counts alarms with no operator response written down: an
	// alarm with nothing to do is, by ISA-18.2's definition, not an alarm.
	NoResponse  int      `json:"noResponse"`
	PendingHigh []string `json:"pendingHigh,omitempty"`
}

// Cover summarises a set of definitions.
func Cover(defs []alarm.Def) Coverage {
	c := Coverage{Total: len(defs)}
	for _, d := range defs {
		switch d.Rationalization.Effective() {
		case alarm.RationalizationReviewed:
			c.Reviewed++
		case alarm.RationalizationApproved:
			c.Approved++
		default:
			c.Pending++
			if d.Priority >= alarm.High {
				c.PendingHigh = append(c.PendingHigh, d.ID)
			}
		}
		if strings.TrimSpace(d.Response) == "" {
			c.NoResponse++
		}
	}
	sort.Strings(c.PendingHigh)
	return c
}
