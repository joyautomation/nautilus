// `naut alarms master` and `naut alarms report`: ISA-18.2 rationalization
// (#715). The master is the table a rationalization team reviews (one row
// per alarm: priority, consequence, operator response, review state),
// exported from the manifest and checked back against it. The reports read
// the alarm journal and compute the alarm rate and floods, chattering
// alarms, the most frequent alarms and stale alarms.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/rationalize"
	"github.com/joyautomation/nautilus/internal/apiclient"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

const alarmsMasterUsage = `naut alarms master — the master alarm database (ISA-18.2 rationalization)

Usage:
  naut alarms master export [dir] [-f master.csv] [-o csv|yaml]
      Write one row per alarm the manifest declares: id, name, tag,
      priority, class, site, area, consequence, response, and the
      rationalization state, by and at. source says where each alarm is
      declared (the manifest, an alarm file, or the rule that generated
      it). Sorted by id. The format follows -f's extension, else -o
      (default csv).
  naut alarms master check [dir] -f master.csv
      Compare a master (CSV or YAML, by extension) with the manifest and
      list every difference: an alarm only one side has, or a column that
      differs. Exit 1 if anything differs, 2 if the check could not run.

Both print how many alarms are pending, reviewed and approved, and name
the high and critical alarms still pending.
`

const alarmsReportUsage = `naut alarms report — ISA-18.2 alarm performance from the journal

Usage:
  naut alarms report flood|chatter|bad-actors|stale  (-journal FILE | -host HOST [-port 8080]) [flags]

Reports:
  flood        alarm rate per 10 minutes and every flood (more than 10
               alarms in a 10-minute window; consecutive windows are one)
  chatter      alarms that activate 3 or more times within 60 seconds
  bad-actors   the 10 most frequent alarms and their share of all alarms
  stale        alarms continuously active for more than 24 hours

Where the journal comes from:
  -journal FILE  a file journal (alarms.journal.sink: file), read with its
                 rotated generation FILE.1; nothing is created
  -host HOST     a running controller's journal API (any sink)

Flags:
  -from, -to     the range: RFC3339, "YYYY-MM-DD" or "YYYY-MM-DD HH:MM"
                 (local time); -to defaults to now
  -since 7d      the range's length when -from is not given (default 7d;
                 90d for stale, which needs to see when alarms began)
  -site, -priority   only these alarms
  -top N         how many alarms bad-actors names (default 10)
  -o table|json

An alarm's activation is counted once per occurrence: a controller
restart journals a standing alarm as active again, and that is not
counted as a new alarm.
`

func runAlarmsMaster(args []string) int {
	return alarmsMaster(args, os.Stdout, os.Stderr)
}

