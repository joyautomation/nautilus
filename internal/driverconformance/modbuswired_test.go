package driverconformance

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/wirebind"
	"github.com/joyautomation/nautilus/wiring"
)

// modbus carrying a wired instance (#1458 slice 5): the manifest is the one
// wirebind derives for an instance P with a from: member In and a to: member
// Out, so the driver's bindings are named by the member's path ("P.In",
// "P.Out"). The row pins that an instance's members keep the Modbus
// driver's own rules, dropAndAsk among them (design §2.3): the wiring adapts
// names, never a mode.
func init() {
	register(subject{
		name: "modbus-wired-instance",
		gate: dropAndAsk,
		start: func(t tb, o startOpts) *rig {
			srv := slave.NewServer("", slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := srv.Start(); err != nil {
				t.Fatalf("slave: %v", err)
			}
			t.Cleanup(srv.Stop)
			u := srv.Unit(1)
			u.SetInput(0, 42)

			l := newTCPLink(t, srv.Addr(), o.cut)
			wordT := &ir.Type{Kind: ir.TypeInt, Name: "UDINT"}
			set := &wirebind.Set{Instances: []*wirebind.Instance{{
				Name: "P", Type: "Dev",
				Members: []wirebind.Member{
					{Path: "In", Kind: wiring.From, Source: "dev", Type: wordT,
						Addr: map[string]any{"table": "input", "address": 0, "format": "uint16"}},
					{Path: "Out", Kind: wiring.To, Source: "dev", Type: wordT,
						Addr: map[string]any{"table": "holding", "address": 10, "format": "uint16"}},
				},
				Conns: []wirebind.Conn{{Source: "dev", Driver: "modbus", DriverType: "modbus", ID: "P",
					Keys: map[string]any{"endpoint": "127.0.0.1:" + strconv.Itoa(l.port()), "unit-id": 1, "timeout": "300ms"}}},
			}}}
			m, problems := set.ModbusManifest(func(string) bool { return true })
			if err := wiring.Err(problems); err != nil {
				t.Fatalf("wirebind: %v", err)
			}
			for i := range m.Sources {
				m.Sources[i].RetryMin, m.Sources[i].RetryMax = 20*time.Millisecond, 80*time.Millisecond
			}
			d, err := modbus.New(m, modbus.WithScanRate(10*time.Millisecond), modbus.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("modbus.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			return &rig{
				drv: d, in: "P.In", out: "P.Out",
				outVals:  [2]any{float64(11), float64(22)},
				field:    func() any { return float64(u.Holding(10)) },
				polls:    func() uint64 { return d.Health().Reads },
				failRead: func() { u.InjectExceptionAt(4, 0, 2) },
				link:     l,
			}
		},
	})
}
