package snmp

// client_test.go pins the wire/decoder layer with no socket: a GetResponse
// assembled byte by byte from X.690 (not through gosnmp's own encoder, so
// the two cannot agree by sharing a bug) is decoded by gosnmp and mapped to
// hw.Raw; and the poll planner's request composition is driven through the
// Getter seam against an in-memory walk.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// tlv builds one BER TLV (short or long definite length).
func tlv(tag byte, body ...[]byte) []byte {
	var b []byte
	for _, x := range body {
		b = append(b, x...)
	}
	n := len(b)
	switch {
	case n < 0x80:
		return append([]byte{tag, byte(n)}, b...)
	case n < 0x100:
		return append([]byte{tag, 0x81, byte(n)}, b...)
	default:
		return append([]byte{tag, 0x82, byte(n >> 8), byte(n)}, b...)
	}
}

// oidBytes encodes a dotted OID's content octets (X.690 §8.19).
func oidBytes(s string) []byte {
	var arcs []uint64
	for _, a := range strings.Split(s, ".") {
		var n uint64
		fmt.Sscan(a, &n)
		arcs = append(arcs, n)
	}
	out := []byte{byte(arcs[0]*40 + arcs[1])}
	for _, a := range arcs[2:] {
		var tmp []byte
		tmp = append(tmp, byte(a&0x7f))
		for a >>= 7; a > 0; a >>= 7 {
			tmp = append([]byte{byte(a&0x7f | 0x80)}, tmp...)
		}
		out = append(out, tmp...)
	}
	return out
}

func vb(oid string, value []byte) []byte {
	return tlv(0x30, tlv(0x06, oidBytes(oid)), value)
}

func TestDecodeGoldenResponse(t *testing.T) {
	varbinds := [][]byte{
		vb("1.3.6.1.2.1.2.2.1.8.1", []byte{0x02, 0x01, 0x01}),                                                    // INTEGER 1
		vb("1.3.6.1.2.1.33.1.2.7.0", []byte{0x02, 0x01, 0xfb}),                                                   // INTEGER -5
		vb("1.3.6.1.2.1.2.2.1.10.1", []byte{0x41, 0x05, 0x00, 0xff, 0xff, 0xff, 0xff}),                           // Counter32 max
		vb("1.3.6.1.2.1.31.1.1.1.15.1", []byte{0x42, 0x02, 0x03, 0xe8}),                                          // Gauge32 1000
		vb("1.3.6.1.2.1.1.3.0", []byte{0x43, 0x04, 0x07, 0x5b, 0xcd, 0x15}),                                      // TimeTicks 123456789
		vb("1.3.6.1.2.1.31.1.1.1.6.1", []byte{0x46, 0x09, 0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}), // Counter64 max
		vb("1.3.6.1.2.1.1.5.0", tlv(0x04, []byte("sw1-lab\x00"))),                                                // printable, NUL-padded
		vb("1.3.6.1.2.1.2.2.1.6.1", tlv(0x04, []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0x01})),                       // a MAC
		vb("1.3.6.1.2.1.1.2.0", tlv(0x06, oidBytes("1.3.6.1.4.1.32473.1.28"))),                                   // OID
		vb("1.3.6.1.2.1.4.20.1.1.192.0.2.1", []byte{0x40, 0x04, 192, 0, 2, 1}),                                   // IpAddress
		vb("1.3.6.1.2.1.2.2.1.8.99", []byte{0x81, 0x00}),                                                         // noSuchInstance
		vb("1.3.6.1.2.1.99", []byte{0x82, 0x00}),                                                                 // endOfMibView
	}
	pdu := tlv(0xa2, []byte{0x02, 0x01, 0x2a}, []byte{0x02, 0x01, 0x00}, []byte{0x02, 0x01, 0x00}, tlv(0x30, varbinds...))
	msg := tlv(0x30, []byte{0x02, 0x01, 0x01}, tlv(0x04, []byte("public")), pdu)

	g := &gosnmp.GoSNMP{Version: gosnmp.Version2c, Community: "public"}
	pkt, err := g.SnmpDecodePacket(msg)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.RequestID != 42 || pkt.PDUType != gosnmp.GetResponse || len(pkt.Variables) != len(varbinds) {
		t.Fatalf("packet = %+v", pkt)
	}
	want := []struct {
		raw hw.Raw
		ok  bool
	}{
		{hw.RawIntVal(1), true},
		{hw.RawIntVal(-5), true},
		{hw.RawUintVal(4294967295), true},
		{hw.RawUintVal(1000), true},
		{hw.RawUintVal(123456789), true},
		{hw.RawUintVal(1<<64 - 1), true},
		{hw.RawStringVal("sw1-lab"), true},
		{hw.RawStringVal("00:00:5E:00:53:01"), true},
		{hw.RawStringVal("1.3.6.1.4.1.32473.1.28"), true},
		{hw.RawStringVal("192.0.2.1"), true},
		{hw.Raw{}, false},
		{hw.Raw{}, false},
	}
	for i, p := range pkt.Variables {
		v, err := fromPDU(p)
		if err != nil {
			t.Fatalf("#%d: %v", i, err)
		}
		raw, ok, err := RawOf(v)
		if err != nil || ok != want[i].ok || raw != want[i].raw {
			t.Errorf("#%d %s (%s): raw = %+v ok=%v err=%v, want %+v", i, v.OID, v.Type, raw, ok, err, want[i].raw)
		}
	}
	// And back: every value type survives toPDU → gosnmp marshal → decode.
	var pdus []gosnmp.SnmpPDU
	for _, p := range pkt.Variables {
		v, _ := fromPDU(p)
		back, err := toPDU(v)
		if err != nil {
			t.Fatal(err)
		}
		pdus = append(pdus, back)
	}
	out := &gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: "public", PDUType: gosnmp.GetResponse, RequestID: 42, Variables: pdus}
	enc, err := out.MarshalMsg()
	if err != nil {
		t.Fatal(err)
	}
	again, err := g.SnmpDecodePacket(enc)
	if err != nil {
		t.Fatal(err)
	}
	for i := range again.Variables {
		a, _ := fromPDU(again.Variables[i])
		b, _ := fromPDU(pkt.Variables[i])
		if a.ValueString() != b.ValueString() || a.Type != b.Type {
			t.Errorf("#%d re-encode: %+v vs %+v", i, a, b)
		}
	}
}

