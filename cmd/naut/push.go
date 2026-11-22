package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/internal/apiclient"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/webpush"
	"github.com/joyautomation/nautilus/server"
)

// Web Push composition (#722): the VAPID key from the secrets file, the
// server's push options, and the escalation channel that pages through the
// server's sender.

// loadPushKey reads the VAPID key from the secrets file in stateDir. A
// missing key is generated and saved when generate says so (a controller
// that is not half of a pair, which has no other box to disagree with);
// otherwise the error names the one command that makes it. The same
// secrets file content must be on both members of a pair: a phone's
// subscription is bound to the key.
func loadPushKey(stateDir, by string, generate bool, stderr io.Writer) (*webpush.Key, error) {
	path := oncall.SecretsPath(stateDir)
	s, err := oncall.ReadSecrets(path)
	if err != nil {
		return nil, err
	}
	if v := s.Secrets[channel.SecretPushKey]; v != "" {
		k, err := webpush.ParseKey(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %s does not hold a VAPID key (%v): naut alarms secrets rm %s, then naut alarms secrets generate %s (phones subscribe again)", path, channel.SecretPushKey, err, channel.SecretPushKey, channel.SecretPushKey)
		}
		return k, nil
	}
	if !generate {
		return nil, fmt.Errorf("%s has no %s, and this controller is half of a pair, so it will not make one that the other box lacks: "+
			"run `naut alarms secrets generate %s` on one box, then copy alarm-secrets.json to the other (scp -p)", path, channel.SecretPushKey, channel.SecretPushKey)
	}
	k, err := generatePushKey(path, by)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(stderr, "naut run: push: made the VAPID key (%s) in %s\n", k.Fingerprint(), path)
	return k, nil
}

