//go:build !js

package project

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/joyautomation/nautilus/alarm"
)

// alarmNotifiers builds a controller's alarm notifiers (alarms.notify:).
// It lives in a !js file: the webhook notifier carries net/http's client, and
// with it crypto/tls and HTTP/2, about a megabyte of the browser playground's
// download that never sends a webhook. alarms_notify_js.go is its stand-in.
func (p *Project) alarmNotifiers(log *slog.Logger) ([]alarm.Notifier, error) {
	var out []alarm.Notifier
	for i, n := range p.Alarms.Notify {
		switch {
		case n.Log:
			out = append(out, alarm.NewLogNotifier(log))
		case n.Webhook != "":
			o := alarm.WebhookOptions{}
			if n.HeaderEnv != "" {
				name := n.Header
				if name == "" {
					name = "Authorization"
				}
				v := os.Getenv(n.HeaderEnv)
				if v == "" {
					return nil, fmt.Errorf("alarms.notify[%d]: header-env %s is empty", i, n.HeaderEnv)
				}
				o.Headers = map[string]string{name: v}
			}
			out = append(out, alarm.NewWebhook(n.Webhook, o))
		}
	}
	return out, nil
}
