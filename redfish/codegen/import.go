// Package codegen turns a Redfish service — live, or recorded as a DMTF
// mockup directory — into the three files a manifest project consumes:
// redfish_manifest.yaml (redfish.LoadManifest), tags/redfish.yaml (composed
// via tag-files:) and hw_types.st (the IT-hardware UDT set). `naut redfish
// import` is the command wrapper. docs/design/it-drivers.md §2, §3.2, §8.
//
// This is where ALL the Redfish schema knowledge lives. The driver is a
// dumb executor of explicit (resource, path) bindings; this package knows
// that a 2019 BMC keeps its fans in Chassis/{id}/Thermal.Fans[] and a 2021+
// one in the ThermalSubsystem/Fans collection, that a temperature Sensor
// may carry ReadingType or only ReadingUnits "Cel", and which sensor is
// the inlet. It probes the service root, walks Systems and Chassis, prefers
// ThermalSubsystem/PowerSubsystem when a chassis serves both generations,
// and writes one explicit binding per member it can fill — a member the
// device does not serve is left out (zero-of-field), never guessed.
//
// Everything is a pure function of what the Getter returns, so a
// regeneration is byte-identical, and a live import gives the same bytes
// as an import of `browse --record`'s recording of the same BMC.
package codegen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

// Getter reads one resource of the service being imported. A resource the
// service does not have is mockup.ErrNotFound (wrapped).
type Getter interface {
	Get(ctx context.Context, uri string) (map[string]any, error)
}

// TreeGetter reads a recording.
func TreeGetter(t mockup.Tree) Getter { return treeGetter(t) }

type treeGetter mockup.Tree

func (t treeGetter) Get(_ context.Context, uri string) (map[string]any, error) {
	return mockup.Tree(t).Get(uri)
}

// FetcherGetter reads a live service through the driver's own transport:
// 404 is ErrNotFound; any other refusal (403 on a resource the account may
// not read) is also treated as not-there, with the status in the error, so
// one forbidden resource skips that part of the device instead of failing
// the whole import.
func FetcherGetter(f redfish.Fetcher) Getter {
	return &fetchGetter{f: f, cache: map[string]map[string]any{}}
}

type fetchGetter struct {
	f     redfish.Fetcher
	cache map[string]map[string]any
}

func (g *fetchGetter) Get(ctx context.Context, uri string) (map[string]any, error) {
	uri = mockup.Normalize(uri)
	if doc, ok := g.cache[uri]; ok {
		return doc, nil
	}
	resp, err := g.f.Get(ctx, uri)
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, fmt.Errorf("%s: HTTP %d: %w", uri, resp.Status, mockup.ErrNotFound)
	}
	doc, err := mockup.Decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	g.cache[uri] = doc
	return doc, nil
}

// Options steer one import.
type Options struct {
	// Tag is the device root: the Server tag's name, the source id and the
	// children's prefix (NODE1 → NODE1_Fan1, NODE1_Temp_CPU).
	Tag string
	// Host is the base URL the manifest's source polls — given even for an
	// offline import, so the recording and the live BMC produce one file.
	Host string
	// Credentials, by name only.
	User, PasswordEnv, PasswordFile string
	// TLS policy for the generated source.
	Insecure bool
	CAFile   string
	// System and Chassis pick by Id when the service has several; empty
	// takes the first system and the chassis it links.
	System, Chassis string
}

// Output is one import: the manifest, and the notes worth printing (what
// was skipped and why).
type Output struct {
	Manifest redfish.Manifest
	// Generation is "ThermalSubsystem/PowerSubsystem" or "Thermal/Power".
	Generation string
	Notes      []string
}

var idRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Import builds the manifest for one system of one service.
func Import(ctx context.Context, g Getter, o Options) (Output, error) {
	if !idRE.MatchString(o.Tag) {
		return Output{}, fmt.Errorf("--tag %q: a tag prefix is [A-Za-z0-9_] and starts with a letter", o.Tag)
	}
	im := &importer{ctx: ctx, g: g, o: o, names: map[string]string{}}
	return im.run()
}

