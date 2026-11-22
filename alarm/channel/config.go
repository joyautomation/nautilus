// Package channel is the manifest form of the channels that send
// escalation pages outside the box (#715 PR 3): `alarms.channels:` for
// email, SMS, voice, webhook and push, the names of their secrets in the
// secrets file, and the check of an inbound provider's signature. The
// senders themselves are package alarm/channel/send, which only the
// controller links: cmd/naut registers them with the manifest, so the
// playground's wasm reads and checks this config without net/smtp.
//
// It imports only alarm/oncall (for the address checks the contacts file
// shares) (docs/design/context-map.md §2).
package channel

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/joyautomation/nautilus/alarm/oncall"
)

// Channel kinds.
const (
	KindEmail   = "email"
	KindSMS     = "sms"
	KindVoice   = "voice"
	KindWebhook = "webhook"
	// KindPush pages a person's phones by Web Push (#722). There is no
	// address to look up: the person's own subscriptions are the address.
	KindPush = "push"
)

// Secret keys in the secrets file (alarm/oncall), one per credential.
const (
	SecretEmailPassword = "email.password"
	SecretSMSToken      = "sms.token"
	SecretVoiceToken    = "voice.token"
	SecretWebhookHeader = "webhook.header"
	// SecretPushKey is the VAPID private key (generated at first start).
	SecretPushKey = "push.vapid-key"
)

// SecretKeys lists every key the secrets file may hold.
var SecretKeys = []string{SecretEmailPassword, SecretSMSToken, SecretVoiceToken, SecretWebhookHeader, SecretPushKey}

// Config is the manifest's `alarms.channels:`. Nothing here is secret:
// the credentials are in the secrets file on the box, the addresses in
// the contacts file.
type Config struct {
	Email   *Email    `yaml:"email" json:"email,omitempty"`
	SMS     *Provider `yaml:"sms" json:"sms,omitempty"`
	Voice   *Provider `yaml:"voice" json:"voice,omitempty"`
	Webhook *Webhook  `yaml:"webhook" json:"webhook,omitempty"`
	Push    *Push     `yaml:"push" json:"push,omitempty"`
}

// Push is `alarms.channels.push:`: Web Push to the phones people
// subscribed through the HMI (#722). Nothing here is secret: the VAPID key
// is in the secrets file on the box (push.vapid-key), generated at the
// first start, and must be the same file content on both members of a pair.
type Push struct {
	// Subject is the mailto: or https: URL a push service may reach the
	// site by (RFC 8292). Required, and never defaulted: it is sent to
	// Google's, Apple's and Mozilla's push services with every page.
	Subject string `yaml:"subject" json:"subject"`
	// AllowHosts limits the push services the controller sends to: host
	// names ("fcm.googleapis.com", "*.push.apple.com"). Empty: any public
	// host. A name here may also be a private address, for a site's own
	// push relay; without it a private address is refused.
	AllowHosts []string `yaml:"allow-hosts" json:"allowHosts,omitempty"`
}

// TLS is what every channel says about its transport.
type TLS struct {
	// CAFile is a PEM bundle on the box to verify the server's certificate
	// against, instead of the system roots.
	CAFile string `yaml:"ca-file" json:"caFile,omitempty"`
	// Insecure skips certificate verification and, for a channel that
	// sends no credential, allows plaintext. Warned by naut check and
	// journalled at start.
	Insecure bool `yaml:"insecure" json:"insecure,omitempty"`
}

// SMTP TLS modes.
const (
	SMTPStartTLS = "starttls"
	SMTPImplicit = "implicit"
)

// Email is `alarms.channels.email:`.
type Email struct {
	// SMTP is the server, host:port (587 for STARTTLS, 465 implicit).
	SMTP string `yaml:"smtp" json:"smtp"`
	// TLS is "starttls" (default) or "implicit".
	TLS  string `yaml:"tls" json:"tls,omitempty"`
	From string `yaml:"from" json:"from"`
	// Username signs in with AUTH PLAIN, over TLS only; the password is
	// the secret email.password.
	Username string `yaml:"username" json:"username,omitempty"`
	TLSConf  TLS    `yaml:",inline" json:"transport,omitzero"`
}

