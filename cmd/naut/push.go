package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/alarm/oncall"
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
type pushPayload struct {
	Alarm    string `json:"alarm"`
	Name     string `json:"name,omitempty"`
	State    string `json:"state"`
	Priority string `json:"priority,omitempty"`
	Area     string `json:"area,omitempty"`
	AtMs     int64  `json:"atMs,omitempty"`
	Level    int    `json:"level,omitempty"`
}

func pageBody(m escalate.Message) []byte {
	r := m.Alarm
	b, _ := json.Marshal(pushPayload{
		Alarm: r.ID, Name: r.Name, State: string(r.State), Priority: fmt.Sprint(r.Priority),
		Area: r.Area, AtMs: r.ActiveMs, Level: m.Level,
	})
	return b
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
	case errors.Is(err, server.ErrPushOff), errors.Is(err, server.ErrPushNotLeading):
		return escalate.Failed(escalate.FailConfig)
	case err != nil:
		return escalate.Failed(escalate.FailUnknown)
	case res.Sent > 0:
		return nil
	case res.NoSubscription, res.Pruned > 0 && res.Failed == 0 && res.Sealed == 0 && res.Skipped == 0:
		return escalate.Failed(escalate.FailNoContact)
	case res.Failed > 0:
		return escalate.Failed(res.Class)
	}
	return escalate.Failed(escalate.FailConfig) // sealed under another key, or not allowed to read
}
