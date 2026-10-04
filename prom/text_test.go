package prom

import (
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestParseTextHandcrafted(t *testing.T) {
	body := `# HELP node_load1 1m load average.
# TYPE node_load1 gauge
node_load1 0.42
# HELP node_hwmon_temp_celsius Hardware monitor for temperature (input)
# TYPE node_hwmon_temp_celsius gauge
node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp1"} 41.5
# a bare comment, ignored
node_weird_label{name="a \"quoted\" value, with a comma",path="C:\\Users\\x"} 1
node_special{a="x"} +Inf
node_special{a="y"} -Inf
node_special{a="z"} NaN
node_special{a="w"} Inf
http_requests_total{code="200"} 1027 1395066363000
`
	sc, err := ParseText([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Samples) != 8 {
		t.Fatalf("got %d samples, want 8: %+v", len(sc.Samples), sc.Samples)
	}
	if fam := sc.Families["node_load1"]; fam.Type != "gauge" || fam.Help == "" {
		t.Errorf("node_load1 family = %+v", fam)
	}
	byName := map[string][]Sample{}
	for _, s := range sc.Samples {
		byName[s.Name] = append(byName[s.Name], s)
	}
	if v := byName["node_load1"][0].Value; v != 0.42 {
		t.Errorf("node_load1 = %v", v)
	}
	temp := byName["node_hwmon_temp_celsius"][0]
	if temp.Labels["chip"] != "platform_coretemp_0" || temp.Labels["sensor"] != "temp1" || temp.Value != 41.5 {
		t.Errorf("temp sample = %+v", temp)
	}
	weird := byName["node_weird_label"][0]
	if weird.Labels["name"] != `a "quoted" value, with a comma` {
		t.Errorf("unescaped name label = %q", weird.Labels["name"])
	}
	if weird.Labels["path"] != `C:\Users\x` {
		t.Errorf("unescaped path label = %q", weird.Labels["path"])
	}
	for _, s := range byName["node_special"] {
		switch s.Labels["a"] {
		case "x", "w":
			if !math.IsInf(s.Value, 1) {
				t.Errorf("%s: +Inf expected, got %v", s.Labels["a"], s.Value)
			}
		case "y":
			if !math.IsInf(s.Value, -1) {
				t.Errorf("-Inf expected, got %v", s.Value)
			}
		case "z":
			if !math.IsNaN(s.Value) {
				t.Errorf("NaN expected, got %v", s.Value)
			}
		}
	}
	if v := byName["http_requests_total"][0].Value; v != 1027 {
		t.Errorf("timestamped sample = %v, want 1027 (timestamp ignored)", v)
	}
}

func TestParseTextRejectsGarbage(t *testing.T) {
	cases := []string{
		"123abc 1\n",            // starts with a digit: not a metric name
		"node_x{unterminated\n", // no closing brace
		"node_x{a=\"b\"}\n",     // no value at all
		"node_x{a=\"unterminated} 1\n",
	}
	for _, body := range cases {
		if _, err := ParseText([]byte(body)); err == nil {
			t.Errorf("body %q: want an error", body)
		}
	}
}

// The parser must swallow node_exporter's own real scrape without error and
// recover the shapes the "node" profile binds to — recorded live from a real
// workstation (node_exporter v1.12.1), not synthesised, so a wire format
// surprise shows up here before it shows up in codegen or the foreign test.
// The recording is scrubbed (see TestRealScrapeFixtureIsScrubbed): only the
// identifying label values were replaced, every family, label key and
// sample value is exactly as the exporter served it.
func TestParseTextRealScrape(t *testing.T) {
	body, err := os.ReadFile("testdata/workstation.prom")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := ParseText(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Samples) < 1000 {
		t.Fatalf("only %d samples parsed from a real scrape", len(sc.Samples))
	}
	want := []string{
		"node_load1", "node_boot_time_seconds", "node_uname_info",
		"node_cpu_seconds_total", "node_hwmon_temp_celsius", "node_hwmon_fan_rpm",
		"node_memory_MemTotal_bytes", "node_memory_MemAvailable_bytes",
		"node_filesystem_avail_bytes", "node_filesystem_size_bytes",
	}
	byName := map[string]bool{}
	for _, s := range sc.Samples {
		byName[s.Name] = true
	}
	for _, name := range want {
		if !byName[name] {
			t.Errorf("real scrape is missing %s — the node profile depends on it", name)
		}
	}
}

// The real-scrape fixture is committed to the repo, so it must carry
// nothing that identifies the machine, its owner or the network it sits on.
// A raw node_exporter scrape leaks all three: the hostname (uname), the
// board and BIOS (dmi), disk serials and WWNs, filesystem UUIDs, every
// interface name and MAC (a USB NIC's name IS its MAC — "enx" + 12 hex
// digits), mount paths under a user's home or /media/<user>, zpool and
// dataset names (lab VM names) and the local time zone. Re-recording the
// fixture means replacing those label values the way the current one was
// scrubbed — the placeholders below, everything else untouched — and this
// test fails until that is done. It is an allowlist on purpose: a denylist
// of the real values would commit exactly what it is guarding against.
func TestRealScrapeFixtureIsScrubbed(t *testing.T) {
	body, err := os.ReadFile("testdata/workstation.prom")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := ParseText(body)
	if err != nil {
		t.Fatal(err)
	}
	var (
		mac     = regexp.MustCompile(`^(|00:00:00:00:00:00|ff:ff:ff:ff:ff:ff|02:00:00:00:[0-9a-f]{2}:[0-9a-f]{2})$`)
		serial  = regexp.MustCompile(`^(|SERIAL[0-9]{4})$`)
		wwn     = regexp.MustCompile(`^(|eui\.0{28}[0-9a-f]{4})$`)
		uuid    = regexp.MustCompile(`^0{12}[0-9a-f]{4}$`)
		netdev  = regexp.MustCompile(`^(lo|net[0-9]+)$`)
		zpool   = regexp.MustCompile(`^pool[0-9]+$`)
		pooled  = regexp.MustCompile(`^pool[0-9]+(/|$)`)
		dmiVals = map[string]bool{
			"Acme": true, "Workstation": true, "rev-a": true, "Mainboard": true,
			"1.0": true, "01/01/2024": true, "Default string": true,
		}
	)
	check := func(s Sample, key string, ok bool) {
		if !ok {
			t.Errorf("%s{%s=%q}: not a scrubbed placeholder", s.Name, key, s.Labels[key])
		}
	}
	for _, s := range sc.Samples {
		for k, v := range s.Labels {
			switch k {
			case "address", "broadcast":
				check(s, k, mac.MatchString(v))
			case "serial":
				check(s, k, serial.MatchString(v))
			case "wwn":
				check(s, k, wwn.MatchString(v))
			case "uuid":
				check(s, k, uuid.MatchString(v))
			case "nodename":
				check(s, k, v == "node1")
			case "domainname":
				check(s, k, v == "(none)")
			case "time_zone":
				check(s, k, v == "UTC")
			case "zpool":
				check(s, k, zpool.MatchString(v))
			case "dataset":
				check(s, k, pooled.MatchString(v))
			case "mountpoint":
				check(s, k, !strings.HasPrefix(v, "/home/") &&
					(!strings.HasPrefix(v, "/media/") || strings.HasPrefix(v, "/media/user/")))
			}
			if s.Name == "node_dmi_info" {
				check(s, k, dmiVals[v])
			}
		}
		if strings.HasPrefix(s.Name, "node_network_") {
			if d, ok := s.Labels["device"]; ok {
				check(s, "device", netdev.MatchString(d))
			}
		}
	}
}