func alarmsMaster(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, alarmsMasterUsage)
		return 2
	}
	switch args[0] {
	case "export", "check":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, alarmsMasterUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "naut alarms master: unknown command %q\n\n%s", args[0], alarmsMasterUsage)
		return 2
	}
	verb := args[0]
	fs := flag.NewFlagSet("alarms master "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifest := fs.String("m", "", manifestFlagUsage)
	file := fs.String("f", "", "the master file")
	format := fs.String("o", "csv", "export format when -f has no .csv/.yaml extension: csv or yaml")
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return 2
	}
	if len(pos) > 1 {
		fmt.Fprintf(stderr, "naut alarms master %s: one project directory, got %q\n", verb, pos)
		return 2
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	defs, err := loadAlarmDefs(dir, *manifest)
	if err != nil {
		fmt.Fprintln(stderr, "naut alarms master:", err)
		return 2
	}
	rows := rationalize.Rows(defs)

	yamlFormat := isYAMLPath(*file)
	if *file == "" || filepath.Ext(*file) != ".csv" && !yamlFormat {
		switch *format {
		case "csv":
		case "yaml":
			yamlFormat = true
		default:
			fmt.Fprintf(stderr, "naut alarms master: -o %q: want csv or yaml\n", *format)
			return 2
		}
	}

	if verb == "export" {
		var b bytes.Buffer
		if yamlFormat {
			err = rationalize.WriteYAML(&b, rows)
		} else {
			err = rationalize.WriteCSV(&b, rows)
		}
		if err != nil {
			fmt.Fprintln(stderr, "naut alarms master export:", err)
			return 2
		}
		if *file == "" {
			_, _ = stdout.Write(b.Bytes())
		} else if err := os.WriteFile(*file, b.Bytes(), 0o644); err != nil {
			fmt.Fprintln(stderr, "naut alarms master export:", err)
			return 2
		} else {
			fmt.Fprintf(stderr, "wrote %d alarm(s) to %s\n", len(rows), *file)
		}
		printMasterCoverage(stderr, rationalize.Cover(defs))
		return 0
	}

	if *file == "" {
		fmt.Fprintln(stderr, "naut alarms master check: -f is required (the master to compare, CSV or YAML)")
		return 2
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintln(stderr, "naut alarms master check:", err)
		return 2
	}
	var master []rationalize.Row
	if yamlFormat {
		master, err = rationalize.ReadYAML(bytes.NewReader(raw))
	} else {
		master, err = rationalize.ReadCSV(bytes.NewReader(raw))
	}
	if err != nil {
		fmt.Fprintf(stderr, "naut alarms master check: %s: %v\n", *file, err)
		return 2
	}
	diffs := rationalize.Compare(master, rows)
	for _, d := range diffs {
		fmt.Fprintln(stdout, d)
	}
	printMasterCoverage(stdout, rationalize.Cover(defs))
	if len(diffs) > 0 {
		fmt.Fprintf(stdout, "%d difference(s) between %s and the manifest\n", len(diffs), *file)
		return 1
	}
	fmt.Fprintf(stdout, "%s matches the manifest (%d alarms)\n", *file, len(rows))
	return 0
}

func isYAMLPath(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".yaml" || ext == ".yml"
}

func printMasterCoverage(w io.Writer, c rationalize.Coverage) {
	fmt.Fprintf(w, "rationalization: %d alarms, %d approved, %d reviewed, %d pending; %d with no operator response\n",
		c.Total, c.Approved, c.Reviewed, c.Pending, c.NoResponse)
	if len(c.PendingHigh) > 0 {
		names := c.PendingHigh
		more := ""
		if len(names) > 10 {
			more = fmt.Sprintf(" and %d more", len(names)-10)
			names = names[:10]
		}
		fmt.Fprintf(w, "high and critical still pending: %s%s\n", strings.Join(names, ", "), more)
	}
}