// generatePushKey makes the VAPID key and stores it; it refuses to replace
// one (every subscribed phone would stop being paged).
func generatePushKey(path, by string) (*webpush.Key, error) {
	k, err := webpush.NewKey()
	if err != nil {
		return nil, err
	}
	err = oncall.UpdateSecrets(path, by, func(s *oncall.Secrets) error {
		if s.Secrets[channel.SecretPushKey] != "" {
			return fmt.Errorf("%s is already set: replacing it ends every phone's subscription (naut alarms secrets rm %s first, if that is what you mean)", channel.SecretPushKey, channel.SecretPushKey)
		}
		s.Secrets[channel.SecretPushKey] = k.Private()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return k, nil
}

// pushOptions is the server's Web Push configuration when the manifest
// has alarms.channels.push: nil otherwise. A key that cannot be had is
// said, and push stays off (the channel then fails config at each page).
func pushOptions(proj *project.Project, stateDir string, paired bool, stderr io.Writer) *server.PushOptions {
	if proj.Alarms == nil || proj.Alarms.Channels == nil || proj.Alarms.Channels.Push == nil || stateDir == "" {
		return nil
	}
	k, err := loadPushKey(stateDir, "naut run", !paired, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "naut run: warning: push is off: %v\n", err)
		return nil
	}
	return &server.PushOptions{Key: k, Subject: proj.Alarms.Channels.Push.Subject, AllowHosts: proj.Alarms.Channels.Push.AllowHosts}
}

// pushPayload is what the phone's worker shows: the alarm and its state,
// nothing the push service could use (it sees only ciphertext anyway).
// Ack is the page's ack link (#715 PR 6), a credential: it rides only
// inside the encrypted payload, and the worker opens it on a tap.
type pushPayload struct {
	Alarm    string `json:"alarm"`
	Name     string `json:"name,omitempty"`
	State    string `json:"state"`
	Priority string `json:"priority,omitempty"`
	Area     string `json:"area,omitempty"`
	AtMs     int64  `json:"atMs,omitempty"`
	Level    int    `json:"level,omitempty"`
	Ack      string `json:"ack,omitempty"`
}

// pageFieldMax bounds a shown field in bytes: the worker shows 120
// characters of it, and the page must fit one push with its link.
const pageFieldMax = 480

func pageBody(m escalate.Message) []byte {
	r := m.Alarm
	p := pushPayload{
		Alarm: r.ID, Name: cutUTF8(r.Name, pageFieldMax), State: string(r.State), Priority: fmt.Sprint(r.Priority),
		Area: cutUTF8(r.Area, pageFieldMax), AtMs: r.ActiveMs, Level: m.Level, Ack: m.AckURL,
	}
	b := pageJSON(p)
	// What a push cannot carry is dropped, least useful first: the words,
	// then the link, then most of an id that alone is too long. The page
	// itself always goes (a person who cannot tap to acknowledge can still
	// open the HMI).
	if len(b) > webpush.MaxPayload {
		p.Name, p.Area = "", ""
		b = pageJSON(p)
	}
	if len(b) > webpush.MaxPayload {
		p.Ack = ""
		b = pageJSON(p)
	}
	if len(b) > webpush.MaxPayload {
		p.Alarm = cutUTF8(p.Alarm, pageFieldMax)
		b = pageJSON(p)
	}
	return b
}

// pageJSON is p as JSON without HTML escaping: a < in a name is one byte
// of the page, not six.
func pageJSON(p pushPayload) []byte {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	_ = e.Encode(p)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// cutUTF8 is s cut to at most n bytes on a character boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// pushChannel pages through srv: Sent is success; no subscription is a
// missing contact (reported like a missing address); everything else is a
// class and a code, never the endpoint.
func pushChannel(ref *atomic.Pointer[server.Server]) func(context.Context, escalate.Message) error {
	return func(ctx context.Context, m escalate.Message) error {
		srv := ref.Load()
		if srv == nil {
			return escalate.Failed(escalate.FailConfig)
		}
		res, err := srv.PushSend(ctx, m.To, server.PushMessage{
			Target: m.Alarm.Tag, Topic: m.Alarm.ID, Payload: pageBody(m), Urgency: "high", TTL: time.Hour,
		})
		return pushOutcome(res, err)
	}
}

// pushOutcome is what a page's result says to the escalation: nil when the
// push service accepted it on any phone, a missing contact when the person
// has no phone, otherwise a class and never the endpoint.
func pushOutcome(res server.PushResult, err error) error {
	switch {
	case errors.Is(err, server.ErrPushOff), errors.Is(err, server.ErrPushNotLeading), errors.Is(err, server.ErrPushUsers):
		return escalate.Failed(escalate.FailConfig)
	case err != nil:
		return escalate.Failed(escalate.FailUnknown)
	case res.Sent > 0:
		return nil
	case res.NoSubscription, (res.Pruned > 0 || res.Held > 0) && res.Failed == 0 && res.Sealed == 0 && res.Skipped == 0:
		return escalate.Failed(escalate.FailNoContact)
	case res.Failed > 0:
		return escalate.Failed(res.Class)
	}
	return escalate.Failed(escalate.FailConfig) // sealed under another key, or not allowed to read
}

const alarmsPushUsage = `naut alarms push — Web Push subscriptions (#722)

Usage:
  naut alarms push confirm-revocations --host <controller> [--port 8080]

When many people stop standing at once (a wrong users.json, a bad deploy,
a pair member with separately salted passwords), the controller HOLDS
their phones instead of deleting them: kept, not sent, and the system
alarm nautilus.push-held stays up. Put users.json right and they stand
again by themselves. If the removal is real (you deliberately removed
those users), this command ends the held subscriptions. It needs the
config permission and a fresh password (NAUTILUS_TOKEN or a token is
always fresh), and is journalled with the count. The credential is
NAUTILUS_TOKEN, sent as a bearer token (a personal access token from naut
users token <you>, or a service token with config).
`

// runAlarmsPush is naut alarms push: today, confirming a held mass revocation.
func runAlarmsPush(args []string) int {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Print(alarmsPushUsage)
		return 0
	}
	if len(args) == 0 || args[0] != "confirm-revocations" {
		fmt.Fprint(os.Stderr, alarmsPushUsage)
		return 2
	}
	fs := flag.NewFlagSet("alarms push confirm-revocations", flag.ContinueOnError)
	host := fs.String("host", "", "controller hostname or IP")
	port := fs.Int("port", 8080, "tag API port")
	fs.Usage = func() { fmt.Fprint(os.Stderr, alarmsPushUsage) }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "naut alarms push confirm-revocations: --host is required")
		return 2
	}
	base, err := apiclient.BaseURL(*host, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms push:", err)
		return 2
	}
	url := base + "/api/push/confirm-revocations"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms push:", err)
		return 2
	}
	apiclient.Authorize(req)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut alarms push: no controller at %s: %v\n", url, err)
		return 2
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var a struct {
		Ended     int    `json:"ended"`
		StillHeld int    `json:"stillHeld"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(out, &a)
	if resp.StatusCode != http.StatusOK {
		msg := a.Error
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		fmt.Fprintf(os.Stderr, "naut alarms push: %s returned %s: %s%s\n", url, resp.Status, msg, apiclient.RefusalHint(resp.StatusCode))
		return 1
	}
	fmt.Printf("ended %d held subscription(s); %d still held\n", a.Ended, a.StillHeld)
	return 0
}
