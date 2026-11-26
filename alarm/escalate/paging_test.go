package escalate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// Paging under chattering and floods (#1249 slice 3), over a real engine
// with live detection on a stopped clock.

// chatterRig is a rig whose engine detects chattering at 3 activations in
// 60 s, and whose dispatcher pages a chattering alarm once per episode.
func chatterRig(t *testing.T, opt func(*Options), defs ...alarm.Def) *rig {
	t.Helper()
	return newRigEng(t, false, func(o *alarm.Options) {
		o.Chatter = alarm.ChatterConfig{Window: alarm.Duration(time.Minute), Count: 3}
	}, func(o *Options) {
		o.Chatter = true
		o.Home = "https://hmi.example:8443/"
		if opt != nil {
			opt(o)
		}
	}, defs...)
}

// bounce turns tag on for on and off for off, n times.
func (r *rig) bounce(tag string, on, off time.Duration, n int) {
	for range n {
		r.set(tag, true)
		r.run(on)
		r.set(tag, false)
		r.run(off)
	}
}

// msgs is every message sent, email then sms, with the channel kind.
type sentMsg struct {
	via string
	m   Message
}

func (r *rig) msgs() []sentMsg {
	var out []sentMsg
	for _, x := range []struct {
		via string
		rec *Recorder
	}{{"email", r.email}, {"sms", r.sms}} {
		for _, m := range x.rec.Messages() {
			out = append(out, sentMsg{x.via, m})
		}
	}
	return out
}

// count counts the messages about alarm id (a grouped page counts for
// every alarm it lists) by kind: "alarm", "chatter" or "group".
func (r *rig) count(id, kind string) int {
	n := 0
	for _, s := range r.msgs() {
		switch {
		case s.m.Group != nil:
			if kind == "group" && slices.ContainsFunc(s.m.Group.Alarms, func(a GroupAlarm) bool { return a.ID == id }) {
				n++
			}
		case s.m.Alarm.ID != id:
		case s.m.Chatter != nil:
			if kind == "chatter" {
				n++
			}
		default:
			if kind == "alarm" {
				n++
			}
		}
	}
	return n
}

// rows is the notify rows for id with result, as "level:to:via".
func (r *rig) rows(id, result string) []string {
	evs, err := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for i := len(evs) - 1; i >= 0; i-- {
		if e := evs[i]; e.ID == id && e.Result == result {
			out = append(out, fmt.Sprintf("%d:%s:%s", e.Level, e.To, e.Via))
		}
	}
	return out
}

// withAck mints ack links and codes, so a test sees which pages carry one.
func withAck(o *Options) {
	o.AckLink = func(Message, string) string { return "https://hmi.example/alarm-ack/x" }
	o.AckCode = func(Message, string) string { return "123" }
}

