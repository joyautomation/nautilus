package checkout

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ExportFormat names the JSON export's shape.
const ExportFormat = "nautilus-checkout-export/1"

// Export is a run as a record: the sheet it was begun against, and every
// result recorded in it (a fail before a pass is kept, not replaced by the
// latest), grouped by point in the sheet's order, results oldest first. The
// order is fixed, so the same run exports to the same bytes (a sealed record
// later signs these bytes' hash). It says in its own content that it is
// unsealed and, for a rehearsal, that it can never be signed.
type Export struct {
	Format string `json:"format"`
	// Sealed is false: nobody has signed this export (sealing is a later
	// step, and a rehearsal can never be sealed).
	Sealed bool `json:"sealed"`
	// Status says it in words, for the person who reads the file.
	Status    string `json:"status"`
	Rehearsal bool   `json:"rehearsal"`
	Run       Run    `json:"run"`
	// Sheet is the sheet's content hash the run was begun against, and
	// Points the points as the controller holds them now: if the sheet
	// changed since (SheetChanged), Orphans carries results for points it no
	// longer lists, so nothing recorded is dropped.
	Sheet        string        `json:"sheet"`
	SheetChanged bool          `json:"sheetChanged,omitempty"`
	Title        string        `json:"title,omitempty"`
	Points       []ExportPoint `json:"points"`
	Orphans      []Result      `json:"orphans,omitempty"`
}

// ExportPoint is a point and its results.
type ExportPoint struct {
	Point
	// Status is the latest result's outcome, or "not checked".
	Status  string   `json:"status"`
	Results []Result `json:"results"`
}

// NotChecked is the status of a point with no result.
const NotChecked = "not checked"

// StatusOf says what the export of a run is.
func StatusOf(r Run) string {
	switch {
	case r.Rehearsal:
		return "REHEARSAL, UNSEALED: a rehearsal run can never be signed"
	default:
		return "LIVE, UNSEALED: not signed off"
	}
}

// BuildExport builds the export of a run against the sheet the controller
// holds. results is every result of the run (Store.Results).
func BuildExport(run Run, sh *Sheet, results []Result) Export {
	ex := Export{Format: ExportFormat, Status: StatusOf(run), Rehearsal: run.Rehearsal, Run: run,
		Sheet: run.SheetHash, SheetChanged: run.SheetHash != sh.Hash(), Title: run.Title, Points: []ExportPoint{}}
	if ex.Title == "" {
		ex.Title = sh.Title
	}
	by := map[string][]Result{}
	for _, r := range results {
		by[r.Point] = append(by[r.Point], r)
	}
	known := map[string]bool{}
	for _, p := range sh.Points {
		known[p.ID] = true
		rs := by[p.ID]
		if rs == nil {
			rs = []Result{}
		}
		st := NotChecked
		if len(rs) > 0 {
			st = rs[len(rs)-1].Outcome
		}
		ex.Points = append(ex.Points, ExportPoint{Point: p, Status: st, Results: rs})
	}
	for _, r := range results { // results is in sequence order already
		if !known[r.Point] {
			ex.Orphans = append(ex.Orphans, r)
		}
	}
	return ex
}

// JSON is the export as indented JSON.
func (e Export) JSON() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// csvHeader is the CSV's columns: one row per result, and one row for a point
// with none, every row saying what the record is.
var csvHeader = []string{"record", "run", "point", "tag", "kind", "device", "address", "area", "equipment", "expect",
	"n", "seq", "at", "outcome", "by", "seen", "quality", "note", "forced", "force_seconds", "force_ended"}

// Neutralize makes a text cell safe to open in a spreadsheet: one that starts
// with = + - @ (or a tab or carriage return, which some programs strip before
// reading) would be a formula, so it gets a leading apostrophe. The check
// reads past leading spaces.
func Neutralize(s string) string {
	t := strings.TrimLeft(s, " ")
	if t == "" {
		return s
	}
	switch t[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// cell is a value as a cell: a number or boolean as it is, anything else as
// neutralized text.
func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case string:
		return Neutralize(x)
	default:
		b, _ := json.Marshal(x)
		return Neutralize(string(b))
	}
}

func at(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// CSV is the export as CSV (encoding/csv: quoting is the library's, formulas
// are neutralized by Neutralize).
func (e Export) CSV() ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	record := Neutralize(e.Status)
	put := func(row []string) error { return w.Write(row) }
	if err := put(csvHeader); err != nil {
		return nil, err
	}
	base := func(p Point) []string {
		return []string{record, Neutralize(e.Run.ID), Neutralize(p.ID), Neutralize(p.Tag), p.Kind, Neutralize(p.Device), Neutralize(p.Address),
			Neutralize(p.Area), Neutralize(p.Equipment), Neutralize(p.Expect)}
	}
	for _, p := range e.Points {
		if len(p.Results) == 0 {
			row := append(base(p.Point), "", "", "", NotChecked, "", "", "", "", "", "", "")
			if err := put(row); err != nil {
				return nil, err
			}
			continue
		}
		for i, r := range p.Results {
			if err := put(append(base(p.Point), resultCells(i+1, r)...)); err != nil {
				return nil, err
			}
		}
	}
	for i, r := range e.Orphans {
		row := append(base(Point{ID: r.Point}), resultCells(i+1, r)...)
		row[3] = "(not on the sheet now)"
		if err := put(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

func resultCells(n int, r Result) []string {
	forced, secs, ended := "", "", ""
	if r.Force != nil {
		forced, secs, ended = cell(r.Force.Value), strconv.Itoa(r.Force.Seconds), Neutralize(r.Force.Ended)
	}
	return []string{strconv.Itoa(n), strconv.FormatInt(r.Seq, 10), at(r.At), Neutralize(r.Outcome), Neutralize(r.By),
		cell(r.Seen), Neutralize(r.Quality), Neutralize(r.Note), forced, secs, ended}
}

// FileStem is a name for the export's file, built from the run's id alone
// (never a title or a note): letters, digits, dot, dash and underscore.
func FileStem(run string) string {
	var b strings.Builder
	for _, c := range run {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	return fmt.Sprintf("checkout-%s", b.String())
}
