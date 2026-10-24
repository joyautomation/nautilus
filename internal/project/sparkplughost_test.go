package project

// Sparkplug host driver claims that cross the manifest: group-ids alone name
// the group, stale-after reaches the driver's ticker, an absent tag faults the
// scan until its site births, on-unknown reaches the driver, and queued
// writes show on /api/drivers.

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// SPH-001: `group-ids` alone satisfies the group requirement.
func TestSparkplugHostGroupIDsAlone(t *testing.T) {
	files := fsys(`tasks:
  - program: program.fbd
driver:
  type: sparkplug-host
  broker: tcp://b:1883
  host-id: central
  manifest: m.yaml
  group-ids: [Plant, Plant2]
`)
	files["m.yaml"] = &fstest.MapFile{Data: []byte("group: Plant\n")}
	p, err := Load(files, "")
	if err != nil {
		t.Fatalf("group-ids alone must satisfy the group requirement: %v", err)
	}
	drv, ok := p.Runtime.Driver.(*sphost.Driver)
	if !ok {
		t.Fatalf("driver = %T", p.Runtime.Driver)
	}
	if got := drv.Status().Groups; len(got) != 2 || got[0] != "Plant" || got[1] != "Plant2" {
		t.Errorf("groups = %v, want [Plant Plant2]", got)
	}
}

// edgeClient is a raw MQTT client standing in for an edge node.
func edgeClient(t *testing.T, url string) mqtt.Client {
	t.Helper()
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(url).SetClientID("edge1-sim").
		SetCleanSession(true).SetOrderMatters(true))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("edge connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	return cli
}

// birth publishes an NBIRTH for Edge1 carrying the given metrics.
func birth(t *testing.T, cli mqtt.Client, metrics ...sparkplug.Metric) {
	t.Helper()
	body, err := sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics: append([]sparkplug.Metric{
			{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(1)},
		}, metrics...),
	}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if tok := cli.Publish("spBv1.0/Plant/NBIRTH/Edge1", 1, false, body); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("publish NBIRTH: %v", tok.Error())
	}
}

func fit(v float64) sparkplug.Metric {
	return sparkplug.Metric{Name: "Zone1/FIT_001", Datatype: spb.DataType_Double, Value: v}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startHost(t *testing.T, url, extra string) *sphost.Driver {
	t.Helper()
	drv := loadHostDriver(t, url, extra)
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	waitFor(t, "the host to connect", func() bool { return drv.Status().Connected })
	return drv
}

// SPH-020: `stale-after: 200ms` in the manifest reaches the driver, and the
// driver's own ticker (nobody calls the sweep) marks the silent node stale
// while its value stays.
func TestSparkplugHostStaleAfterReachesTheDriver(t *testing.T) {
	url := testBroker(t, nil)
	drv := startHost(t, url, "  stale-after: 200ms\n  rebirth-on-start: false\n")
	birth(t, edgeClient(t, url), fit(12.5))
	waitFor(t, "the site to birth", func() bool {
		v, err := drv.ReadInputs()
		return err == nil && v["Edge1__Online"] == true
	})

	// The edge says nothing more. Without the ticker nothing would ever
	// change; with it the node goes stale within a couple of ticks.
	tag := "Edge1_ZONE1_FIT_001"
	waitFor(t, "the silent node to go stale", func() bool { return drv.Quality()[tag] == nio.Stale })

	v, err := drv.ReadInputs()
	if err != nil {
		t.Fatal(err)
	}
	if v[tag] != 12.5 {
		t.Errorf("%s = %v after going stale, want the last value 12.5 undisturbed", tag, v[tag])
	}
	if v["Edge1__Online"] != true {
		t.Errorf("a stale node is silent, not dead: Edge1__Online = %v, want true", v["Edge1__Online"])
	}
}

// SPH-041: `on-unknown: strict` reaches the driver — a metric the manifest
// never bound makes the driver, and /api/drivers, report degraded.
func TestSparkplugHostOnUnknownReachesTheDriver(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		degraded bool
	}{{"strict", true}, {"log", false}, {"ignore", false}} {
		t.Run(tc.mode, func(t *testing.T) {
			url := testBroker(t, nil)
			p := hostProject(t, url, "  on-unknown: "+tc.mode+"\n  rebirth-on-start: false\n")
			drv := p.Runtime.Driver.(*sphost.Driver)
			drv.Start(context.Background())
			t.Cleanup(drv.Stop)
			waitFor(t, "the host to connect", func() bool { return drv.Status().Connected })
			birth(t, edgeClient(t, url), fit(1), sparkplug.Metric{
				Name: "Zone9/Unbound", Datatype: spb.DataType_Double, Value: 2.0})
			waitFor(t, "the site to birth", func() bool {
				v, err := drv.ReadInputs()
				return err == nil && v["Edge1__Online"] == true
			})
			st := drv.Status()
			if st.Degraded != tc.degraded {
				t.Errorf("on-unknown %s: Degraded = %v, want %v", tc.mode, st.Degraded, tc.degraded)
			}
			if st.Unknown != 1 {
				t.Errorf("on-unknown %s: Unknown = %d, want the one unbound metric counted", tc.mode, st.Unknown)
			}
			if got := hostStatus(st).State; (got == "degraded") != tc.degraded {
				t.Errorf("on-unknown %s: /api/drivers state = %q", tc.mode, got)
			}
		})
	}
}