// An ack-or-rtn alarm pages per bounce: once it chatters, the bounces are
// held and each level goes once, as a chatter notice, on a clock pinned to
// the episode's start (so level 2 still climbs).
func TestChatterPagesOncePerLevel(t *testing.T) {
	r := chatterRig(t, withAck)
	r.bounce("DR1", 2*time.Second, 3*time.Second, 10)
	// Bounces 1 and 2 paged on their own before the third made it chatter.
	if n := r.count("DR1", "alarm"); n != 2 {
		t.Fatalf("pages per bounce %d, want 2 (before it chattered): %v", n, r.sent())
	}
	if n := r.count("DR1", "chatter"); n != 1 {
		t.Fatalf("chatter notices %d, want 1: %v", n, r.sent())
	}
	// Still chattering past level 2's 5 minutes: level 2 goes, once.
	r.bounce("DR1", 2*time.Second, 8*time.Second, 32)
	r.wantSent("DR1:1:alice:email", "DR1:1:alice:email", "DR1:1:alice:email", "DR1:2:bob:sms")
	for _, s := range r.msgs() {
		if s.m.Chatter == nil {
			continue
		}
		if s.m.AckURL != "" || s.m.AckCode != "" {
			t.Fatalf("a chatter notice carries an ack link or code: %+v", s.m)
		}
		if s.m.Link != "https://hmi.example:8443/alarms?alarm=DR1" {
			t.Fatalf("chatter notice link %q", s.m.Link)
		}
	}
	// One held row at the first held bounce; one at the end with the count.
	r.run(2 * time.Minute)
	held := r.rows("DR1", alarm.NotifyHeld)
	if len(held) != 2 {
		t.Fatalf("held rows %v, want 2 (first held, end)", held)
	}
	evs, _ := r.ring.Query(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	var ended string
	for _, e := range evs {
		if e.Result == alarm.NotifyHeld && strings.HasPrefix(e.Reason, "chattering ended") {
			ended = e.Reason
		}
	}
	if !strings.Contains(ended, "40 bounces") {
		t.Fatalf("end row %q, want 40 bounces held", ended)
	}
	// The notice's row names the chatter.
	var noted bool
	for _, e := range evs {
		noted = noted || (e.ID == "DR1" && e.Result == alarm.NotifySent && strings.HasPrefix(e.Reason, "chattering: "))
	}
	if !noted {
		t.Fatal("no sent row says chattering")
	}
}

// With the setting off, every bounce pages, as before.
func TestChatterPagingOff(t *testing.T) {
	r := chatterRig(t, func(o *Options) { o.Chatter = false })
	r.bounce("DR1", 2*time.Second, 3*time.Second, 10)
	if n := r.count("DR1", "alarm"); n != 10 {
		t.Fatalf("pages %d, want one per bounce (10)", n)
	}
}

// An alarm that needs no acknowledgement sits in normal between bounces,
// across several looks: the pinned clock lasts as long as the episode, so
// the notice goes once, not again at the next bounce.
func TestChatterThroughNormalSendsOnce(t *testing.T) {
	r := chatterRig(t, nil,
		alarm.Def{ID: "DR1", Tag: "DR1", Name: "Dryer fault", Area: "dryer", Priority: alarm.Medium, AckRequired: false, AutoClear: true})
	r.bounce("DR1", 2*time.Second, 6*time.Second, 7)
	if n := r.count("DR1", "chatter"); n != 1 {
		t.Fatalf("chatter notices %d, want 1: %v", n, r.sent())
	}
	if n := r.count("DR1", "alarm"); n != 2 {
		t.Fatalf("pages per bounce %d, want 2", n)
	}
}

// A latched alarm never acknowledged waits from its first activation, the
// episode's start: it already pages once per level and keeps its own
// clock (and its ack link), chattering or not.
func TestChatterLeavesTheLatchedWaitAlone(t *testing.T) {
	r := chatterRig(t, func(o *Options) {
		o.AckLink = func(m Message, via string) string { return "https://hmi.example/alarm-ack/x" }
	})
	r.bounce("TT1", 2*time.Second, 3*time.Second, 8)
	if n := r.count("TT1", "chatter"); n != 0 {
		t.Fatalf("a latched never-acked alarm got %d chatter notices: %v", n, r.sent())
	}
	r.wantSent("TT1:1:alice:email", "TT1:1:dave:email")
	for _, s := range r.msgs() {
		if s.m.AckURL == "" {
			t.Fatalf("the latched wait's page lost its ack link: %+v", s.m)
		}
	}
	if h := r.rows("TT1", alarm.NotifyHeld); len(h) != 0 {
		t.Fatalf("held rows %v for a wait that was never restarted", h)
	}
}

// Acknowledged between bounces, each bounce is a new wait and pages: once
// it chatters, the notice goes once per level.
func TestChatterAckBetweenBounces(t *testing.T) {
	r := chatterRig(t, nil)
	for range 8 {
		r.set("TT1", true)
		r.run(2 * time.Second)
		if _, err := r.eng.Ack([]string{"TT1"}, "alice"); err != nil {
			t.Fatal(err)
		}
		r.set("TT1", false)
		r.run(3 * time.Second)
	}
	if n := r.count("TT1", "alarm"); n != 4 { // bounces 1 and 2, to alice and dave
		t.Fatalf("pages per bounce %d, want 4 (two bounces to two people): %v", n, r.sent())
	}
	if n := r.count("TT1", "chatter"); n != 2 { // level 1, to alice and dave
		t.Fatalf("chatter notices %d, want 2: %v", n, r.sent())
	}
}

const slowFirstLevel = `
oncall:
  timezone: America/Chicago
escalation:
  - id: rtn
    match: { area: dryer }
    stop: ack-or-rtn
    levels:
      - {after: 30s, notify: [alice], via: [email]}
`

// A fast chatter under ack-or-rtn with a first level that waits: each
// bounce restarts the clock and returns before it, so nobody would ever
// be paged. The pinned clock pages level 1 after its 30 s.
func TestChatterFastWithDelayedLevelStillPages(t *testing.T) {
	r := chatterRig(t, func(o *Options) { o.Config = prepare(t, slowFirstLevel) })
	r.bounce("DR1", 4*time.Second, 6*time.Second, 6)
	r.wantSent("DR1:1:alice:email")
	if n := r.count("DR1", "chatter"); n != 1 {
		t.Fatalf("chatter notices %d, want 1", n)
	}
	// Off, the same chatter pages nobody.
	q := chatterRig(t, func(o *Options) { o.Config = prepare(t, slowFirstLevel); o.Chatter = false })
	q.bounce("DR1", 4*time.Second, 6*time.Second, 6)
	q.wantSent()
}

// A shelf stops the notice: a shelved alarm's chatter goes on by time but
// pages nobody.
func TestChatterShelvedSendsNothing(t *testing.T) {
	r := chatterRig(t, nil)
	r.bounce("DR1", 2*time.Second, 3*time.Second, 3) // the third starts the episode and its notice
	if err := r.eng.Shelve("DR1", r.now.Add(time.Hour), "alice"); err != nil {
		t.Fatal(err)
	}
	before := len(r.sent())
	r.bounce("DR1", 2*time.Second, 8*time.Second, 40) // past level 2's 5 minutes
	if got := r.sent(); len(got) != before {
		t.Fatalf("a shelved chattering alarm paged: %v", got[before:])
	}
}

// ---- floods ----

// floodDefs: pumps in an area no policy covers (they make the flood and
// page nobody), roast alarms the roast policy covers, a critical one and
// a safety one.
func floodDefs() []alarm.Def {
	var defs []alarm.Def
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("P%d", i)
		defs = append(defs, alarm.Def{ID: id, Tag: id, Name: "Pump " + id, Area: "loadout", Priority: alarm.Low, AckRequired: false, AutoClear: true})
	}
	for i := 1; i <= 8; i++ {
		id := fmt.Sprintf("R%d", i)
		defs = append(defs, alarm.Def{ID: id, Tag: id, Name: "Roast " + id, Area: "roast", Priority: alarm.Medium, AckRequired: true, AutoClear: true, Equipment: "Roaster"})
	}
	defs = append(defs,
		alarm.Def{ID: "C1", Tag: "C1", Name: "Roaster fire", Area: "roast", Priority: alarm.Critical, AckRequired: true, AutoClear: true},
		alarm.Def{ID: "S1", Tag: "S1", Name: "Guard open", Area: "roast", Priority: alarm.Medium, Class: "safety", AckRequired: true, AutoClear: true},
		alarm.Def{ID: "S2", Tag: "S2", Name: "Light curtain", Area: "roast", Priority: alarm.Medium, Class: "Safety", AckRequired: true, AutoClear: true},
		alarm.Def{ID: "H1", Tag: "H1", Name: "Hopper high", Area: "roast", Priority: alarm.High, Class: "level", AckRequired: true, AutoClear: true},
		alarm.Def{ID: "DR1", Tag: "DR1", Name: "Dryer fault", Area: "dryer", Priority: alarm.Medium, AckRequired: true, AutoClear: true},
		alarm.Def{ID: "DR2", Tag: "DR2", Name: "Dryer burner", Area: "dryer", Priority: alarm.Critical, AckRequired: true, AutoClear: true},
	)
	return defs
}