// ── the planner, through the Getter seam ─────────────────────────────────

// memGetter answers from a walk and records what it was asked.
type memGetter struct {
	w      walk.Walk
	calls  []string
	fail   error
	status map[string]*StatusError // column/oid → refusal
	closed int
}

func (g *memGetter) Get(ctx context.Context, oids []string) ([]walk.Varbind, error) {
	g.calls = append(g.calls, fmt.Sprintf("get %d", len(oids)))
	if g.fail != nil {
		return nil, g.fail
	}
	for _, o := range oids {
		if se := g.status[o]; se != nil {
			return nil, se
		}
	}
	var out []walk.Varbind
	for _, o := range oids {
		if v, ok := g.w.Get(o); ok {
			out = append(out, v)
		} else {
			out = append(out, walk.Varbind{OID: o, Type: walk.NoSuchInstance})
		}
	}
	return out, nil
}

func (g *memGetter) GetBulk(ctx context.Context, oid string, maxRep int) ([]walk.Varbind, error) {
	g.calls = append(g.calls, "bulk "+oid)
	if g.fail != nil {
		return nil, g.fail
	}
	for k, se := range g.status {
		if walk.HasPrefix(oid, k) {
			return nil, se
		}
	}
	var out []walk.Varbind
	cur := oid
	for i := 0; i < maxRep; i++ {
		v, ok := g.w.Next(cur)
		if !ok {
			out = append(out, walk.Varbind{OID: cur, Type: walk.EndOfMibView})
			break
		}
		out = append(out, v)
		cur = v.OID
	}
	return out, nil
}

func (g *memGetter) Set(ctx context.Context, v walk.Varbind) error {
	g.calls = append(g.calls, "set "+v.OID)
	return g.fail
}

func (g *memGetter) Close() error { g.closed++; return nil }

func portWalk(n int) walk.Walk {
	var w walk.Walk
	for i := 1; i <= n; i++ {
		w = append(w,
			walk.Varbind{OID: fmt.Sprintf("1.3.6.1.2.1.2.2.1.8.%d", i), Type: walk.Integer, Int: 1},
			walk.Varbind{OID: fmt.Sprintf("1.3.6.1.2.1.31.1.1.1.6.%d", i), Type: walk.Counter64, Uint: uint64(1000 * i)},
		)
	}
	w = append(w,
		walk.Varbind{OID: "1.3.6.1.2.1.1.5.0", Type: walk.OctetString, Bytes: []byte("sw1")},
		walk.Varbind{OID: "1.3.6.1.2.1.1.3.0", Type: walk.TimeTicks, Uint: 12345},
		walk.Varbind{OID: "1.3.6.1.2.1.2.2.1.9.1", Type: walk.TimeTicks, Uint: 1}, // the next column, past the table end
	)
	w.Sort()
	return w
}