type importer struct {
	ctx   context.Context
	g     Getter
	o     Options
	out   Output
	names map[string]string // generated tag name → what it came from (collisions)
	tags  []redfish.Tag
}

func (im *importer) note(format string, a ...any) {
	im.out.Notes = append(im.out.Notes, fmt.Sprintf(format, a...))
}

func (im *importer) get(uri string) (map[string]any, error) { return im.g.Get(im.ctx, uri) }

// tryGet is get where absence is an answer, not a failure.
func (im *importer) tryGet(uri string) (map[string]any, bool, error) {
	if uri == "" {
		return nil, false, nil
	}
	doc, err := im.get(uri)
	if errors.Is(err, mockup.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return doc, true, nil
}

func (im *importer) run() (Output, error) {
	root, err := im.get(mockup.Root)
	if err != nil {
		return Output{}, fmt.Errorf("service root: %w", err)
	}
	sysURI, err := im.pick(link(root, "Systems"), im.o.System, "system", "--system")
	if err != nil {
		return Output{}, err
	}
	sys, err := im.get(sysURI)
	if err != nil {
		return Output{}, err
	}
	chURI := ""
	if im.o.Chassis != "" {
		chURI, err = im.pick(link(root, "Chassis"), im.o.Chassis, "chassis", "--chassis")
		if err != nil {
			return Output{}, err
		}
	} else if links := linkList(sys, "Links", "Chassis"); len(links) > 0 {
		chURI = links[0]
	} else if chURI, err = im.pick(link(root, "Chassis"), "", "chassis", "--chassis"); err != nil {
		return Output{}, err
	}
	ch, err := im.get(chURI)
	if err != nil {
		return Output{}, err
	}

	server := newTag(im.o.Tag, "Server", im.o.Tag)
	server.Desc = strings.TrimSpace(str(sys, "Manufacturer") + " " + str(sys, "Model"))
	im.names[im.o.Tag] = "the system " + sysURI
	// Online is not bound: hw.Base keeps an unbound Online member equal to
	// the source's freshness, so it goes false when the BMC stops answering
	// instead of holding its last true.
	if has(sys, "PowerState") {
		server.bind("PowerOn", sysURI, "PowerState", func(b *redfish.MemberBinding) { b.Eq = "On" })
	}
	if has(sys, "Status", "Health") {
		server.bind("Health", sysURI, "Status.Health", func(b *redfish.MemberBinding) { b.Map = healthMap() })
		server.derived("Fault", "Health == 2")
		server.derived("Warning", "Health == 1")
	}
	if has(sys, "Model") {
		server.bind("Model", sysURI, "Model", nil)
	}
	if has(sys, "SerialNumber") {
		server.bind("Serial", sysURI, "SerialNumber", nil)
	}
	if pm := link(obj(sys, "ProcessorSummary"), "Metrics"); pm != "" {
		if doc, ok, err := im.tryGet(pm); err != nil {
			return Output{}, err
		} else if ok && has(doc, "BandwidthPercent") {
			server.bind("CpuPct", pm, "BandwidthPercent", nil)
		}
	}

	var fans, psus, temps []*tagB
	var inlet *tagB
	thermalNew, powerNew := link(ch, "ThermalSubsystem"), link(ch, "PowerSubsystem")
	tsub, haveTsub, err := im.tryGet(thermalNew)
	if err != nil {
		return Output{}, err
	}
	psub, havePsub, err := im.tryGet(powerNew)
	if err != nil {
		return Output{}, err
	}
	if haveTsub || havePsub {
		im.out.Generation = "ThermalSubsystem/PowerSubsystem"
		if link(ch, "Thermal") != "" || link(ch, "Power") != "" {
			im.note("the chassis serves both generations; bound the ThermalSubsystem/PowerSubsystem form")
		}
		if fans, err = im.newFans(tsub, haveTsub); err != nil {
			return Output{}, err
		}
		if psus, err = im.newPSUs(psub, havePsub); err != nil {
			return Output{}, err
		}
		if temps, inlet, err = im.newTemps(ch, chURI); err != nil {
			return Output{}, err
		}
		if err := im.newServerThermal(server, ch, tsub, haveTsub); err != nil {
			return Output{}, err
		}
	} else {
		im.out.Generation = "Thermal/Power"
		thermalURI, powerURI := link(ch, "Thermal"), link(ch, "Power")
		thermal, haveThermal, err := im.tryGet(thermalURI)
		if err != nil {
			return Output{}, err
		}
		power, havePower, err := im.tryGet(powerURI)
		if err != nil {
			return Output{}, err
		}
		if !haveThermal && !havePower {
			im.note("chassis %s serves neither ThermalSubsystem/PowerSubsystem nor Thermal/Power: only the Server tag is generated", chURI)
		}
		if haveThermal {
			fans = im.legacyFans(thermalURI, thermal)
			if temps, inlet, err = im.legacyTemps(thermalURI, thermal); err != nil {
				return Output{}, err
			}
			if n := len(arr(thermal, "Temperatures")); n > 0 {
				server.bind("MaxTempC", thermalURI, "Temperatures[*].ReadingCelsius", func(b *redfish.MemberBinding) { b.Agg = "max" })
			}
		}
		if havePower {
			psus = im.legacyPSUs(powerURI, power)
			im.legacyPowerW(server, powerURI, power)
		}
	}
	if inlet != nil {
		server.bind("InletTempC", inlet.valueRes, inlet.valuePath, nil)
	} else {
		im.note("no inlet/intake/ambient temperature sensor: Server.InletTempC stays 0")
	}
	if _, ok := server.members["PowerW"]; !ok {
		im.note("no chassis power reading: Server.PowerW stays 0")
	}
	if _, ok := server.members["MaxTempC"]; !ok {
		im.note("no aggregate temperature list: Server.MaxTempC stays 0")
	}
	server.constant("FanCount", len(fans))
	server.constant("PsuCount", len(psus))
	server.constant("TempCount", len(temps))

	parts, err := im.parts(root, sys, temps)
	if err != nil {
		return Output{}, err
	}

	im.add(server)
	for _, list := range [][]*tagB{fans, psus, temps, parts} {
		for _, t := range list {
			im.add(t)
		}
	}
	src := redfish.Source{
		ID: im.o.Tag, Host: im.o.Host, User: im.o.User,
		PasswordEnv: im.o.PasswordEnv, PasswordFile: im.o.PasswordFile,
		TLS: redfish.TLS{Insecure: im.o.Insecure, CAFile: im.o.CAFile},
	}
	im.out.Manifest = redfish.Manifest{Sources: []redfish.Source{src}, Tags: im.tags}
	if err := im.out.Manifest.Validate(); err != nil {
		return Output{}, fmt.Errorf("the service generates an invalid manifest:\n%w", err)
	}
	return im.out, nil
}

// pick resolves a collection member: by Id when asked, else the first.
func (im *importer) pick(collURI, want, what, flag string) (string, error) {
	if collURI == "" {
		return "", fmt.Errorf("the service root links no %s collection", what)
	}
	coll, err := im.get(collURI)
	if err != nil {
		return "", err
	}
	members := members(coll)
	if len(members) == 0 {
		return "", fmt.Errorf("%s: no %s", collURI, what)
	}
	if want == "" {
		if len(members) > 1 {
			im.note("%d %s resources; took %s (pick another with %s <Id>)", len(members), what, members[0], flag)
		}
		return members[0], nil
	}
	var ids []string
	for _, m := range members {
		id := m[strings.LastIndexByte(m, '/')+1:]
		if id == want {
			return m, nil
		}
		ids = append(ids, id)
	}
	return "", fmt.Errorf("%s %q: not in %s (have %s)", what, want, collURI, strings.Join(ids, ", "))
}

func (im *importer) add(t *tagB) {
	im.tags = append(im.tags, t.build())
}

// claim registers a generated name, refusing a collision (two sensors that
// sanitise to the same name must not silently merge into one tag).
func (im *importer) claim(name, from string) error {
	if prev, ok := im.names[name]; ok {
		return fmt.Errorf("%s and %s both generate the tag %s — rename one on the BMC, or import with --tag chosen so they differ", prev, from, name)
	}
	im.names[name] = from
	return nil
}

// ── 2021+: ThermalSubsystem / PowerSubsystem / Sensors ───────────────────

func (im *importer) newFans(tsub map[string]any, have bool) ([]*tagB, error) {
	if !have {
		return nil, nil
	}
	collURI := link(tsub, "Fans")
	coll, ok, err := im.tryGet(collURI)
	if err != nil || !ok {
		return nil, err
	}
	uris := members(coll)
	var out []*tagB
	for i, u := range uris {
		fan, ok, err := im.tryGet(u)
		if err != nil {
			return nil, err
		}
		if !ok {
			im.note("fan %s is listed but not served: skipped", u)
			continue
		}
		name := childName(im.o.Tag, "Fan", i+1, len(uris))
		if err := im.claim(name, "fan "+u); err != nil {
			return nil, err
		}
		t := newTag(name, "Fan", im.o.Tag)
		t.Desc = str(fan, "Name")
		if has(fan, "Name") {
			t.bind("Name", u, "Name", nil)
		}
		statusBindings(t, u, "", fan)
		if has(fan, "SpeedPercent", "SpeedRPM") {
			t.bind("RPM", u, "SpeedPercent.SpeedRPM", nil)
		}
		if has(fan, "SpeedPercent", "Reading") {
			t.bind("Pct", u, "SpeedPercent.Reading", nil)
		}
		// A BMC that reports no Health on a fan (the X14 omits it) still
		// says a fan stopped: Fault is the contract's "present and stopped".
		_, fault := t.members["Fault"]
		_, present := t.members["Present"]
		_, rpm := t.members["RPM"]
		if !fault && present && rpm {
			t.derived("Fault", "Present && RPM < 1")
		}
		out = append(out, t)
	}
	return out, nil
}

func (im *importer) newPSUs(psub map[string]any, have bool) ([]*tagB, error) {
	if !have {
		return nil, nil
	}
	coll, ok, err := im.tryGet(link(psub, "PowerSupplies"))
	if err != nil || !ok {
		return nil, err
	}
	uris := members(coll)
	var out []*tagB
	for i, u := range uris {
		psu, ok, err := im.tryGet(u)
		if err != nil {
			return nil, err
		}
		if !ok {
			im.note("power supply %s is listed but not served: skipped", u)
			continue
		}
		name := childName(im.o.Tag, "PSU", i+1, len(uris))
		if err := im.claim(name, "power supply "+u); err != nil {
			return nil, err
		}
		t := newTag(name, "PSU", im.o.Tag)
		t.Desc = str(psu, "Name")
		if has(psu, "Name") {
			t.bind("Name", u, "Name", nil)
		}
		statusBindings(t, u, "", psu)
		if has(psu, "PowerCapacityWatts") {
			t.bind("CapacityW", u, "PowerCapacityWatts", nil)
		}
		if has(psu, "LineInputStatus") {
			t.bind("InputOk", u, "LineInputStatus", func(b *redfish.MemberBinding) {
				b.Map = map[string]any{"Normal": true, "LossOfInput": false, "OutOfRange": false}
			})
		}
		if mu := link(psu, "Metrics"); mu != "" {
			metrics, ok, err := im.tryGet(mu)
			if err != nil {
				return nil, err
			}
			if ok {
				if has(metrics, "InputVoltage", "Reading") {
					t.bind("InputV", mu, "InputVoltage.Reading", nil)
				}
				if has(metrics, "OutputPowerWatts", "Reading") {
					t.bind("OutputW", mu, "OutputPowerWatts.Reading", nil)
				}
			}
		}
		if _, ok := t.members["InputOk"]; !ok {
			if _, ok := t.members["InputV"]; ok {
				t.derived("InputOk", "Present && InputV > 0")
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// newTemps: the chassis' Sensors collection, filtered to temperatures. A
// temperature Sensor says so by ReadingType "Temperature" — or, in DMTF's
// own public-rackmount1 mockup and older firmware, only by ReadingUnits
// "Cel": both count.
func (im *importer) newTemps(ch map[string]any, chURI string) ([]*tagB, *tagB, error) {
	collURI := link(ch, "Sensors")
	coll, ok, err := im.tryGet(collURI)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		im.note("chassis %s links no Sensors collection: no TempSensor tags", chURI)
		return nil, nil, nil
	}
	type found struct {
		uri string
		doc map[string]any
	}
	var ts []found
	for _, u := range members(coll) {
		doc, ok, err := im.tryGet(u)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		if str(doc, "ReadingType") != "Temperature" && str(doc, "ReadingUnits") != "Cel" {
			continue
		}
		if st := str(obj(doc, "Status"), "State"); st == "Absent" || st == "Disabled" {
			im.note("temperature sensor %q is %s: skipped", str(doc, "Name"), st)
			continue
		}
		ts = append(ts, found{u, doc})
	}
	var out []*tagB
	var inlet *tagB
	best := 0
	for _, f := range ts {
		label := str(f.doc, "Name")
		if label == "" {
			label = str(f.doc, "Id")
		}
		name := im.o.Tag + "_Temp_" + Sanitise(label)
		if err := im.claim(name, fmt.Sprintf("temperature sensor %q", label)); err != nil {
			return nil, nil, err
		}
		t := newTag(name, "TempSensor", im.o.Tag)
		t.Desc = label
		if has(f.doc, "Name") {
			t.bind("Name", f.uri, "Name", nil)
		}
		t.bind("Value", f.uri, "Reading", nil)
		t.valueRes, t.valuePath = f.uri, "Reading"
		t.bind("Fault", f.uri, "Reading", func(b *redfish.MemberBinding) { b.Exists = boolPtr(false) })
		if has(f.doc, "Thresholds", "UpperCaution", "Reading") {
			t.bind("HighSP", f.uri, "Thresholds.UpperCaution.Reading", nil)
			t.derived("High", "HighSP > 0 && Value >= HighSP")
		}
		if has(f.doc, "Thresholds", "UpperCritical", "Reading") {
			t.bind("HighHighSP", f.uri, "Thresholds.UpperCritical.Reading", nil)
			t.derived("HighHigh", "HighHighSP > 0 && Value >= HighHighSP")
		}
		if r := inletRank(str(f.doc, "PhysicalContext"), label); r > best {
			inlet, best = t, r
		}
		out = append(out, t)
	}
	return out, inlet, nil
}

// newServerThermal binds MaxTempC (ThermalMetrics' reading list) and
// PowerW (the chassis EnvironmentMetrics) when the service has them.
func (im *importer) newServerThermal(server *tagB, ch, tsub map[string]any, haveTsub bool) error {
	if haveTsub {
		mu := link(tsub, "ThermalMetrics")
		metrics, ok, err := im.tryGet(mu)
		if err != nil {
			return err
		}
		if ok && len(arr(metrics, "TemperatureReadingsCelsius")) > 0 {
			server.bind("MaxTempC", mu, "TemperatureReadingsCelsius[*].Reading", func(b *redfish.MemberBinding) { b.Agg = "max" })
		}
	}
	eu := link(ch, "EnvironmentMetrics")
	env, ok, err := im.tryGet(eu)
	if err != nil {
		return err
	}
	if ok && has(env, "PowerWatts", "Reading") {
		server.bind("PowerW", eu, "PowerWatts.Reading", nil)
		return nil
	}
	// Some services carry the new thermal form and only the legacy power
	// reading; take it rather than leave PowerW empty.
	if pu := link(ch, "Power"); pu != "" {
		power, ok, err := im.tryGet(pu)
		if err != nil {
			return err
		}
		if ok {
			im.legacyPowerW(server, pu, power)
		}
	}
	return nil
}

// ── legacy: Chassis/{id}/Thermal and Power ────────────────────────────────

func (im *importer) legacyFans(uri string, thermal map[string]any) []*tagB {
	list := arr(thermal, "Fans")
	var out []*tagB
	for i, e := range list {
		fan, _ := e.(map[string]any)
		sel, ok := selector(list, fan)
		if !ok {
			im.note("fan #%d in %s has no unique MemberId or Name to select it by: skipped", i, uri)
			continue
		}
		name := childName(im.o.Tag, "Fan", i+1, len(list))
		if err := im.claim(name, "fan "+sel); err != nil {
			im.note("%v", err)
			continue
		}
		p := "Fans" + sel + "."
		t := newTag(name, "Fan", im.o.Tag)
		t.Desc = firstStr(fan, "Name", "FanName")
		switch {
		case has(fan, "Name"):
			t.bind("Name", uri, p+"Name", nil)
		case has(fan, "FanName"):
			t.bind("Name", uri, p+"FanName", nil)
		}
		statusBindings(t, uri, p, fan)
		units := str(fan, "ReadingUnits")
		switch {
		case has(fan, "Reading") && units == "Percent":
			t.bind("Pct", uri, p+"Reading", nil)
		case has(fan, "Reading"):
			t.bind("RPM", uri, p+"Reading", nil)
		case has(fan, "ReadingRPM"):
			t.bind("RPM", uri, p+"ReadingRPM", nil)
		}
		out = append(out, t)
	}
	return out
}

func (im *importer) legacyTemps(uri string, thermal map[string]any) ([]*tagB, *tagB, error) {
	list := arr(thermal, "Temperatures")
	var out []*tagB
	var inlet *tagB
	best := 0
	for i, e := range list {
		ts, _ := e.(map[string]any)
		label := str(ts, "Name")
		if st := str(obj(ts, "Status"), "State"); st == "Absent" || st == "Disabled" {
			im.note("temperature sensor %q is %s: skipped", label, st)
			continue
		}
		sel, ok := selector(list, ts)
		if !ok {
			im.note("temperature #%d in %s has no unique MemberId or Name to select it by: skipped", i, uri)
			continue
		}
		if label == "" {
			label = str(ts, "MemberId")
		}
		name := im.o.Tag + "_Temp_" + Sanitise(label)
		if err := im.claim(name, fmt.Sprintf("temperature sensor %q", label)); err != nil {
			return nil, nil, err
		}
		p := "Temperatures" + sel + "."
		t := newTag(name, "TempSensor", im.o.Tag)
		t.Desc = label
		if has(ts, "Name") {
			t.bind("Name", uri, p+"Name", nil)
		}
		t.bind("Value", uri, p+"ReadingCelsius", nil)
		t.valueRes, t.valuePath = uri, p+"ReadingCelsius"
		t.bind("Fault", uri, p+"ReadingCelsius", func(b *redfish.MemberBinding) { b.Exists = boolPtr(false) })
		if has(ts, "UpperThresholdNonCritical") {
			t.bind("HighSP", uri, p+"UpperThresholdNonCritical", nil)
			t.derived("High", "HighSP > 0 && Value >= HighSP")
		}
		if has(ts, "UpperThresholdCritical") {
			t.bind("HighHighSP", uri, p+"UpperThresholdCritical", nil)
			t.derived("HighHigh", "HighHighSP > 0 && Value >= HighHighSP")
		}
		if r := inletRank(str(ts, "PhysicalContext"), label); r > best {
			inlet, best = t, r
		}
		out = append(out, t)
	}
	return out, inlet, nil
}

func (im *importer) legacyPSUs(uri string, power map[string]any) []*tagB {
	list := arr(power, "PowerSupplies")
	var out []*tagB
	for i, e := range list {
		psu, _ := e.(map[string]any)
		sel, ok := selector(list, psu)
		if !ok {
			im.note("power supply #%d in %s has no unique MemberId or Name to select it by: skipped", i, uri)
			continue
		}
		name := childName(im.o.Tag, "PSU", i+1, len(list))
		if err := im.claim(name, "power supply "+sel); err != nil {
			im.note("%v", err)
			continue
		}
		p := "PowerSupplies" + sel + "."
		t := newTag(name, "PSU", im.o.Tag)
		t.Desc = str(psu, "Name")
		if has(psu, "Name") {
			t.bind("Name", uri, p+"Name", nil)
		}
		statusBindings(t, uri, p, psu)
		if has(psu, "LineInputVoltage") {
			t.bind("InputV", uri, p+"LineInputVoltage", nil)
			t.derived("InputOk", "Present && InputV > 0")
		}
		switch {
		case has(psu, "PowerOutputWatts"):
			t.bind("OutputW", uri, p+"PowerOutputWatts", nil)
		case has(psu, "LastPowerOutputWatts"):
			t.bind("OutputW", uri, p+"LastPowerOutputWatts", nil)
		}
		if has(psu, "PowerCapacityWatts") {
			t.bind("CapacityW", uri, p+"PowerCapacityWatts", nil)
		}
		out = append(out, t)
	}
	return out
}

func (im *importer) legacyPowerW(server *tagB, uri string, power map[string]any) {
	list := arr(power, "PowerControl")
	if len(list) == 0 {
		return
	}
	pc, _ := list[0].(map[string]any)
	sel, ok := selector(list, pc)
	if ok && has(pc, "PowerConsumedWatts") {
		server.bind("PowerW", uri, "PowerControl"+sel+".PowerConsumedWatts", nil)
	}
}

// statusBindings: Present from Status.State (anything but Absent), Fault
// from Status.Health (anything but OK). prefix is the array selector path
// for a legacy array element, "" for a resource of its own.
func statusBindings(t *tagB, uri, prefix string, doc map[string]any) {
	if has(doc, "Status", "State") {
		t.bind("Present", uri, prefix+"Status.State", func(b *redfish.MemberBinding) { b.Map = presentMap() })
	}
	if has(doc, "Status", "Health") {
		t.bind("Fault", uri, prefix+"Status.Health", func(b *redfish.MemberBinding) {
			b.Map = map[string]any{"OK": false, "Warning": true, "Critical": true}
		})
	}
}

// presentMap is Resource.State in full (DSP8010 2026.2): every state but
// Absent means the part is there. A value outside the map is logged and
// the member left as it was — a new enum member must be added here, not
// guessed.
func presentMap() map[string]any {
	m := map[string]any{"Absent": false}
	for _, s := range []string{"Enabled", "Disabled", "StandbyOffline", "StandbySpare", "InTest", "Starting",
		"UnavailableOffline", "Deferring", "Quiesced", "Updating", "Qualified", "Degraded"} {
		m[s] = true
	}
	return m
}

func healthMap() map[string]any { return map[string]any{"OK": 0, "Warning": 1, "Critical": 2} }

// selector picks an array element by MemberId when it is unique, else by
// Name — never by position (path.go).
func selector(list []any, e map[string]any) (string, bool) {
	for _, key := range []string{"MemberId", "Name"} {
		v, ok := scalarKey(e[key])
		if !ok || strings.ContainsAny(v, "]") {
			continue
		}
		n := 0
		for _, o := range list {
			if om, _ := o.(map[string]any); om != nil {
				if ov, ok := scalarKey(om[key]); ok && ov == v {
					n++
				}
			}
		}
		if n == 1 {
			return "[" + key + "=" + v + "]", true
		}
	}
	return "", false
}

func scalarKey(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, x != ""
	case json.Number:
		return x.String(), true
	}
	return "", false
}

// inletRank scores how surely a temperature sensor is the server's inlet:
// PhysicalContext Intake first, then a name that says inlet/intake, then
// the room/ambient sensor as a last resort — DMTF's public-rackmount1 lists
// "Ambient Temperature" (Room, 22.5 °C) BEFORE "Front Panel Intake
// Temperature" (Intake, 24.8 °C), and the first-match rule this replaced
// reported the room, not the air the server breathes. 0 is not an inlet.
func inletRank(ctx, name string) int {
	l := strings.ToLower(name)
	switch {
	case ctx == "Intake":
		return 4
	case strings.Contains(l, "inlet") || strings.Contains(l, "intake"):
		return 3
	case ctx == "Room":
		return 2
	case strings.Contains(l, "ambient"):
		return 1
	}
	return 0
}

// childName is <tag>_<Kind><NN>, NN zero-padded to the widest index on the
// device (Fan1…Fan6, Port01…Port28).
func childName(tag, kind string, i, n int) string {
	w := len(strconv.Itoa(n))
	return fmt.Sprintf("%s_%s%0*d", tag, kind, w, i)
}

var nonWord = regexp.MustCompile(`[^A-Za-z0-9]+`)

// Sanitise turns a sensor's own name into a tag-name segment: runs of
// anything outside [A-Za-z0-9] become one underscore, and a "Temp" /
// "Temperature" word is dropped when others remain — "CPU1 Temp" is
// NODE1_Temp_CPU1, not NODE1_Temp_CPU1_Temp.
func Sanitise(label string) string {
	words := strings.FieldsFunc(nonWord.ReplaceAllString(label, " "), func(r rune) bool { return r == ' ' })
	var kept []string
	for _, w := range words {
		lw := strings.ToLower(w)
		if lw == "temp" || lw == "temperature" {
			continue
		}
		kept = append(kept, w)
	}
	if len(kept) == 0 {
		kept = words
	}
	s := strings.Join(kept, "_")
	if s == "" {
		return "Sensor"
	}
	return s
}

// ── tag builder ─────────────────────────────────────────────────────────

type tagB struct {
	name, typ, source, Desc string
	members                 map[string]redfish.MemberBinding
	valueRes, valuePath     string // a TempSensor's reading, for Server.InletTempC
}

func newTag(name, typ, source string) *tagB {
	return &tagB{name: name, typ: typ, source: source, members: map[string]redfish.MemberBinding{}}
}

func (t *tagB) bind(member, resource, path string, with func(*redfish.MemberBinding)) {
	b := redfish.MemberBinding{Resource: resource, Path: path}
	if with != nil {
		with(&b)
	}
	t.members[member] = b
}

func (t *tagB) derived(member, expr string) {
	t.members[member] = redfish.MemberBinding{Binding: hw.Binding{Derived: expr}}
}

func (t *tagB) constant(member string, v any) {
	t.members[member] = redfish.MemberBinding{Binding: hw.Binding{Const: v}}
}

// build hoists the most-used resource to the tag, so a fan's members say
// only their paths.
func (t *tagB) build() redfish.Tag {
	count := map[string]int{}
	for _, b := range t.members {
		if b.Resource != "" {
			count[b.Resource]++
		}
	}
	best := ""
	for r, n := range count {
		if n > count[best] || n == count[best] && r < best {
			best = r
		}
	}
	out := redfish.Tag{Name: t.name, Type: t.typ, Source: t.source, Desc: t.Desc, Resource: best, Members: map[string]redfish.MemberBinding{}}
	for m, b := range t.members {
		if b.Resource == best {
			b.Resource = ""
		}
		out.Members[m] = b
	}
	return out
}

// ── JSON helpers ────────────────────────────────────────────────────────

func obj(doc map[string]any, key string) map[string]any {
	m, _ := doc[key].(map[string]any)
	return m
}

func arr(doc map[string]any, key string) []any {
	a, _ := doc[key].([]any)
	return a
}

func str(doc map[string]any, key string) string {
	s, _ := doc[key].(string)
	return s
}

func firstStr(doc map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(doc, k); s != "" {
			return s
		}
	}
	return ""
}

// has reports whether a nested property exists and is not null.
func has(doc map[string]any, keys ...string) bool {
	var cur any = doc
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = m[k]
		if !ok || cur == nil {
			return false
		}
	}
	return true
}

// link is doc[key]["@odata.id"].
func link(doc map[string]any, key string) string {
	return str(obj(doc, key), "@odata.id")
}

// linkList is doc[a][b][]["@odata.id"].
func linkList(doc map[string]any, a, b string) []string {
	var out []string
	for _, e := range arr(obj(doc, a), b) {
		if m, ok := e.(map[string]any); ok {
			if id := str(m, "@odata.id"); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// members is a collection's member URIs, in the order the service lists
// them (which is the child numbering: Fan1 is the first listed).
func members(coll map[string]any) []string {
	var out []string
	for _, e := range arr(coll, "Members") {
		if m, ok := e.(map[string]any); ok {
			if id := str(m, "@odata.id"); id != "" {
				out = append(out, mockup.Normalize(id))
			}
		}
	}
	return out
}

func boolPtr(b bool) *bool { return &b }
