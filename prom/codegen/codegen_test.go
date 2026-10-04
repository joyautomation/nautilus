package codegen

import (
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/prom"
)

const smallFixture = `# HELP node_load1 1m load average.
# TYPE node_load1 gauge
node_load1 0.75
# HELP node_boot_time_seconds Unix time of boot.
# TYPE node_boot_time_seconds gauge
node_boot_time_seconds 1700000000
# HELP node_cpu_seconds_total Seconds the CPUs spent in each mode.
# TYPE node_cpu_seconds_total counter
node_cpu_seconds_total{cpu="0",mode="idle"} 12345.6
node_cpu_seconds_total{cpu="1",mode="idle"} 12340.1
node_cpu_seconds_total{cpu="0",mode="user"} 100
node_cpu_seconds_total{cpu="1",mode="user"} 105
# HELP node_memory_MemTotal_bytes Total memory.
# TYPE node_memory_MemTotal_bytes gauge
node_memory_MemTotal_bytes 16000000000
# HELP node_memory_MemAvailable_bytes Available memory.
# TYPE node_memory_MemAvailable_bytes gauge
node_memory_MemAvailable_bytes 8000000000
# HELP node_uname_info Labeled system information.
# TYPE node_uname_info gauge
node_uname_info{nodename="testbox",machine="x86_64"} 1
# HELP node_hwmon_sensor_label Sensor label.
# TYPE node_hwmon_sensor_label gauge
node_hwmon_sensor_label{chip="c1",sensor="temp1"} 1
node_hwmon_sensor_label{chip="c1",sensor="fan1"} 1
# HELP node_hwmon_temp_celsius Temperature.
# TYPE node_hwmon_temp_celsius gauge
node_hwmon_temp_celsius{chip="c1",sensor="temp1"} 41.5
node_hwmon_temp_celsius{chip="c1",sensor="temp2"} 55
# HELP node_hwmon_temp_max_celsius Warning threshold.
# TYPE node_hwmon_temp_max_celsius gauge
node_hwmon_temp_max_celsius{chip="c1",sensor="temp1"} 80
# HELP node_hwmon_temp_crit_celsius Critical threshold.
# TYPE node_hwmon_temp_crit_celsius gauge
node_hwmon_temp_crit_celsius{chip="c1",sensor="temp1"} 100
# HELP node_hwmon_fan_rpm Fan speed.
# TYPE node_hwmon_fan_rpm gauge
node_hwmon_fan_rpm{chip="c1",sensor="fan1"} 1200
`

func mustParse(t *testing.T, body string) prom.Scrape {
	t.Helper()
	sc, err := prom.ParseText([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestGenerateNodeIsDeterministic(t *testing.T) {
	sc := mustParse(t, smallFixture)
	opts := Options{Tag: "TESTBOX", URL: "http://127.0.0.1:9100/metrics"}
	out1, err := Generate(sc, opts)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := Generate(sc, opts)
	if err != nil {
		t.Fatal(err)
	}
	m1 := ManifestYAML(out1.Manifest, "naut prometheus import --tag TESTBOX")
	m2 := ManifestYAML(out2.Manifest, "naut prometheus import --tag TESTBOX")
	if string(m1) != string(m2) {
		t.Fatal("Generate is not deterministic across two runs of the same scrape")
	}
	tg1, err := TagsYAML(out1)
	if err != nil {
		t.Fatal(err)
	}
	tg2, err := TagsYAML(out2)
	if err != nil {
		t.Fatal(err)
	}
	if string(tg1) != string(tg2) {
		t.Fatal("TagsYAML is not deterministic across two runs")
	}
}

// Golden: fixture → the three files byte-for-byte, refreshed with -update
// (the same flag cmd/naut's golden tests use).
var update = updateFlag()

func TestGenerateNodeGolden(t *testing.T) {
	sc := mustParse(t, smallFixture)
	out, err := Generate(sc, Options{Tag: "TESTBOX", URL: "http://127.0.0.1:9100/metrics"})
	if err != nil {
		t.Fatal(err)
	}
	manifest := ManifestYAML(out.Manifest, "naut prometheus import --host 127.0.0.1:9100 --tag TESTBOX")
	tags, err := TagsYAML(out)
	if err != nil {
		t.Fatal(err)
	}

	goldenCompare(t, "prometheus_manifest.yaml", manifest)
	goldenCompare(t, "tags_prometheus.yaml", tags)

	// The generated hw_types.st must compile, and be the SAME bytes any of
	// the three importers would write (hw/types_test.go pins that generally;
	// this just proves prom's own call site uses it, not a private copy).
	src, err := hwTypesST()
	if err != nil {
		t.Fatal(err)
	}
	prog, err := st.Parse(string(src) + "\nPROGRAM P\nVAR_EXTERNAL TESTBOX : Server; END_VAR\nEND_PROGRAM\n")
	if err != nil {
		t.Fatalf("generated hw_types.st does not parse: %v", err)
	}
	if _, err := st.Lower(prog); err != nil {
		t.Fatalf("generated hw_types.st does not lower: %v", err)
	}
}

// Every generated binding resolves against the scrape it was generated
// from — the "no bound metric goes unreported" contract, checked the other
// way around: ReportMissing must find nothing when the manifest was
// generated from exactly this scrape.
func TestGenerateNodeEveryBindingResolves(t *testing.T) {
	sc := mustParse(t, smallFixture)
	out, err := Generate(sc, Options{Tag: "TESTBOX", URL: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if missing := ReportMissing(sc, out.Manifest); len(missing) != 0 {
		t.Fatalf("bindings generated from this scrape must all resolve against it: %v", missing)
	}
}

// Two hwmon rows whose chip/sensor identifiers sanitise to the SAME tag
// name (no node_hwmon_sensor_label for either, so the fallback name is
// "<chip>_<sensor>", and sanitising collapses their punctuation
// differently) must be a generation error, not a silent pick of one.
func TestGenerateNodeTempCollision(t *testing.T) {
	sc := prom.Scrape{Samples: []prom.Sample{
		{Name: "node_hwmon_temp_celsius", Labels: map[string]string{"chip": "c-1", "sensor": "temp_1"}, Value: 40},
		{Name: "node_hwmon_temp_celsius", Labels: map[string]string{"chip": "c", "sensor": "1_temp_1"}, Value: 41},
	}}
	_, err := Generate(sc, Options{Tag: "TESTBOX", URL: "x"})
	if err == nil || !strings.Contains(err.Error(), "sanitise to the same tag name") {
		t.Fatalf("err = %v, want a collision error", err)
	}
}

// The real, scrubbed workstation scrape (prom/testdata): codegen must run over
// it without error, produce a sane tag count, and every generated binding
// must resolve (this box has no inlet/ambient sensor, so InletTempC must
// fall back to const 0 rather than erroring).
func TestGenerateNodeAgainstRealScrape(t *testing.T) {
	body, err := os.ReadFile("../testdata/workstation.prom")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := prom.ParseText(body)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(sc, Options{Tag: "NODE1", URL: "http://127.0.0.1:9100/metrics"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Manifest.Tags) < 10 {
		t.Fatalf("only %d tags generated from a real scrape", len(out.Manifest.Tags))
	}
	if missing := ReportMissing(sc, out.Manifest); len(missing) != 0 {
		t.Fatalf("bindings generated from this scrape must all resolve against it: %v", missing)
	}
	if err := out.Manifest.Validate(); err != nil {
		t.Fatalf("generated manifest must validate: %v", err)
	}
}
