// node.go is the "node" profile: what node_exporter's series mean as the
// hw.Types contract set (docs/design/it-drivers.md §3.3's table). It targets
// node_exporter **v1.12.1** (github.com/prometheus/node_exporter/releases —
// checked 2026-09-26, the latest release; scripts/prom-sim.sh pins the same
// version for the foreign test). Metric names move between node_exporter
// releases (§3.3); `import` reports a bound metric the scrape does not
// carry (ReportMissing) rather than silently emitting a binding that will
// never resolve.
//
// Two members the brief asks for are NOT bound here, and stay at their
// zero value (Health always 0/"ok"): hw.Expr (docs/design/it-drivers.md
// §6.3) is deliberately arithmetic-only — no ternary, no boolean-to-number
// coercion — so "Health := Fault ? 2 : Warning ? 1 : 0" cannot be expressed
// in it. Fault and Warning (both plain boolean comparisons) ARE bound; a
// project that wants Health combines them in its own ST, which is exactly
// the brief's fence for `derived:` ("anything else is ST in the project").
// See the report's open questions for the alternative (a small hw/ Expr
// extension) rather than reaching for it here.
package codegen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/prom"
)

// NodeExporterVersion is the release this profile was written against.
const NodeExporterVersion = "1.12.1"

func generateNode(scrape prom.Scrape, opts Options) (Output, error) {
	out := Output{Desc: map[string]string{}}
	prefix := opts.Tag

	serverMembers := map[string]prom.Binding{
		"Online":  {Binding: hw.Binding{Const: true}},
		"PowerOn": {Binding: hw.Binding{Const: true}},
		"UptimeS": {Expr: "__now - boot", From: map[string]prom.Selector{
			"boot": {Metric: "node_boot_time_seconds"},
		}},
		"CpuPct": {Expr: "100 - 100 * idle / cpus", From: map[string]prom.Selector{
			"idle": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "sum", Rate: true},
			"cpus": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "count"},
		}},
		"MemPct": {Expr: "100 * (1 - avail / total)", From: map[string]prom.Selector{
			"avail": {Metric: "node_memory_MemAvailable_bytes"},
			"total": {Metric: "node_memory_MemTotal_bytes"},
		}},
		"Load1": {Metric: "node_load1"},
	}

	if hasMetric(scrape, "node_filesystem_avail_bytes") {
		serverMembers["RootDiskPct"] = prom.Binding{Expr: "100 * (1 - avail / size)", From: map[string]prom.Selector{
			"avail": {Metric: "node_filesystem_avail_bytes", Labels: map[string]string{"mountpoint": "/"}},
			"size":  {Metric: "node_filesystem_size_bytes", Labels: map[string]string{"mountpoint": "/"}},
		}}
	}
	if hasMetric(scrape, "node_uname_info") {
		serverMembers["Model"] = prom.Binding{Metric: "node_uname_info", Label: "nodename"}
	}
	if s, ok := findSample(scrape, "node_dmi_info", nil); ok {
		if _, ok := s.Labels["product_serial"]; ok {
			serverMembers["Serial"] = prom.Binding{Metric: "node_dmi_info", Label: "product_serial"}
		}
	}

	temps := hwmonTemps(scrape)
	fans := hwmonFans(scrape)
	labels := hwmonLabels(scrape)

	if len(temps) > 0 {
		serverMembers["MaxTempC"] = prom.Binding{Metric: "node_hwmon_temp_celsius", Agg: "max"}
		if crit, ok := minMetric(scrape, "node_hwmon_temp_crit_celsius", nil); ok {
			serverMembers["Fault"] = prom.Binding{Expr: "maxtemp >= crit", From: map[string]prom.Selector{
				"maxtemp": {Metric: "node_hwmon_temp_celsius", Agg: "max"},
				"crit":    {Metric: "node_hwmon_temp_crit_celsius", Agg: "min"},
			}}
			_ = crit
		}
		if warn, ok := minMetric(scrape, "node_hwmon_temp_max_celsius", nil); ok {
			serverMembers["Warning"] = prom.Binding{Expr: "maxtemp >= warn", From: map[string]prom.Selector{
				"maxtemp": {Metric: "node_hwmon_temp_celsius", Agg: "max"},
				"warn":    {Metric: "node_hwmon_temp_max_celsius", Agg: "min"},
			}}
			_ = warn
		}
	}
	inletKey, inletFound := findInlet(temps, labels)
	if inletFound {
		serverMembers["InletTempC"] = prom.Binding{Metric: "node_hwmon_temp_celsius", Labels: map[string]string{
			"chip": inletKey.chip, "sensor": inletKey.sensor,
		}}
	} else {
		serverMembers["InletTempC"] = prom.Binding{Binding: hw.Binding{Const: 0.0}}
	}
	serverMembers["FanCount"] = prom.Binding{Binding: hw.Binding{Const: int64(len(fans))}}
	serverMembers["PsuCount"] = prom.Binding{Binding: hw.Binding{Const: int64(0)}}
	serverMembers["TempCount"] = prom.Binding{Binding: hw.Binding{Const: int64(len(temps))}}

	out.Manifest.Sources = []prom.Source{{ID: prefix, URL: opts.URL}}
	out.Manifest.Tags = append(out.Manifest.Tags, prom.Tag{
		Name: prefix, Type: "Server", Source: prefix, Members: serverMembers,
	})
	out.Desc[prefix] = "node_exporter host"

	fanWidth := digits(len(fans))
	for i, k := range fans {
		name := fmt.Sprintf("%s_Fan%0*d", prefix, fanWidth, i+1)
		out.Manifest.Tags = append(out.Manifest.Tags, prom.Tag{
			Name: name, Type: "Fan", Source: prefix, Members: map[string]prom.Binding{
				"Name":    {Binding: hw.Binding{Const: labelOr(labels, k, k.chip+" "+k.sensor)}},
				"Present": {Binding: hw.Binding{Const: true}},
				"RPM":     {Metric: "node_hwmon_fan_rpm", Labels: map[string]string{"chip": k.chip, "sensor": k.sensor}},
				"Pct":     {Binding: hw.Binding{Const: 0.0}},
				"Fault": {Expr: "rpm == 0", From: map[string]prom.Selector{
					"rpm": {Metric: "node_hwmon_fan_rpm", Labels: map[string]string{"chip": k.chip, "sensor": k.sensor}},
				}},
			},
		})
		out.Desc[name] = "fan " + labelOr(labels, k, k.chip+"/"+k.sensor)
	}

	// Two sensors on the same box legitimately share a human label — a
	// second identical NVMe drive also calls its primary sensor "Composite"
	// (seen on a real workstation with two). A label that collides
	// falls back to the always-unique "<chip>_<sensor>" form for every
	// sensor sharing it, rather than erroring outright; only a name that
	// STILL collides after that fallback (unreachable — chip/sensor pairs
	// are already deduplicated) is the hard error §2 asks for.
	human := map[hwmonKey]string{}    // the display name (Fan/TempSensor.Name)
	tempName := map[hwmonKey]string{} // this sensor's sanitised tag-name segment
	byLabel := map[string][]hwmonKey{}
	for _, k := range temps {
		byLabel[sanitize(labelOr(labels, k, k.chip+"_"+k.sensor))] = append(byLabel[sanitize(labelOr(labels, k, k.chip+"_"+k.sensor))], k)
	}
	for _, k := range temps {
		h := labelOr(labels, k, k.chip+"_"+k.sensor)
		if len(byLabel[sanitize(h)]) > 1 {
			h = k.chip + "_" + k.sensor
		}
		human[k] = h
		tempName[k] = sanitize(h)
	}
	used := map[string]string{} // sanitised name -> chip/sensor, for the collision safety net
	for _, k := range temps {
		san := tempName[k]
		if prev, dup := used[san]; dup && prev != k.chip+"/"+k.sensor {
			return Output{}, fmt.Errorf("codegen: two temperature sensors sanitise to the same tag name %q: %s and %s", san, prev, k.chip+"/"+k.sensor)
		}
		used[san] = k.chip + "/" + k.sensor
		name := fmt.Sprintf("%s_Temp_%s", prefix, san)
		members := map[string]prom.Binding{
			"Name":  {Binding: hw.Binding{Const: human[k]}},
			"Value": {Metric: "node_hwmon_temp_celsius", Labels: map[string]string{"chip": k.chip, "sensor": k.sensor}},
		}
		// HighSP/High (and HighHighSP/HighHigh) are only bound when THIS
		// exact sensor actually carries a threshold — leaving them unbound
		// (rather than defaulting to 0 and letting `derived: "Value >=
		// HighSP"` compare against a phantom zero) is a deliberate codegen
		// choice: a sensor with no threshold must never read High.
		if hasMetric(scrape, "node_hwmon_temp_max_celsius") {
			if _, ok := findSample(scrape, "node_hwmon_temp_max_celsius", map[string]string{"chip": k.chip, "sensor": k.sensor}); ok {
				members["HighSP"] = prom.Binding{Metric: "node_hwmon_temp_max_celsius", Labels: map[string]string{"chip": k.chip, "sensor": k.sensor}}
				members["High"] = prom.Binding{Binding: hw.Binding{Derived: "Value >= HighSP"}}
			}
		}
		if hasMetric(scrape, "node_hwmon_temp_crit_celsius") {
			if _, ok := findSample(scrape, "node_hwmon_temp_crit_celsius", map[string]string{"chip": k.chip, "sensor": k.sensor}); ok {
				members["HighHighSP"] = prom.Binding{Metric: "node_hwmon_temp_crit_celsius", Labels: map[string]string{"chip": k.chip, "sensor": k.sensor}}
				members["HighHigh"] = prom.Binding{Binding: hw.Binding{Derived: "Value >= HighHighSP"}}
			}
		}
		out.Manifest.Tags = append(out.Manifest.Tags, prom.Tag{Name: name, Type: "TempSensor", Source: prefix, Members: members})
		out.Desc[name] = "temperature " + human[k]
	}

	sort.Slice(out.Manifest.Tags, func(i, j int) bool { return out.Manifest.Tags[i].Name < out.Manifest.Tags[j].Name })
	if err := out.Manifest.Validate(); err != nil {
		return Output{}, fmt.Errorf("the node profile generated an invalid manifest:\n%w", err)
	}
	return out, nil
}

