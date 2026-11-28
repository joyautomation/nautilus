//go:build !js

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/oplog"
)

// The alarms' and sessions' provenance models and scratch consumers for
// the keep decision (compact_keep.go).

// ── alarms ──

// aoVal is one alarm's acknowledgement and occurrence, as the view holds
// them (alarmIntent's ack and occ fields): one item, since an ack-clear,
// an occurrence-start and -end read both.
type aoVal struct {
	acked            bool
	by               string
	ms               int64
	seq              uint64
	occ              string
	activeMs, waitMs int64
}

// shelfVal is one alarm's shelf as the view holds it, normalised: an
// unshelve leaves no shelf (its seq is never read without one).
type shelfVal struct {
	until int64
	by    string
	seq   uint64
	set   bool
}

type chatVal struct {
	since     int64
	acts, eps int
}

// alarmModel is AlarmLog.Apply's provenance model.
type alarmModel struct {
	ao    map[string]aoVal
	shelf map[string]shelfVal
	// shelvedTo is the latest end any shelve gave an alarm: while it is to
	// come, a shelf record stays (an unsynced takeover lays the view's
	// shelf, or its absence, over its own retain store's, which may hold
	// that shelve).
	shelvedTo map[string]int64
	chat      map[string]chatVal
	flood     string // the flood's JSON, "" none
	oos       map[string]oosIntent
	links     map[string]int64
	lapsed    map[uint64]bool
}

func newAlarmModel() *alarmModel {
	return &alarmModel{ao: map[string]aoVal{}, shelf: map[string]shelfVal{}, shelvedTo: map[string]int64{}, chat: map[string]chatVal{},
		oos: map[string]oosIntent{}, links: map[string]int64{}, lapsed: map[uint64]bool{}}
}

func aoItem(id string) string    { return "a/ao/" + id }
func shelfItem(id string) string { return "a/sh/" + id }
func chatItem(id string) string  { return "a/ch/" + id }
func oosItem(k string) string    { return "a/oos/" + k }
func linkItem(l string) string   { return "a/link/" + l }
func lapsedItem(s uint64) string { return fmt.Sprintf("a/lapsed/%d", s) }

const floodItem = "a/flood"

// fx collects one step's reads and writes.
type fx struct{ reads, writes []string }

func (f *fx) read(it string)  { f.reads = append(f.reads, it) }
func (f *fx) write(it string) { f.writes = append(f.writes, it) }

// setAO writes v as alarm id's ack and occurrence, when it changes them.
func (m *alarmModel) setAO(f *fx, id string, v aoVal) {
	if m.ao[id] == v {
		return
	}
	if v == (aoVal{}) {
		delete(m.ao, id)
	} else {
		m.ao[id] = v
	}
	f.write(aoItem(id))
}

// readAO is alarm id's ack and occurrence, read: a dependency when set.
func (m *alarmModel) readAO(f *fx, id string) aoVal {
	v, ok := m.ao[id]
	if ok {
		f.read(aoItem(id))
	}
	return v
}

// readShelf is alarm id's shelf record, read: a dependency when there is
// one.
func (m *alarmModel) readShelf(f *fx, id string) {
	if _, ok := m.shelf[id]; ok {
		f.read(shelfItem(id))
	}
}

func (m *alarmModel) setShelf(f *fx, id string, v shelfVal) {
	if v.until == 0 {
		v.by, v.seq = "", 0
	}
	if m.shelf[id] == v {
		return
	}
	m.shelf[id] = v
	f.write(shelfItem(id))
}

