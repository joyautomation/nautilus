//go:build !js

package server

import (
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/retain"
)

// The check before proposing compares Digests: a field of a consumer's
// state the digest leaves out is state a compaction could lose unseen. So
// every field is either digested (and changing it changes the digest) or
// named here as left out, with its reason; a new field fails until it is
// one or the other (and the keep rules version is bumped).

// settable is f made writable (the fields are unexported).
func settable(f reflect.Value) reflect.Value {
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

// bump sets f to another value of its kind.
func bump(t *testing.T, name string, f reflect.Value) {
	t.Helper()
	f = settable(f)
	switch f.Kind() {
	case reflect.String:
		f.SetString(f.String() + "x")
	case reflect.Bool:
		f.SetBool(!f.Bool())
	case reflect.Int, reflect.Int64:
		f.SetInt(f.Int() + 7)
	case reflect.Uint64:
		f.SetUint(f.Uint() + 7)
	case reflect.Slice:
		f.Set(reflect.Append(f, reflect.ValueOf("g2")))
	case reflect.Struct:
		if tm, ok := f.Interface().(time.Time); ok {
			f.Set(reflect.ValueOf(tm.Add(time.Hour)))
			return
		}
		t.Fatalf("%s: no way to change a %s", name, f.Type())
	default:
		t.Fatalf("%s: no way to change a %s", name, f.Type())
	}
}

// fieldsOf is a struct type's field names.
func fieldsOf(v any) []string {
	ty := reflect.TypeOf(v)
	var out []string
	for i := range ty.NumField() {
		out = append(out, ty.Field(i).Name)
	}
	return out
}

// accounted fails on a field of v neither digested nor left out.
func accounted(t *testing.T, v any, digested []string, out map[string]string) {
	t.Helper()
	known := map[string]bool{}
	for _, f := range digested {
		known[f] = true
	}
	for f := range out {
		known[f] = true
	}
	for _, f := range fieldsOf(v) {
		if !known[f] {
			t.Errorf("%T.%s is neither in the compaction digest nor named as left out: add it to the digest (and bump the keep rules version) or say why not", v, f)
		}
	}
}

var digestT = time.UnixMilli(1_700_000_000_000)

func fullIntent() alarmIntent {
	ms := digestT.UnixMilli()
	return alarmIntent{acked: true, ackBy: "op", ackMs: ms - 9, ackSeq: 4, shelfUntilMs: ms + 3600_000, shelfBy: "op", shelfSeq: 5, shelfSet: true,
		occ: "o1", activeMs: ms - 99, waitMs: 3, chatSinceMs: ms - 999, chatActs: 2, chatEpisodes: 1}
}

func fullOOS() oosIntent {
	ms := digestT.UnixMilli()
	return oosIntent{name: "Pump_1", untilMs: ms + 3600_000, grantedMs: 3600_000, startMs: ms - 9, by: "op", reason: "work", seq: 6}
}

func alarmWith(f func(l *AlarmLog)) string {
	l := newAlarmCheck().l
	l.view["A1"] = fullIntent()
	l.oos["pump_1"] = fullOOS()
	l.ending["tank_2"] = fullOOS()
	l.spent = map[string]int64{"L1": digestT.UnixMilli() - 9}
	l.adopt = map[string]adoption{"A2": {occ: "o2", activeMs: 9, by: "op"}}
	l.flood = &retain.AlarmFlood{ID: "f1", SinceMs: 9}
	if f != nil {
		f(l)
	}
	d, _ := l.digest(digestT)
	return d
}

// TestAlarmDigestCoversEveryField: every field of the alarm view's state
// changes the digest, but for those named as left out.
func TestAlarmDigestCoversEveryField(t *testing.T) {
	accounted(t, AlarmLog{}, []string{"view", "oos", "ending", "spent", "lapsed", "adopt", "flood"}, map[string]string{
		"eng": "wiring", "leading": "wiring", "term": "wiring", "node": "wiring", "refusal": "text", "scratch": "wiring",
		"restored": "this process's takeover", "liveMu": "lock", "mu": "lock", "clrMu": "lock",
		"clr": "the leader's queued proposals", "clrOcc": "the leader's queued proposals", "clrRetry": "timing", "clrRunning": "a proposal in flight",
		"match": "the escalation policy (configuration)", "occBusy": "a proposal in flight", "oosBusy": "a proposal in flight", "detBusy": "a proposal in flight",
		"heardMs": "a liveness hint: the newest applied TS, which a compacted log may make earlier (episodes then read as over sooner, the safe direction)",
		"ended":   "the engine's hand-off channel", "ackGen": "a count this process keeps, never the log's",
		"seen": "a hook", "journal": "a hook", "equip": "the program's alarm definitions",
	})
	base := alarmWith(nil)
	for _, name := range fieldsOf(alarmIntent{}) {
		if name == "ackGen" {
			continue
		}
		got := alarmWith(func(l *AlarmLog) {
			v := l.view["A1"]
			bump(t, name, reflect.ValueOf(&v).Elem().FieldByName(name))
			l.view["A1"] = v
		})
		if got == base {
			t.Errorf("alarmIntent.%s does not change the digest", name)
		}
	}
	for _, name := range fieldsOf(oosIntent{}) {
		for _, which := range []string{"oos", "ending"} {
			got := alarmWith(func(l *AlarmLog) {
				m, k := l.oos, "pump_1"
				if which == "ending" {
					m, k = l.ending, "tank_2"
				}
				v := m[k]
				bump(t, name, reflect.ValueOf(&v).Elem().FieldByName(name))
				m[k] = v
			})
			if got == base {
				t.Errorf("%s oosIntent.%s does not change the digest", which, name)
			}
		}
	}
	for _, name := range fieldsOf(adoption{}) {
		got := alarmWith(func(l *AlarmLog) {
			v := l.adopt["A2"]
			bump(t, name, reflect.ValueOf(&v).Elem().FieldByName(name))
			l.adopt["A2"] = v
		})
		if got == base {
			t.Errorf("adoption.%s does not change the digest", name)
		}
	}
	for name, f := range map[string]func(l *AlarmLog){
		"spent":  func(l *AlarmLog) { l.spent["L2"] = digestT.UnixMilli() },
		"lapsed": func(l *AlarmLog) { l.lapsed = map[uint64]oplog.Entry{9: {Seq: 9}} },
		"flood":  func(l *AlarmLog) { l.flood.SinceMs++ },
	} {
		if alarmWith(f) == base {
			t.Errorf("%s does not change the digest", name)
		}
	}
}

func sessionsWith(f func(ss *sessions)) string {
	c := newSessionCheck(digestT)
	ss := c.ss
	ss.byHash["h"] = &session{hash: "h", user: "u", oidc: true, issuer: "i", sub: "s", groups: []string{"g"},
		created: digestT.Add(-time.Hour), expires: digestT.Add(time.Hour), authAt: digestT.Add(-time.Minute), fails: 1, cred: "c", local: true}
	ss.ended["e"] = digestT.Add(time.Hour)
	if f != nil {
		f(ss)
	}
	d, _ := c.digest(digestT, 0)
	return d
}

// TestSessionDigestCoversEveryField: every field of a session the store
// holds, and every tombstone, changes the digest.
func TestSessionDigestCoversEveryField(t *testing.T) {
	accounted(t, sessions{}, []string{"byHash", "ended"}, map[string]string{
		"now": "the clock", "life": "configuration", "mu": "lock", "onEnd": "hooks", "rep": "wiring", "synced": "wiring", "adopted": "a hook",
	})
	base := sessionsWith(nil)
	for _, name := range fieldsOf(session{}) {
		if name == "hash" {
			continue // the map's key, printed as such
		}
		got := sessionsWith(func(ss *sessions) {
			se := ss.byHash["h"]
			bump(t, name, reflect.ValueOf(se).Elem().FieldByName(name))
		})
		if got == base {
			t.Errorf("session.%s does not change the digest", name)
		}
	}
	if sessionsWith(func(ss *sessions) { ss.ended["e"] = digestT.Add(2 * time.Hour) }) == base {
		t.Error("a tombstone's end does not change the digest")
	}
}
