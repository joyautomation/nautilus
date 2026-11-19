package driverconformance

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

// redfish against redfish/mockup serving the subsystem-1u fixture. The
// input is the Server struct tag; the output is its PowerCmd, whose
// device value is the System's PowerState (1 On, 3 ForceOff, the
// ResetTypes the driver posts), and every reset the mockup accepted is
// what the device received. A failed read on a live link is the BMC
// answering the System's GET with a 500.
func init() {
	register(subject{
		name: "redfish",
		// A reset is an event: dropped while the gate is closed, never
		// replayed (hw/base.go WriteOutputs rule 2).
		gate: dropAndRebaseline,
		// The liveness gate naut run installs does not reach hw.Base.
		expect: map[string]string{"C5": "#938"},
		start: func(t tb, o startOpts) *rig {
			tree, err := mockup.LoadDir("../../redfish/testdata/subsystem-1u")
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			srv := mockup.New(tree, mockup.Options{})
			if err := srv.Start("127.0.0.1:0"); err != nil {
				t.Fatalf("mockup: %v", err)
			}
			t.Cleanup(srv.Stop)
			l := newTCPLink(t, srv.Addr(), o.cut)
			m, err := redfish.ParseManifest([]byte(fmt.Sprintf(`
sources:
  - id: NODE1
    host: http://127.0.0.1:%d
    auth: none
    interval: 5ms
    stale-after: 2s
    timeout: 300ms
tags:
  - name: NODE1
    type: Server
    source: NODE1
    resource: /redfish/v1/Systems/1
    members:
      Online: {path: Id, exists: true}
      PowerOn: {path: PowerState, eq: "On"}
writes:
  - name: NODE1_PowerCmd
    tag: NODE1
    member: PowerOn
    target: /redfish/v1/Systems/1/Actions/ComputerSystem.Reset
`, l.port())))
			if err != nil {
				t.Fatalf("manifest: %v", err)
			}
			d, err := redfish.New(m, redfish.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("redfish.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			cmd := map[string]int64{"On": 1, "ForceOff": 3}
			return &rig{
				drv: d, in: "NODE1", out: "NODE1_PowerCmd",
				outVals: [2]any{int64(1), int64(3)},
				field: func() any {
					doc, err := srv.Tree().Get("/redfish/v1/Systems/1")
					if err != nil {
						return nil
					}
					switch doc["PowerState"] {
					case "On":
						return int64(1)
					case "Off":
						return int64(3)
					}
					return doc["PowerState"]
				},
				sent: func() []any {
					var out []any
					for _, a := range srv.Actions() {
						out = append(out, cmd[fmt.Sprint(a.Body["ResetType"])])
					}
					return out
				},
				polls:    func() uint64 { return d.Health().Polls },
				failRead: func() { srv.Fail("/redfish/v1/Systems/1", 500) },
				link:     l,
			}
		},
	})
}