// Provider is `alarms.channels.sms:` or `voice:`: a Twilio-compatible
// REST API. The token is the secret sms.token or voice.token.
type Provider struct {
	// URL is the API's base, e.g. https://api.twilio.com.
	URL string `yaml:"provider" json:"provider"`
	// Account is the account id the API path and basic auth name.
	Account string `yaml:"account" json:"account"`
	// From is the number messages and calls come from, E.164 (+15550100).
	From    string `yaml:"from" json:"from"`
	TLSConf TLS    `yaml:",inline" json:"transport,omitzero"`
}

// Webhook is `alarms.channels.webhook:`: each page is POSTed as JSON
// (principal, never address), for a paging service or a chat bridge.
type Webhook struct {
	URL string `yaml:"url" json:"url"`
	// Header names the header that carries the secret webhook.header,
	// e.g. Authorization. Empty sends none.
	Header  string `yaml:"header" json:"header,omitempty"`
	TLSConf TLS    `yaml:",inline" json:"transport,omitzero"`
}

// Kinds lists the configured channel kinds.
func (c *Config) Kinds() []string {
	if c == nil {
		return nil
	}
	var out []string
	if c.Email != nil {
		out = append(out, KindEmail)
	}
	if c.SMS != nil {
		out = append(out, KindSMS)
	}
	if c.Voice != nil {
		out = append(out, KindVoice)
	}
	if c.Webhook != nil {
		out = append(out, KindWebhook)
	}
	if c.Push != nil {
		out = append(out, KindPush)
	}
	return out
}

// Insecure lists the configured kinds with insecure: true.
func (c *Config) Insecure() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, k := range c.Kinds() {
		if c.tls(k).Insecure {
			out = append(out, k)
		}
	}
	return out
}

func (c *Config) tls(kind string) TLS {
	switch kind {
	case KindEmail:
		return c.Email.TLSConf
	case KindSMS:
		return c.SMS.TLSConf
	case KindVoice:
		return c.Voice.TLSConf
	case KindWebhook:
		return c.Webhook.TLSConf
	}
	return TLS{}
}

// Proxy is a channel whose requests go through an HTTP proxy.
type Proxy struct {
	Kind string
	Host string // host:port, never credentials
}

// Proxied lists the HTTP channels (sms, voice, webhook) that the
// environment sends through a proxy (HTTPS_PROXY, HTTP_PROXY, NO_PROXY, as
// http.ProxyFromEnvironment reads them).
func (c *Config) Proxied() []Proxy {
	if c == nil {
		return nil
	}
	var out []Proxy
	for _, x := range []struct {
		kind, url string
	}{{KindSMS, c.urlOf(KindSMS)}, {KindVoice, c.urlOf(KindVoice)}, {KindWebhook, c.urlOf(KindWebhook)}} {
		if x.url == "" {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, x.url, nil)
		if err != nil {
			continue
		}
		if u, err := proxyFor(req); err == nil && u != nil {
			out = append(out, Proxy{Kind: x.kind, Host: u.Host})
		}
	}
	return out
}

// proxyFor is http.ProxyFromEnvironment, a variable for a test.
var proxyFor = http.ProxyFromEnvironment

func (c *Config) urlOf(kind string) string {
	switch {
	case kind == KindSMS && c.SMS != nil:
		return c.SMS.URL
	case kind == KindVoice && c.Voice != nil:
		return c.Voice.URL
	case kind == KindWebhook && c.Webhook != nil:
		return c.Webhook.URL
	}
	return ""
}

// Secrets lists the secret keys the configured channels need.
func (c *Config) Secrets() []string {
	if c == nil {
		return nil
	}
	var out []string
	if c.Email != nil && c.Email.Username != "" {
		out = append(out, SecretEmailPassword)
	}
	if c.SMS != nil {
		out = append(out, SecretSMSToken)
	}
	if c.Voice != nil {
		out = append(out, SecretVoiceToken)
	}
	if c.Webhook != nil && c.Webhook.Header != "" {
		out = append(out, SecretWebhookHeader)
	}
	if c.Push != nil {
		out = append(out, SecretPushKey)
	}
	return out
}

// NeedsContact reports whether kind sends to a person's own address
// (email, sms, voice) rather than to one endpoint (webhook).
func NeedsContact(kind string) bool {
	return kind == KindEmail || kind == KindSMS || kind == KindVoice
}

// ValidPhone and ValidEmail check an address as the contacts file does.
func ValidPhone(s string) error { return oncall.ValidPhone(s) }
func ValidEmail(s string) error { return oncall.ValidEmail(s) }