func (m *alarmModel) step(e oplog.Entry) ([]string, []string) {
	var f fx
	if e.Lapsed && (e.Kind == oplog.KindAck || e.Kind == oplog.KindShelve || e.Kind == oplog.KindOutOfService) {
		m.lapsed[e.Seq] = true
		f.write(lapsedItem(e.Seq))
		if len(m.lapsed) > maxLapsedAlarms {
			gone := slices.Min(slices.Collect(maps.Keys(m.lapsed)))
			delete(m.lapsed, gone)
			f.write(lapsedItem(gone))
		}
		return f.reads, f.writes
	}
	switch e.Kind {
	case oplog.KindAck:
		if e.Link != "" {
			if _, spent := m.links[e.Link]; spent {
				f.read(linkItem(e.Link)) // its link was used: not applied
				return f.reads, f.writes
			}
			for l, ts := range m.links {
				if e.TS-ts > linkSpentFor.Milliseconds() {
					delete(m.links, l)
					f.write(linkItem(l))
				}
			}
			m.links[e.Link] = e.TS
			f.write(linkItem(e.Link))
		}
		occ := occsByID(e.Occs)
		for _, id := range e.IDs {
			v := aoVal{acked: true, by: e.By, ms: e.TS, seq: e.Seq}
			if o, ok := occ[id]; ok {
				v.occ, v.activeMs = o.Occ, o.ActiveMs
			} else {
				old := m.readAO(&f, id) // the occurrence stays
				v.occ, v.activeMs, v.waitMs = old.occ, old.activeMs, old.waitMs
			}
			m.setAO(&f, id, v)
		}
	case oplog.KindShelve:
		if m.shelvedTo[e.ID] > e.Until && !mutServer("keep_undone_shelf_dropped") {
			// It overrides a longer shelve some retain store may hold:
			// kept with it (see shelvedTo), or a later compaction finds no
			// longer shelve left to keep this one for (Fable, 2b-1).
			m.readShelf(&f, e.ID)
		}
		m.shelvedTo[e.ID] = max(m.shelvedTo[e.ID], e.Until)
		m.setShelf(&f, e.ID, shelfVal{until: e.Until, by: e.By, seq: e.Seq, set: true})
	case oplog.KindUnshelve:
		if m.shelvedTo[e.ID] > 0 && !mutServer("keep_undone_shelf_dropped") {
			m.readShelf(&f, e.ID) // as a shorter shelve: kept with the one it undid
		}
		m.setShelf(&f, e.ID, shelfVal{set: true})
	case oplog.KindOutOfService:
		k := ir.NameKey(e.ID)
		o := oosIntent{name: e.ID, untilMs: e.Until, grantedMs: e.Until - e.TS, startMs: e.TS, by: e.By, reason: e.Reason, seq: e.Seq}
		if m.oos[k] != o {
			m.oos[k] = o
			f.write(oosItem(k))
		}
	case oplog.KindInService:
		k := ir.NameKey(e.ID)
		if o, ok := m.oos[k]; ok {
			f.read(oosItem(k))
			if e.Ref == 0 || o.seq == e.Ref {
				delete(m.oos, k)
				f.write(oosItem(k))
			}
		}
	case oplog.KindAckClear:
		occ := occsByID(e.Occs)
		for _, id := range append(slices.Clone(e.IDs), e.ID) {
			if id == "" {
				continue
			}
			v := m.readAO(&f, id)
			o, named := occ[id]
			if named && o.Occ != "" && v.occ != "" && o.Occ != v.occ {
				continue
			}
			v.acked, v.by, v.ms, v.seq = false, "", 0, 0
			if named && o.Occ == v.occ {
				v.occ, v.activeMs, v.waitMs = "", 0, 0
			}
			m.setAO(&f, id, v)
		}
	case oplog.KindOccurrenceStart:
		for _, o := range e.Occs {
			v := m.readAO(&f, o.ID)
			if v.acked && v.occ != "" && v.occ != o.Occ {
				v.acked, v.by, v.ms, v.seq = false, "", 0, 0
			}
			v.occ, v.activeMs, v.waitMs = o.Occ, o.ActiveMs, o.WaitMs
			m.setAO(&f, o.ID, v)
		}
	case oplog.KindOccurrenceEnd:
		for _, o := range e.Occs {
			v := m.readAO(&f, o.ID)
			if v.occ == o.Occ {
				m.setAO(&f, o.ID, aoVal{})
			}
		}
	case oplog.KindAlarmDetect:
		var b detectEntry
		if json.Unmarshal(e.Value, &b) != nil || b.V != detectEntryVersion {
			return nil, nil
		}
		if b.FloodEnd != "" && m.flood != "" {
			f.read(floodItem)
			var cur struct{ ID string }
			_ = json.Unmarshal([]byte(m.flood), &cur)
			if cur.ID == b.FloodEnd {
				m.flood = ""
				f.write(floodItem)
			}
		}
		for _, c := range b.ChatterEnd {
			if v, ok := m.chat[c.ID]; ok {
				f.read(chatItem(c.ID))
				if v.since == c.SinceMs {
					delete(m.chat, c.ID)
					f.write(chatItem(c.ID))
				}
			}
		}
		if b.Flood != nil && b.Flood.ID != "" {
			j, _ := json.Marshal(b.Flood)
			if string(j) != m.flood {
				m.flood = string(j)
				f.write(floodItem)
			}
		}
		for _, c := range b.Chatter {
			if c.ID == "" || c.SinceMs == 0 {
				continue
			}
			v := chatVal{since: c.SinceMs, acts: c.Acts, eps: c.Episodes}
			if m.chat[c.ID] != v {
				m.chat[c.ID] = v
				f.write(chatItem(c.ID))
			}
		}
	case oplog.KindLapse:
		for id, v := range m.ao {
			if v.acked && slices.Contains(e.Refs, v.seq) {
				f.read(aoItem(id))
				v.acked, v.by, v.ms, v.seq = false, "", 0, 0
				m.setAO(&f, id, v)
			}
		}
		for id, v := range m.shelf {
			if v.set && v.until > 0 && slices.Contains(e.Refs, v.seq) {
				f.read(shelfItem(id))
				m.setShelf(&f, id, shelfVal{set: true})
			}
		}
		for k, o := range m.oos {
			if slices.Contains(e.Refs, o.seq) {
				f.read(oosItem(k))
				delete(m.oos, k)
				f.write(oosItem(k))
			}
		}
		for _, r := range e.Refs {
			if m.lapsed[r] {
				delete(m.lapsed, r)
				f.write(lapsedItem(r))
			}
		}
	}
	return f.reads, f.writes
}

