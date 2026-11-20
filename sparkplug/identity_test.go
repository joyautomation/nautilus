// identity_test.go pins a Sparkplug command under an auth: block (#708 PR
// 8b, identity.go): a write needs sparkplug.writable AND auth.sparkplug's
// write on the tag's area, judged on the declared spelling; every write
// metric is journalled as sparkplug:<host>, a label, in one batch per
// command; Rebirth is always honoured; and a node that needs an identity
// and has none refuses every write.

package sparkplug

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// fakeIdentity decides with a real compiled auth: block and keeps what
// the node asked and recorded.
type fakeIdentity struct {
	policy *auth.Policy

	mu      sync.Mutex
	asked   []string // "principal perm target"
	batches [][]audit.Entry
}

func (f *fakeIdentity) May(p auth.Principal, perm, target string) bool {
	f.mu.Lock()
	f.asked = append(f.asked, p.String()+" "+perm+" "+target)
	f.mu.Unlock()
	return f.policy.Can(p, perm, target).Allowed
}

func (f *fakeIdentity) RecordBatch(es []audit.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, append([]audit.Entry(nil), es...))
}

func newFakeIdentity(t *testing.T, s auth.Settings) *fakeIdentity {
	t.Helper()
	p, err := auth.Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeIdentity{policy: p}
}

// onlyBatch is the one batch the identity got.
func (f *fakeIdentity) onlyBatch(t *testing.T) []audit.Entry {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.batches) != 1 {
		t.Fatalf("%d batches recorded, want 1: %+v", len(f.batches), f.batches)
	}
	return f.batches[0]
}

// With an auth: block and no auth.sparkplug, a command write the write gate
// opens is refused, the value stays, and the refusal is journalled naming
// the fix, as sparkplug:<broker host>, a label, from the broker.
func TestSparkplugCommandUnderAuthNeedsAGrant(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"), WithIdentityRequired())
	id := newFakeIdentity(t, auth.Settings{})
	n.SetIdentity(id)

	n.applyCommand("", setSpeedSP(99))

	if v := speedSP(t, n); v != 12.5 {
		t.Errorf("SpeedSP = %v, want 12.5: auth: granted Sparkplug nothing", v)
	}
	if got := n.Status().CommandsRefused; got != 1 {
		t.Errorf("CommandsRefused = %d, want 1", got)
	}
	es := id.onlyBatch(t)
	if len(es) != 1 {
		t.Fatalf("entries = %+v, want 1", es)
	}
	e := es[0]
	if e.User != "sparkplug:localhost" || e.Action != "tag.write" || e.Target != "SpeedSP" || e.Result != audit.ResultRefused ||
		e.From != "localhost:1883" || e.Detail["device"] != "node" {
		t.Errorf("entry = %+v", e)
	}
	if r, _ := e.Detail["reason"].(string); !strings.Contains(r, "auth.sparkplug") || !strings.Contains(r, "restart naut run") {
		t.Errorf("reason %q does not name the fix", r)
	}
	if l, _ := e.Detail["label"].(string); !strings.Contains(l, "not a proof") || !strings.Contains(l, "anyone who can publish to the broker") {
		t.Errorf("detail does not say the principal is a label: %v", e.Detail)
	}
}

// Granted, the write lands and is journalled with its value before and
// after.
func TestSparkplugCommandUnderAuthGranted(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"), WithIdentityRequired())
	id := newFakeIdentity(t, auth.Settings{Sparkplug: []auth.Grant{{Role: auth.Operator}}})
	n.SetIdentity(id)

	n.applyCommand("", setSpeedSP(99))

	if v := speedSP(t, n); v != 99 {
		t.Errorf("SpeedSP = %v, want 99", v)
	}
	es := id.onlyBatch(t)
	if len(es) != 1 || es[0].Result != audit.ResultOK || es[0].Target != "SpeedSP" || es[0].Before != 12.5 || es[0].After != 99.0 {
		t.Errorf("entries = %+v", es)
	}
}

// sparkplug.writable still gates under an auth: block: a grant does not
// open a tag the write gate keeps closed.
func TestSparkplugGrantDoesNotOpenTheWriteGate(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("Motor1"), WithIdentityRequired())
	id := newFakeIdentity(t, auth.Settings{Sparkplug: []auth.Grant{{Role: auth.Administrator}}})
	n.SetIdentity(id)

	n.applyCommand("", setSpeedSP(99))

	if v := speedSP(t, n); v != 12.5 {
		t.Errorf("SpeedSP = %v, want 12.5: not in sparkplug.writable", v)
	}
	if es := id.onlyBatch(t); len(es) != 1 || es[0].Result != audit.ResultRefused || es[0].Detail["reason"] != "not writable" {
		t.Errorf("entries = %+v", es)
	}
}

