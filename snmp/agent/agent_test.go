package agent

// The agent against gosnmp as a plain client: the PDU semantics the driver
// tests lean on without naming — GetBulk with non-repeaters and the end of
// the MIB view, GetNext across columns, Set's all-or-nothing refusals, and
// a wrong community met with silence.

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

func testWalk() walk.Walk {
	w := walk.Walk{
		{OID: "1.3.6.1.2.1.1.5.0", Type: walk.OctetString, Bytes: []byte("sw1")},
		{OID: "1.3.6.1.2.1.2.2.1.8.1", Type: walk.Integer, Int: 1},
		{OID: "1.3.6.1.2.1.2.2.1.8.2", Type: walk.Integer, Int: 2},
		{OID: "1.3.6.1.2.1.2.2.1.10.1", Type: walk.Counter32, Uint: 100},
		{OID: "1.3.6.1.2.1.31.1.1.1.6.1", Type: walk.Counter64, Uint: 1 << 40},
	}
	w.Sort()
	return w
}

func start(t *testing.T, community string) (*Agent, *gosnmp.GoSNMP) {
	t.Helper()
	a := New(testWalk(), "public")
	a.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := a.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	host, port, _ := strings.Cut(a.Addr(), ":")
	var p uint16
	for _, c := range port {
		p = p*10 + uint16(c-'0')
	}
	g := &gosnmp.GoSNMP{Target: host, Port: p, Community: community, Version: gosnmp.Version2c, Timeout: 300 * time.Millisecond, Retries: 0, MaxOids: gosnmp.MaxOids}
	if err := g.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Conn.Close() })
	return a, g
}

func names(pkt *gosnmp.SnmpPacket) string {
	var out []string
	for _, v := range pkt.Variables {
		out = append(out, strings.TrimPrefix(v.Name, ".")+"="+v.Type.String())
	}
	return strings.Join(out, " ")
}

func TestGetAndNext(t *testing.T) {
	a, g := start(t, "public")
	pkt, err := g.Get([]string{".1.3.6.1.2.1.1.5.0", ".1.3.6.1.2.1.1.6.0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(pkt); got != "1.3.6.1.2.1.1.5.0=OctetString 1.3.6.1.2.1.1.6.0=NoSuchInstance" {
		t.Errorf("Get = %s", got)
	}
	pkt, err = g.GetNext([]string{".1.3.6.1.2.1.2.2.1.8.2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(pkt); got != "1.3.6.1.2.1.2.2.1.10.1=Counter32" {
		t.Errorf("GetNext across columns = %s", got)
	}
	if a.Requests()["get"] != 1 || a.Requests()["getnext"] != 1 {
		t.Errorf("requests = %v", a.Requests())
	}
}

// One non-repeater, one repeater running into the end of the MIB view.
func TestGetBulk(t *testing.T) {
	_, g := start(t, "public")
	pkt, err := g.GetBulk([]string{".1.3.6.1.2.1.1", ".1.3.6.1.2.1.2.2.1.10"}, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := "1.3.6.1.2.1.1.5.0=OctetString 1.3.6.1.2.1.2.2.1.10.1=Counter32 1.3.6.1.2.1.31.1.1.1.6.1=Counter64 1.3.6.1.2.1.31.1.1.1.6.1=EndOfMibView"
	if got := names(pkt); got != want {
		t.Errorf("GetBulk =\n%s\nwant\n%s", got, want)
	}
	if v := gosnmp.ToBigInt(pkt.Variables[2].Value).Uint64(); v != 1<<40 {
		t.Errorf("Counter64 = %d", v)
	}
}

func TestSet(t *testing.T) {
	a, g := start(t, "public")
	pkt, err := g.Set([]gosnmp.SnmpPDU{{Name: ".1.3.6.1.2.1.2.2.1.8.2", Type: gosnmp.Integer, Value: 1}})
	if err != nil || pkt.Error != gosnmp.NoError {
		t.Fatalf("Set = %v, %v", pkt.Error, err)
	}
	if v, _ := a.Value("1.3.6.1.2.1.2.2.1.8.2"); v.Int != 1 {
		t.Errorf("read-back = %+v", v)
	}
	// All or nothing: the second varbind's wrong type refuses both.
	pkt, err = g.Set([]gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.2.2.1.8.1", Type: gosnmp.Integer, Value: 2},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gosnmp.Integer, Value: 7},
	})
	if err != nil || pkt.Error != gosnmp.WrongType || pkt.ErrorIndex != 2 {
		t.Fatalf("wrong type = %v@%d, %v", pkt.Error, pkt.ErrorIndex, err)
	}
	if v, _ := a.Value("1.3.6.1.2.1.2.2.1.8.1"); v.Int != 1 {
		t.Error("a refused Set applied its first varbind")
	}
	pkt, err = g.Set([]gosnmp.SnmpPDU{{Name: ".1.3.6.1.2.1.99.0", Type: gosnmp.Integer, Value: 1}})
	if err != nil || pkt.Error != gosnmp.NoCreation {
		t.Fatalf("unknown OID = %v, %v", pkt.Error, err)
	}
	if n := len(a.Sets()); n != 1 {
		t.Errorf("sets = %d", n)
	}
}

func TestWrongCommunityIsSilent(t *testing.T) {
	a, g := start(t, "private")
	if _, err := g.Get([]string{".1.3.6.1.2.1.1.5.0"}); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("a wrong community must go unanswered, got %v", err)
	}
	if len(a.Requests()) != 0 {
		t.Errorf("answered: %v", a.Requests())
	}
}

func TestRampAndDrop(t *testing.T) {
	a, g := start(t, "public")
	if err := a.Ramp("1.3.6.1.2.1.1.5.0", 1); err == nil {
		t.Error("ramping a string")
	}
	if err := a.Ramp("1.3.6.1.2.1.2.2.1.10.1", 1e6); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	pkt, err := g.Get([]string{".1.3.6.1.2.1.2.2.1.10.1"})
	if err != nil {
		t.Fatal(err)
	}
	if v := gosnmp.ToBigInt(pkt.Variables[0].Value).Uint64(); v < 100+10000 {
		t.Errorf("ramped counter = %d", v)
	}
	a.SetDrop(true)
	if _, err := g.Get([]string{".1.3.6.1.2.1.1.5.0"}); err == nil {
		t.Error("a dropping agent answered")
	}
	a.SetDrop(false)
	if _, err := g.Get([]string{".1.3.6.1.2.1.1.5.0"}); err != nil {
		t.Errorf("after drop off: %v", err)
	}
}
