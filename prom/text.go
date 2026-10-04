// text.go parses the Prometheus text exposition format (the format
// node_exporter, and every other exporter, answers `/metrics` with):
//
//	# HELP node_load1 1m load average.
//	# TYPE node_load1 gauge
//	node_load1 0.42
//	node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp1"} 41.5
//
// It is a parser, nothing more: every line becomes a Sample (name, labels,
// value) regardless of the family's declared TYPE. A histogram's `_bucket`/
// `_sum`/`_count` series and a summary's `_sum`/`_count`/quantile series are
// ordinary samples here — the format does not distinguish them from a
// gauge's single series, and "parsed but not bindable" (brief §3.3) is a
// codegen-time policy (the node profile never emits a binding onto one), not
// a parsing rule.
package prom

import (
	"bufio"
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Sample is one exposed time series at one instant: a metric name, its
// label set (empty, never nil, so callers can range it safely), and the
// value the line carried.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Family is what a `# HELP`/`# TYPE` pair says about one metric name.
// Type is "counter", "gauge", "histogram", "summary" or "untyped" (the
// exposition format's default when a TYPE line is absent).
type Family struct {
	Name string
	Help string
	Type string
}

// Scrape is one parsed body: every sample, plus the family metadata keyed by
// metric name (missing means "untyped, no HELP text" — a well-formed scrape
// need not declare either).
type Scrape struct {
	Samples  []Sample
	Families map[string]Family
}

// ParseText parses one exposition-format body. It is lenient about what
// node_exporter and its cousins actually emit and strict about the one thing
// that matters to a driver: a malformed line is an error naming it, because
// a driver that silently drops half a scrape is worse than one that refuses
// it and counts it as a transport failure.
func ParseText(body []byte) (Scrape, error) {
	sc := Scrape{Families: map[string]Family{}}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // a busy hwmon/smart line set can be long
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}
		if line[0] == '#' {
			if f, ok := parseMeta(line); ok {
				fam := sc.Families[f.Name]
				fam.Name = f.Name
				if f.Help != "" {
					fam.Help = f.Help
				}
				if f.Type != "" {
					fam.Type = f.Type
				}
				sc.Families[f.Name] = fam
			}
			continue // an unrecognised "#" line is a plain comment
		}
		s, err := parseSample(line)
		if err != nil {
			return Scrape{}, fmt.Errorf("prom: line %d: %w", lineNo, err)
		}
		sc.Samples = append(sc.Samples, s)
	}
	if err := scanner.Err(); err != nil {
		return Scrape{}, fmt.Errorf("prom: %w", err)
	}
	return sc, nil
}

// parseMeta reads "# HELP name text..." / "# TYPE name kind"; any other
// "#..." line (including a bare "#" comment) returns ok=false.
func parseMeta(line string) (Family, bool) {
	rest := strings.TrimPrefix(line, "#")
	rest = strings.TrimPrefix(rest, " ")
	switch {
	case strings.HasPrefix(rest, "HELP "):
		rest = rest[len("HELP "):]
		name, text, ok := cutSpace(rest)
		if !ok {
			return Family{}, false
		}
		return Family{Name: name, Help: text}, true
	case strings.HasPrefix(rest, "TYPE "):
		rest = rest[len("TYPE "):]
		name, kind, ok := cutSpace(rest)
		if !ok {
			return Family{}, false
		}
		return Family{Name: name, Type: strings.TrimSpace(kind)}, true
	}
	return Family{}, false
}

