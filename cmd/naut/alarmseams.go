package main

import (
	"context"
	"io"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/channel/send"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/alarm/pgjournal"
	"github.com/joyautomation/nautilus/internal/project"
)

// The controller registers the alarm parts the manifest does not import
// (internal/project/alarmseams.go), so the playground's wasm, which runs
// the same manifest code, links neither database/sql nor net/smtp (#1239).
func init() {
	project.RegisterAlarmPostgres(openAlarmPostgres)
	project.RegisterAlarmSenders(buildAlarmSenders)
}

func openAlarmPostgres(dsn string) (alarm.Journal, io.Closer, error) {
	j, err := pgjournal.New(dsn)
	if err != nil {
		return nil, nil, err
	}
	return j, j, nil
}

func buildAlarmSenders(c *channel.Config, dir channel.Directory, push func(context.Context, escalate.Message) error) (map[string]escalate.Channel, error) {
	return send.Build(c, dir, send.Options{Push: push})
}