func (m *alarmModel) live(t time.Time) []string {
	ms := t.UnixMilli()
	var out []string
	for id := range m.ao {
		out = append(out, aoItem(id))
	}
	for id, v := range m.shelf {
		if v.set && (v.until > ms || m.shelvedTo[id] > ms) {
			out = append(out, shelfItem(id))
		}
	}
	for id := range m.chat {
		out = append(out, chatItem(id))
	}
	if m.flood != "" {
		out = append(out, floodItem)
	}
	for k, o := range m.oos {
		if o.untilMs > ms {
			out = append(out, oosItem(k))
		}
	}
	for l, ts := range m.links {
		if ms-ts <= linkSpentFor.Milliseconds() {
			out = append(out, linkItem(l))
		}
	}
	for s := range m.lapsed {
		out = append(out, lapsedItem(s))
	}
	return out
}

// alarmCheck is the alarms' scratch consumer: the real AlarmLog.Apply, in
// view mode (never leading: no engine is touched), with no hooks, no
// goroutine and nothing journalled.
type alarmCheck struct{ l *AlarmLog }

func newAlarmCheck() *alarmCheck {
	return &alarmCheck{l: &AlarmLog{leading: func() bool { return false }, term: func() uint64 { return 0 }, scratch: true,
		view: map[string]alarmIntent{}, oos: map[string]oosIntent{}, equip: map[string][]string{}, ending: map[string]oosIntent{}}}
}

func (c *alarmCheck) name() string        { return "alarms" }
func (c *alarmCheck) apply(e oplog.Entry) { _, _ = c.l.Apply(e) }
func (c *alarmCheck) base(uint64)         {}

func (c *alarmCheck) digest(t time.Time, _ uint64) (string, string) {
	return c.l.digest(t)
}

// digest is the view's complete state, normalised at t (see the keep
// decision): a shelf not in force at t is none, an out-of-service window
// ended by t is none, a spent link past its window is forgotten, an alarm
// with nothing held is absent. Left out: ackGen (a count this process
// keeps, never the log's) and heardMs (the newest applied entry's TS, a
// liveness hint a compacted log may make earlier, the safe direction:
// episodes then read as over sooner). TestAlarmDigestCoversEveryField
// keeps the list honest.
func (l *AlarmLog) digest(t time.Time) (string, string) {
	ms := t.UnixMilli()
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	ids := slices.Sorted(maps.Keys(l.view))
	for _, id := range ids {
		v := l.view[id]
		v.ackGen = 0
		if !v.shelfSet || v.shelfUntilMs <= ms {
			v.shelfUntilMs, v.shelfBy, v.shelfSeq, v.shelfSet = 0, "", 0, false
		}
		if v == (alarmIntent{}) {
			continue
		}
		fmt.Fprintf(&b, "view %q acked %t by %q ms %d seq %d occ %q active %d wait %d shelf %d %q %d %t chat %d %d %d\n",
			id, v.acked, v.ackBy, v.ackMs, v.ackSeq, v.occ, v.activeMs, v.waitMs, v.shelfUntilMs, v.shelfBy, v.shelfSeq, v.shelfSet,
			v.chatSinceMs, v.chatActs, v.chatEpisodes)
	}
	oos := func(tag string, m map[string]oosIntent) {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			if o := m[k]; o.untilMs > ms {
				fmt.Fprintf(&b, "%s %q %q until %d granted %d start %d by %q reason %q seq %d\n", tag, k, o.name, o.untilMs, o.grantedMs, o.startMs, o.by, o.reason, o.seq)
			}
		}
	}
	oos("oos", l.oos)
	oos("ending", l.ending)
	for _, id := range slices.Sorted(maps.Keys(l.spent)) {
		if ts := l.spent[id]; ms-ts <= linkSpentFor.Milliseconds() {
			fmt.Fprintf(&b, "spent %q %d\n", id, ts)
		}
	}
	for _, s := range slices.Sorted(maps.Keys(l.lapsed)) {
		j, _ := json.Marshal(l.lapsed[s])
		fmt.Fprintf(&b, "lapsed %d %s\n", s, j)
	}
	for _, id := range slices.Sorted(maps.Keys(l.adopt)) {
		a := l.adopt[id]
		fmt.Fprintf(&b, "adopt %q %q %d %q\n", id, a.occ, a.activeMs, a.by)
	}
	if l.flood != nil {
		j, _ := json.Marshal(l.flood)
		fmt.Fprintf(&b, "flood %s\n", j)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:]), b.String()
}