func floodRig(t *testing.T, gated bool, opt func(*Options)) *rig {
	t.Helper()
	return newRigEng(t, gated, func(o *alarm.Options) {
		o.Flood = alarm.FloodConfig{Window: alarm.Duration(10 * time.Minute), Threshold: 3}
		o.Chatter = alarm.ChatterConfig{Window: alarm.Duration(time.Minute), Count: 3}
	}, func(o *Options) {
		o.Chatter = true
		o.Flood = FloodPaging{Group: true}
		o.Home = "https://hmi.example:8443"
		if opt != nil {
			opt(o)
		}
	}, floodDefs()...)
}

// pump keeps a flood going: every pump bounces once (4 activations).
func (r *rig) pump() {
	for i := 1; i <= 4; i++ {
		r.set(fmt.Sprintf("P%d", i), true)
	}
	r.run(time.Second)
	for i := 1; i <= 4; i++ {
		r.set(fmt.Sprintf("P%d", i), false)
	}
	r.run(time.Second)
}

// runPumped runs d, pumping every 30 s.
func (r *rig) runPumped(d time.Duration) {
	for end := r.now.Add(d); r.now.Before(end); {
		r.pump()
		r.run(28 * time.Second)
	}
}

// groups lists the grouped pages sent, as "level:to:via:[ids]".
func (r *rig) groups() []string {
	var out []string
	for _, s := range r.msgs() {
		if s.m.Group == nil {
			continue
		}
		var ids []string
		for _, a := range s.m.Group.Alarms {
			ids = append(ids, a.ID)
		}
		out = append(out, fmt.Sprintf("%d:%s:%s:%v", s.m.Level, s.m.To, s.via, ids))
	}
	return out
}

func (r *rig) wantGroups(want ...string) {
	r.t.Helper()
	if got := r.groups(); !sameSet(got, want) {
		r.t.Fatalf("grouped %v\n want %v", got, want)
	}
}

// In a flood, the covered alarms' steps go as one grouped page per person
// per channel, the first at once, then at most every RegroupEvery for the
// ones newly due; critical and safety page on their own; every alarm a
// delivered grouped page lists has its row.
func TestFloodGroupsPages(t *testing.T) {
	r := floodRig(t, false, withAck)
	r.pump()
	if r.eng.Flood() == nil {
		t.Fatal("no flood after the pumps")
	}
	r.set("R1", true)
	r.run(time.Second)
	r.wantGroups("1:alice:email:[R1]", "1:dave:email:[R1]")
	for _, id := range []string{"R2", "R3", "C1", "S1"} {
		r.set(id, true)
		r.run(10 * time.Second)
	}
	// C1 and S1 paged on their own; R2 and R3 wait for the next batch.
	if r.count("C1", "alarm") != 2 || r.count("S1", "alarm") != 2 {
		t.Fatalf("critical/safety not paged on their own: %v", r.sent())
	}
	if r.count("R2", "group")+r.count("R2", "alarm") != 0 {
		t.Fatalf("R2 paged before the next batch: %v", r.sent())
	}
	r.runPumped(5 * time.Minute)
	r.wantGroups("1:alice:email:[R1]", "1:dave:email:[R1]", "1:alice:email:[R2 R3]", "1:dave:email:[R2 R3]")
	for _, id := range []string{"R1", "R2", "R3"} {
		if n := r.count(id, "alarm"); n != 0 {
			t.Fatalf("%s paged on its own %d times in a flood", id, n)
		}
		if got := r.rows(id, alarm.NotifyGrouped); !sameSet(got, []string{"1:alice:email", "1:dave:email"}) {
			t.Fatalf("%s grouped rows %v", id, got)
		}
	}
	for _, s := range r.msgs() {
		if s.m.Group == nil {
			continue
		}
		if s.m.AckURL != "" || s.m.AckCode != "" {
			t.Fatalf("a grouped page carries an ack: %+v", s.m)
		}
		if s.m.Link != "https://hmi.example:8443/alarms?focus=1" {
			t.Fatalf("grouped link %q", s.m.Link)
		}
	}
}

