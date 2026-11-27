package project

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/escalate"
)

// PairAlarm is the alarm a pair member raises while it sees no leader
// (#1436). A pair with no leader scans nothing, so the alarm engine, which
// evaluates after each scan, evaluates nothing and pages nobody. This one
// alarm is evaluated apart: on an engine holding only it, on a timer of
// its own, the same on either member whatever its role. Its condition
// comes from the elector (quorum/node LeaderlessAlarm), which decides
// which member raises it so that it pages once.
type PairAlarm struct {
	Def alarm.Def
	// Active says whether this member raises it now; Episode names the
	// spell with no leader, so a page queued in one never leaves in the
	// next and each spell's escalation starts afresh. Both lock-free:
	// Active is the dispatcher's gate too, asked before every send.
	Active  func() bool
	Episode func() uint64
}

// PairAlarmEvery is how often a pair member evaluates its no-leader alarm.
var PairAlarmEvery = time.Second

// PairAlarms is a pair member's no-leader alarm engine, its dispatcher
// (nil without alarms.escalation) and the timer that evaluates them.
type PairAlarms struct {
	Engine     *alarm.Engine
	Escalation *escalate.Dispatcher
	// failed: per channel kind that reaches people (not log), whether
	// the last attempt to send one of its pages failed.
	failed map[string]*atomic.Bool
	stop   chan struct{}
	done   sync.WaitGroup
}

// Failing reports whether its pages are failing: the last attempt on any
// channel kind that reaches people failed. Kept per kind, so a log send
// beside a refused SMS does not hide it. Not the dispatcher's AnyFailing
// alone, which counts failures in a row within one episode and forgets
// them at the next: this alarm pages about once a spell, so a member
// whose every page fails would never read as failing, and its hello would
// keep its peer from paging (#1436). False with no page tried yet, and
// without a dispatcher.
func (a *PairAlarms) Failing() bool {
	if a == nil || a.Escalation == nil {
		return false
	}
	for _, f := range a.failed {
		if f.Load() {
			return true
		}
	}
	return a.Escalation.AnyFailing()
}

// pairChannel is a channel whose every attempt's outcome sets its kind's
// failed flag.
type pairChannel struct {
	ch     escalate.Channel
	failed *atomic.Bool
}

func (c pairChannel) Send(ctx context.Context, m escalate.Message) error {
	err := c.ch.Send(ctx, m)
	c.failed.Store(err != nil)
	return err
}

// pairAlarms builds p.PairAlarm's engine over the same durable journal
// sink (nil: the ring alone), the manifest's notifiers and, with esc, the
// same channels, and starts its timer. Its pages carry no ack link or
// reply code: nobody can act on a pair with no leader, and the alarm ends
// itself when a leader is seen.
func (p *Project) pairAlarms(sink alarm.Journal, notify []alarm.Notifier, esc *escalate.Config, chans map[string]escalate.Channel, log *slog.Logger) (*PairAlarms, error) {
	pa := p.PairAlarm
	if pa.Active == nil || pa.Episode == nil {
		return nil, errors.New("alarms: the pair's no-leader alarm needs its condition and its episode")
	}
	def := pa.Def
	def.AckRequired = false
	journal := alarm.Journal(alarm.NewRing(p.Alarms.Journal.Keep))
	if sink != nil {
		journal = alarm.NewMulti(journal, sink)
	}
	active := pa.Active
	eng, err := alarm.New(alarm.Options{
		Defs: []alarm.Def{def},
		Read: func(path string) (any, bool) {
			if path == def.Tag {
				return active(), true
			}
			return nil, false
		},
		// The wall clock: the runtime's does not move on a standby.
		Now:     time.Now,
		Journal: journal,
		Keep:    p.Alarms.Journal.Keep,
		Notify:  notify,
		Log:     log,
	})
	if err != nil {
		return nil, err
	}
	out := &PairAlarms{Engine: eng, stop: make(chan struct{}), failed: map[string]*atomic.Bool{}}
	if esc != nil {
		wrapped := make(map[string]escalate.Channel, len(chans))
		for k, ch := range chans {
			if k == "log" {
				wrapped[k] = ch // reaches nobody: its success proves nothing
				continue
			}
			out.failed[k] = &atomic.Bool{}
			wrapped[k] = pairChannel{ch: ch, failed: out.failed[k]}
		}
		o := escalate.Options{Config: esc, Engine: eng, Log: log, Channels: wrapped, Gate: pa.Active, Term: pa.Episode}
		d, err := escalate.New(o)
		if err != nil {
			eng.Close()
			return nil, err
		}
		out.Escalation = d
	}
	out.done.Add(1)
	go out.run()
	return out, nil
}

func (a *PairAlarms) run() {
	defer a.done.Done()
	t := time.NewTicker(PairAlarmEvery)
	defer t.Stop()
	for {
		a.Engine.Evaluate()
		if a.Escalation != nil {
			a.Escalation.Tick(time.Now())
		}
		select {
		case <-a.stop:
			return
		case <-t.C:
		}
	}
}

func (a *PairAlarms) close() error {
	close(a.stop)
	a.done.Wait()
	if a.Escalation != nil {
		a.Escalation.Close()
	}
	return a.Engine.Close()
}