// loadAlarmDefs is the offline pass `naut alarms list` runs: load the
// project, compile its types (a rule matches a struct type), and expand
// the alarm rules and files. Nothing is opened.
func loadAlarmDefs(dir, manifest string) ([]alarm.Def, error) {
	proj, err := project.Load(os.DirFS(dir), manifest)
	if err != nil {
		return nil, err
	}
	if proj.Alarms == nil {
		return nil, fmt.Errorf("%s declares no `alarms:` section", manifestLabel(manifest))
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	return proj.AlarmDefs(rt)
}

func runAlarmsReport(args []string) int {
	return alarmsReport(args, time.Now(), os.Stdout, os.Stderr)
}

// reportRead is what a report read: shown above every report, so a
// journal that holds only the last few hours (the in-memory ring, or a
// rotated file) cannot read as "no floods this week".
type reportRead struct {
	Source   string     `json:"source"`
	Events   int        `json:"events"`
	Earliest *time.Time `json:"earliest,omitempty"`
	Latest   *time.Time `json:"latest,omitempty"`
	From     time.Time  `json:"from"`
	To       time.Time  `json:"to"`
}

func alarmsReport(args []string, now time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, alarmsReportUsage)
		return 2
	}
	kind := args[0]
	switch kind {
	case "flood", "chatter", "bad-actors", "stale":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, alarmsReportUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "naut alarms report: unknown report %q\n\n%s", kind, alarmsReportUsage)
		return 2
	}
	fs := flag.NewFlagSet("alarms report "+kind, flag.ContinueOnError)
	fs.SetOutput(stderr)
	journal := fs.String("journal", "", "a file journal to read")
	host := fs.String("host", "", "a controller whose journal API to read")
	port := fs.Int("port", 8080, "tag API port")
	fromS := fs.String("from", "", "start of the range")
	toS := fs.String("to", "", "end of the range (default now)")
	sinceDefault := "7d"
	if kind == "stale" {
		sinceDefault = "90d"
	}
	sinceS := fs.String("since", sinceDefault, "the range's length when -from is not given")
	site := fs.String("site", "", "only alarms at these sites (comma-separated)")
	priority := fs.String("priority", "", "only alarms at these priorities (comma-separated)")
	topN := fs.Int("top", rationalize.BadActorTop, "how many alarms bad-actors names")
	format := fs.String("o", "table", "table or json")
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "naut alarms report %s: %v\n", kind, err)
		return 2
	}
	if len(pos) > 0 {
		return fail(fmt.Errorf("unexpected argument %q (name the journal with -journal or -host)", pos[0]))
	}
	if (*journal == "") == (*host == "") {
		return fail(errors.New("name the journal: -journal FILE or -host HOST (one of them)"))
	}
	if *format != "table" && *format != "json" {
		return fail(fmt.Errorf("-o %q: want table or json", *format))
	}
	to := now
	if *toS != "" {
		if to, err = parseReportTime(*toS); err != nil {
			return fail(fmt.Errorf("-to: %w", err))
		}
	}
	var from time.Time
	if *fromS != "" {
		if from, err = parseReportTime(*fromS); err != nil {
			return fail(fmt.Errorf("-from: %w", err))
		}
	} else {
		d, err := parseSince(*sinceS)
		if err != nil {
			return fail(fmt.Errorf("-since: %w", err))
		}
		from = to.Add(-d)
	}
	if !to.After(from) {
		return fail(errors.New("-from must be before -to"))
	}

	filter := alarm.Filter{Sites: splitList(*site), Priorities: splitList(*priority)}
	var events []alarm.Event
	read := reportRead{From: from, To: to}
	if *journal != "" {
		read.Source = *journal
		all, err := alarm.ReadFileJournal(*journal)
		if err != nil {
			return fail(err)
		}
		for _, e := range all {
			if e.TS >= from.UnixMilli() && e.TS < to.UnixMilli() && filter.Match(e) {
				events = append(events, e)
			}
		}
	} else {
		base, err := apiclient.BaseURL(*host, *port)
		if err != nil {
			return fail(err)
		}
		read.Source = base
		if events, err = fetchJournal(base, from, to, filter); err != nil {
			return fail(err)
		}
	}
	read.Events = len(events)
	if len(events) > 0 {
		first, last := events[0].Time(), events[len(events)-1].Time()
		read.Earliest, read.Latest = &first, &last
	}

	acts := rationalize.Activations(events)
	var rep any
	switch kind {
	case "flood":
		rep = rationalize.Floods(acts, from, to, rationalize.FloodWindow, rationalize.FloodThreshold)
	case "chatter":
		rep = rationalize.Chattering(acts, rationalize.ChatterWindow, rationalize.ChatterCount)
	case "bad-actors":
		rep = struct {
			Activations int                    `json:"activations"`
			Top         []rationalize.BadActor `json:"top"`
		}{len(acts), rationalize.BadActors(acts, *topN)}
	case "stale":
		rep = rationalize.StaleAlarms(events, to, rationalize.StaleAfter)
	}

	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			Read   reportRead `json:"read"`
			Report any        `json:"report"`
		}{read, rep}); err != nil {
			return fail(err)
		}
		return 0
	}

	printRead(stdout, read)
	switch r := rep.(type) {
	case rationalize.FloodReport:
		printFloods(stdout, r)
	case []rationalize.Chatter:
		printChatter(stdout, r)
	case []rationalize.Stale:
		printStale(stdout, r, to)
	default:
		printBadActors(stdout, rationalize.BadActors(acts, *topN), len(acts))
	}
	return 0
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseReportTime takes RFC3339, a day, or a day and a minute in local time.
func parseReportTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q: want RFC3339, YYYY-MM-DD or \"YYYY-MM-DD HH:MM\"", s)
}

// parseSince is time.ParseDuration plus whole days ("7d").
func parseSince(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("%q: want e.g. 7d or 12h", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q: want e.g. 7d or 12h", s)
	}
	return d, nil
}