// A covered alarm that falls due long after the last batch (a late
// joiner in a long flood) is grouped at once, not held until the flood
// ends; its next level climbs on its own clock, grouped too.
func TestFloodLateJoinerIsTold(t *testing.T) {
	r := floodRig(t, false, nil)
	r.pump()
	r.set("R1", true)
	r.run(time.Second)
	r.runPumped(40 * time.Minute)
	if r.eng.Flood() == nil {
		t.Fatal("the flood ended")
	}
	r.set("R4", true)
	r.run(2 * time.Second)
	if r.count("R4", "group") != 2 {
		t.Fatalf("a late joiner was not grouped at once: %v", r.groups())
	}
	r.runPumped(11 * time.Minute)
	g := strings.Join(r.groups(), " ")
	if !strings.Contains(g, "2:erin:sms:[R4]") && !strings.Contains(g, "2:bob:sms:[R4]") {
		t.Fatalf("R4's level 2 was not grouped: %v", r.groups())
	}
	if n := r.count("R4", "alarm"); n != 0 {
		t.Fatalf("R4 paged on its own %d times", n)
	}
}

// failGroups fails every grouped page, rejected, and passes the rest.
type failGroups struct{ Channel }

func (f failGroups) Send(ctx context.Context, m Message) error {
	if m.Group != nil {
		_ = f.Channel.Send(ctx, m)
		return Failed(FailRejected)
	}
	return f.Channel.Send(ctx, m)
}

// A grouped page that fails for good covers nobody: its alarms page on
// their own to that person by that channel, and their rows say so.
func TestFloodFailedGroupFallsBackToIndividual(t *testing.T) {
	r := floodRig(t, false, func(o *Options) { o.Channels["email"] = failGroups{o.Channels["email"]} })
	r.pump()
	r.set("R1", true)
	r.run(3 * time.Second)
	if n := r.count("R1", "alarm"); n != 2 {
		t.Fatalf("after a failed grouped page R1 paged on its own %d times, want 2 (alice, dave): %v", n, r.sent())
	}
	if got := r.rows("R1", alarm.NotifyFailed); len(got) != 2 {
		t.Fatalf("failed rows %v, want the grouped page's two", got)
	}
	if got := r.rows("R1", alarm.NotifySent); !sameSet(got, []string{"1:alice:email", "1:dave:email"}) {
		t.Fatalf("sent rows %v", got)
	}
}

// A takeover mid-flood: the new leadership's first look resends the
// current level once, grouped and labelled, and pages no covered alarm on
// its own.
func TestFloodTakeoverResendsGroupedOnce(t *testing.T) {
	r := floodRig(t, true, nil)
	r.pump()
	for _, id := range []string{"R1", "R2", "R3"} {
		r.set(id, true)
	}
	r.run(time.Second)
	r.wantGroups("1:alice:email:[R1 R2 R3]", "1:dave:email:[R1 R2 R3]")
	r.runPumped(11 * time.Minute) // level 2 (erin) grouped at 06:10
	r.wantGroups("1:alice:email:[R1 R2 R3]", "1:dave:email:[R1 R2 R3]", "2:erin:sms:[R1 R2 R3]")
	r.term.Add(1)
	r.run(3 * time.Second)
	// The current level goes again, once, grouped; level 1 is not resent.
	r.wantGroups("1:alice:email:[R1 R2 R3]", "1:dave:email:[R1 R2 R3]", "2:erin:sms:[R1 R2 R3]",
		"2:erin:sms:[R1 R2 R3]")
	r.runPumped(time.Minute)
	if n := len(r.groups()); n != 4 {
		t.Fatalf("grouped pages after the takeover: %v", r.groups())
	}
	var labelled int
	for _, s := range r.msgs() {
		if s.m.Group != nil && s.m.Reason == ResendTakeover {
			labelled++
		}
	}
	if labelled != 1 {
		t.Fatalf("resent grouped pages labelled %d, want 1", labelled)
	}
	for _, id := range []string{"R1", "R2", "R3"} {
		if n := r.count(id, "alarm"); n != 0 {
			t.Fatalf("%s paged on its own after the takeover", id)
		}
	}
}

