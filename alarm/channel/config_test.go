package channel

import (
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
