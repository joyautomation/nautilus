package project

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/alarm"
)

// The playground's notifiers (a js build's alarmNotifiers) keep the log
// notifier, which the alarms guide's first example uses, and skip a webhook
// with a warning instead of refusing the project.
func TestLogOnlyNotifiersKeepLogAndSkipWebhook(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	got := logOnlyNotifiers([]AlarmNotifyConfig{{Log: true}, {Webhook: "https://example.test/hook"}, {Log: true}}, log)
	if len(got) != 2 {
		t.Fatalf("%d notifiers, want the two log notifiers", len(got))
	}
	for _, n := range got {
		if _, ok := n.(*alarm.WebhookNotifier); ok {
			t.Errorf("a webhook notifier was built")
		}
	}
	if !strings.Contains(buf.String(), "webhook is not sent from the browser playground") {
		t.Errorf("no warning for the skipped webhook: %q", buf.String())
	}
	if got := logOnlyNotifiers(nil, nil); len(got) != 0 {
		t.Errorf("no entries gave %d notifiers", len(got))
	}
}