// A covered alarm that chatters in a flood is grouped (the flood wins),
// marked chattering; a critical one that chatters pages on its own, once
// per level per episode.
func TestFloodAndChatter(t *testing.T) {
	r := floodRig(t, false, nil)
	r.pump()
	for range 64 { // 5 min 20 s: past the next batch for DR1's policy step
		r.set("DR1", true)
		r.set("DR2", true)
		r.run(2 * time.Second)
		r.set("DR1", false)
		r.set("DR2", false)
		r.run(3 * time.Second)
	}
	if n := r.count("DR1", "chatter") + r.count("DR1", "alarm"); n != 0 {
		t.Fatalf("a covered chattering alarm paged on its own %d times: %v", n, r.sent())
	}
	var marked bool
	for _, s := range r.msgs() {
		if s.m.Group != nil {
			for _, a := range s.m.Group.Alarms {
				marked = marked || (a.ID == "DR1" && a.Chattering)
			}
		}
	}
	if !marked {
		t.Fatalf("no grouped page lists DR1 as chattering: %v", r.groups())
	}
	if n := r.count("DR2", "group"); n != 0 {
		t.Fatalf("the critical DR2 was grouped")
	}
	// Two bounces paged before it chattered; then one notice per level
	// (level 2 at 5 minutes), never per bounce.
	if a, c := r.count("DR2", "alarm"), r.count("DR2", "chatter"); a != 2 || c != 2 {
		t.Fatalf("critical chattering DR2: %d pages per bounce, %d notices; want 2 and 2 (levels 1 and 2)", a, c)
	}
}

// What alarms.flood.individual adds pages on its own; critical and safety
// still do.
func TestFloodIndividualAdds(t *testing.T) {
	r := floodRig(t, false, func(o *Options) {
		o.Flood.Priorities, o.Flood.Classes = []alarm.Priority{alarm.High}, []string{"level"}
	})
	r.pump()
	for _, id := range []string{"H1", "C1", "S1", "R1"} {
		r.set(id, true)
	}
	r.run(time.Second)
	for _, id := range []string{"H1", "C1", "S1"} {
		if r.count(id, "alarm") != 2 || r.count(id, "group") != 0 {
			t.Fatalf("%s not on its own: %v", id, r.sent())
		}
	}
	r.wantGroups("1:alice:email:[R1]", "1:dave:email:[R1]")
}

// When the flood ends, a step a grouped page covered is not paged again
// on its own, and a roster handover during the flood does not re-page the
// covered level to the new person. After the flood, alarms page on their
// own as usual.
func TestFloodEndNoRepage(t *testing.T) {
	r := floodRig(t, false, nil)
	r.setNow(chicago(t, "2026-11-20 06:52"))
	r.pump()
	r.set("R1", true)
	r.run(time.Second)
	r.runPumped(12 * time.Minute) // R1's level 2 (maintenance) falls due at 07:02: bob's turn
	if g := strings.Join(r.groups(), " "); !strings.Contains(g, "2:bob:sms:[R1]") {
		t.Fatalf("level 2 not grouped to bob: %v", r.groups())
	}
	// No more pumps: the flood ends a full window after the rate falls.
	r.run(22 * time.Minute)
	if r.eng.Flood() != nil {
		t.Fatal("the flood did not end")
	}
	r.run(10 * time.Minute)
	if n := r.count("R1", "alarm"); n != 0 {
		t.Fatalf("R1 re-paged on its own after the flood: %v", r.sent())
	}
	if g := strings.Join(r.groups(), " "); !strings.Contains(g, "3:carol:sms:[R1]") {
		t.Fatalf("level 3 (due in the flood) not grouped: %v", r.groups())
	}
	r.set("R5", true)
	r.run(2 * time.Second)
	r.wantSent2(t, "R5", "R5:1:alice:email", "R5:1:dave:email")
}

// wantSent2 checks the individual pages about id.
func (r *rig) wantSent2(t *testing.T, id string, want ...string) {
	t.Helper()
	var got []string
	for _, s := range r.sent() {
		if strings.HasPrefix(s, id+":") {
			got = append(got, s)
		}
	}
	if !sameSet(got, want) {
		t.Fatalf("individual pages about %s: %v, want %v", id, got, want)
	}
}

// Grouping needs an engine that reports its flood.
func TestFloodNeedsAFlooder(t *testing.T) {
	_, err := New(Options{Config: prepare(t, policy), Engine: noFlood{}, Flood: FloodPaging{Group: true},
		Channels: map[string]Channel{"email": &Recorder{}, "sms": &Recorder{}, "log": LogChannel{}}})
	if err == nil {
		t.Fatal("New took a grouping dispatcher over an engine with no flood")
	}
}

type noFlood struct{}

func (noFlood) Active() []alarm.Record             { return nil }
func (noFlood) Lookup(string) (alarm.Record, bool) { return alarm.Record{}, false }
func (noFlood) Notified(string, alarm.Notice)      {}

