// flatten_test.go covers WithFlattenUDTs: a UDT tag goes on the wire as one
// plain metric per member ("Motor1/Drive/Torque") instead of a Template, for
// hosts that do not read Templates — births, data, and commands back.

package sparkplug

import (
	"bytes"
	"sort"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// newFlatNode is newCommandNode (Motor1 with a nested Drive, every member
// seeded) flattening UDTs, born on a fake client.
func newFlatNode(t *testing.T, opts ...Option) (*Node, *fakeClient) {
	t.Helper()
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, append(opts, WithFlattenUDTs())...)
	fc := &fakeClient{open: true}
	n.cli = fc
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	return n, fc
}

func decodeLast(t *testing.T, fc *fakeClient, msgType string) Payload {
	t.Helper()
	pubs := fc.published(msgType)
	if len(pubs) == 0 {
		t.Fatalf("no %s published", msgType)
	}
	p, err := DecodePayload(pubs[len(pubs)-1].payload)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func metricNames(ms []Metric) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	sort.Strings(out)
	return out
}

// TestFlattenBirth — the UDT is born as its leaf members, each with its own
// datatype and value, nested members by their full path; no Template metric
// and no Template definition goes out at all.
func TestFlattenBirth(t *testing.T) {
	_, fc := newFlatNode(t)
	p := decodeLast(t, fc, "NBIRTH")

	byName := map[string]Metric{}
	for _, m := range p.Metrics {
		if m.Datatype == spb.DataType_Template {
			t.Errorf("NBIRTH carries Template metric %q", m.Name)
		}
		byName[m.Name] = m
	}
	want := map[string]struct {
		dt spb.DataType
		v  any
	}{
		"Motor1/Speed":        {spb.DataType_Double, 1450.0},
		"Motor1/START":        {spb.DataType_Boolean, false},
		"Motor1/Drive/Torque": {spb.DataType_Double, 88.5},
		"Motor1/Drive/Fault":  {spb.DataType_Boolean, true},
		"SpeedSP":             {spb.DataType_Double, 12.5},
	}
	for name, w := range want {
		m, ok := byName[name]
		if !ok {
			t.Errorf("NBIRTH has no %s (got %v)", name, metricNames(p.Metrics))
			continue
		}
		if m.Datatype != w.dt || m.Value != w.v {
			t.Errorf("%s = %v %v, want %v %v", name, m.Datatype, m.Value, w.dt, w.v)
		}
	}
	for _, gone := range []string{"Motor1", "Motor", "Drv"} {
		if _, ok := byName[gone]; ok {
			t.Errorf("NBIRTH still carries %q", gone)
		}
	}
}

// TestFlattenDataOnlyChangedMembers — one member moves, and the NDATA carries
// that member alone, not the whole UDT.
func TestFlattenDataOnlyChangedMembers(t *testing.T) {
	n, fc := newFlatNode(t)
	if err := n.rt.Tags().SetPath("Motor1.Drive.Torque", 91.0); err != nil {
		t.Fatal(err)
	}
	n.scanAndPublish()

	p := decodeLast(t, fc, "NDATA")
	if got := metricNames(p.Metrics); len(got) != 1 || got[0] != "Motor1/Drive/Torque" {
		t.Fatalf("NDATA metrics = %v, want [Motor1/Drive/Torque]", got)
	}
	if v := p.Metrics[0].Value; v != 91.0 {
		t.Errorf("Motor1/Drive/Torque = %v, want 91", v)
	}

	// Nothing moved since: nothing more goes out.
	before := len(fc.published("NDATA"))
	n.scanAndPublish()
	if got := len(fc.published("NDATA")); got != before {
		t.Errorf("%d NDATA after an idle tick, want %d", got, before)
	}
}

// TestFlattenHeartbeatSendsEveryMember — a publish the max-interval forces,
// with nothing changed, carries every member: a heartbeat restates the whole
// tag, as the Template it replaces would have.
func TestFlattenHeartbeatSendsEveryMember(t *testing.T) {
	n, fc := newFlatNode(t, WithDefaultRBE(RBE{MaxInterval: time.Millisecond}))
	time.Sleep(5 * time.Millisecond)
	n.scanAndPublish()

	p := decodeLast(t, fc, "NDATA")
	got := map[string]bool{}
	for _, m := range p.Metrics {
		got[m.Name] = true
	}
	for _, name := range []string{"Motor1/Speed", "Motor1/START", "Motor1/Drive/Torque", "Motor1/Drive/Fault"} {
		if !got[name] {
			t.Errorf("heartbeat NDATA has no %s (got %v)", name, metricNames(p.Metrics))
		}
	}
}

// TestFlattenCommandWritesMember — a host writes a member back under the name
// it was published as, and that member alone changes.
func TestFlattenCommandWritesMember(t *testing.T) {
	n, _ := newFlatNode(t)
	n.applyCommand(Payload{Metrics: []Metric{
		{Name: "Motor1/Drive/Torque", Datatype: spb.DataType_Double, Value: 42.0},
	}})
	m := motor1(t, n)
	if got := fld(t, fld(t, m, "Drive"), "Torque"); got.F != 42 {
		t.Errorf("Motor1.Drive.Torque = %v, want 42", got.F)
	}
	if got := fld(t, m, "Speed"); got.F != 1450 {
		t.Errorf("Motor1.Speed = %v, want 1450 untouched", got.F)
	}
}

// TestTemplatesStillDefault — without the option, nothing changes: the UDT is
// one Template instance and its definitions are in the birth.
func TestTemplatesStillDefault(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf)
	fc := &fakeClient{open: true}
	n.cli = fc
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	p := decodeLast(t, fc, "NBIRTH")
	byName := map[string]Metric{}
	for _, m := range p.Metrics {
		byName[m.Name] = m
	}
	for _, name := range []string{"Motor1", "Motor", "Drv"} {
		if m, ok := byName[name]; !ok || m.Datatype != spb.DataType_Template {
			t.Errorf("%s missing or not a Template: %+v", name, m)
		}
	}
	if _, ok := byName["Motor1/Speed"]; ok {
		t.Error("Motor1/Speed published without WithFlattenUDTs")
	}
}

// TestFlattenKeepsMemberProperties — a member's unit and description go on
// its flattened metric, as they went on the Template member before.
func TestFlattenKeepsMemberProperties(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   propsProgramST,
		Libraries: []string{propsTypesST},
		Driver:    nio.NewMemory(),
		Scan:      50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("Level", 3.5),
			runtime.Typed("Motor1", runtime.RoleState, "Motor", runtime.Desc("Pump 1 motor")),
		},
		Meta: map[string]runtime.TagMeta{"Motor1.Speed": {Unit: "rpm", Desc: "Shaft speed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(rt, Config{GroupID: "g", EdgeNode: "e"}, WithFlattenUDTs())
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{open: true}
	n.cli = fc
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	p := decodeLast(t, fc, "NBIRTH")
	for _, m := range p.Metrics {
		if m.Name == "Motor1/Speed" {
			if u, d := m.PropertyString(PropEngUnit), m.PropertyString(PropDocumentation); u != "rpm" || d != "Shaft speed" {
				t.Errorf("Motor1/Speed engUnit=%q documentation=%q, want rpm / Shaft speed", u, d)
			}
			return
		}
	}
	t.Fatalf("no Motor1/Speed in %v", metricNames(p.Metrics))
}
