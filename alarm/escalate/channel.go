package escalate

import (
	"context"
	"log/slog"
	"sync"
)

// LogChannel writes each notification to a structured log: the channel
// kind "log". It needs no address, so it pages nobody; it is how a policy
// is tried out before a real channel is configured.
type LogChannel struct{ Log *slog.Logger }

func (c LogChannel) Send(ctx context.Context, m Message) error {
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	if g := m.Group; g != nil {
		ids := make([]string, 0, min(len(g.Alarms), 20))
		for i, a := range g.Alarms {
			if i == 20 {
				break
			}
			ids = append(ids, a.ID)
		}
		log.LogAttrs(ctx, slog.LevelWarn, "alarm escalation, grouped (flood)",
			slog.String("flood", g.Flood.ID), slog.Int("alarms", len(g.Alarms)), slog.Any("ids", ids),
			slog.String("policy", m.Policy), slog.Int("level", m.Level), slog.Int("repeat", m.Repeat),
			slog.String("to", m.To), slog.String("reason", m.Reason))
		return nil
	}
	if c := m.Chatter; c != nil {
		log.LogAttrs(ctx, slog.LevelWarn, "alarm escalation, chattering",
			slog.String("id", m.Alarm.ID), slog.String("name", m.Alarm.Name),
			slog.String("priority", m.Alarm.Priority.String()), slog.Int("activations", c.Activations),
			slog.Int64("windowMs", c.WindowMs), slog.String("policy", m.Policy),
			slog.Int("level", m.Level), slog.Int("repeat", m.Repeat), slog.String("to", m.To),
			slog.String("reason", m.Reason))
		return nil
	}
	log.LogAttrs(ctx, slog.LevelWarn, "alarm escalation",
		slog.String("id", m.Alarm.ID), slog.String("name", m.Alarm.Name),
		slog.String("priority", m.Alarm.Priority.String()), slog.String("policy", m.Policy),
		slog.Int("level", m.Level), slog.Int("repeat", m.Repeat), slog.String("to", m.To),
		slog.String("reason", m.Reason))
	return nil
}

// Recorder is a Channel that keeps what it is sent, for tests. SetErr
// makes every later Send fail (the message is still recorded).
type Recorder struct {
	mu   sync.Mutex
	msgs []Message
	err  error
}

// SetErr sets the error Send returns; nil makes sends succeed again.
func (r *Recorder) SetErr(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

func (r *Recorder) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return r.err
}

// Messages returns a copy of what was sent.
func (r *Recorder) Messages() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.msgs...)
}