// Validate checks c at load time; errors name the manifest key.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	const at = "alarms.channels"
	if e := c.Email; e != nil {
		host, port, err := net.SplitHostPort(e.SMTP)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("%s.email.smtp: %q is not host:port (e.g. mail.example.com:587)", at, e.SMTP)
		}
		switch e.TLS {
		case "", SMTPStartTLS, SMTPImplicit:
		default:
			return fmt.Errorf("%s.email.tls: %q is not starttls or implicit (plaintext is never chosen; insecure: true allows it only without a username)", at, e.TLS)
		}
		if err := ValidEmail(e.From); err != nil {
			return fmt.Errorf("%s.email.from: %w", at, err)
		}
		if strings.ContainsAny(e.Username, "\r\n") {
			return fmt.Errorf("%s.email.username: holds a line break", at)
		}
	}
	for _, p := range []struct {
		kind string
		p    *Provider
	}{{KindSMS, c.SMS}, {KindVoice, c.Voice}} {
		if p.p == nil {
			continue
		}
		// A refusal names the host only: a URL written with credentials
		// in it must not land in naut check's or CI's output.
		u, err := url.Parse(p.p.URL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%s.%s.provider: not a base URL with no credentials, query or fragment (e.g. https://api.twilio.com)", at, p.kind)
		}
		if u.Scheme != "https" {
			return fmt.Errorf("%s.%s.provider: the URL to %s is not https: the provider's token never crosses plaintext, insecure or not", at, p.kind, u.Host)
		}
		if p.p.Account == "" || strings.ContainsAny(p.p.Account, "/?#%:@\r\n ") {
			return fmt.Errorf("%s.%s.account: the provider's account id is required (letters and digits)", at, p.kind)
		}
		if err := ValidPhone(p.p.From); err != nil {
			return fmt.Errorf("%s.%s.from: %w", at, p.kind, err)
		}
	}
	if pu := c.Push; pu != nil {
		if err := validPushSubject(pu.Subject); err != nil {
			return fmt.Errorf("%s.push.subject: %w", at, err)
		}
		for i, h := range pu.AllowHosts {
			if h == "" || strings.ContainsAny(h, "/?#@: \r\n\t") || strings.Count(h, "*") > 1 || (strings.Contains(h, "*") && !strings.HasPrefix(h, "*.")) {
				return fmt.Errorf("%s.push.allow-hosts[%d]: %q is not a host name (push.example.net) or a wildcard under one (*.example.net)", at, i, h)
			}
		}
	}
	if w := c.Webhook; w != nil {
		u, err := url.Parse(w.URL)
		// A webhook URL may carry its own token (in the path or query), so
		// a refusal names its host only.
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return fmt.Errorf("%s.webhook.url: not an https URL", at)
		}
		if u.Scheme == "http" && !w.TLSConf.Insecure {
			return fmt.Errorf("%s.webhook.url: the URL to %s is plaintext: use https, or set insecure: true (allowed only with no header)", at, u.Host)
		}
		if u.Scheme == "http" && w.Header != "" {
			return fmt.Errorf("%s.webhook.url: the URL to %s is plaintext and header: would send the secret in the clear; use https", at, u.Host)
		}
		if w.Header != "" && !validHeaderName(w.Header) {
			return fmt.Errorf("%s.webhook.header: %q is not a header name", at, w.Header)
		}
	}
	return nil
}

// validPushSubject accepts what RFC 8292 §2.1 allows the subject to be: a
// mailto: address or an https: URL, which a push service may use to reach
// the site.
func validPushSubject(s string) error {
	switch {
	case strings.HasPrefix(s, "mailto:"):
		if err := ValidEmail(strings.TrimPrefix(s, "mailto:")); err != nil {
			return fmt.Errorf("not a mailto: address: %w", err)
		}
		return nil
	case strings.HasPrefix(s, "https://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" || u.User != nil {
			return errors.New("not an https URL with a host and no credentials")
		}
		return nil
	}
	return errors.New("is required: a mailto: address or an https: URL the site owns (a push service uses it to reach you); it is not defaulted, because it is sent with every page")
}

func validHeaderName(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return s != ""
}

// Directory is where a channel finds what is not in the manifest: a
// person's address for a kind and a credential by key. The contacts and
// secrets files on the box (alarm/oncall) are one; a test's map is
// another. Address returns "" and no error for a person with no address
// for kind (a FailNoContact send); an error means the files could not be
// read (FailConfig).
type Directory interface {
	Address(principal, kind string) (string, error)
	Secret(key string) (string, error)
}
