package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// With audit.sink set, the drivers block carries the copy's card in every
// state: current, catching up, behind for an outage (with the error), and
// stopped on a mismatch. Without one there is no card.
func TestAuditCopyCard(t *testing.T) {
	if ds := New(newTestRuntime(t)).driverStatus(time.Now()); len(ds) != 0 {
		t.Fatalf("no sink: %+v, want no card", ds)
	}
	since := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	stopped := (&audit.CopyMismatch{File: "audit-20261120.jsonl", Offset: 10, Reason: "the line the copy holds as seq 4 was changed"}).Error()
	for _, c := range []struct {
		st               audit.CopyStatus
		state, msg, last string
	}{
		{audit.CopyStatus{State: audit.CopyCurrent, Copied: 9, Head: 9, Since: since}, "connected", "holds every journal entry", ""},
		{audit.CopyStatus{State: audit.CopyStarting}, "connecting", "reaching Postgres", ""},
		{audit.CopyStatus{State: audit.CopyBehind, Copied: 5, Head: 9}, "connecting", "catching up: 4 entries", ""},
		{audit.CopyStatus{State: audit.CopyBehind, Copied: 5, Head: 9, LastError: errors.New("connection refused").Error()}, "degraded", "4 entries behind", "connection refused"},
		{audit.CopyStatus{State: audit.CopyStopped, Copied: 5, Head: 9, LastError: stopped}, "error", "naut audit verify", "seq 4 was changed"},
	} {
		st := c.st
		srv := New(newTestRuntime(t), Options{AuditCopy: func() audit.CopyStatus { return st }})
		ds := srv.driverStatus(time.Now())
		if len(ds) != 1 {
			t.Fatalf("%s: %d cards, want the copy's", st.State, len(ds))
		}
		d := ds[0]
		if d.Kind != "audit" || d.Name != "audit copy" || d.State != c.state || !strings.Contains(d.Message, c.msg) || !strings.Contains(d.LastError, c.last) {
			t.Errorf("%s: %+v, want state %s, message with %q, error with %q", st.State, d, c.state, c.msg, c.last)
		}
		if !st.Since.IsZero() && d.SinceMs != st.Since.UnixMilli() {
			t.Errorf("%s: sinceMs %d", st.State, d.SinceMs)
		}
		if d.Metrics[0].Value != float64(st.Copied) || d.Metrics[1].Value != float64(st.Head) {
			t.Errorf("%s: metrics %+v", st.State, d.Metrics)
		}
	}
	gaps := New(newTestRuntime(t), Options{AuditCopy: func() audit.CopyStatus {
		return audit.CopyStatus{State: audit.CopyCurrent, Gaps: 2}
	}}).driverStatus(time.Now())[0]
	if len(gaps.Metrics) != 3 || gaps.Metrics[2].Label != "gaps" || gaps.Metrics[2].Value != 2 {
		t.Errorf("gaps not shown: %+v", gaps.Metrics)
	}
}