func cutSpace(s string) (before, after string, ok bool) {
	i := strings.IndexByte(s, ' ')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// parseSample reads one sample line: `name{labels} value` or `name value`,
// with an optional trailing timestamp this driver ignores (a scrape is
// already "now" to the poll that fetched it).
func parseSample(line string) (Sample, error) {
	i := 0
	name, i, err := scanMetricName(line, i)
	if err != nil {
		return Sample{}, err
	}
	labels := map[string]string{}
	i = skipSpace(line, i)
	if i < len(line) && line[i] == '{' {
		labels, i, err = scanLabels(line, i)
		if err != nil {
			return Sample{}, err
		}
	}
	i = skipSpace(line, i)
	valTok, _, _ := cutSpace(line[i:] + " ") // trailing space guarantees a cut even with no timestamp
	valTok = strings.TrimSpace(valTok)
	if valTok == "" {
		return Sample{}, fmt.Errorf("%q: no value", line)
	}
	v, err := parseValue(valTok)
	if err != nil {
		return Sample{}, fmt.Errorf("%q: %w", line, err)
	}
	return Sample{Name: name, Labels: labels, Value: v}, nil
}

func skipSpace(s string, i int) int {
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return i
}

func isNameByte(b byte, first bool) bool {
	switch {
	case b == '_' || b == ':':
		return true
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return !first
	}
	return false
}

func scanMetricName(s string, i int) (string, int, error) {
	start := i
	first := true
	for i < len(s) && isNameByte(s[i], first) {
		i++
		first = false
	}
	if i == start {
		return "", i, fmt.Errorf("%q: expected a metric name", s)
	}
	return s[start:i], i, nil
}

// scanLabels parses "{k=\"v\", k2=\"v2\"}" starting at the '{', unescaping
// each value per the format's rule: \\ -> \, \" -> ", \n -> newline (the
// only three escapes the spec defines for a label value).
func scanLabels(s string, i int) (map[string]string, int, error) {
	labels := map[string]string{}
	i++ // past '{'
	for {
		i = skipSpace(s, i)
		if i < len(s) && s[i] == '}' {
			return labels, i + 1, nil
		}
		key, ni, err := scanLabelName(s, i)
		if err != nil {
			return nil, i, err
		}
		i = ni
		i = skipSpace(s, i)
		if i >= len(s) || s[i] != '=' {
			return nil, i, fmt.Errorf("%q: expected '=' after label %s", s, key)
		}
		i++
		i = skipSpace(s, i)
		if i >= len(s) || s[i] != '"' {
			return nil, i, fmt.Errorf("%q: expected a quoted value for label %s", s, key)
		}
		val, ni, err := scanQuoted(s, i)
		if err != nil {
			return nil, i, err
		}
		i = ni
		labels[key] = val
		i = skipSpace(s, i)
		if i < len(s) && s[i] == ',' {
			i++
			continue
		}
		i = skipSpace(s, i)
		if i < len(s) && s[i] == '}' {
			return labels, i + 1, nil
		}
		return nil, i, fmt.Errorf("%q: expected ',' or '}' after label %s", s, key)
	}
}

func scanLabelName(s string, i int) (string, int, error) {
	start := i
	first := true
	for i < len(s) && isNameByte(s[i], first) && s[i] != ':' {
		i++
		first = false
	}
	if i == start {
		return "", i, fmt.Errorf("%q: expected a label name", s)
	}
	return s[start:i], i, nil
}

// scanQuoted reads a double-quoted string starting at the opening '"',
// unescaping \\, \" and \n, and returns the index just past the closing '"'.
func scanQuoted(s string, i int) (string, int, error) {
	i++ // past opening quote
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			return b.String(), i + 1, nil
		case c == '\\' && i+1 < len(s):
			switch s[i+1] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			default:
				// An exporter that emits an unknown escape is out of spec;
				// pass it through literally rather than refusing the whole
				// scrape over one cosmetic label.
				b.WriteByte('\\')
				b.WriteByte(s[i+1])
			}
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return "", i, fmt.Errorf("%q: unterminated quoted label value", s)
}

// parseValue reads a sample value: a float64, or the format's three special
// tokens. strconv.ParseFloat already accepts "+Inf"/"-Inf"/"NaN" — the only
// wrinkle is that the format's "Inf" (no sign) is also legal and Go's parser
// wants "+Inf".
func parseValue(tok string) (float64, error) {
	if tok == "Inf" {
		tok = "+Inf"
	}
	v, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", tok)
	}
	return v, nil
}

// RenderText renders a Scrape back to exposition-format bytes: HELP/TYPE
// per family (sorted by name for determinism), each family's samples in
// their original order. It is ParseText's inverse closely enough for
// round-tripping a Scrape a test built or mutated in memory — prom/serve
// uses it to answer `/metrics`, and the foreign test uses it to turn a live
// scrape into "a recording" without shelling out to `naut prometheus
// browse --record`.
func RenderText(sc Scrape) []byte {
	var b strings.Builder
	names := make([]string, 0, len(sc.Families)+8)
	seen := map[string]bool{}
	for _, s := range sc.Samples {
		if !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	for name := range sc.Families {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	byName := map[string][]Sample{}
	for _, s := range sc.Samples {
		byName[s.Name] = append(byName[s.Name], s)
	}
	for _, name := range names {
		if f, ok := sc.Families[name]; ok {
			if f.Help != "" {
				fmt.Fprintf(&b, "# HELP %s %s\n", name, f.Help)
			}
			if f.Type != "" {
				fmt.Fprintf(&b, "# TYPE %s %s\n", name, f.Type)
			}
		}
		for _, sm := range byName[name] {
			b.WriteString(name)
			if len(sm.Labels) > 0 {
				b.WriteByte('{')
				keys := make([]string, 0, len(sm.Labels))
				for k := range sm.Labels {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for i, k := range keys {
					if i > 0 {
						b.WriteByte(',')
					}
					fmt.Fprintf(&b, "%s=\"%s\"", k, escapeLabelValue(sm.Labels[k]))
				}
				b.WriteByte('}')
			}
			b.WriteByte(' ')
			b.WriteString(strconv.FormatFloat(sm.Value, 'g', -1, 64))
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

func escapeLabelValue(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}