func portManifest(n int) Manifest {
	m := Manifest{Sources: []Source{{ID: "SW1", Host: "192.0.2.2", CommunityEnv: "X", MaxRepetitions: 10}}}
	m.Tags = append(m.Tags, Tag{Name: "SW1", Type: "Switch", Source: "SW1", Members: map[string]Member{
		"Name":    {OID: "1.3.6.1.2.1.1.5.0"},
		"UptimeS": {OID: "1.3.6.1.2.1.1.3.0", Binding: hw.Binding{Scale: 0.01}},
	}})
	for i := 1; i <= n; i++ {
		m.Tags = append(m.Tags, Tag{Name: fmt.Sprintf("SW1_Port%02d", i), Type: "SwitchPort", Source: "SW1", Members: map[string]Member{
			"OperUp": {OID: fmt.Sprintf("1.3.6.1.2.1.2.2.1.8.%d", i), Binding: hw.Binding{Eq: 1}},
			"InBps":  {OID: fmt.Sprintf("1.3.6.1.2.1.31.1.1.1.6.%d", i), Binding: hw.Binding{Rate: true, Width: 64, Scale: 8}},
		}})
	}
	return m
}

func newMem(t *testing.T, m Manifest, g *memGetter) (*Driver, *int) {
	t.Helper()
	dials := 0
	d, err := New(m, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), WithDialer(func(ctx context.Context, s Source) (Getter, error) {
		dials++
		return g, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return d, &dials
}

func update(res hw.Result, tag, member string) (ir.Value, bool) {
	for _, u := range res.Updates {
		if u.Tag == tag && u.Member == member {
			return u.Value, true
		}
	}
	return ir.Value{}, false
}

// 28 ports × 2 members: two column walks of ceil(28/10)=3 PDUs each, and
// ONE Get for the two scalars — not 56 Gets. Each walk stops at its last
// bound row rather than running on to the end of the column.
func TestPollComposition(t *testing.T) {
	g := &memGetter{w: portWalk(28)}
	d, _ := newMem(t, portManifest(28), g)
	res, err := d.poll(context.Background(), "SW1", hw.DefaultClass)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bulk 1.3.6.1.2.1.2.2.1.8", "bulk 1.3.6.1.2.1.2.2.1.8.10", "bulk 1.3.6.1.2.1.2.2.1.8.20",
		"bulk 1.3.6.1.2.1.31.1.1.1.6", "bulk 1.3.6.1.2.1.31.1.1.1.6.10", "bulk 1.3.6.1.2.1.31.1.1.1.6.20",
		"get 2",
	}
	if strings.Join(g.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(g.calls, "\n"), strings.Join(want, "\n"))
	}
	if res.Requests != 7 {
		t.Errorf("Requests = %v", res.Requests)
	}
	if len(res.Bad) != 0 {
		t.Errorf("Bad = %v", res.Bad)
	}
	if v, ok := update(res, "SW1_Port28", "OperUp"); !ok || !v.B {
		t.Errorf("Port28.OperUp = %+v %v", v, ok)
	}
	if v, _ := update(res, "SW1", "UptimeS"); v.I != 123 {
		t.Errorf("UptimeS = %+v (12345 ticks × 0.01 = 123 s)", v)
	}
	if _, ok := update(res, "SW1_Port01", "InBps"); ok {
		t.Error("a rate on the first sample must not deliver")
	}
	if !strings.Contains(d.Plan(), "walk 1.3.6.1.2.1.2.2.1.8 (28 rows bound, max-repetitions 10)") {
		t.Errorf("Plan:\n%s", d.Plan())
	}

	// Second poll after a counter step: 1000 octets more on port 3 in ~50ms.
	for i, v := range g.w {
		if v.OID == "1.3.6.1.2.1.31.1.1.1.6.3" {
			g.w[i].Uint += 1000
		}
	}
	time.Sleep(50 * time.Millisecond)
	res, err = d.poll(context.Background(), "SW1", hw.DefaultClass)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := update(res, "SW1_Port03", "InBps")
	if !ok || v.F < 8000/0.2 || v.F > 8000/0.045 {
		t.Errorf("InBps after 1000 octets in ~50ms = %v (%v)", v.F, ok)
	}
	if v, _ := update(res, "SW1_Port04", "InBps"); v.F != 0 {
		t.Errorf("a still counter rates %v", v.F)
	}
}

