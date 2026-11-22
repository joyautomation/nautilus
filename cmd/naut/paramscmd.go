package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/apiclient"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

const paramsUsage = `naut params — a controller's tuned setpoints as a file (read-only)

A parameter is a scalar setpoint tag with a literal init: in the manifest.
Operators tune it live; the repo keeps the init. These commands read the
live values; nothing here writes to the controller.

Usage:
  naut params export --host <controller> [--port 8080] [--drift] [-o <file>]
  naut params diff   --host <controller> [--port 8080] [--file <set.yaml>] [--check]

export writes a parameter set: a YAML file with "version: 1" and a
"parameters:" map of tag name to live value (the unit and the drift state
ride along as comments). --drift keeps only the parameters whose live value
is not the manifest's init. Without -o it prints to stdout.

diff with no --file lists the parameters whose live value differs from the
manifest's init (the drift GET /api/parameters reports: plant-ahead,
repo-ahead, conflict). With --file it compares a parameter set to the live
values: each line is what applying the file would change, and a name the
controller does not know, a value of the wrong kind, or one outside the
tag's min/max is marked REFUSED (a controller never takes such a value from
outside). Exit 0 when nothing differs; --check makes a difference or a
refusal exit 1. A failure to reach or read the controller is exit 2.

--host may be a URL (https://plc.example:8443) for a controller with
server.tls. A controller with a token: set NAUTILUS_TOKEN.
`

func runParams(args []string) int {
	if len(args) < 1 || args[0] == "-h" || args[0] == "--help" {
		if len(args) < 1 {
			fmt.Fprint(os.Stderr, paramsUsage)
			return 2
		}
		fmt.Print(paramsUsage)
		return 0
	}
	switch args[0] {
	case "export":
		return runParamsExport(args[1:])
	case "diff":
		return runParamsDiff(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut params: unknown subcommand %q\n\n%s", args[0], paramsUsage)
		return 2
	}
}

func runParamsExport(args []string) int {
	fs := flag.NewFlagSet("params export", flag.ContinueOnError)
	host := fs.String("host", "", "controller hostname or IP")
	port := fs.Int("port", 8080, "tag API port")
	drift := fs.Bool("drift", false, "only parameters whose live value is not the manifest's init")
	out := fs.String("o", "", "write to this file (default stdout)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, paramsUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "naut params export: --host is required")
		return 2
	}
	ps, err := fetchParameters(*host, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut params export:", err)
		return 2
	}
	if *drift {
		ps = paramsDrifted(ps)
	}
	b, err := encodeParamSet(ps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut params export:", err)
		return 2
	}
	if *out == "" {
		_, _ = os.Stdout.Write(b)
		return 0
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut params export:", err)
		return 2
	}
	fmt.Fprintf(os.Stderr, "wrote %d parameter(s) to %s\n", len(ps), *out)
	return 0
}

func runParamsDiff(args []string) int {
	fs := flag.NewFlagSet("params diff", flag.ContinueOnError)
	host := fs.String("host", "", "controller hostname or IP")
	port := fs.Int("port", 8080, "tag API port")
	file := fs.String("file", "", "parameter set to compare with the live values")
	check := fs.Bool("check", false, "exit 1 when anything differs or is refused")
	fs.Usage = func() { fmt.Fprint(os.Stderr, paramsUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "naut params diff: --host is required")
		return 2
	}
	ps, err := fetchParameters(*host, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut params diff:", err)
		return 2
	}
	var lines []string
	var differs bool
	if *file == "" {
		lines, differs = diffAgainstRepo(ps)
	} else {
		raw, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut params diff:", err)
			return 2
		}
		set, err := decodeParamSet(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "naut params diff: %s: %v\n", *file, err)
			return 2
		}
		lines, differs = diffSet(ps, set)
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	if !differs {
		fmt.Println("no differences")
		return 0
	}
	if *check {
		return 1
	}
	return 0
}

