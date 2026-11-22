// Package channel sends escalation pages outside the box (#715 PR 3):
// email over SMTP, SMS and voice through a Twilio-compatible REST
// provider, and a webhook. Each is an escalate.Channel: it turns the
// message's principal into an address through a Directory (the contacts
// file on the box, package alarm/oncall), reads its credential from the
// same Directory (the secrets file beside it, never the manifest, never
// the environment), and sends.
//
// Transport security is on by default: SMTP runs over TLS (STARTTLS or
// implicit) with the certificate verified, and the provider and the
// webhook are https. `insecure: true` on a channel relaxes only two
// things: the certificate is not verified, and a channel that sends no
// credential (email with no username, a webhook with no header) may run
// in plaintext. A password or a token never crosses a connection that is
// not TLS, insecure or not. `ca-file:` names a PEM bundle on the box to
// verify against instead of the system roots: how a site with its own CA
// keeps verification on.
//
// A failed send is an *escalate.SendError: a class and a numeric code,
// never the server's or the provider's text, which may echo an address.
//
// It imports only alarm, alarm/escalate and alarm/oncall (for the address
// checks the contacts file shares) (docs/design/context-map.md §2).
package channel

import (
	"fmt"
	"net"
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
)

// Secret keys in the secrets file (alarm/oncall), one per credential.
const (
	SecretEmailPassword = "email.password"
	SecretSMSToken      = "sms.token"
	SecretVoiceToken    = "voice.token"
	SecretWebhookHeader = "webhook.header"
)

// SecretKeys lists every key the secrets file may hold.
var SecretKeys = []string{SecretEmailPassword, SecretSMSToken, SecretVoiceToken, SecretWebhookHeader}

// Config is the manifest's `alarms.channels:`. Nothing here is secret:
// the credentials are in the secrets file on the box, the addresses in
// the contacts file.
type Config struct {
	Email   *Email    `yaml:"email" json:"email,omitempty"`
	SMS     *Provider `yaml:"sms" json:"sms,omitempty"`
	Voice   *Provider `yaml:"voice" json:"voice,omitempty"`
	Webhook *Webhook  `yaml:"webhook" json:"webhook,omitempty"`
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
		u, err := url.Parse(p.p.URL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%s.%s.provider: %q is not a base URL (e.g. https://api.twilio.com)", at, p.kind, p.p.URL)
		}
		if u.Scheme != "https" {
			return fmt.Errorf("%s.%s.provider: %q is not https: the provider's token never crosses plaintext, insecure or not", at, p.kind, p.p.URL)
		}
		if p.p.Account == "" || strings.ContainsAny(p.p.Account, "/?#%\r\n ") {
			return fmt.Errorf("%s.%s.account: the provider's account id is required (letters and digits)", at, p.kind)
		}
		if err := ValidPhone(p.p.From); err != nil {
			return fmt.Errorf("%s.%s.from: %w", at, p.kind, err)
		}
	}
	if w := c.Webhook; w != nil {
		u, err := url.Parse(w.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return fmt.Errorf("%s.webhook.url: %q is not an https URL", at, w.URL)
		}
		if u.Scheme == "http" && !w.TLSConf.Insecure {
			return fmt.Errorf("%s.webhook.url: %q is plaintext: use https, or set insecure: true (allowed only with no header)", at, w.URL)
		}
		if u.Scheme == "http" && w.Header != "" {
			return fmt.Errorf("%s.webhook.url: %q is plaintext and header: would send the secret in the clear; use https", at, w.URL)
		}
		if w.Header != "" && !validHeaderName(w.Header) {
			return fmt.Errorf("%s.webhook.header: %q is not a header name", at, w.Header)
		}
	}
	return nil
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
