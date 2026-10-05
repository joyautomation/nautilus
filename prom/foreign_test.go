// foreign_test.go is the foreign-implementation suite: the real driver
// against a REAL node_exporter binary (scripts/prom-sim.sh downloads and
// starts the pinned release; NAUTILUS_PROM_SIM names it), instead of our
// own stand-in, so the parser, the "node" profile and the expr/rate
// machinery are checked against somebody else's exporter — exactly what
// pymodbus does for modbus/foreign_test.go.
//
// Gated on NAUTILUS_PROM_SIM=host:port; normal `go test ./...` skips it.
package prom_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/prom"
	"github.com/joyautomation/nautilus/prom/codegen"
	"github.com/joyautomation/nautilus/prom/serve"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// simURL returns the scrape URL in env or skips the test.
func simURL(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("NAUTILUS_PROM_SIM")
	if addr == "" {
		t.Skip("set NAUTILUS_PROM_SIM=host:port (see scripts/prom-sim.sh) to run the foreign-stack test")
	}
	return "http://" + addr + "/metrics"
}

func scrapeForeign(t *testing.T, url string) prom.Scrape {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cannot reach %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := prom.ParseText(body)
	if err != nil {
		t.Fatalf("real node_exporter's own scrape failed to parse: %v", err)
	}
	return sc
}

// Import of the live scrape is byte-identical on re-run, and against its OWN
// recording (docs/design/it-drivers.md §8's core promise, and §9.5's
// foreign-test list for prometheus).
func TestForeignImportByteIdenticalOnRerun(t *testing.T) {
	url := simURL(t)
	sc := scrapeForeign(t, url)

	out1, err := codegen.Generate(sc, codegen.Options{Tag: "SIM", URL: url})
	if err != nil {
		t.Fatal(err)
	}
	out2, err := codegen.Generate(sc, codegen.Options{Tag: "SIM", URL: url})
	if err != nil {
		t.Fatal(err)
	}
	cmd := "naut prometheus import --tag SIM --url " + url
	if string(codegen.ManifestYAML(out1.Manifest, cmd)) != string(codegen.ManifestYAML(out2.Manifest, cmd)) {
		t.Fatal("Generate is not byte-identical on re-run against the same scrape")
	}

	// And against a RECORDING of the same scrape (the file path a fixture
	// takes): re-parsing the exact bytes we just fetched must generate the
	// identical manifest.
	recorded, err := prom.ParseText(prom.RenderText(sc))
	if err != nil {
		t.Fatal(err)
	}
	out3, err := codegen.Generate(recorded, codegen.Options{Tag: "SIM", URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if string(codegen.ManifestYAML(out1.Manifest, cmd)) != string(codegen.ManifestYAML(out3.Manifest, cmd)) {
		t.Fatal("import from a recording of the live scrape must generate the same manifest as the live scrape itself")
	}

	// Every generated binding resolves against the scrape it came from.
	if missing := codegen.ReportMissing(sc, out1.Manifest); len(missing) != 0 {
		t.Fatalf("bindings generated from this scrape must all resolve against it: %v", missing)
	}
}

// CpuPct/MemPct land in [0,100] against a real host, and a second poll
// (real wall-clock interval) yields a rate rather than holding at zero.
func TestForeignCpuMemPollAndRate(t *testing.T) {
	url := simURL(t)
	m := prom.Manifest{
		Sources: []prom.Source{{ID: "SIM", URL: url, Interval: 300 * time.Millisecond, Timeout: 5 * time.Second}},
		Tags: []prom.Tag{{
			Name: "SIM", Type: "Server", Source: "SIM",
			Members: map[string]prom.Binding{
				"CpuPct": {Expr: "100 - 100 * idle / cpus", From: map[string]prom.Selector{
					"idle": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "sum", Rate: true},
					"cpus": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "count"},
				}},
				"MemPct": {Expr: "100 * (1 - avail / total)", From: map[string]prom.Selector{
					"avail": {Metric: "node_memory_MemAvailable_bytes"},
					"total": {Metric: "node_memory_MemTotal_bytes"},
				}},
			},
		}},
	}
	d, err := prom.New(m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	get := func(v ir.Value, name string) float64 { return v.Fld[v.Struct.FieldIndex[name]].F }
	waitFor(t, "first delivery", func() bool {
		v, _ := d.ReadInputs()
		_, ok := v["SIM"]
		return ok
	})
	vals, _ := d.ReadInputs()
	sv := vals["SIM"].(ir.Value)
	if mem := get(sv, "MemPct"); mem < 0 || mem > 100 {
		t.Fatalf("MemPct = %v, want [0,100]", mem)
	}
	// CpuPct needs a second sample; wait past a second interval.
	waitFor(t, "a rated CpuPct", func() bool {
		v, _ := d.ReadInputs()
		sv, ok := v["SIM"].(ir.Value)
		return ok && get(sv, "CpuPct") != 0
	})
	vals, _ = d.ReadInputs()
	if cpu := get(vals["SIM"].(ir.Value), "CpuPct"); cpu < 0 || cpu > 100 {
		t.Fatalf("CpuPct = %v, want [0,100]", cpu)
	}
	if q := d.Quality(); q["SIM"] == nio.Bad {
		t.Fatalf("SIM must not be Bad: %v", q)
	}
}

// A scrape with a metric renamed away (a real recording, doctored) marks
// only the tag whose expr depends on it Bad; a sibling with no expr stays
// Good — the profile's "bound metric missing" path, checked against real
// exporter output rather than a hand-written fixture.
func TestForeignMetricRenamedAway(t *testing.T) {
	url := simURL(t)
	sc := scrapeForeign(t, url)
	srv, err := serve.New(prom.RenderText(sc))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	m := prom.Manifest{
		Sources: []prom.Source{{ID: "SIM", URL: srv.URL(), Interval: 20 * time.Millisecond, StaleAfter: 200 * time.Millisecond}},
		Tags: []prom.Tag{{
			Name: "SIM", Type: "Server", Source: "SIM",
			Members: map[string]prom.Binding{
				"Load1": {Metric: "node_load1"},
				"CpuPct": {Expr: "100 - 100 * idle / cpus", From: map[string]prom.Selector{
					"idle": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "sum", Rate: true},
					"cpus": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "count"},
				}},
			},
		}},
	}
	d, err := prom.New(m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	waitFor(t, "clean delivery before the rename", func() bool {
		q := d.Quality()
		return q["SIM"] != nio.Bad && q["SIM"] != nio.NotConnected
	})

	srv.RemoveAll("node_cpu_seconds_total")
	waitFor(t, "SIM marked Bad after the rename", func() bool {
		return d.Quality()["SIM"] == nio.Bad
	})
	vals, _ := d.ReadInputs()
	if _, ok := vals["SIM"]; !ok {
		t.Fatal("a Bad tag must still be present (siblings/other members hold)")
	}
}