// A restart mid-flood: the flood carries over (the engine's retain path)
// and the new dispatcher's first look resends the current level once,
// grouped and labelled, paging no covered alarm on its own.
func TestFloodCarriedOverARestartResendsGrouped(t *testing.T) {
	r := floodRig(t, false, nil)
	r.pump()
	for _, id := range []string{"R1", "R2", "R3"} {
		r.set(id, true)
	}
	r.run(time.Second)
	r.runPumped(11 * time.Minute) // level 2 (erin) grouped
	f := r.eng.Flood()
	if f == nil {
		t.Fatal("no flood")
	}

	snap, carried := r.eng.Snapshot(), r.eng.RetainedFlood()
	n := &rig{t: t, tags: map[string]any{}}
	n.setNow(r.now.Add(2 * time.Second))
	for k, v := range r.tags {
		n.tags[k] = v
	}
	n.ring = alarm.NewRing(1000)
	var err error
	n.eng, err = alarm.New(alarm.Options{Defs: floodDefs(), Now: func() time.Time { return *n.clock.Load() },
		Read: func(p string) (any, bool) { v, ok := n.tags[p]; return v, ok }, Journal: n.ring,
		Flood:   alarm.FloodConfig{Window: alarm.Duration(10 * time.Minute), Threshold: 3},
		Chatter: alarm.ChatterConfig{Window: alarm.Duration(time.Minute), Count: 3}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.eng.Close() })
	n.eng.Restore(snap)
	n.eng.RestoreFlood(carried)
	n.email, n.sms = &Recorder{}, &Recorder{}
	n.d, err = New(Options{Config: prepare(t, policy), Engine: n.eng, Chatter: true, Flood: FloodPaging{Group: true},
		Channels: map[string]Channel{"email": n.email, "sms": n.sms, "log": LogChannel{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.d.Close)
	n.run(2 * time.Second)
	if nf := n.eng.Flood(); nf == nil || nf.ID != f.ID {
		t.Fatalf("the flood was not carried: %+v", nf)
	}
	n.wantGroups("2:erin:sms:[R1 R2 R3]")
	for _, s := range n.msgs() {
		if s.m.Group != nil && s.m.Reason != ResendRestart {
			t.Fatalf("resend labelled %q", s.m.Reason)
		}
	}
	for _, id := range []string{"R1", "R2", "R3"} {
		if c := n.count(id, "alarm"); c != 0 {
			t.Fatalf("%s paged on its own after the restart", id)
		}
	}
}

// ---- regressions from the Fable review ----

const whenFromSix01 = `
oncall:
  timezone: America/Chicago
escalation:
  - id: rtn
    match: { area: dryer }
    when: { from: "06:01", to: "18:00" }
    levels:
      - {after: 0s, notify: [alice], via: [email]}
`

const dayNight = `
oncall:
  timezone: America/Chicago
escalation:
  - id: night
    match: { area: dryer }
    when: { from: "18:00", to: "06:01" }
    levels:
      - {after: 0s, notify: [nina], via: [email]}
      - {after: 3m, notify: [nora], via: [sms]}
  - id: day
    match: { area: dryer }
    levels:
      - {after: 0s, notify: [alice], via: [email]}
      - {after: 3m, notify: [bob], via: [sms]}
`

var dryerNoAck = alarm.Def{ID: "DR1", Tag: "DR1", Name: "Dryer fault", Area: "dryer", Priority: alarm.Medium, AckRequired: false, AutoClear: true}

// The pinned clock pages under the policy of the wait the bounce began,
// as the alarm's own clock would: a chatter that began before a policy's
// when: window still pages once the window covers its bounces, and a
// chatter across a shift change pages the new shift's people.
func TestChatterFollowsTheWaitsPolicy(t *testing.T) {
	r := chatterRig(t, func(o *Options) { o.Config = prepare(t, whenFromSix01) }, dryerNoAck)
	r.bounce("DR1", 2*time.Second, 8*time.Second, 60) // 06:00 to 06:10
	r.wantSent("DR1:1:alice:email")
	if n := r.count("DR1", "chatter"); n != 1 {
		t.Fatalf("chatter notices %d, want 1", n)
	}

	q := chatterRig(t, func(o *Options) { o.Config = prepare(t, dayNight) }, dryerNoAck)
	q.bounce("DR1", 2*time.Second, 8*time.Second, 60)
	got := strings.Join(q.sent(), " ")
	if strings.Contains(got, "nora") {
		t.Fatalf("the night shift's level 2 paged after the day began: %v", q.sent())
	}
	if !strings.Contains(got, "DR1:1:alice:email") || !strings.Contains(got, "DR1:2:bob:sms") {
		t.Fatalf("the day shift was not paged: %v", q.sent())
	}
}

const slowLevels = `
oncall:
  timezone: America/Chicago
escalation:
  - id: rtn
    match: { area: dryer }
    stop: ack-or-rtn
    levels:
      - {after: 10s, notify: [alice], via: [email]}
      - {after: 40s, notify: [bob], via: [sms]}
`

// A chatter seen after level 2 fell due on its clock still pages level 1:
// the bounces before it lasted less than level 1's wait, so they paged
// nobody.
func TestChatterPagesLevel1EvenWhenLate(t *testing.T) {
	r := newRigEng(t, false, func(o *alarm.Options) {
		o.Chatter = alarm.ChatterConfig{Window: alarm.Duration(5 * time.Minute), Count: 3}
	}, func(o *Options) { o.Chatter = true; o.Config = prepare(t, slowLevels) })
	r.bounce("DR1", 5*time.Second, 20*time.Second, 12)
	r.wantSent("DR1:1:alice:email", "DR1:2:bob:sms")
}

const latchedLevels = `
oncall:
  timezone: America/Chicago
escalation:
  - id: roast
    match: { area: roast }
    levels:
      - {after: 0s, notify: [alice], via: [email]}
      - {after: 2m, notify: [bob], via: [sms]}
      - {after: 4m, notify: [carol], via: [sms]}
`

// A latched alarm acknowledged once and then never again chatters: its
// wait's own clock stands through the episode. When the episode ends, the
// levels its chatter notices already sent are not sent again.
func TestChatterEndDoesNotRepage(t *testing.T) {
	r := chatterRig(t, func(o *Options) { o.Config = prepare(t, latchedLevels) })
	r.set("TT1", true)
	r.run(2 * time.Second)
	if _, err := r.eng.Ack([]string{"TT1"}, "alice"); err != nil {
		t.Fatal(err)
	}
	r.set("TT1", false)
	r.run(3 * time.Second)
	r.bounce("TT1", 2*time.Second, 8*time.Second, 36) // 6 minutes of chatter, never acked
	during := len(r.sent())
	r.run(3 * time.Minute) // the episode ends a window after the last bounce
	if rec, _ := r.eng.Lookup("TT1"); rec.Chatter != nil {
		t.Fatal("still chattering")
	}
	if got := r.sent(); len(got) != during {
		t.Fatalf("the episode's end paged again: %v", got[during:])
	}
}

// After a chatter ends, a new activation is a new escalation: it pages
// level 1 as usual.
func TestActivationAfterChatterPages(t *testing.T) {
	r := chatterRig(t, nil)
	r.bounce("DR1", 2*time.Second, 3*time.Second, 6)
	r.run(2 * time.Minute)
	if rec, _ := r.eng.Lookup("DR1"); rec.Chatter != nil {
		t.Fatal("still chattering")
	}
	before := r.count("DR1", "alarm")
	r.set("DR1", true)
	r.run(2 * time.Second)
	if n := r.count("DR1", "alarm"); n != before+1 {
		t.Fatalf("a new activation after the chatter paged %d times, want once", n-before)
	}
}

// gateCh blocks its sends until gate closes.
type gateCh struct {
	Channel
	gate chan struct{}
}

func (b gateCh) Send(ctx context.Context, m Message) error {
	select {
	case <-b.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.Channel.Send(ctx, m)
}

// step runs d a second at a time without waiting for the senders, so
// sends stay in flight across looks.
func (r *rig) step(d time.Duration) {
	for end := r.now.Add(d); r.now.Before(end); {
		r.advance(time.Second)
		r.eng.Evaluate()
		r.d.Tick(r.now)
		time.Sleep(5 * time.Millisecond)
	}
}

const twoChannels = `
oncall:
  timezone: America/Chicago
escalation:
  - id: roast
    match: { area: roast }
    levels:
      - {after: 0s, notify: [alice], via: [email, sms]}
  - id: rtn
    match: { area: dryer }
    levels:
      - {after: 0s, notify: [alice], via: [sms]}
`

// A grouped send the queue could not take is not lost when another of its
// batch is delivered: its alarms page that person by that channel on
// their own once the queue has room.
func TestFloodDroppedGroupSendIsNotLost(t *testing.T) {
	gate := make(chan struct{})
	r := floodRig(t, false, func(o *Options) {
		o.Config = prepare(t, twoChannels)
		o.Queue = 1
		o.Channels["sms"] = gateCh{o.Channels["sms"], gate}
	})
	r.set("DR2", true) // critical: its sms blocks the sender
	r.step(time.Second)
	r.set("C1", true) // critical: its sms fills the queue
	r.step(time.Second)
	for i := 1; i <= 4; i++ {
		r.set(fmt.Sprintf("P%d", i), true)
	}
	r.step(time.Second)
	r.set("R1", true)
	r.step(2 * time.Second)
	if r.d.Dropped() == 0 {
		t.Fatal("the test did not fill the queue")
	}
	close(gate)
	r.step(time.Minute)
	r.d.Drain()
	sms := append(r.rows("R1", alarm.NotifySent), r.rows("R1", alarm.NotifyGrouped)...)
	if !slices.ContainsFunc(sms, func(s string) bool { return strings.HasSuffix(s, ":alice:sms") }) {
		t.Fatalf("R1 never reached alice by sms: rows %v", sms)
	}
}

const rtnRoast = `
oncall:
  timezone: America/Chicago
escalation:
  - id: roast
    match: { area: roast }
    stop: ack-or-rtn
    levels:
      - {after: 0s, notify: [alice], via: [email]}
`

// A grouped page released before it left (its alarm returned and came
// back with a new clock) told nobody, so it does not hold the next one
// back 5 minutes.
func TestFloodReleasedBatchHoldsNoFloor(t *testing.T) {
	gate := make(chan struct{})
	r := floodRig(t, false, func(o *Options) {
		o.Config = prepare(t, rtnRoast)
		o.Channels["email"] = gateCh{o.Channels["email"], gate}
	})
	r.set("C1", true) // critical: blocks the email sender
	r.step(time.Second)
	for i := 1; i <= 4; i++ {
		r.set(fmt.Sprintf("P%d", i), true)
	}
	r.step(time.Second)
	r.set("R1", true)
	r.step(2 * time.Second) // its grouped page waits behind C1's
	r.set("R1", false)
	r.step(2 * time.Second)
	r.set("R1", true) // a new clock: the queued page is for the old one
	r.step(time.Second)
	close(gate)
	r.step(10 * time.Second)
	r.d.Drain()
	if len(r.groups()) == 0 {
		t.Fatalf("R1's new activation waited for the floor: %v", r.sent())
	}
}

// "Safety" is the safety class in any case: never grouped.
func TestFloodSafetyClassAnyCase(t *testing.T) {
	r := floodRig(t, false, nil)
	r.pump()
	r.set("S2", true)
	r.run(time.Second)
	if r.count("S2", "alarm") != 2 || r.count("S2", "group") != 0 {
		t.Fatalf("class Safety was grouped: %v", r.groups())
	}
}

// ---- regressions from the Fable re-check ----

const repeatsLate = `
oncall:
  timezone: America/Chicago
escalation:
  - id: rtn
    match: { area: dryer }
    stop: ack-or-rtn
    repeat: {every: 30s, times: 5}
    levels:
      - {after: 20s, notify: [alice], via: [email]}
      - {after: 1m, notify: [bob], via: [sms]}
`

// A chatter seen after the last level's repeats fell due sends each level
// once, not every repeat at once.
func TestChatterLateSendsEachLevelOnce(t *testing.T) {
	r := newRigEng(t, false, func(o *alarm.Options) {
		o.Chatter = alarm.ChatterConfig{Window: alarm.Duration(10 * time.Minute), Count: 3}
	}, func(o *Options) { o.Chatter = true; o.Config = prepare(t, repeatsLate) })
	r.bounce("DR1", 5*time.Second, 115*time.Second, 3)
	r.wantSent("DR1:1:alice:email", "DR1:2:bob:sms")
}

const shiftWithRepeats = `
oncall:
  timezone: America/Chicago
escalation:
  - id: night
    match: { area: dryer }
    when: { from: "18:00", to: "06:05" }
    levels:
      - {after: 0s, notify: [nina], via: [email]}
  - id: day
    match: { area: dryer }
    repeat: {every: 30s, times: 4}
    levels:
      - {after: 0s, notify: [alice], via: [email]}
      - {after: 1m, notify: [bob], via: [sms]}
`

// A pinned clock that moves to another policy with several of its levels
// due sends each level once.
func TestChatterPolicyMoveSendsEachLevelOnce(t *testing.T) {
	r := chatterRig(t, func(o *Options) { o.Config = prepare(t, shiftWithRepeats) }, dryerNoAck)
	r.bounce("DR1", 2*time.Second, 8*time.Second, 40) // 06:00 to 06:06:40
	sent := strings.Join(r.sent(), " ")
	if a, b := strings.Count(sent, "alice"), strings.Count(sent, "bob"); a != 1 || b != 1 {
		t.Fatalf("after the move alice %d, bob %d pages; want once each: %v", a, b, r.sent())
	}
}

// An activation the dispatcher first sees after the episode ended is new:
// it pages as usual, even on the first look after the end; one it sees
// while the alarm still chatters is held, and the chatter notices cover
// it.
func TestActivationRightAfterChatterPages(t *testing.T) {
	for _, every := range []time.Duration{time.Second, 5 * time.Second} {
		for _, wait := range []int{50, 55, 56, 57, 58, 59, 60, 62} {
			r := chatterRig(t, func(o *Options) { o.Every = every })
			r.bounce("DR1", 2*time.Second, 3*time.Second, 4)
			n0 := r.count("DR1", "alarm") + r.count("DR1", "chatter")
			r.run(time.Duration(wait) * time.Second)
			r.set("DR1", true)
			at := r.now
			// The dispatcher holds it if the episode is still on at its
			// first look after the activation.
			var held bool
			for !r.d.last.After(at) {
				r.run(time.Second)
				rec, _ := r.eng.Lookup("DR1")
				held = rec.Chatter != nil
			}
			continued := held
			r.run(20 * time.Second)
			n := r.count("DR1", "alarm") + r.count("DR1", "chatter") - n0
			switch {
			case !continued && n != 1:
				t.Errorf("every %v, activation %d s after the last bounce, new: %d pages, want 1", every, wait, n)
			case continued && n != 0:
				t.Errorf("every %v, activation %d s after the last bounce, continuing the chatter: %d pages, want 0", every, wait, n)
			}
		}
	}
}

// A second chatter of the same wait that ends with the alarm standing
// does not page again what its notices sent.
func TestSecondChatterEndDoesNotRepage(t *testing.T) {
	r := chatterRig(t, nil)
	r.bounce("DR1", 2*time.Second, 3*time.Second, 5)
	r.run(90 * time.Second) // the first episode ends in unack-rtn
	r.bounce("DR1", 2*time.Second, 3*time.Second, 5)
	r.set("DR1", true) // stands through the second episode's end
	r.run(time.Second)
	n0 := len(r.sent())
	r.run(100 * time.Second)
	if rec, _ := r.eng.Lookup("DR1"); rec.Chatter != nil {
		t.Fatal("still chattering")
	}
	if got := r.sent(); len(got) != n0 {
		t.Fatalf("the second episode's end paged again: %v", got[n0:])
	}
}
