package send

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/escalate"
)

type webhookSender struct {
	cfg    channel.Webhook
	dir    channel.Directory
	client *http.Client
}

func newWebhookSender(w channel.Webhook, dir channel.Directory, pool *x509.CertPool) *webhookSender {
	// No ServerName: the transport sets it per connection (target or proxy).
	return &webhookSender{cfg: w, dir: dir, client: newHTTPClient(tlsConfig("", w.TLSConf, pool))}
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
	// Chattering marks a flood's listed alarm that chatters.
	Chattering bool `json:"chattering,omitempty"`
}

// webhookBody is what a webhook receives. Kind is "alarm" (one alarm's
// page), "chatter" (a chattering alarm's once-per-level notice, #1249) or
// "flood" (a grouped page: no alarm, the flood and the alarms it lists).
type webhookBody struct {
	Kind    string         `json:"kind"`
	Alarm   *webhookAlarm  `json:"alarm,omitempty"`
	Chatter *alarm.Chatter `json:"chatter,omitempty"`
	Flood   *webhookFlood  `json:"flood,omitempty"`
	Policy  string         `json:"policy"`
	Level   int            `json:"level"`
	Repeat  int            `json:"repeat"`
	To      string         `json:"to"`
	Reason  string         `json:"reason"`
	Link    string         `json:"link,omitempty"`
}

type webhookFlood struct {
	alarm.FloodBrief
	FirstOut []alarm.Brief      `json:"firstOut"`
	Groups   []alarm.FloodGroup `json:"groups"`
	Alarms   []webhookAlarm     `json:"alarms"`
}

// webhookFloodMax is how many alarms a flood's webhook body lists.
const webhookFloodMax = 500

func webhookAlarmOf(a alarm.Record) webhookAlarm {
	return webhookAlarm{ID: a.ID, Name: a.Name, Priority: a.Priority.String(), Site: a.Site,
		Area: a.Area, Class: a.Class, State: a.State.String(), ActiveMs: a.ActiveMs}
}

func webhookPayload(m escalate.Message) webhookBody {
	b := webhookBody{Kind: "alarm", Policy: m.Policy, Level: m.Level, Repeat: m.Repeat, To: m.To, Reason: m.Reason, Link: m.Link}
	if g := m.Group; g != nil {
		b.Kind = "flood"
		f := &webhookFlood{FloodBrief: g.Flood, FirstOut: g.FirstOut, Groups: g.Groups, Alarms: []webhookAlarm{}}
		for i, a := range g.Alarms {
			if i == webhookFloodMax {
				break
			}
			f.Alarms = append(f.Alarms, webhookAlarm{ID: a.ID, Name: a.Name, Priority: a.Priority.String(),
				Area: a.Area, State: a.State.String(), ActiveMs: a.ActiveMs, Chattering: a.Chattering})
		}
		b.Flood = f
		return b
	}
	a := webhookAlarmOf(m.Alarm)
	b.Alarm = &a
	if m.Chatter != nil {
		b.Kind, b.Chatter = "chatter", m.Chatter
	}
	return b
}

func (w *webhookSender) attempt(ctx context.Context, m escalate.Message) error {
	u, err := url.Parse(w.cfg.URL)
	if err != nil {
		return escalate.Failed(escalate.FailConfig)
	}
	plain := u.Scheme == "http"
	// Validate enforces these; a secret never rides plaintext, whatever
	// reached here.
	if plain && (w.cfg.Header != "" || !w.cfg.TLSConf.Insecure) {
		return escalate.Failed(escalate.FailTLS)
	}
	if !plain && u.Scheme != "https" {
		return escalate.Failed(escalate.FailConfig)
	}
	hdr := http.Header{"Content-Type": {"application/json"}}
	if w.cfg.Header != "" {
		v, err := secret(w.dir, channel.SecretWebhookHeader)
		if err != nil {
			return err
		}
		if strings.ContainsAny(v, "\r\n") {
			return escalate.Failed(escalate.FailConfig)
		}
		hdr.Set(w.cfg.Header, v)
	}
	b, err := json.Marshal(webhookPayload(m))
	if err != nil {
		return escalate.Failed(escalate.FailUnknown)
	}
	return post(ctx, w.client, w.cfg.URL, hdr, "", "", b)
}
