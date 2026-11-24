package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
)

// The pair's side of the takeover hold (#710 (b2), runtime/hold.go): the
// manifest's redundancy.takeover-hold set on the runtime, its events in
// the journal, a condition on /api/cluster while it holds, and system
// alarms at its max and for a safe value not delivered.

// pairHold is a pair member's takeover hold, nil without one.
type pairHold struct {
	rt      *runtime.Runtime
	release string
	max     time.Duration
}

// setupHold sets the manifest's takeover hold on the runtime, before Run.
func (m *pairMember) setupHold(rt *runtime.Runtime, r *project.RedundancyConfig) error {
	cfg := r.RuntimeTakeoverHold()
	if cfg == nil {
		return nil
	}
	// The safe values by the tags' own spelling: a driver routes outputs by
	// the name it was bound with.
	for k, v := range cfg.Safe {
		if c, ok := rt.Tags().Canonical(k); ok && c != k {
			delete(cfg.Safe, k)
			cfg.Safe[c] = v
		}
	}
	if err := rt.SetTakeoverHold(cfg, m.holdEvent); err != nil {
		return err
	}
	m.hold = &pairHold{rt: rt, release: cfg.Release, max: cfg.Max}
	return nil
}

// holdEvent journals each change of the hold.
func (m *pairMember) holdEvent(ev runtime.HoldEvent) {
	j := m.journal.Load()
	if j == nil {
		return
	}
	detail := map[string]any{"event": string(ev.Kind), "ageMs": ev.Age.Milliseconds()}
	if ev.Inherited {
		detail["inherited"] = true
	}
	if ev.By != "" {
		detail["by"] = ev.By
	}
	if len(ev.Undelivered) > 0 {
		detail["undelivered"] = ev.Undelivered
	}
	result := audit.ResultOK
	if ev.Kind == runtime.HoldAtMax || ev.Kind == runtime.HoldReminder || ev.Kind == runtime.HoldSafeUndelivered {
		result = audit.ResultError
	}
	user := "system"
	if ev.Kind == runtime.HoldReleased && ev.By != runtime.HoldByProgram && ev.By != runtime.HoldByMax {
		user = ev.By
	}
	_, _ = (*j).Record(audit.Entry{
		User: user, Source: audit.SourceSystem,
		Action: "redundancy.takeover-hold", Target: string(m.o.Self),
		Detail: detail, Result: result,
	})
}

// holdCondition is the hold's line in /api/cluster's warnings while it
// holds on this leader, "" otherwise.
func (m *pairMember) holdCondition() string {
	h := m.hold
	if h == nil {
		return ""
	}
	if _, lead := h.rt.LeadingTerm(); !lead {
		return ""
	}
	st := h.rt.HoldStatus()
	if !st.Holding {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "takeover hold: the outputs are held (nothing goes to the field) for %s, until the program sets %s TRUE or an operator runs `naut redundancy release-hold`", st.Age.Round(100*time.Millisecond), h.release)
	if st.Inherited {
		b.WriteString("; inherited from the previous leader")
	}
	if st.AtMax {
		fmt.Fprintf(&b, "; PAST ITS MAX (%s): nobody is controlling the outputs", h.max)
	}
	if len(st.Undelivered) > 0 {
		fmt.Fprintf(&b, "; safe values not delivered: %s", strings.Join(st.Undelivered, ", "))
	}
	return b.String()
}

// holdStatus is the hold's block of /api/cluster, nil when none is set
// or this controller does not lead.
func (m *pairMember) holdStatus() *leader.HoldStatus {
	h := m.hold
	if h == nil {
		return nil
	}
	if _, lead := h.rt.LeadingTerm(); !lead {
		return nil
	}
	st := h.rt.HoldStatus()
	out := &leader.HoldStatus{Release: h.release, MaxMs: h.max.Milliseconds(), Holding: st.Holding}
	if st.Holding {
		out.Inherited, out.AtMax, out.AgeMs, out.Undelivered = st.Inherited, st.AtMax, st.Age.Milliseconds(), st.Undelivered
	}
	return out
}

// Takeover hold system alarms (alarms: section only, as every system
// alarm).
const (
	HoldMaxAlarmID  = "nautilus.takeover-hold-max"
	HoldSafeAlarmID = "nautilus.takeover-hold-safe"
)

// holdAlarms are raised while the hold is past its max (nobody controls
// the outputs), and while a safe value has not reached its device.
func holdAlarms(m *pairMember) []project.SystemAlarm {
	if m == nil || m.hold == nil {
		return nil
	}
	h := m.hold
	// Never the runtime's LeadingTerm here: Active runs under the alarm
	// engine's lock, which a takeover takes under leadMu (Fable M1). The
	// elector's gate is a lock-free read, and HoldStatus takes only the
	// hold's own lock.
	leading := m.q.IsLeader
	return []project.SystemAlarm{{
		Def: alarm.Def{
			ID: HoldMaxAlarmID, Tag: "__nautilus.takeover-hold-max",
			Name:     "Takeover hold reached its max: the program did not release the outputs in time (still held unless on-max: release; the program's release tag, or naut redundancy release-hold)",
			Priority: alarm.Critical, Class: "system",
			AckRequired: true, AutoClear: true,
		},
		Active: func() bool {
			st := h.rt.HoldStatus()
			return leading() && (st.AtMax || st.ReleasedAtMax)
		},
	}, {
		Def: alarm.Def{
			ID: HoldSafeAlarmID, Tag: "__nautilus.takeover-hold-safe",
			Name:     "Takeover hold: a safe value has not reached its device",
			Priority: alarm.High, Class: "system",
			AckRequired: true, AutoClear: true,
		},
		Active: func() bool { st := h.rt.HoldStatus(); return leading() && st.SafeAlarm },
	}}
}
