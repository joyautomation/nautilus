package rationalize

import (
	"sort"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// ISA-18.2 defaults for the reports.
const (
	// FloodWindow and FloodThreshold: an alarm flood is more than ten
	// alarms in ten minutes for one operator.
	FloodWindow    = 10 * time.Minute
	FloodThreshold = 10

	// ChatterWindow and ChatterCount: an alarm chatters when it activates
	// three or more times within sixty seconds.
	ChatterWindow = 60 * time.Second
	ChatterCount  = 3

	// BadActorTop is how many of the most frequent alarms a report names.
	BadActorTop = 10

	// StaleAfter: an alarm continuously active for more than a day is
	// stale; it has stopped telling anyone anything.
	StaleAfter = 24 * time.Hour
)

// Activations picks out of a journal the events that begin a new
// occurrence, in the order given, which must be oldest first (append
// order).
//
// An `active` counts only if the alarm's previous occurrence has ended:
// by a return to normal, or by suppression (the alarm stops being
// evaluated, so its next activation is one the operator sees again). The
// engine keeps only an acknowledgement and a shelf across a restart, so
// a standing alarm journals `active` again each time the controller
// starts, with no `rtn` between. Counting those would make every restart
// look like a flood and reset every stale clock.
func Activations(events []alarm.Event) []alarm.Event {
	open := map[string]bool{}
	var out []alarm.Event
	for _, e := range events {
		switch e.Kind {
		case alarm.KindActive:
			if !open[e.ID] {
				out = append(out, e)
			}
			open[e.ID] = true
		case alarm.KindRTN, alarm.KindSuppress:
			open[e.ID] = false
		}
	}
	return out
}

// Count is how often one alarm activated.
type Count struct {
	ID       string         `json:"id"`
	Name     string         `json:"name,omitempty"`
	Priority alarm.Priority `json:"priority"`
	Count    int            `json:"count"`
}

// counts tallies activations per alarm, most first, ties by id so two runs
// print the same list.
func counts(acts []alarm.Event) []Count {
	idx := map[string]int{}
	var out []Count
	for _, e := range acts {
		i, ok := idx[e.ID]
		if !ok {
			i = len(out)
			idx[e.ID] = i
			out = append(out, Count{ID: e.ID})
		}
		out[i].Count++
		out[i].Name, out[i].Priority = e.Name, e.Priority // the latest name wins
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func top(cs []Count, n int) []Count {
	if n > 0 && len(cs) > n {
		return cs[:n]
	}
	return cs
}

// Flood is one alarm flood: consecutive windows each over the threshold.
type Flood struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Count int       `json:"count"` // activations in the flood
	Peak  int       `json:"peak"`  // the busiest window's activations
	Top   []Count   `json:"top"`   // the alarms that contributed most
}

// FloodReport is the ISA-18.2 alarm rate over a range.
type FloodReport struct {
	From        time.Time     `json:"from"`
	To          time.Time     `json:"to"`
	Window      time.Duration `json:"windowNs"`
	Threshold   int           `json:"threshold"`
	Activations int           `json:"activations"`
	Windows     int           `json:"windows"`
	PerHour     float64       `json:"perHour"`
	PerWindow   float64       `json:"perWindow"`
	Max         int           `json:"max"`
	MaxAt       time.Time     `json:"maxAt"`
	// FloodWindows is how many windows were over the threshold, and
	// FloodPercent that as a share of all windows (ISA-18.2 aims for under
	// about 1%).
	FloodWindows int     `json:"floodWindows"`
	FloodPercent float64 `json:"floodPercent"`
	Floods       []Flood `json:"floods"`
}

// Floods buckets activations into consecutive windows starting at from
// (from, from+10m, …) covering [from, to), and reports the rate and every
// flood. A window is in flood when it holds MORE than threshold
// activations. Activations outside the range are ignored. When the range
// is not a whole number of windows, the last one is short; it still counts
// as a window.
func Floods(acts []alarm.Event, from, to time.Time, window time.Duration, threshold int) FloodReport {
	rep := FloodReport{From: from, To: to, Window: window, Threshold: threshold, Floods: []Flood{}}
	if window <= 0 || !to.After(from) {
		return rep
	}
	w := window.Milliseconds()
	lo := from.UnixMilli()
	rep.Windows = int((to.UnixMilli() - lo + w - 1) / w) // to is exclusive

	byWindow := make([][]alarm.Event, rep.Windows)
	for _, e := range acts {
		if e.TS < from.UnixMilli() || e.TS >= to.UnixMilli() {
			continue
		}
		i := (e.TS - lo) / w
		byWindow[i] = append(byWindow[i], e)
		rep.Activations++
	}
	rep.PerHour = float64(rep.Activations) / to.Sub(from).Hours()
	rep.PerWindow = rep.PerHour * window.Hours()

	var cur *Flood
	var curEvents []alarm.Event
	closeFlood := func() {
		if cur != nil {
			cur.Top = top(counts(curEvents), 3)
			rep.Floods = append(rep.Floods, *cur)
			cur, curEvents = nil, nil
		}
	}
	for i, evs := range byWindow {
		start := time.UnixMilli(lo + int64(i)*w)
		if len(evs) > rep.Max {
			rep.Max, rep.MaxAt = len(evs), start
		}
		if len(evs) <= threshold {
			closeFlood()
			continue
		}
		rep.FloodWindows++
		if cur == nil {
			cur = &Flood{Start: start}
		}
		cur.End = start.Add(window)
		if cur.End.After(to) {
			cur.End = to
		}
		cur.Count += len(evs)
		cur.Peak = max(cur.Peak, len(evs))
		curEvents = append(curEvents, evs...)
	}
	closeFlood()
	rep.FloodPercent = 100 * float64(rep.FloodWindows) / float64(rep.Windows)
	return rep
}

// Chatter is one chattering alarm.
type Chatter struct {
	ID          string         `json:"id"`
	Name        string         `json:"name,omitempty"`
	Priority    alarm.Priority `json:"priority"`
	Activations int            `json:"activations"` // in the range, chattering or not
	// Episodes is how many separate bursts of chatter there were: windows
	// holding count or more activations that overlap are one burst.
	Episodes int `json:"episodes"`
	// Peak is the most activations inside any one window.
	Peak  int       `json:"peak"`
	First time.Time `json:"first"` // when the first burst began
}

// Chattering lists every alarm that activated count or more times within
// window (inclusive: three activations exactly sixty seconds apart end to
// end chatter), most bursts first, then most activations, then by id.
func Chattering(acts []alarm.Event, window time.Duration, count int) []Chatter {
	type series struct {
		name string
		pri  alarm.Priority
		ts   []int64
	}
	byID := map[string]*series{}
	var ids []string
	for _, e := range acts {
		s, ok := byID[e.ID]
		if !ok {
			s = &series{}
			byID[e.ID] = s
			ids = append(ids, e.ID)
		}
		s.name, s.pri = e.Name, e.Priority
		s.ts = append(s.ts, e.TS)
	}
	w := window.Milliseconds()
	out := []Chatter{}
	for _, id := range ids {
		s := byID[id]
		ts := s.ts
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		c := Chatter{ID: id, Name: s.name, Priority: s.pri, Activations: len(ts)}
		var burstEnd int64 = -1 << 62
		k := 0
		for i := range ts {
			if k < i {
				k = i
			}
			for k+1 < len(ts) && ts[k+1]-ts[i] <= w {
				k++
			}
			n := k - i + 1
			c.Peak = max(c.Peak, n)
			if n < count {
				continue
			}
			if ts[i] > burstEnd {
				c.Episodes++
				if c.Episodes == 1 {
					c.First = time.UnixMilli(ts[i])
				}
			}
			burstEnd = max(burstEnd, ts[k])
		}
		if c.Episodes > 0 {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Episodes != b.Episodes {
			return a.Episodes > b.Episodes
		}
		if a.Activations != b.Activations {
			return a.Activations > b.Activations
		}
		return a.ID < b.ID
	})
	return out
}

// BadActors is the ISA-18.2 "most frequent alarms" list: the n alarms that
// activated most, with each one's share of all activations. Ties are
// broken by id.
type BadActor struct {
	Count
	Percent float64 `json:"percent"`
}

// BadActors ranks alarms by activations.
func BadActors(acts []alarm.Event, n int) []BadActor {
	cs := top(counts(acts), n)
	out := make([]BadActor, 0, len(cs))
	for _, c := range cs {
		out = append(out, BadActor{Count: c, Percent: 100 * float64(c.Count) / float64(len(acts))})
	}
	return out
}

// Stale is one alarm that has been active longer than the stale limit.
type Stale struct {
	ID       string         `json:"id"`
	Name     string         `json:"name,omitempty"`
	Priority alarm.Priority `json:"priority"`
	Since    time.Time      `json:"since"`
	For      time.Duration  `json:"forNs"`
}

// StaleAlarms lists the alarms still active at asOf whose occurrence began
// more than after earlier, longest first, then by id. events must be
// oldest first and should start well before asOf: an alarm whose
// activation is older than the first event read is not seen.
//
// The age runs from the activation that began the occurrence, so a
// controller restart (which journals `active` again) does not reset it.
func StaleAlarms(events []alarm.Event, asOf time.Time, after time.Duration) []Stale {
	type occ struct {
		e    alarm.Event
		open bool
	}
	cur := map[string]*occ{}
	var ids []string
	for _, e := range events {
		if e.TS > asOf.UnixMilli() {
			continue
		}
		o, ok := cur[e.ID]
		if !ok {
			o = &occ{}
			cur[e.ID] = o
			ids = append(ids, e.ID)
		}
		switch e.Kind {
		case alarm.KindActive:
			if !o.open {
				o.e, o.open = e, true
			}
			o.e.Name = e.Name
		case alarm.KindRTN, alarm.KindSuppress:
			o.open = false
		}
	}
	out := []Stale{}
	for _, id := range ids {
		o := cur[id]
		if !o.open {
			continue
		}
		age := asOf.Sub(o.e.Time())
		if age <= after {
			continue
		}
		out = append(out, Stale{ID: id, Name: o.e.Name, Priority: o.e.Priority, Since: o.e.Time(), For: age})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].For != out[j].For {
			return out[i].For > out[j].For
		}
		return out[i].ID < out[j].ID
	})
	return out
}
