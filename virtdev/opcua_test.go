package virtdev

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
)

// Three makers' IO-Link masters on their wire faces, at the node ids the
// real ones use, fed by one process model.
func TestIOLinkMastersOverOPCUA(t *testing.T) {
	dir := writeTestProject(t, map[string]string{
		"virtual-devices.yaml": `
devices:
  - { name: PF1,   profile: pepperl-fuchs-ice3, endpoint: 10.1.1.21 }
  - { name: SICK1, profile: sick-sig300,        endpoint: "opc.tcp://10.1.1.22:4840" }
  - { name: IFM1,  profile: ifm-al1590,         endpoint: 10.1.1.23, params: { location: "Line A" } }
model:
  program: plant.st
`,
		"plant.st": `
PROGRAM Plant
PF1_Port3_SioInput := TRUE;
SICK1_Port5_Pin4 := TRUE;
SICK1_Port6_Pin2 := PF1_Port2_PdoValid;
IFM1_Port2_Word := 646;
END_PROGRAM
`,
	})
	set, err := LoadSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := Start(context.Background(), set, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(run.Stop)

	url := func(ep string) string {
		u, ok := run.Redirect(ep)
		if !ok {
			t.Fatalf("no redirect for %s: %+v", ep, run.Endpoints)
		}
		return u
	}
	pf := dial(t, url("opc.tcp://10.1.1.21:4840"))
	sick := dial(t, url("opc.tcp://10.1.1.22:4840"))
	ifm := dial(t, url("opc.tcp://10.1.1.23:4843")) // the profile's port

	want := func(c *opcua.Client, nid string, v any) {
		t.Helper()
		eventually(t, nid, func() error {
			dv := read(t, c, nid)
			if dv.Status != ua.StatusGood {
				return fmt.Errorf("status %s", dv.Status)
			}
			if got := dv.Value.Value(); got != v {
				return fmt.Errorf("= %#v, want %#v", got, v)
			}
			return nil
		})
	}
	want(pf, "ns=1;s=IOLM/Port 3/SIO Input", true)
	want(pf, "ns=1;s=IOLM/Manufacturer", "Pepperl+Fuchs")
	want(sick, "ns=3;i=8028", true) // port 5 pin 4
	want(ifm, "ns=5;s=Line A/iolinkmaster/port[2]/iolinkdevice/pdin", "0286")

	// A write from the controller lands in the plant and moves the model.
	resp, err := pf.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: ua.MustParseNodeID("ns=1;s=IOLM/Port 2/Attached Device/PDO Valid"), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(true)},
	}}})
	if err != nil || resp.Results[0] != ua.StatusGood {
		t.Fatalf("write PDO Valid: %v %v", err, resp)
	}
	want(sick, "ns=3;i=8031", true) // port 6 pin 2, wired to it by the model

	// UInt32 in, UInt32 out: the node's type is the point's (UDINT).
	resp, err = pf.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: ua.MustParseNodeID("ns=1;s=IOLM/Port 2/Attached Device/PDO Data Unsigned32"), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(uint32(70000))},
	}}})
	if err != nil || resp.Results[0] != ua.StatusGood {
		t.Fatalf("write PDO Data Unsigned32: %v %v", err, resp)
	}
	want(pf, "ns=1;s=IOLM/Port 2/Attached Device/PDO Data Unsigned32", uint32(70000))

	// A read-only node refuses the write a real master refuses.
	resp, err = pf.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: ua.MustParseNodeID("ns=1;s=IOLM/Port 3/SIO Input"), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(false)},
	}}})
	if err != nil || resp.Results[0] != ua.StatusBadNotWritable {
		t.Errorf("write SIO Input: %v %v, want BadNotWritable", err, resp)
	}
}

func dial(t *testing.T, url string) *opcua.Client {
	t.Helper()
	ctx := context.Background()
	c, err := opcua.NewClient(url, opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := gopcuatest.Connect(ctx, c); err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.Close(cctx)
	})
	return c
}

func read(t *testing.T, c *opcua.Client, nid string) *ua.DataValue {
	t.Helper()
	resp, err := c.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: ua.MustParseNodeID(nid), AttributeID: ua.AttributeIDValue}}})
	if err != nil {
		t.Fatalf("read %s: %v", nid, err)
	}
	return resp.Results[0]
}
