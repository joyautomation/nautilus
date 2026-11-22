//go:build js

package project

import (
	"log/slog"

	"github.com/joyautomation/nautilus/alarm"
)

// The browser build sends no webhooks (alarms_notify.go): it keeps the log
// notifier, which needs no HTTP client, and skips a webhook with a warning.
func (p *Project) alarmNotifiers(log *slog.Logger) ([]alarm.Notifier, error) {
	return logOnlyNotifiers(p.Alarms.Notify, log), nil
}
