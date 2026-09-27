package prom

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/hw"
)

func okManifest() Manifest {
	return Manifest{
		Sources: []Source{{ID: "NODE1", URL: "http://127.0.0.1:9100/metrics", Interval: 15 * time.Second}},
		Tags: []Tag{{
			Name: "NODE1", Type: "Server", Source: "NODE1",
			Members: map[string]Binding{
				"PowerOn": {Binding: hw.Binding{Const: true}},
				"Load1":   {Metric: "node_load1"},
				"CpuPct": {
					Expr: "100 - 100 * idle / cpus",
					From: map[string]Selector{
						"idle": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "sum", Rate: true},
						"cpus": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "count"},
					},
				},
				"Model": {Metric: "node_uname_info", Label: "nodename"},
			},
		}},
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := okManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	raw, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, raw)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("re-parsed manifest fails Validate: %v", err)
	}
	if len(got.Sources) != 1 || got.Sources[0].URL != m.Sources[0].URL {
		t.Fatalf("sources did not round-trip: %+v", got.Sources)
	}
}

func TestManifestUnknownKey(t *testing.T) {
	body := `
sources:
  - id: NODE1
    url: http://127.0.0.1:9100/metrics
    bogus: true
tags: []
`
	if _, err := ParseManifest([]byte(body)); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("err = %v, want an unknown-key error", err)
	}
}

func TestManifestCredentialRules(t *testing.T) {
	m := okManifest()
	m.Sources[0].BearerEnv = "TOK"
	if err := m.Validate(); err != nil {
		t.Fatalf("one credential key set must be fine: %v", err)
	}
	m.Sources[0].BearerFile = "/run/secrets/tok"
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one home") {
		t.Fatalf("both bearerenv and bearerfile set: err = %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(m *Manifest)
		want string
	}{
		{"no id", func(m *Manifest) { m.Sources[0].ID = "" }, "missing id"},
		{"dup source", func(m *Manifest) { m.Sources = append(m.Sources, m.Sources[0]) }, "duplicate source"},
		{"no url", func(m *Manifest) { m.Sources[0].URL = "" }, "missing url"},
		{"unknown type", func(m *Manifest) { m.Tags[0].Type = "Router" }, "unknown type"},
		{"unknown source", func(m *Manifest) { m.Tags[0].Source = "NOPE" }, "unknown source"},
		{"unknown member", func(m *Manifest) {
			m.Tags[0].Members["Colour"] = Binding{Metric: "x"}
		}, "no such member"},
		{"dup tag", func(m *Manifest) { m.Tags = append(m.Tags, m.Tags[0]) }, "duplicate tag"},
		{"dotted tag", func(m *Manifest) { m.Tags[0].Name = "NODE1.X" }, "cannot contain"},
		{"no locator", func(m *Manifest) {
			m.Tags[0].Members["Load1"] = Binding{}
		}, "needs one of"},
		{"two locators", func(m *Manifest) {
			m.Tags[0].Members["Load1"] = Binding{Metric: "node_load1", Expr: "1"}
		}, "exactly one of"},
		{"const with metric", func(m *Manifest) {
			m.Tags[0].Members["Load1"] = Binding{Binding: hw.Binding{Const: 1.0}, Metric: "x"}
		}, "take no metric"},
		{"bad agg", func(m *Manifest) {
			m.Tags[0].Members["Load1"] = Binding{Metric: "node_load1", Agg: "median"}
		}, "want sum, max, min, avg or count"},
		{"label on non-string", func(m *Manifest) {
			m.Tags[0].Members["Load1"] = Binding{Metric: "node_load1", Label: "x"}
		}, "makes a STRING"},
		{"label with agg", func(m *Manifest) {
			m.Tags[0].Members["Model"] = Binding{Metric: "node_uname_info", Label: "nodename", Agg: "sum"}
		}, "takes no agg"},
		{"expr unknown selector", func(m *Manifest) {
			m.Tags[0].Members["CpuPct"] = Binding{Expr: "1 + missing"}
		}, "has no entry in from"},
		{"from unused", func(m *Manifest) {
			m.Tags[0].Members["CpuPct"] = Binding{Expr: "1", From: map[string]Selector{"x": {Metric: "m"}}}
		}, "not used by expr"},
		{"from no metric", func(m *Manifest) {
			m.Tags[0].Members["CpuPct"] = Binding{Expr: "x", From: map[string]Selector{"x": {}}}
		}, "missing metric"},
		{"write unknown tag", func(m *Manifest) {
			m.Writes = []hw.WriteDecl{{Name: "X", Tag: "NOPE", Member: "Load1"}}
		}, "unknown tag"},
	}
	for _, c := range cases {
		m := okManifest()
		m.Tags[0].Members = map[string]Binding{
			"PowerOn": m.Tags[0].Members["PowerOn"],
			"Load1":   m.Tags[0].Members["Load1"],
			"CpuPct":  m.Tags[0].Members["CpuPct"],
			"Model":   m.Tags[0].Members["Model"],
		}
		c.mut(&m)
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestManifestExprBuiltinNow(t *testing.T) {
	m := okManifest()
	m.Tags[0].Members["UptimeS"] = Binding{
		Expr: "__now - boot",
		From: map[string]Selector{"boot": {Metric: "node_boot_time_seconds"}},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("__now must need no from: entry: %v", err)
	}
}
