package driverconformance

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/prom"
	"github.com/joyautomation/nautilus/prom/serve"
)

// prom against prom/serve, the exporter stand-in. The input is a Server
// tag whose Load1 is an expression over node_load1, so a scrape that no
// longer carries that metric (the exporter renamed it) is a failed read
// on a live link: the tag reads Bad. prom sends nothing to the exporter.
func init() {
	register(subject{
		name: "prom",
		byDesign: map[string]string{
			"C5": "prom sends nothing to an exporter: its only output, an enable: tag, parks the source",
			"C8": "prom sends nothing to an exporter: its only output, an enable: tag, parks the source",
		},
		start: func(t tb, o startOpts) *rig {
			srv, err := serve.New([]byte("# TYPE node_load1 gauge\nnode_load1 1.5\n"))
			if err != nil {
				t.Fatalf("stand-in: %v", err)
			}
			if err := srv.Start("127.0.0.1:0"); err != nil {
				t.Fatalf("stand-in: %v", err)
			}
			t.Cleanup(srv.Stop)
			l := newTCPLink(t, srv.Addr(), o.cut)
			d, err := prom.New(prom.Manifest{
				Sources: []prom.Source{{
					ID: "NODE1", URL: "http://127.0.0.1:" + strconv.Itoa(l.port()) + "/metrics",
					Interval: 5 * time.Millisecond, StaleAfter: 2 * time.Second, Timeout: 300 * time.Millisecond,
				}},
				Tags: []prom.Tag{{
					Name: "NODE1", Type: "Server", Source: "NODE1",
					Members: map[string]prom.Binding{
						"PowerOn": {Binding: hw.Binding{Const: true}},
						"Load1":   {Expr: "x", From: map[string]prom.Selector{"x": {Metric: "node_load1"}}},
					},
				}},
			}, prom.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("prom.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			return &rig{
				drv: d, in: "NODE1",
				polls:    func() uint64 { return d.Health().Polls },
				failRead: func() { srv.RemoveAll("node_load1") },
				link:     l,
			}
		},
	})
}
