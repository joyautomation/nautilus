package driverconformance

import (
	"context"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/agent"
	"github.com/joyautomation/nautilus/snmp/codegen"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// snmp against snmp/agent serving the PDU fixture, over UDP. The input is
// outlet 5's tag; the output is its command, SET to the outlet's state
// OID (1 immediateOn, 2 immediateOff), which is also the device value. A
// failed read on a live link is the agent no longer answering for the
// outlet's name OID: the tag reads Bad.
func init() {
	const (
		stateOID = "1.3.6.1.4.1.3808.1.1.3.3.3.1.1.4.5"
		nameOID  = "1.3.6.1.4.1.3808.1.1.3.3.3.1.1.2.5"
	)
	register(subject{
		name: "snmp",
		// A SET is a command: dropped while the gate is closed, never
		// replayed (hw/base.go WriteOutputs rule 2).
		gate: dropAndRebaseline,
		start: func(t tb, o startOpts) *rig {
			raw, err := os.ReadFile("../../snmp/testdata/pdu.snmpwalk")
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			w, err := walk.ParseBytes(raw)
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			a := agent.New(w, "public")
			a.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := a.Start("127.0.0.1:0"); err != nil {
				t.Fatalf("agent: %v", err)
			}
			t.Cleanup(a.Stop)
			l := newUDPLink(t, a.Addr(), o.cut)
			gen, err := codegen.Generate(w, codegen.Options{Tag: "PDU1", Host: "127.0.0.1", Port: l.port()})
			if err != nil {
				t.Fatalf("codegen: %v", err)
			}
			m := gen.Manifest
			zero := 0
			m.Sources[0].Timeout = 150 * time.Millisecond
			m.Sources[0].Retries = &zero
			m.Sources[0].Interval = 5 * time.Millisecond
			m.Sources[0].StaleAfter = 2 * time.Second
			m.Writes = []snmp.Write{{Name: "PDU1_Outlet05_Cmd", Tag: "PDU1_Outlet05", Member: "On", OID: stateOID, Set: map[string]int64{"true": 1, "false": 2}}}
			t.Setenv(m.Sources[0].CommunityEnv, "public")
			d, err := snmp.New(m, snmp.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("snmp.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			onOff := func(v walk.Varbind) any {
				switch v.Int {
				case 1:
					return true
				case 2:
					return false
				}
				return v.Int
			}
			return &rig{
				drv: d, in: "PDU1_Outlet05", out: "PDU1_Outlet05_Cmd",
				outVals: [2]any{false, true},
				field: func() any {
					v, _ := a.Value(stateOID)
					return onOff(v)
				},
				sent: func() []any {
					var out []any
					for _, s := range a.Sets() {
						if s.OID == stateOID {
							out = append(out, onOff(s))
						}
					}
					return out
				},
				polls:    func() uint64 { return d.Health().Polls },
				failRead: func() { a.Remove(nameOID) },
				link:     l,
			}
		},
	})
}