// ── sessions ──

// sessionModel is the session store's provenance model (sessions.adopt,
// sessions.endByLog), with the clock at t: one item per session hash.
type sessionModel struct {
	t     time.Time
	open  map[string]bool
	ended map[string]int64
	exp   map[string]int64 // an open session's expiry
}

func newSessionModel(t time.Time) *sessionModel {
	return &sessionModel{t: t, open: map[string]bool{}, ended: map[string]int64{}, exp: map[string]int64{}}
}

func sessItem(h string) string { return "s/" + h }

func (m *sessionModel) step(e oplog.Entry) ([]string, []string) {
	body, ok := e.SessionBody()
	if !ok || m.t.IsZero() {
		return nil, nil
	}
	ms := m.t.UnixMilli()
	var f fx
	it := sessItem(body.Hash)
	switch body.Op {
	case oplog.SessionOpen:
		switch {
		case body.Expires <= ms:
			// expired by t: never adopted
		case m.ended[body.Hash] > ms, m.open[body.Hash]:
			f.read(it) // ended, or held: nothing changes
		default:
			m.open[body.Hash], m.exp[body.Hash] = true, body.Expires
			f.write(it)
		}
	case oplog.SessionEnd:
		was, wasEnd := m.open[body.Hash], m.ended[body.Hash]
		delete(m.open, body.Hash)
		delete(m.exp, body.Hash)
		if body.Expires > ms {
			m.ended[body.Hash] = body.Expires
		} else if wasEnd > 0 {
			f.read(it) // an earlier end's tombstone stands
		}
		if was || wasEnd != m.ended[body.Hash] {
			f.write(it)
		}
	}
	return f.reads, f.writes
}

func (m *sessionModel) live(time.Time) []string {
	var out []string
	for h := range m.open {
		out = append(out, sessItem(h))
	}
	for h := range m.ended {
		out = append(out, sessItem(h))
	}
	return out
}

// sessionCheck is the sessions' scratch consumer: a session store of its
// own, its clock pinned to t, fed as SessionLog.Apply feeds the live one.
type sessionCheck struct{ ss *sessions }

func newSessionCheck(t time.Time) *sessionCheck {
	ss := newSessions(0)
	ss.now = func() time.Time { return t }
	return &sessionCheck{ss: ss}
}

func (c *sessionCheck) name() string { return "sessions" }
func (c *sessionCheck) base(uint64)  {}

func (c *sessionCheck) apply(e oplog.Entry) {
	if body, ok := e.SessionBody(); ok {
		apply(c.ss, body)
	}
}

func (c *sessionCheck) digest(time.Time, uint64) (string, string) {
	ss := c.ss
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.pruneLocked()
	var b strings.Builder
	for _, h := range slices.Sorted(maps.Keys(ss.byHash)) {
		se := ss.byHash[h]
		fmt.Fprintf(&b, "session %s user %q oidc %t iss %q sub %q groups %q created %d expires %d cred %q local %t authAt %d fails %d\n",
			h, se.user, se.oidc, se.issuer, se.sub, se.groups, se.created.UnixMilli(), se.expires.UnixMilli(), se.cred, se.local, se.authAt.UnixMilli(), se.fails)
	}
	for _, h := range slices.Sorted(maps.Keys(ss.ended)) {
		fmt.Fprintf(&b, "ended %s %d\n", h, ss.ended[h].UnixMilli())
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), b.String()
}
