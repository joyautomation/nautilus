package alarm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Notifier ships an alarm event somewhere outside the process.
//
// Notification PIPELINES — the dialers, escalation trees and on-call
// rotations a real alarm system grows — are out of scope: this is the seam
// they would attach to, not an attempt at one. Def.Class carries the
// routing key, so a future pipeline routes on it without the engine
// learning anything new.
//
// Notify is called on the engine's own queue goroutine, never on the scan
// goroutine, so an implementation may block — but only up to its own
// timeout, because everything behind it is waiting in a bounded buffer.
type Notifier interface {
	Notify(context.Context, Event) error
}

// LogNotifier writes events to a structured log. The default notifier, and
// often the only one a site needs: an alarm journal that also lands in the
// log stream is searchable by whatever already collects logs.
type LogNotifier struct {
	log   *slog.Logger
	level slog.Level
}

// NewLogNotifier logs every event at Info. A nil logger uses slog.Default.
func NewLogNotifier(log *slog.Logger) *LogNotifier {
	if log == nil {
		log = slog.Default()
	}
	return &LogNotifier{log: log, level: slog.LevelInfo}
}

func (n *LogNotifier) Notify(ctx context.Context, e Event) error {
	n.log.Log(ctx, n.level, "alarm",
		"id", e.ID, "name", e.Name, "kind", e.Kind,
		"priority", e.Priority.String(), "site", e.Site,
		"state", e.State, "by", e.By, "ts", e.TS)
	return nil
}

// WebhookOptions tunes a WebhookNotifier. Every field has a working
// default, so NewWebhook(url, WebhookOptions{}) is a complete notifier.
type WebhookOptions struct {
	// Timeout bounds one attempt. Default 5s.
	Timeout time.Duration
	// Retries is how many times to try again after a failure. Default 2,
	// so three attempts in all. Kept small deliberately: the queue behind
	// this is bounded, and a notifier that retries for a minute converts a
	// slow endpoint into dropped events.
	Retries int
	// Backoff is the first retry delay; it doubles each attempt. Default
	// 250ms.
	Backoff time.Duration
	// Headers are added to every request — an API key, typically.
	Headers map[string]string
	// Client overrides the HTTP client (a test's, or one with a proxy).
	Client *http.Client
}

// WebhookNotifier POSTs each event as JSON.
type WebhookNotifier struct {
	url     string
	client  *http.Client
	retries int
	backoff time.Duration
	headers map[string]string
}

// NewWebhook builds a notifier that POSTs the Event JSON to url.
func NewWebhook(url string, o WebhookOptions) *WebhookNotifier {
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	if o.Retries < 0 {
		o.Retries = 0
	} else if o.Retries == 0 {
		o.Retries = 2
	}
	if o.Backoff <= 0 {
		o.Backoff = 250 * time.Millisecond
	}
	c := o.Client
	if c == nil {
		c = &http.Client{Timeout: o.Timeout}
	}
	return &WebhookNotifier{url: url, client: c, retries: o.Retries, backoff: o.Backoff, headers: o.Headers}
}

func (n *WebhookNotifier) Notify(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wait := n.backoff
	var last error
	for attempt := 0; attempt <= n.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
		if last = n.post(ctx, body); last == nil {
			return nil
		}
	}
	return last
}

func (n *WebhookNotifier) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range n.headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused; a webhook is called often
	// enough for that to matter.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %s: %s", n.url, resp.Status)
	}
	return nil
}