// fetchParameters GETs a controller's /api/parameters.
func fetchParameters(host string, port int) ([]runtime.Parameter, error) {
	base, err := apiclient.BaseURL(host, port)
	if err != nil {
		return nil, err
	}
	url := base + "/api/parameters"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	apiclient.Authorize(req)
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no controller at %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	var doc struct {
		Parameters []runtime.Parameter `json:"parameters"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("bad response from %s: %w", url, err)
	}
	return doc.Parameters, nil
}

func paramsDrifted(ps []runtime.Parameter) []runtime.Parameter {
	var out []runtime.Parameter
	for _, p := range ps {
		if p.State != runtime.ParamInStep {
			out = append(out, p)
		}
	}
	return out
}

// paramSetVersion is the parameter-set file format version.
const paramSetVersion = 1

// encodeParamSet writes the parameters as a parameter set, sorted by name.
func encodeParamSet(ps []runtime.Parameter) ([]byte, error) {
	sorted := append([]runtime.Parameter(nil), ps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range sorted {
		k := &yaml.Node{Kind: yaml.ScalarNode, Value: p.Name}
		v := &yaml.Node{}
		if err := v.Encode(normalizeParamValue(p.Value)); err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
		var note []string
		if p.Unit != "" {
			note = append(note, p.Unit)
		}
		if p.State != runtime.ParamInStep {
			note = append(note, fmt.Sprintf("%s, init %s", p.State, fmtParam(p.Init)))
		}
		if len(note) > 0 {
			v.LineComment = strings.Join(note, "; ")
		}
		m.Content = append(m.Content, k, v)
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "version"}, {Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(paramSetVersion)},
		{Kind: yaml.ScalarNode, Value: "parameters"}, m,
	}}
	if len(m.Content) == 0 {
		m.Style = yaml.FlowStyle
	}
	b, err := yaml.Marshal(root)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// decodeParamSet reads a parameter set. It is strict: an unknown top-level
// key, a wrong version or a non-scalar value is an error that names it.
func decodeParamSet(raw []byte) (map[string]any, error) {
	var doc struct {
		Version    int       `yaml:"version"`
		Parameters yaml.Node `yaml:"parameters"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("empty file: a parameter set starts with \"version: %d\"", paramSetVersion)
		}
		return nil, err
	}
	if doc.Version != paramSetVersion {
		return nil, fmt.Errorf("version %d is not supported (this naut reads version %d): add \"version: %d\" at the top", doc.Version, paramSetVersion, paramSetVersion)
	}
	if doc.Parameters.Kind != yaml.MappingNode || len(doc.Parameters.Content) == 0 {
		return nil, fmt.Errorf("no parameters: add a \"parameters:\" map of tag name to value (an empty set would compare as no differences)")
	}
	out := map[string]any{}
	c := doc.Parameters.Content
	for i := 0; i+1 < len(c); i += 2 {
		// The key's text, so a tag named null or ~ is not read as YAML null.
		name := c[i].Value
		var v any
		if err := c[i+1].Decode(&v); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		switch v.(type) {
		case bool, int, int64, uint64, float64, string:
		default:
			return nil, fmt.Errorf("parameter %q: a value must be a number, a bool or a string, not %T", name, v)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("parameter %q appears twice", name)
		}
		out[name] = v
	}
	return out, nil
}

// normalizeParamValue turns a JSON number into something YAML prints
// plainly (42 not 42.0).
func normalizeParamValue(v any) any {
	if f, ok := v.(float64); ok && f == float64(int64(f)) && f < 1e15 && f > -1e15 {
		return int64(f)
	}
	return v
}

func fmtParam(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(normalizeParamValue(v))
}

// valueKind classes a value as number, bool or string for the kind check.
func valueKind(v any) string {
	switch v.(type) {
	case bool:
		return "bool"
	case string:
		return "string"
	case nil:
		return "none"
	}
	return "number"
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	}
	return 0, false
}

// diffAgainstRepo lists the parameters whose live value is not the init.
func diffAgainstRepo(ps []runtime.Parameter) (lines []string, differs bool) {
	for _, p := range paramsDrifted(ps) {
		line := fmt.Sprintf("%-12s %s: live %s, repo %s", p.State, p.Name, fmtParam(p.Value), fmtParam(p.Init))
		if p.EditedBy != "" {
			line += ", edited by " + p.EditedBy
		}
		if p.Committed {
			line += " (committed, waiting for a redeploy)"
		}
		lines = append(lines, line)
	}
	return lines, len(lines) > 0
}

// diffSet compares a parameter set with the live values: what applying it
// would change. A refused line is one the controller would not take from
// outside: an unknown name, a value of another kind, one outside min/max.
func diffSet(ps []runtime.Parameter, set map[string]any) (lines []string, differs bool) {
	live := map[string]runtime.Parameter{}
	for _, p := range ps {
		live[ir.NameKey(p.Name)] = p
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		want := set[n]
		p, ok := live[ir.NameKey(n)]
		if !ok {
			lines = append(lines, fmt.Sprintf("REFUSED  %s: not a parameter on this controller (a scalar tag with a literal init: in the manifest)", n))
			differs = true
			continue
		}
		if runtime.ParamValuesEqual(p.Value, want) {
			continue
		}
		n = p.Name // the declared spelling (tag names are case-insensitive)
		// Stricter than the controller for an unranged tag (it may coerce a
		// number to a bool tag): conservative, never "change" for a refusal.
		if valueKind(p.Value) != valueKind(want) {
			lines = append(lines, fmt.Sprintf("REFUSED  %s: %s is a %s, the file has a %s (%s)", n, fmtParam(p.Value), valueKind(p.Value), valueKind(want), fmtParam(want)))
			differs = true
			continue
		}
		if f, isNum := toFloat(want); isNum {
			if p.Min != nil && f < *p.Min {
				lines = append(lines, fmt.Sprintf("REFUSED  %s: %s is below min %s", n, fmtParam(want), fmtParam(*p.Min)))
				differs = true
				continue
			}
			if p.Max != nil && f > *p.Max {
				lines = append(lines, fmt.Sprintf("REFUSED  %s: %s is above max %s", n, fmtParam(want), fmtParam(*p.Max)))
				differs = true
				continue
			}
		}
		lines = append(lines, fmt.Sprintf("change   %s: %s -> %s", n, fmtParam(p.Value), fmtParam(want)))
		differs = true
	}
	return lines, differs
}