// journalPage is the most events one journal request asks for. The API
// says when more match (truncated), and fetchJournal then halves the range.
var journalPage = 50_000

// fetchJournal reads [from, to) from a controller's journal API, oldest
// first. Only the kinds the reports need are asked for, so the journal
// filters them itself.
//
// The API's range is inclusive at both ends, so each request asks for
// [lo, hi-1 ms] and adjacent ranges never share an event. A range that
// holds more than one page is split in half and each half read, which is
// exactly what a flood needs: the busiest day is the one that matters.
func fetchJournal(base string, from, to time.Time, f alarm.Filter) ([]alarm.Event, error) {
	client := http.Client{Timeout: 30 * time.Second}
	var get func(lo, hi int64) ([]alarm.Event, error)
	get = func(lo, hi int64) ([]alarm.Event, error) {
		q := url.Values{}
		q.Set("from", strconv.FormatInt(lo, 10))
		q.Set("to", strconv.FormatInt(hi-1, 10))
		q.Set("kind", strings.Join([]string{alarm.KindActive, alarm.KindRTN, alarm.KindSuppress}, ","))
		q.Set("limit", strconv.Itoa(journalPage))
		if len(f.Sites) > 0 {
			q.Set("site", strings.Join(f.Sites, ","))
		}
		if len(f.Priorities) > 0 {
			q.Set("priority", strings.Join(f.Priorities, ","))
		}
		u := base + "/api/alarms/journal?" + q.Encode()
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		apiclient.Authorize(req)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("no controller at %s: %w", base, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s/api/alarms/journal returned %s: %s", base, resp.Status, strings.TrimSpace(string(body)))
		}
		var page struct {
			Events    []alarm.Event `json:"events"`
			Truncated bool          `json:"truncated"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("bad journal response from %s: %w", base, err)
		}
		if page.Truncated {
			if hi-lo <= 1000 {
				return nil, fmt.Errorf("more than %d journal events within one second at %s; "+
					"read the journal file instead", journalPage, time.UnixMilli(lo).Format(time.RFC3339))
			}
			mid := lo + (hi-lo)/2
			older, err := get(lo, mid)
			if err != nil {
				return nil, err
			}
			newer, err := get(mid, hi)
			if err != nil {
				return nil, err
			}
			return append(older, newer...), nil
		}
		slices.Reverse(page.Events) // the API answers newest first
		return page.Events, nil
	}
	return get(from.UnixMilli(), to.UnixMilli())
}

const reportTime = "2006-01-02 15:04"

func printRead(w io.Writer, r reportRead) {
	fmt.Fprintf(w, "journal %s: %d events from %s to %s", r.Source, r.Events,
		r.From.Format(reportTime), r.To.Format(reportTime))
	if r.Earliest != nil {
		fmt.Fprintf(w, " (earliest %s, latest %s)", r.Earliest.Format(reportTime), r.Latest.Format(reportTime))
	}
	fmt.Fprintln(w)
	if r.Earliest != nil && r.Earliest.Sub(r.From) > 24*time.Hour {
		fmt.Fprintf(w, "note: the journal holds nothing before %s; the range before that is not measured\n",
			r.Earliest.Format(reportTime))
	}
	if r.Events == 0 {
		fmt.Fprintln(w, "note: no events in this range; check the journal (a ring-only journal keeps only recent events)")
	}
	fmt.Fprintln(w)
}

func printFloods(w io.Writer, r rationalize.FloodReport) {
	fmt.Fprintf(w, "alarm rate, %s windows, flood = more than %d in a window\n", shortDur(r.Window), r.Threshold)
	fmt.Fprintf(w, "  alarms          %d (%.1f per hour, %.2f per window; ISA-18.2: about 1 per 10 min is likely acceptable, 2 is the most manageable)\n",
		r.Activations, r.PerHour, r.PerWindow)
	if r.Max > 0 {
		fmt.Fprintf(w, "  busiest window  %d at %s\n", r.Max, r.MaxAt.Format(reportTime))
	}
	fmt.Fprintf(w, "  in flood        %d of %d windows (%.1f%%; ISA-18.2 aims for under about 1%%)\n",
		r.FloodWindows, r.Windows, r.FloodPercent)
	if len(r.Floods) == 0 {
		fmt.Fprintln(w, "\nno floods")
		return
	}
	fmt.Fprintf(w, "\n%-16s  %-16s  %6s  %4s  %s\n", "START", "END", "ALARMS", "PEAK", "MOST")
	for _, f := range r.Floods {
		var most []string
		for _, c := range f.Top {
			most = append(most, fmt.Sprintf("%s %d", c.ID, c.Count))
		}
		fmt.Fprintf(w, "%-16s  %-16s  %6d  %4d  %s\n", f.Start.Format(reportTime), f.End.Format(reportTime),
			f.Count, f.Peak, strings.Join(most, ", "))
	}
	fmt.Fprintf(w, "\n%d flood(s)\n", len(r.Floods))
}

func printChatter(w io.Writer, cs []rationalize.Chatter) {
	if len(cs) == 0 {
		fmt.Fprintf(w, "no chattering alarms (%d or more activations within %s)\n", rationalize.ChatterCount, shortDur(rationalize.ChatterWindow))
		return
	}
	wID := colWidth("ID", len(cs), func(i int) string { return cs[i].ID })
	fmt.Fprintf(w, "%-*s  %-8s  %8s  %4s  %11s  %-16s  %s\n", wID, "ID", "PRIORITY", "EPISODES", "PEAK", "ACTIVATIONS", "FIRST", "NAME")
	for _, c := range cs {
		fmt.Fprintf(w, "%-*s  %-8s  %8d  %4d  %11d  %-16s  %s\n", wID, c.ID, c.Priority, c.Episodes, c.Peak,
			c.Activations, c.First.Format(reportTime), c.Name)
	}
	fmt.Fprintf(w, "\n%d chattering alarm(s): %d or more activations within %s\n", len(cs), rationalize.ChatterCount, shortDur(rationalize.ChatterWindow))
}

func printBadActors(w io.Writer, bs []rationalize.BadActor, total int) {
	if len(bs) == 0 {
		fmt.Fprintln(w, "no alarms activated")
		return
	}
	wID := colWidth("ID", len(bs), func(i int) string { return bs[i].ID })
	fmt.Fprintf(w, "%4s  %-*s  %-8s  %6s  %6s  %s\n", "RANK", wID, "ID", "PRIORITY", "COUNT", "SHARE", "NAME")
	var share float64
	for i, b := range bs {
		share += b.Percent
		fmt.Fprintf(w, "%4d  %-*s  %-8s  %6d  %5.1f%%  %s\n", i+1, wID, b.ID, b.Priority, b.Count.Count, b.Percent, b.Name)
	}
	fmt.Fprintf(w, "\nthese %d of the alarms are %.1f%% of all %d activations\n", len(bs), share, total)
}

func printStale(w io.Writer, ss []rationalize.Stale, asOf time.Time) {
	if len(ss) == 0 {
		fmt.Fprintf(w, "no stale alarms (active for more than %s at %s)\n", shortDur(rationalize.StaleAfter), asOf.Format(reportTime))
		return
	}
	wID := colWidth("ID", len(ss), func(i int) string { return ss[i].ID })
	fmt.Fprintf(w, "%-*s  %-8s  %-16s  %10s  %s\n", wID, "ID", "PRIORITY", "ACTIVE SINCE", "FOR", "NAME")
	for _, s := range ss {
		fmt.Fprintf(w, "%-*s  %-8s  %-16s  %10s  %s\n", wID, s.ID, s.Priority, s.Since.Format(reportTime), humanAge(s.For), s.Name)
	}
	fmt.Fprintf(w, "\n%d stale alarm(s): active for more than %s at %s\n", len(ss), shortDur(rationalize.StaleAfter), asOf.Format(reportTime))
}

// shortDur prints a whole number of hours, minutes or seconds without
// the trailing zeros time.Duration.String adds ("24h", not "24h0m0s").
func shortDur(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

func humanAge(d time.Duration) string {
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	if days == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dd %dh", days, hours)
}

func colWidth(head string, n int, get func(int) string) int {
	w := len(head)
	for i := range n {
		w = max(w, len(get(i)))
	}
	return w
}
