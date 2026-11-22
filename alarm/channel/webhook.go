package channel

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/joyautomation/nautilus/alarm/escalate"
)

type webhookSender struct {
	cfg    Webhook
	dir    Directory
	client *http.Client
}

func newWebhookSender(w Webhook, dir Directory, pool *x509.CertPool) *webhookSender {
	host := ""
	if u, err := url.Parse(w.URL); err == nil {
		host = u.Hostname()
	}
	return &webhookSender{cfg: w, dir: dir, client: newHTTPClient(tlsConfig(host, w.TLSConf, pool))}
}

type webhookAlarm struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Priority string `json:"priority"`
	Site     string `json:"site"`
	Area     string `json:"area"`
	Class    string `json:"class"`
	State    string `json:"state"`
	ActiveMs int64  `json:"activeMs"`
}

type webhookBody struct {
	Alarm  webhookAlarm `json:"alarm"`
	Policy string       `json:"policy"`
	Level  int          `json:"level"`
	Repeat int          `json:"repeat"`
	To     string       `json:"to"`
	Reason string       `json:"reason"`
}

func (w *webhookSender) attempt(ctx context.Context, m escalate.Message) error {
	plain := strings.HasPrefix(w.cfg.URL, "http://")
	// Validate enforces these; a secret never rides plaintext, whatever
	// reached here.
	if plain && (w.cfg.Header != "" || !w.cfg.TLSConf.Insecure) {
		return escalate.Failed(escalate.FailTLS)
	}
	if !plain && !strings.HasPrefix(w.cfg.URL, "https://") {
		return escalate.Failed(escalate.FailConfig)
	}
	hdr := http.Header{"Content-Type": {"application/json"}}
	if w.cfg.Header != "" {
		v, err := secret(w.dir, SecretWebhookHeader)
		if err != nil {
			return err
		}
		if strings.ContainsAny(v, "\r\n") {
			return escalate.Failed(escalate.FailConfig)
		}
		hdr.Set(w.cfg.Header, v)
	}
	a := m.Alarm
	b, err := json.Marshal(webhookBody{
		Alarm: webhookAlarm{ID: a.ID, Name: a.Name, Priority: a.Priority.String(), Site: a.Site,
			Area: a.Area, Class: a.Class, State: a.State.String(), ActiveMs: a.ActiveMs},
		Policy: m.Policy, Level: m.Level, Repeat: m.Repeat, To: m.To, Reason: m.Reason,
	})
	if err != nil {
		return escalate.Failed(escalate.FailUnknown)
	}
	return post(ctx, w.client, w.cfg.URL, hdr, "", "", b)
}
