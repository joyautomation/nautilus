package project

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/channel/channeltest"
	"github.com/joyautomation/nautilus/alarm/channel/send"
	"github.com/joyautomation/nautilus/alarm/escalate"
)

// cmd/naut registers the real senders (cmd/naut/alarmseams.go); this
// package's own tests stand in for it, so the channel tests page through
// the same code a controller runs.
func init() {
	RegisterAlarmSenders(func(c *channel.Config, dir channel.Directory, push func(context.Context, escalate.Message) error) (map[string]escalate.Channel, error) {
		return send.Build(c, dir, send.Options{Push: push})
	})
}

// withSeams swaps the registered alarm parts for the test and puts the
// originals back after it.
func withSeams(t *testing.T, pg AlarmJournalOpener, senders AlarmSenders) {
	t.Helper()
	oldPG, oldSenders := alarmPostgres, alarmSenders
	alarmPostgres, alarmSenders = pg, senders
	t.Cleanup(func() { alarmPostgres, alarmSenders = oldPG, oldSenders })
}

const seamsChannelsManifest = alarmTags + `
alarms:
  defs:
    - { id: intrusion, tag: RTU9_ZONE15_INT_001_YA, area: zone15 }
  channels:
    email: { smtp: "127.0.0.1:1", from: alarms@example.com }
  oncall:
    rosters:
      - { id: ops, members: [alice] }
  escalation:
    - id: zone15
      match: { area: zone15 }
      levels:
        - { after: 0s, notify: [roster:ops], via: [email] }
`

// A build with no senders registered (the playground's wasm) refuses a
// manifest that pages people, and says why, instead of linking net/smtp.
func TestAlarmChannelsRefusedWithoutSenders(t *testing.T) {
	withSeams(t, nil, nil)
	proj, rt := loadAlarms(t, alarmFS(seamsChannelsManifest, nil))
	proj.AlarmDirectory = channeltest.Dir{}
	_, err := proj.NewAlarms(rt)
	if err == nil || !strings.Contains(err.Error(), "alarms.channels: email cannot send from this build") {
		t.Fatalf("NewAlarms = %v, want the not-in-this-build refusal", err)
	}
}

// The registered senders get the manifest's channels, the controller's
// directory and its push sender, and what they build is what escalation
// pages through.
func TestAlarmChannelsGoThroughTheRegisteredSenders(t *testing.T) {
	proj, rt := loadAlarms(t, alarmFS(seamsChannelsManifest, nil))
	dir := &channeltest.Dir{Addrs: map[string]map[string]string{"alice": {"email": "a@example.com"}}}
	proj.AlarmDirectory = dir
	pushed := errors.New("push called")
	proj.PushSend = func(context.Context, escalate.Message) error { return pushed }
	sent := make(chan escalate.Message, 1)
	var gotCfg *channel.Config
	var gotDir channel.Directory
	var gotPush error
	withSeams(t, nil, func(c *channel.Config, d channel.Directory, push func(context.Context, escalate.Message) error) (map[string]escalate.Channel, error) {
		gotCfg, gotDir = c, d
		if push != nil {
			gotPush = push(context.Background(), escalate.Message{})
		}
		return map[string]escalate.Channel{"email": chanFunc(func(_ context.Context, m escalate.Message) error {
			sent <- m
			return nil
		})}, nil
	})
	a, err := proj.NewAlarms(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if gotCfg != proj.Alarms.Channels || gotCfg == nil || gotCfg.Email == nil {
		t.Errorf("senders got config %+v, want the manifest's alarms.channels", gotCfg)
	}
	if gotDir != channel.Directory(dir) {
		t.Errorf("senders got directory %v, want the controller's", gotDir)
	}
	if gotPush != pushed {
		t.Errorf("senders' push returned %v, want the controller's PushSend", gotPush)
	}
	rt.Tags().SetPath("RTU9_ZONE15_INT_001_YA", true)
	rt.Scan()
	a.Escalation.Tick(time.Now())
	a.Escalation.Drain()
	select {
	case m := <-sent:
		if m.To != "alice" {
			t.Errorf("paged %q, want alice", m.To)
		}
	default:
		t.Fatal("escalation did not page through the registered sender")
	}
}

type chanFunc func(context.Context, escalate.Message) error

func (f chanFunc) Send(ctx context.Context, m escalate.Message) error { return f(ctx, m) }

const seamsPostgresManifest = alarmTags + `
alarms:
  journal: { sink: postgres }
  defs:
    - { id: a, tag: RTU9_ZONE15_INT_001_YA }
`

// A build with no Postgres journal registered refuses sink: postgres with
// a reason; an empty connection string is still refused first, by name.
func TestPostgresSinkRefusedWithoutAnOpener(t *testing.T) {
	withSeams(t, nil, alarmSenders)
	proj, rt := loadAlarms(t, alarmFS(seamsPostgresManifest, nil))
	t.Setenv(DefaultAlarmDSNEnv, "")
	t.Setenv(FallbackAlarmDSNEnv, "")
	if _, err := proj.NewAlarms(rt); err == nil || !strings.Contains(err.Error(), DefaultAlarmDSNEnv) {
		t.Errorf("empty DSN: want an error naming %s, got %v", DefaultAlarmDSNEnv, err)
	}
	t.Setenv(DefaultAlarmDSNEnv, "postgres://db.invalid/alarms")
	if _, err := proj.NewAlarms(rt); err == nil || !strings.Contains(err.Error(), "sink: postgres is not in this build") {
		t.Errorf("no opener: want the not-in-this-build refusal, got %v", err)
	}
}

// The registered opener gets the connection string from the environment,
// and the engine journals into what it opens; Close closes it.
func TestPostgresSinkGoesThroughTheRegisteredOpener(t *testing.T) {
	ring := alarm.NewRing(16)
	closed := false
	var gotDSN string
	withSeams(t, func(dsn string) (alarm.Journal, io.Closer, error) {
		gotDSN = dsn
		return ring, closerFunc(func() error { closed = true; return nil }), nil
	}, alarmSenders)
	proj, rt := loadAlarms(t, alarmFS(seamsPostgresManifest, nil))
	t.Setenv(DefaultAlarmDSNEnv, "postgres://db.invalid/alarms")
	a, err := proj.NewAlarms(rt)
	if err != nil {
		t.Fatal(err)
	}
	if gotDSN != "postgres://db.invalid/alarms" {
		t.Errorf("opener got DSN %q, want %s's", gotDSN, DefaultAlarmDSNEnv)
	}
	rt.Tags().SetPath("RTU9_ZONE15_INT_001_YA", true)
	rt.Scan()
	a.Close()
	if !closed {
		t.Error("Close did not close the opened journal")
	}
	evs, _ := ring.Query(time.Time{}, time.Time{}, alarm.Filter{})
	if len(evs) == 0 {
		t.Error("the engine journalled nothing into the opened journal")
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }
