//go:build !js

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/oplog"
)

// The oplog is never compacted, so what a checkout adds to it is kept for good.
// A realistic run (3,000 points, two passes each, a note on one in ten, a
// force on one in fifty, then a seal) is measured as the log's own entries,
// framing included, and held to a few MB.
func TestARealisticCheckoutRunIsAFewMegabytesOfLog(t *testing.T) {
	var total, n int
	var seq uint64
	var store *checkout.Store
	store, err := checkout.OpenLog(ckDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	sh := &checkout.Sheet{Title: "plant I/O"}
	for i := 0; i < 3000; i++ {
		kind, method := checkout.DI, checkout.Observe
		if i%3 == 0 {
			kind, method = checkout.DO, checkout.Force
		}
		sh.Points = append(sh.Points, checkout.Point{ID: fmt.Sprintf("P%04d_Cmd", i), Tag: fmt.Sprintf("P%04d_Cmd", i), Kind: kind, Method: method, Device: "rio1", Area: "RCV", Equipment: "Pump1"})
	}
	store.SetSheet(sh)
	store.AttachLog(func(_ context.Context, by string, body []byte) (uint64, error) {
		seq++
		e := oplog.CheckoutEntryOf(by, body)
		e.Seq, e.Term, e.TS, e.Origin, e.Principal = seq, 3, 1795383005283+int64(seq), "http://10.0.0.1:8080", "user:alice"
		b, _ := json.Marshal(e)
		total += len(b)
		n++
		store.ApplyLogged(seq, e.TS, body)
		return seq, nil
	})
	ev := checkout.EvidenceDigest(checkout.Evidence{Drivers: []checkout.DriverEvidence{{Name: "plc", Kind: "modbus"}}})
	run, err := store.BeginWith(false, sh.Hash(), "plant I/O", "alice", "user:alice", ev, "")
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		for i, p := range sh.Points {
			outcome, note := checkout.Pass, ""
			if pass == 0 && i%10 == 0 {
				outcome, note = checkout.Fail, "wired to the next terminal, swapped with the neighbouring point"
			}
			if pass == 1 && i%10 == 0 {
				note = "re-landed and rechecked at the field device"
			}
			if _, err := store.RecordWith(run.ID, p.ID, outcome, note, 1.5, "good", "", ev, "alice", "user:alice"); err != nil {
				t.Fatal(err)
			}
		}
	}
	resultsBytes := total
	t.Logf("%d results: %d entries, %.2f MB of log (%.0f bytes each)", 6000, n, float64(total)/1e6, float64(total)/float64(n))
	if _, err := store.Seal(checkout.SealInput{Run: run.ID, Sheet: sh, By: "bob", Principal: "user:bob", Mode: "two-person",
		Evidence: checkout.Evidence{Drivers: []checkout.DriverEvidence{{Name: "plc", Kind: "modbus"}}}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("with the seal: %d entries, %.2f MB; the seal entry is %d bytes", n, float64(total)/1e6, total-resultsBytes)
	if total > 4<<20 {
		t.Errorf("a realistic run adds %.1f MB to a log that is never compacted: batch the results", float64(total)/1e6)
	}
	if total-resultsBytes > 16<<10 {
		t.Errorf("the seal entry is %d bytes: it should carry the payload, not the export", total-resultsBytes)
	}
}
