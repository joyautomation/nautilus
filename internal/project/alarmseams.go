package project

import (
	"context"
	"io"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/escalate"
)

// The controller's alarm parts that the playground's wasm must not link
// (#1239): the journal in Postgres (database/sql and the driver) and the
// senders of escalation pages (net/smtp among them). The manifest never
// imports them; cmd/naut registers them once at start, as database/sql
// drivers register themselves. A build that registers neither (the
// playground) refuses a manifest that names them, with a message that says
// so, instead of linking them.

// AlarmJournalOpener opens the journal in Postgres at dsn
// (alarms.journal.sink: postgres). The Closer stops its writer.
type AlarmJournalOpener func(dsn string) (alarm.Journal, io.Closer, error)

// AlarmSenders builds one escalate.Channel per kind configured in
// alarms.channels. dir holds the addresses and the credentials; push pages
// a person's phones (nil: alarms.channels.push is refused).
type AlarmSenders func(c *channel.Config, dir channel.Directory, push func(context.Context, escalate.Message) error) (map[string]escalate.Channel, error)

var (
	alarmPostgres AlarmJournalOpener
	alarmSenders  AlarmSenders
)

// RegisterAlarmPostgres installs the Postgres journal. cmd/naut calls it
// from an init function; nil removes it.
func RegisterAlarmPostgres(open AlarmJournalOpener) { alarmPostgres = open }

// RegisterAlarmSenders installs the page senders. cmd/naut calls it from an
// init function; nil removes it.
func RegisterAlarmSenders(build AlarmSenders) { alarmSenders = build }

// AlarmSeams reports which of the two are registered in this build.
func AlarmSeams() (postgres, senders bool) {
	return alarmPostgres != nil, alarmSenders != nil
}
