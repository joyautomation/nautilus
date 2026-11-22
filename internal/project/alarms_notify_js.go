//go:build js

package project

import (
	"errors"
	"log/slog"

	"github.com/joyautomation/nautilus/alarm"
)

// The browser build sends no alarm notifications (alarms_notify.go): the
// playground runs alarms without a durable journal or notifiers.
func (p *Project) alarmNotifiers(*slog.Logger) ([]alarm.Notifier, error) {
	if len(p.Alarms.Notify) > 0 {
		return nil, errors.New("alarms.notify is not available in the browser playground")
	}
	return nil, nil
}