// ReportMissing lists every bound metric name this profile's Options.URL
// scrape does not carry — `import`'s "a bound metric absent from the
// scrape ... import reports it" (docs/design/it-drivers.md §3.3, §4).
func ReportMissing(scrape prom.Scrape, m prom.Manifest) []string {
	have := map[string]bool{}
	for name := range scrape.Families {
		have[name] = true
	}
	for _, s := range scrape.Samples {
		have[s.Name] = true
	}
	seen := map[string]bool{}
	var out []string
	walk := func(tag, member, metric string) {
		if metric == "" || have[metric] || seen[metric] {
			return
		}
		seen[metric] = true
		out = append(out, fmt.Sprintf("%s.%s: %s", tag, member, metric))
	}
	for _, tg := range m.Tags {
		for member, b := range tg.Members {
			walk(tg.Name, member, b.Metric)
			for _, sel := range b.From {
				walk(tg.Name, member, sel.Metric)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ── hwmon enumeration ────────────────────────────────────────────────────

type hwmonKey struct{ chip, sensor string }

func (k hwmonKey) String() string { return k.chip + "/" + k.sensor }

func hwmonTemps(scrape prom.Scrape) []hwmonKey {
	return hwmonKeys(scrape, "node_hwmon_temp_celsius")
}

func hwmonFans(scrape prom.Scrape) []hwmonKey {
	return hwmonKeys(scrape, "node_hwmon_fan_rpm")
}

func hwmonKeys(scrape prom.Scrape, metric string) []hwmonKey {
	seen := map[hwmonKey]bool{}
	var out []hwmonKey
	for _, s := range scrape.Samples {
		if s.Name != metric {
			continue
		}
		k := hwmonKey{s.Labels["chip"], s.Labels["sensor"]}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].chip != out[j].chip {
			return out[i].chip < out[j].chip
		}
		return out[i].sensor < out[j].sensor
	})
	return out
}

// hwmonLabels is node_hwmon_sensor_label's {chip,sensor} → human label map
// ("CPU Fan", "Composite", "Core 0") — node_exporter's own naming, when the
// underlying sysfs hwmon device provides one.
func hwmonLabels(scrape prom.Scrape) map[hwmonKey]string {
	out := map[hwmonKey]string{}
	for _, s := range scrape.Samples {
		if s.Name != "node_hwmon_sensor_label" {
			continue
		}
		out[hwmonKey{s.Labels["chip"], s.Labels["sensor"]}] = s.Labels["label"]
	}
	return out
}

func labelOr(labels map[hwmonKey]string, k hwmonKey, fallback string) string {
	if v, ok := labels[k]; ok && v != "" {
		return v
	}
	return fallback
}

// findInlet looks for a hwmon temperature sensor whose human label (or,
// failing that, its chip/sensor identifiers) suggests an inlet/ambient
// reading — the one member (docs/design/it-drivers.md's Server.InletTempC)
// that has no reliable metric name to key off, only vocabulary. Absent any
// match, the caller binds InletTempC const 0, matching every other
// "0 when the device has none" member in the contract.
func findInlet(temps []hwmonKey, labels map[hwmonKey]string) (hwmonKey, bool) {
	for _, k := range temps {
		name := strings.ToLower(labelOr(labels, k, ""))
		if strings.Contains(name, "inlet") || strings.Contains(name, "ambient") {
			return k, true
		}
	}
	return hwmonKey{}, false
}

// ── scrape queries ───────────────────────────────────────────────────────

func hasMetric(scrape prom.Scrape, name string) bool {
	for _, s := range scrape.Samples {
		if s.Name == name {
			return true
		}
	}
	return false
}

func findSample(scrape prom.Scrape, name string, labels map[string]string) (prom.Sample, bool) {
	for _, s := range scrape.Samples {
		if s.Name != name {
			continue
		}
		ok := true
		for k, v := range labels {
			if s.Labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return s, true
		}
	}
	return prom.Sample{}, false
}

func minMetric(scrape prom.Scrape, name string, labels map[string]string) (float64, bool) {
	var v float64
	found := false
	for _, s := range scrape.Samples {
		if s.Name != name {
			continue
		}
		ok := true
		for k, want := range labels {
			if s.Labels[k] != want {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if !found || s.Value < v {
			v = s.Value
		}
		found = true
	}
	return v, found
}

// ── naming ───────────────────────────────────────────────────────────────

// sanitize turns a human sensor label into a tag-name-safe segment:
// [A-Za-z0-9_] only, runs of anything else collapsed to one '_', leading
// digit escaped — docs/design/it-drivers.md §2's naming rule.
func sanitize(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		case !prevUnderscore:
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "X"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

func digits(n int) int {
	if n < 1 {
		n = 1
	}
	return len(strconv.Itoa(n))
}
