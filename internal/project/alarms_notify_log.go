package project

import (
	"log/slog"

	"github.com/joyautomation/nautilus/alarm"
)

// logOnlyNotifiers is what the browser playground builds of alarms.notify:
// the log notifier, and nothing that sends (a webhook needs net/http's client,
// which the browser build does not link; see alarms_notify.go). A webhook entry
// is skipped with a warning, so a project that has one still loads.
// It is in a file of its own with no build tag so a test runs it on the
// controller's build.
func logOnlyNotifiers(entries []AlarmNotifyConfig, log *slog.Logger) []alarm.Notifier {
	var out []alarm.Notifier
	for i, n := range entries {
		switch {
		case n.Log:
			out = append(out, alarm.NewLogNotifier(log))
		case n.Webhook != "":
			if log == nil {
				log = slog.Default()
			}
			log.Warn("alarms.notify: a webhook is not sent from the browser playground; skipped", "index", i)
		}
	}
	return out
}
