package channel

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Validate refuses at load time what would send a credential in the
// clear, or could not be sent at all; each refusal names the key.
func TestValidate(t *testing.T) {
	ok := func() Config {
		return Config{
			Email:   &Email{SMTP: "mail.example.com:587", From: "alarms@example.com", Username: "alarms"},
			SMS:     &Provider{URL: "https://sms.example.com", Account: "AC0001", From: "+15550100"},
			Voice:   &Provider{URL: "https://sms.example.com", Account: "AC0001", From: "+15550100"},
			Webhook: &Webhook{URL: "https://hooks.example.com/page", Header: "Authorization"},
		}
	}
	c := ok()
	if err := c.Validate(); err != nil {
		t.Fatalf("a good config: %v", err)
	}
	for _, tc := range []struct {
		want string
		mod  func(*Config)
	}{
		{"email.smtp", func(c *Config) { c.Email.SMTP = "mail.example.com" }},
		{"email.tls", func(c *Config) { c.Email.TLS = "none" }},
		{"email.from", func(c *Config) { c.Email.From = "Alarms <alarms@example.com>" }},
		{"sms.provider", func(c *Config) { c.SMS.URL = "http://sms.example.com" }},
		{"sms.provider", func(c *Config) { c.SMS.URL = "http://sms.example.com"; c.SMS.TLSConf.Insecure = true }},
		{"voice.account", func(c *Config) { c.Voice.Account = "AC/../x" }},
		{"sms.from", func(c *Config) { c.SMS.From = "5550100" }},
		{"webhook.url", func(c *Config) { c.Webhook.URL = "http://hooks.example.com"; c.Webhook.Header = "" }},
		{"webhook.url", func(c *Config) { c.Webhook.URL = "http://hooks.example.com"; c.Webhook.TLSConf.Insecure = true }},
		{"webhook.header", func(c *Config) { c.Webhook.Header = "X-Key: v" }},
	} {
		c := ok()
		tc.mod(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "alarms.channels."+tc.want) {
			t.Errorf("%s: %v", tc.want, err)
		}
	}
	// The one plaintext allowed: insecure, and no credential to send.
	c = ok()
	c.Webhook.URL, c.Webhook.Header, c.Webhook.TLSConf.Insecure = "http://hooks.example.com", "", true
	if err := c.Validate(); err != nil {
		t.Errorf("an insecure webhook with no header: %v", err)
	}
	if got := strings.Join(c.Insecure(), ","); got != "webhook" {
		t.Errorf("Insecure() = %q", got)
	}
	all := ok()
	if got := strings.Join(all.Secrets(), ","); got != "email.password,sms.token,voice.token,webhook.header" {
		t.Errorf("Secrets() = %q", got)
	}
}

// Proxied names each HTTP channel the environment sends through a proxy,
// by host only: a proxy's credentials never reach the journal.
func TestProxied(t *testing.T) {
	defer func(f func(*http.Request) (*url.URL, error)) { proxyFor = f }(proxyFor)
	proxyFor = func(r *http.Request) (*url.URL, error) {
		if r.URL.Host == "hooks.example.com" {
			return nil, nil // NO_PROXY
		}
		return url.Parse("http://user:pw@proxy.example.com:3128")
	}
	c := Config{
		Email:   &Email{SMTP: "mail.example.com:587", From: "alarms@example.com"},
		SMS:     &Provider{URL: "https://sms.example.com", Account: "AC0001", From: "+15550100"},
		Webhook: &Webhook{URL: "https://hooks.example.com/page"},
	}
	got := c.Proxied()
	if len(got) != 1 || got[0].Kind != "sms" || got[0].Host != "proxy.example.com:3128" {
		t.Fatalf("Proxied() = %+v, want sms via proxy.example.com:3128 without credentials", got)
	}
}

// A webhook URL may hold its own token: a refusal names its host only.
func TestValidateWebhookURLNotEchoed(t *testing.T) {
	c := Config{Webhook: &Webhook{URL: "http://hooks.example.com/T0000/B0000/secret-path-token"}}
	err := c.Validate()
	if err == nil || strings.Contains(err.Error(), "secret-path-token") || !strings.Contains(err.Error(), "hooks.example.com") {
		t.Fatalf("err = %v", err)
	}
	c = Config{SMS: &Provider{URL: "https://sms.example.com", Account: "AC1:x", From: "+15550100"}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "account") {
		t.Errorf("an account with ':' (splits basic auth): %v", err)
	}
}