// A grant over an area writes only the tags in it: a command carrying a
// scalar outside the area and template members inside it writes the
// members, refuses the scalar, and journals all of it, each leaf by its
// member path, as ONE batch.
func TestSparkplugAreaGrantOneBatchPerCommand(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"), WithIdentityRequired())
	id := newFakeIdentity(t, auth.Settings{
		Areas:     map[string][]string{"motors": {"Motor*"}},
		Sparkplug: []auth.Grant{{Role: auth.Operator, Areas: []string{"motors"}}},
	})
	n.SetIdentity(id)

	n.applyCommand("", Payload{Metrics: []Metric{
		{Name: "SpeedSP", Datatype: spb.DataType_Double, Value: 99.0},
		{Name: "Motor1", Datatype: spb.DataType_Template, Value: &Template{TemplateRef: "Motor", Metrics: []Metric{
			{Name: "START", Datatype: spb.DataType_Boolean, Value: true},
			{Name: "Speed", Datatype: spb.DataType_Double, Value: 1200.0},
		}}},
	}})

	if v := speedSP(t, n); v != 12.5 {
		t.Errorf("SpeedSP = %v, want 12.5: outside the grant's area", v)
	}
	m := motor1(t, n)
	if !fld(t, m, "START").B {
		t.Error("Motor1.START not written: inside the grant's area")
	}
	es := id.onlyBatch(t)
	got := map[string]string{}
	for _, e := range es {
		got[e.Target] = e.Result
	}
	want := map[string]string{"SpeedSP": "refused", "Motor1.START": "ok", "Motor1.Speed": "ok"}
	if len(es) != 3 || len(got) != 3 {
		t.Fatalf("entries = %+v", es)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

// The auth: block is asked about the tag's DECLARED spelling, as the
// write gate is: a host's respelling must not decide which area a tag is
// in (the #619 class of bug).
func TestSparkplugAuthJudgesTheDeclaredSpelling(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"), WithIdentityRequired())
	id := newFakeIdentity(t, auth.Settings{Sparkplug: []auth.Grant{{Role: auth.Operator}}})
	n.SetIdentity(id)

	n.applyCommand("", Payload{Metrics: []Metric{
		{Name: "speedsp", Datatype: spb.DataType_Double, Value: 99.0},
		{Name: "motor1", Datatype: spb.DataType_Template, Value: &Template{TemplateRef: "Motor", Metrics: []Metric{
			{Name: "start", Datatype: spb.DataType_Boolean, Value: true},
		}}},
	}})

	id.mu.Lock()
	asked := strings.Join(id.asked, "\n")
	id.mu.Unlock()
	for _, want := range []string{"sparkplug:localhost write SpeedSP", "sparkplug:localhost write Motor1.START"} {
		if !strings.Contains(asked, want) {
			t.Errorf("asked:\n%s\nwant %q", asked, want)
		}
	}
	for _, e := range id.onlyBatch(t) {
		if e.Target != "SpeedSP" && e.Target != "Motor1.START" {
			t.Errorf("journalled %q, not the declared spelling", e.Target)
		}
	}
}

// Node Control/Rebirth is not a write: honoured with no grant, and not
// journalled.
func TestSparkplugRebirthHonouredWithoutAGrant(t *testing.T) {
	n, fc, _ := newSilentLinkNode(t)
	id := newFakeIdentity(t, auth.Settings{})
	n.SetIdentity(id)
	n.applyCommand("", rebirthCommand())
	waitPublished(t, fc, "NBIRTH", 2)
	id.mu.Lock()
	defer id.mu.Unlock()
	if len(id.batches) != 0 || len(id.asked) != 0 {
		t.Errorf("a Rebirth was journalled or judged: %+v %v", id.batches, id.asked)
	}
}

// A node built for an auth: block whose identity was never wired refuses
// every command write: it fails closed, never back to the write gate
// alone.
func TestSparkplugNoIdentityUnderAuthFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"), WithIdentityRequired())

	n.applyCommand("", setSpeedSP(99))
	n.applyCommand("", Payload{Metrics: []Metric{
		memberCmd("Motor1", "Motor", Metric{Name: "START", Datatype: spb.DataType_Boolean, Value: true}),
	}})

	if v := speedSP(t, n); v != 12.5 {
		t.Errorf("SpeedSP = %v, want 12.5", v)
	}
	if fld(t, motor1(t, n), "START").B {
		t.Error("Motor1.START written with no identity under auth:")
	}
	if got := n.Status().CommandsRefused; got != 2 {
		t.Errorf("CommandsRefused = %d, want 2", got)
	}
}

// Without an auth: block nothing changes: no identity, the write gate
// alone decides, and nothing is journalled (there is nowhere to).
func TestSparkplugNoAuthUnchanged(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"))
	n.applyCommand("", setSpeedSP(99))
	if v := speedSP(t, n); v != 99 {
		t.Errorf("SpeedSP = %v, want 99", v)
	}
}

// The principal is the primary host's id when the node waits on one, else
// the broker's host; "from" is the broker's host and port, never a user or
// password the URL carries.
func TestSparkplugPrincipalAndFrom(t *testing.T) {
	for _, c := range []struct {
		cfg       Config
		who, from string
	}{
		{Config{BrokerURL: "tcp://broker.plant:1883"}, "sparkplug:broker.plant", "broker.plant:1883"},
		{Config{BrokerURL: "ssl://u:s3cret@broker.plant:8883", PrimaryHostID: "scada-1"}, "sparkplug:scada-1", "broker.plant:8883"},
		{Config{BrokerURL: "::not a url"}, "sparkplug:broker", "broker"},
	} {
		n := &Node{cfg: c.cfg}
		if got := n.Principal().String(); got != c.who {
			t.Errorf("%+v: principal %q, want %q", c.cfg, got, c.who)
		}
		if got := n.brokerFrom(); got != c.from || strings.Contains(got, "s3cret") {
			t.Errorf("%+v: from %q, want %q", c.cfg, got, c.from)
		}
	}
}