// Per-tag trouble is Bad, not a transport error: a vanished row
// (noSuchInstance / absent from the walk) and a refused request.
func TestPollBadTags(t *testing.T) {
	g := &memGetter{w: portWalk(6)}
	d, _ := newMem(t, portManifest(6), g)

	// Row 6 of ifOperStatus vanishes, so its walk runs past the table end
	// (into ifLastChange) and stops there without it; sysName answers
	// noSuchInstance to the Get.
	var kept walk.Walk
	for _, v := range g.w {
		if v.OID != "1.3.6.1.2.1.2.2.1.8.6" && v.OID != "1.3.6.1.2.1.1.5.0" {
			kept = append(kept, v)
		}
	}
	g.w = kept
	res, err := d.poll(context.Background(), "SW1", hw.DefaultClass)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Bad, ",") != "SW1,SW1_Port06" {
		t.Errorf("Bad = %v", res.Bad)
	}
	if v, ok := update(res, "SW1_Port05", "OperUp"); !ok || !v.B {
		t.Error("a sibling of a Bad tag must still update")
	}

	// The agent refuses the counter column: those tags Bad, the rest fine.
	g.w = portWalk(6)
	g.status = map[string]*StatusError{"1.3.6.1.2.1.31.1.1.1.6": {Status: 5}}
	res, err = d.poll(context.Background(), "SW1", hw.DefaultClass)
	if err != nil {
		t.Fatalf("a refusal is not a transport error: %v", err)
	}
	if len(res.Bad) != 6 {
		t.Errorf("Bad = %v", res.Bad)
	}
}

// A transport failure is an error, drops the session, and the redial
// resets every rate counter of the source (a rate across an unobserved gap
// is a lie).
func TestPollTransportErrorRedials(t *testing.T) {
	g := &memGetter{w: portWalk(4)}
	d, dials := newMem(t, portManifest(4), g)
	ctx := context.Background()
	if _, err := d.poll(ctx, "SW1", hw.DefaultClass); err != nil {
		t.Fatal(err)
	}
	g.fail = errors.New("request timeout (after 2 retries)")
	if _, err := d.poll(ctx, "SW1", hw.DefaultClass); err == nil {
		t.Fatal("a timeout must be a transport error")
	}
	if g.closed != 1 {
		t.Errorf("session closed %d times", g.closed)
	}
	g.fail = nil
	res, err := d.poll(ctx, "SW1", hw.DefaultClass)
	if err != nil {
		t.Fatal(err)
	}
	if *dials != 2 {
		t.Errorf("dials = %d", *dials)
	}
	if _, ok := update(res, "SW1_Port01", "InBps"); ok {
		t.Error("the first poll after a redial must not deliver a rate")
	}
}

// The explanation a commissioning tech gets for each failure.
func TestExplain(t *testing.T) {
	s := &session{src: Source{ID: "SW1", Host: "192.0.2.2", Version: V3, User: "nautilus"}}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{gosnmp.ErrWrongDigest, "wrong auth pass phrase"},
		{gosnmp.ErrUnknownUsername, `does not know user "nautilus"`},
		{gosnmp.ErrDecryption, "wrong priv pass phrase"},
		{errors.New("request timeout (after 2 retries)"), "no answer within 3s × 3 tries"},
		{errors.New("read udp: connection refused"), "nothing is listening"},
	} {
		if got := s.explain(context.Background(), tc.err).Error(); !strings.Contains(got, tc.want) {
			t.Errorf("explain(%v) = %s", tc.err, got)
		}
	}
	v2 := &session{src: Source{ID: "SW1", Host: "192.0.2.2", CommunityEnv: "X"}}
	if got := v2.explain(context.Background(), errors.New("request timeout")).Error(); !strings.Contains(got, "wrong community") {
		t.Errorf("v2c timeout = %s", got)
	}
}

// Writes: set: maps the command value; a value set: does not name is an
// error naming the keys; without set:, BOOL is 1/0.
func TestWriteMapping(t *testing.T) {
	g := &memGetter{w: portWalk(1)}
	m := portManifest(1)
	m.Writes = []Write{
		{Name: "SW1_Port01_Cmd", Tag: "SW1_Port01", Member: "OperUp", OID: "1.3.6.1.2.1.2.2.1.7.1", Set: map[string]int64{"true": 1, "false": 2}},
		{Name: "SW1_Raw_Cmd", Tag: "SW1_Port01", Member: "OperUp", OID: "1.3.6.1.2.1.2.2.1.7.1"},
	}
	d, _ := newMem(t, m, g)
	ctx := context.Background()
	if err := d.write(ctx, "SW1", hw.WriteDecl{Name: "SW1_Port01_Cmd"}, ir.BoolVal(false)); err != nil {
		t.Fatal(err)
	}
	if err := d.write(ctx, "SW1", hw.WriteDecl{Name: "SW1_Port01_Cmd"}, ir.IntVal(7)); err == nil || !strings.Contains(err.Error(), "false, true") {
		t.Errorf("unmapped value = %v", err)
	}
	if err := d.write(ctx, "SW1", hw.WriteDecl{Name: "SW1_Raw_Cmd"}, ir.BoolVal(true)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(g.calls, "|") != "set 1.3.6.1.2.1.2.2.1.7.1|set 1.3.6.1.2.1.2.2.1.7.1" {
		t.Errorf("calls = %v", g.calls)
	}
}