func hostProject(t *testing.T, url, extra string) *Project {
	t.Helper()
	p, err := Load(hostDriverFiles(url, extra), "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// SPH-017: until its site births, a data tag is absent from the driver's
// snapshot, so a program that reads it faults its scan; a program that guards
// on `__Online` (false from t=0) does not. After the birth neither faults.
func TestSparkplugHostAbsentTagFaultsTheScanUntilBirth(t *testing.T) {
	url := testBroker(t, nil)
	files := hostDriverFiles(url, "  rebirth-on-start: false\n")
	files["nautilus.yaml"] = &fstest.MapFile{Data: []byte(`name: central
tasks:
  - name: bare
    program: bare.st
    scan: 50ms
  - name: guarded
    program: guarded.st
    scan: 50ms
driver:
  type: sparkplug-host
  broker: "` + url + `"
  group-id: Plant
  host-id: central
  manifest: sparkplug_manifest.yaml
  rebirth-on-start: false
tags:
  - { name: Edge1_ZONE1_FIT_001, role: input }
  - { name: Edge1__Online, role: input }
  - { name: BareOut, role: state, init: 0.0 }
  - { name: GuardedOut, role: state, init: 0.0 }
`)}
	files["bare.st"] = &fstest.MapFile{Data: []byte(`PROGRAM Bare
VAR_EXTERNAL
	Edge1_ZONE1_FIT_001 : REAL;
	BareOut : REAL;
END_VAR
BareOut := Edge1_ZONE1_FIT_001;
END_PROGRAM
`)}
	files["guarded.st"] = &fstest.MapFile{Data: []byte(`PROGRAM Guarded
VAR_EXTERNAL
	Edge1_ZONE1_FIT_001 : REAL;
	Edge1__Online : BOOL;
	GuardedOut : REAL;
END_VAR
IF Edge1__Online THEN
	GuardedOut := Edge1_ZONE1_FIT_001;
END_IF;
END_PROGRAM
`)}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	drv := p.Runtime.Driver.(*sphost.Driver)
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	waitFor(t, "the host to connect", func() bool { return drv.Status().Connected })

	// Before the birth: the data tag is absent, __Online is present and false.
	snap, err := drv.ReadInputs()
	if err != nil {
		t.Fatal(err)
	}
	if _, present := snap["Edge1_ZONE1_FIT_001"]; present {
		t.Fatal("the data tag is in the snapshot before its site has birthed")
	}
	if v, present := snap["Edge1__Online"]; !present || v != false {
		t.Fatalf("Edge1__Online = %v (present %v), want present and false from t=0", v, present)
	}
	// "bare" is the main task, "guarded" an additional one.
	rt.Scan()
	if err := rt.ScanTask("guarded"); err != nil {
		t.Fatal(err)
	}
	st := rt.Stats()
	if st.LogicErrors != 1 {
		t.Errorf("a program reading an unbirthed tag: LogicErrors = %d, want its scan faulted", st.LogicErrors)
	} else if !strings.Contains(st.LastError, "Edge1_ZONE1_FIT_001") {
		t.Errorf("fault %q does not name the absent tag", st.LastError)
	}
	if len(st.Tasks) != 1 || st.Tasks[0].LogicErrors != 0 {
		t.Errorf("a program guarded on __Online must not fault before the birth: %+v", st.Tasks)
	}

	birth(t, edgeClient(t, url), fit(12.5))
	waitFor(t, "the site to birth", func() bool {
		v, err := drv.ReadInputs()
		return err == nil && v["Edge1__Online"] == true
	})
	rt.Scan() // the main scan pulls the new snapshot into the tag store
	rt.Scan()
	if err := rt.ScanTask("guarded"); err != nil {
		t.Fatal(err)
	}
	st = rt.Stats()
	if st.LogicErrors != 1 {
		t.Errorf("after the birth the bare program still faults: LogicErrors = %d (%s)", st.LogicErrors, st.LastError)
	}
	if st.Tasks[0].LogicErrors != 0 {
		t.Errorf("guarded task faulted after the birth: %+v", st.Tasks[0])
	}
	if v := rt.Tags().Real("BareOut"); v != 12.5 {
		t.Errorf("BareOut = %v after the birth, want the delivered 12.5", v)
	}
}

// SPH-025: a write to a dark site is queued, and the count shows as a
// "queued writes" metric on /api/drivers.
func TestSparkplugHostQueuedWritesAreReportedOnAPIDrivers(t *testing.T) {
	url := testBroker(t, nil)
	files := hostFilesWith(url, "  rebirth-on-start: false\n",
		`    - { name: Edge1_ZONE1_SP_001, node: Edge1, device: "", metric: Zone1/SP_001, type: Double, arraylen: 0, writable: true, init: 0.0 }
`)
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	drv := p.Runtime.Driver.(*sphost.Driver)
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	waitFor(t, "the host to connect", func() bool { return drv.Status().Connected })

	queuedMetric := func() (float64, bool) {
		for _, m := range p.DriverStatus(nil, nil)()[0].Metrics {
			if m.Label == "queued writes" {
				return m.Value, true
			}
		}
		return 0, false
	}
	if _, shown := queuedMetric(); shown {
		t.Fatal("a 'queued writes' metric is shown with nothing queued")
	}
	// Baseline snapshot, then an operator move, for a site that has not birthed.
	if err := drv.WriteOutputs(nio.Values{"Edge1_ZONE1_SP_001": 0.0, "Edge1__Rebirth": false}); err != nil {
		t.Fatal(err)
	}
	if err := drv.WriteOutputs(nio.Values{"Edge1_ZONE1_SP_001": 7.5, "Edge1__Rebirth": false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the write to be queued", func() bool { return drv.Status().QueuedWrites == 1 })
	if v, shown := queuedMetric(); !shown || v != 1 {
		t.Fatalf("queued writes metric = %v (shown %v), want 1", v, shown)
	}
}
