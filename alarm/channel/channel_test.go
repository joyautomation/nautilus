package channel_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/channel/channeltest"
	"github.com/joyautomation/nautilus/alarm/escalate"
)

const (
	alicePhone = "+15550123"
	aliceMail  = "alice@example.com"
)

func opts() channel.Options {
	return channel.Options{Timeout: 2 * time.Second}
}

func dir() channeltest.Dir {
	return channeltest.Dir{
		Addrs: map[string]map[string]string{"alice": {"email": aliceMail, "sms": alicePhone, "voice": alicePhone}, "bob": {}},
		Secrets: map[string]string{
			"email.password": "pw-secret", "sms.token": "tok-sms", "voice.token": "tok-voice", "webhook.header": "Bearer hook-secret",
		},
	}
}

func message(name string) escalate.Message {
	return escalate.Message{
		Alarm: alarm.Record{
			Def:      alarm.Def{ID: "a1", Name: name, Priority: alarm.High, Site: "plant", Area: "north", Class: "process"},
			State:    alarm.UnackActive,
			ActiveMs: 1700000000000,
		},
		Policy: "primary", Level: 2, To: "alice",
	}
}

func class(t *testing.T, err error, want string) *escalate.SendError {
	t.Helper()
	var se *escalate.SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want a *SendError", err, err)
	}
	if se.Class != want {
		t.Fatalf("class = %q (%v), want %q", se.Class, err, want)
	}
	return se
}

func noLeak(t *testing.T, err error, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(err.Error(), b) {
			t.Fatalf("error %q leaks %q", err.Error(), b)
		}
	}
}

func email(t *testing.T, o channeltest.SMTPOptions, mut func(*channel.Email)) (escalate.Channel, *channeltest.SMTP) {
	t.Helper()
	s := channeltest.NewSMTP(t, o)
	e := &channel.Email{SMTP: s.Addr, From: "alerts@example.com", TLSConf: channel.TLS{CAFile: s.CAFile}}
	if o.Implicit {
		e.TLS = channel.SMTPImplicit
	}
	if o.User != "" {
		e.Username = o.User
	}
	if mut != nil {
		mut(e)
	}
	ch, err := channel.Build(&channel.Config{Email: e}, dir(), opts())
	if err != nil {
		t.Fatal(err)
	}
	return ch["email"], s
}

func TestEmailSTARTTLSWithAuth(t *testing.T) {
	ch, s := email(t, channeltest.SMTPOptions{User: "mailer", Pass: "pw-secret"}, nil)
	if err := ch.Send(context.Background(), message("Tank high")); err != nil {
		t.Fatal(err)
	}
	m := s.Mails()
	if len(m) != 1 || !m[0].TLS || !m[0].Authed || m[0].To[0] != aliceMail || m[0].From != "alerts@example.com" {
		t.Fatalf("mails = %+v", m)
	}
	for _, want := range []string{"Subject: [HIGH] Tank high", "Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable", "MIME-Version: 1.0", "Message-ID: <", "Acknowledge it in the HMI.", "Escalation primary, level 2"} {
		if !strings.Contains(m[0].Data, want) {
			t.Errorf("message lacks %q:\n%s", want, m[0].Data)
		}
	}
}

func TestEmailImplicitTLS(t *testing.T) {
	ch, s := email(t, channeltest.SMTPOptions{Implicit: true}, nil)
	if err := ch.Send(context.Background(), message("Tank high")); err != nil {
		t.Fatal(err)
	}
	if m := s.Mails(); len(m) != 1 || !m[0].TLS {
		t.Fatalf("mails = %+v", m)
	}
}

func TestEmailUntrustedCert(t *testing.T) {
	for _, implicit := range []bool{false, true} {
		ch, s := email(t, channeltest.SMTPOptions{Implicit: implicit, User: "mailer", Pass: "pw-secret"},
			func(e *channel.Email) { e.TLSConf.CAFile = "" })
		class(t, ch.Send(context.Background(), message("x")), escalate.FailTLS)
		if s.AuthTried() || len(s.Mails()) != 0 {
			t.Fatal("sent something to a server whose certificate does not verify")
		}
	}
}

func TestEmailNoSTARTTLSWithUsername(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		ch, s := email(t, channeltest.SMTPOptions{NoSTARTTLS: true, User: "mailer", Pass: "pw-secret"},
			func(e *channel.Email) { e.TLSConf.Insecure = insecure })
		err := ch.Send(context.Background(), message("x"))
		class(t, err, escalate.FailTLS)
		if s.AuthTried() {
			t.Fatalf("insecure=%v: AUTH sent over plaintext", insecure)
		}
		if len(s.Mails()) != 0 {
			t.Fatal("mail sent")
		}
	}
}

func TestEmailNoSTARTTLSNoUsername(t *testing.T) {
	ch, s := email(t, channeltest.SMTPOptions{NoSTARTTLS: true}, nil)
	class(t, ch.Send(context.Background(), message("x")), escalate.FailTLS)
	if len(s.Mails()) != 0 {
		t.Fatal("plaintext mail without insecure: true")
	}
	ch, s = email(t, channeltest.SMTPOptions{NoSTARTTLS: true}, func(e *channel.Email) { e.TLSConf.Insecure = true })
	if err := ch.Send(context.Background(), message("x")); err != nil {
		t.Fatal(err)
	}
	if m := s.Mails(); len(m) != 1 || m[0].TLS {
		t.Fatalf("mails = %+v", m)
	}
}

func TestEmailBadPassword(t *testing.T) {
	ch, s := email(t, channeltest.SMTPOptions{User: "mailer", Pass: "other"}, nil)
	se := class(t, ch.Send(context.Background(), message("x")), escalate.FailAuth)
	if se.Code != 535 || se.Proto != "smtp" {
		t.Fatalf("code = %d %s", se.Code, se.Proto)
	}
	if len(s.Mails()) != 0 {
		t.Fatal("mail sent")
	}
}

func TestEmailRcptRejectedNoLeak(t *testing.T) {
	ch, _ := email(t, channeltest.SMTPOptions{RcptCode: 550}, func(e *channel.Email) { e.TLSConf.Insecure = false })
	err := ch.Send(context.Background(), message("x"))
	se := class(t, err, escalate.FailRejected)
	if se.Code != 550 {
		t.Fatalf("code = %d", se.Code)
	}
	noLeak(t, err, "@", aliceMail, "example.com", "unavailable")
}

func TestEmailTransientRetried(t *testing.T) {
	ch, _ := email(t, channeltest.SMTPOptions{RcptCode: 451}, nil)
	class(t, ch.Send(context.Background(), message("x")), escalate.FailProvider)
}

func TestEmailHungServerEndsWithContext(t *testing.T) {
	s := channeltest.NewSMTP(t, channeltest.SMTPOptions{Hang: true, Implicit: false})
	ch, err := channel.Build(&channel.Config{Email: &channel.Email{SMTP: s.Addr, From: "alerts@example.com",
		TLSConf: channel.TLS{CAFile: s.CAFile}}}, dir(), channel.Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// Cancelled, no deadline, a long per-attempt timeout: only closing the
	// connection when the context ends can free Send.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	err = ch["email"].Send(ctx, message("x"))
	if err == nil {
		t.Fatal("hung server: no error")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Send held %v past its context", d)
	}
	// And a deadline context is a timeout.
	dctx, dcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer dcancel()
	start = time.Now()
	class(t, ch["email"].Send(dctx, message("x")), escalate.FailTimeout)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Send held %v past its deadline", d)
	}
}

func TestEmailHeaderInjection(t *testing.T) {
	ch, s := email(t, channeltest.SMTPOptions{}, nil)
	if err := ch.Send(context.Background(), message("x\r\nBcc: evil@example.com\r\n\r\nbody")); err != nil {
		t.Fatal(err)
	}
	m := s.Mails()
	if len(m) != 1 {
		t.Fatal("not sent")
	}
	head, _, _ := strings.Cut(m[0].Data, "\r\n\r\n")
	for _, l := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(l), "bcc:") {
			t.Fatalf("injected header: %q", l)
		}
	}
	if len(m[0].To) != 1 {
		t.Fatalf("recipients = %v", m[0].To)
	}
}

func TestEmailNonASCIISubject(t *testing.T) {
	ch, s := email(t, channeltest.SMTPOptions{}, nil)
	if err := ch.Send(context.Background(), message("Niveau élevé")); err != nil {
		t.Fatal(err)
	}
	if d := s.Mails()[0].Data; !strings.Contains(d, "Subject: =?utf-8?q?") {
		t.Fatalf("subject not Q-encoded:\n%s", d)
	}
}

func TestNoContactDoesNotDial(t *testing.T) {
	ch, s := email(t, channeltest.SMTPOptions{}, nil)
	m := message("x")
	m.To = "bob"
	class(t, ch.Send(context.Background(), m), escalate.FailNoContact)
	m.To = "carol"
	class(t, ch.Send(context.Background(), m), escalate.FailNoContact)
	if len(s.Mails()) != 0 {
		t.Fatal("sent")
	}
}

func TestDirectoryErrorIsConfig(t *testing.T) {
	s := channeltest.NewSMTP(t, channeltest.SMTPOptions{})
	d := dir()
	d.Err = errors.New("read /secret/path: boom")
	ch, _ := channel.Build(&channel.Config{Email: &channel.Email{SMTP: s.Addr, From: "alerts@example.com",
		TLSConf: channel.TLS{CAFile: s.CAFile}}}, d, opts())
	err := ch["email"].Send(context.Background(), message("x"))
	class(t, err, escalate.FailConfig)
	noLeak(t, err, "secret", "boom")
}

func TestMissingSecretIsConfig(t *testing.T) {
	s := channeltest.NewSMTP(t, channeltest.SMTPOptions{User: "mailer", Pass: "x"})
	d := dir()
	delete(d.Secrets, "email.password")
	ch, _ := channel.Build(&channel.Config{Email: &channel.Email{SMTP: s.Addr, From: "alerts@example.com", Username: "mailer",
		TLSConf: channel.TLS{CAFile: s.CAFile}}}, d, opts())
	class(t, ch["email"].Send(context.Background(), message("x")), escalate.FailConfig)
	if s.AuthTried() {
		t.Fatal("dialed")
	}
	d.Secrets["email.password"] = ""
	ch, _ = channel.Build(&channel.Config{Email: &channel.Email{SMTP: s.Addr, From: "alerts@example.com", Username: "mailer",
		TLSConf: channel.TLS{CAFile: s.CAFile}}}, d, opts())
	class(t, ch["email"].Send(context.Background(), message("x")), escalate.FailConfig)
}

func provider(t *testing.T, o channel.Options, mut func(*channel.Config)) (map[string]escalate.Channel, *channeltest.Provider) {
	t.Helper()
	p := channeltest.NewProvider(t)
	tl := channel.TLS{CAFile: p.CAFile}
	c := &channel.Config{
		SMS:   &channel.Provider{URL: p.URL, Account: "AC0000example", From: "+15550100", TLSConf: tl},
		Voice: &channel.Provider{URL: p.URL, Account: "AC0000example", From: "+15550101", TLSConf: tl},
	}
	if mut != nil {
		mut(c)
	}
	ch, err := channel.Build(c, dir(), o)
	if err != nil {
		t.Fatal(err)
	}
	return ch, p
}

func TestSMSAndVoice(t *testing.T) {
	ch, p := provider(t, opts(), nil)
	if err := ch["sms"].Send(context.Background(), message("Tank <high> & more")); err != nil {
		t.Fatal(err)
	}
	if err := ch["voice"].Send(context.Background(), message("Tank <high> & more")); err != nil {
		t.Fatal(err)
	}
	r := p.Requests()
	if len(r) != 2 {
		t.Fatalf("requests = %+v", r)
	}
	sms, voice := r[0], r[1]
	if sms.Kind != "sms" || sms.Account != "AC0000example" || sms.User != "AC0000example" || sms.Token != "tok-sms" ||
		sms.To != alicePhone || sms.From != "+15550100" || !strings.Contains(sms.Body, "[HIGH] Tank <high> & more") {
		t.Fatalf("sms = %+v", sms)
	}
	if strings.Contains(sms.Body, "\n") || len(sms.Body) > 300 {
		t.Fatalf("sms body = %q", sms.Body)
	}
	if voice.Kind != "voice" || voice.Token != "tok-voice" || voice.To != alicePhone || voice.From != "+15550101" {
		t.Fatalf("voice = %+v", voice)
	}
	want := `<Response><Say loop="2">Alarm. Priority high. Tank &lt;high&gt; &amp; more. Escalation level 2. Acknowledge it in the HMI.</Say></Response>`
	if voice.Twiml != want {
		t.Fatalf("twiml = %q", voice.Twiml)
	}
}

func TestProviderUntrustedCert(t *testing.T) {
	ch, p := provider(t, opts(), func(c *channel.Config) { c.SMS.TLSConf.CAFile = "" })
	err := ch["sms"].Send(context.Background(), message("x"))
	class(t, err, escalate.FailTLS)
	noLeak(t, err, "AC0000", "https", alicePhone)
	if p.Hits() != 0 {
		t.Fatal("request went through an unverified connection")
	}
}

func TestProviderAuthNotRetried(t *testing.T) {
	ch, p := provider(t, channel.Options{Timeout: time.Second}, nil)
	p.SetStatus(401, "bad creds")
	se := class(t, ch["sms"].Send(context.Background(), message("x")), escalate.FailAuth)
	if se.Code != 401 || se.Proto != "http" {
		t.Fatalf("%+v", se)
	}
	if p.Hits() != 1 {
		t.Fatalf("hits = %d, an auth failure was retried", p.Hits())
	}
}

func TestProviderRejectedNoLeak(t *testing.T) {
	ch, p := provider(t, channel.Options{Timeout: time.Second}, nil)
	p.SetStatus(400, `{"message":"The number `+alicePhone+` is not valid","account":"AC0000example"}`)
	err := ch["sms"].Send(context.Background(), message("x"))
	class(t, err, escalate.FailRejected)
	noLeak(t, err, alicePhone, "AC0000", "not valid", "127.0.0.1")
	if p.Hits() != 1 {
		t.Fatalf("hits = %d", p.Hits())
	}
}

// A provider's 5xx is transient (the dispatcher retries it, asking the
// gate again first); the channel itself makes one attempt.
func TestProviderServerErrorIsTransient(t *testing.T) {
	ch, p := provider(t, channel.Options{Timeout: time.Second}, nil)
	p.SetStatus(500, alicePhone)
	err := ch["voice"].Send(context.Background(), message("x"))
	class(t, err, escalate.FailProvider)
	noLeak(t, err, alicePhone)
	if p.Hits() != 1 {
		t.Fatalf("hits = %d, want one attempt: the dispatcher retries", p.Hits())
	}
}

func TestProviderRedirectNotFollowed(t *testing.T) {
	var other atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other.Add(1) }))
	defer evil.Close()
	ch, p := provider(t, channel.Options{Timeout: time.Second}, nil)
	p.SetRedirect(evil.URL)
	err := ch["sms"].Send(context.Background(), message("x"))
	se := class(t, err, escalate.FailRejected)
	if se.Code != 302 {
		t.Fatalf("code = %d", se.Code)
	}
	if other.Load() != 0 {
		t.Fatal("redirect followed")
	}
	if p.Hits() != 1 {
		t.Fatalf("hits = %d", p.Hits())
	}
}

func TestProviderNeedsToken(t *testing.T) {
	p := channeltest.NewProvider(t)
	d := dir()
	delete(d.Secrets, "sms.token")
	ch, _ := channel.Build(&channel.Config{SMS: &channel.Provider{URL: p.URL, Account: "AC1", From: "+15550100",
		TLSConf: channel.TLS{CAFile: p.CAFile}}}, d, opts())
	class(t, ch["sms"].Send(context.Background(), message("x")), escalate.FailConfig)
	if p.Hits() != 0 {
		t.Fatal("dialed")
	}
	m := message("x")
	m.To = "bob"
	ch, _ = channel.Build(&channel.Config{SMS: &channel.Provider{URL: p.URL, Account: "AC1", From: "+15550100",
		TLSConf: channel.TLS{CAFile: p.CAFile}}}, dir(), opts())
	class(t, ch["sms"].Send(context.Background(), m), escalate.FailNoContact)
}

func TestWebhook(t *testing.T) {
	h := channeltest.NewWebhook(t)
	c := &channel.Config{Webhook: &channel.Webhook{URL: h.URL, Header: "Authorization", TLSConf: channel.TLS{CAFile: h.CAFile}}}
	ch, err := channel.Build(c, dir(), opts())
	if err != nil {
		t.Fatal(err)
	}
	m := message("Tank high")
	m.Repeat, m.Reason = 1, "joined late"
	if err := ch["webhook"].Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	hk := h.Hooks()
	if len(hk) != 1 {
		t.Fatal("no hook")
	}
	if got := hk[0].Header.Get("Authorization"); got != "Bearer hook-secret" {
		t.Fatalf("header = %q", got)
	}
	var b struct {
		Alarm  map[string]any `json:"alarm"`
		Policy string
		Level  int
		Repeat int
		To     string
		Reason string
	}
	if err := json.Unmarshal(hk[0].Body, &b); err != nil {
		t.Fatal(err)
	}
	if b.To != "alice" || b.Policy != "primary" || b.Level != 2 || b.Repeat != 1 || b.Reason != "joined late" ||
		b.Alarm["id"] != "a1" || b.Alarm["name"] != "Tank high" || b.Alarm["priority"] != "high" || b.Alarm["site"] != "plant" ||
		b.Alarm["area"] != "north" || b.Alarm["class"] != "process" || b.Alarm["state"] == "" || b.Alarm["activeMs"] != float64(1700000000000) {
		t.Fatalf("body = %s", hk[0].Body)
	}
	if strings.Contains(string(hk[0].Body), "@") || strings.Contains(string(hk[0].Body), alicePhone) {
		t.Fatalf("body carries an address: %s", hk[0].Body)
	}
}

func TestWebhookNoContactNeeded(t *testing.T) {
	h := channeltest.NewWebhook(t)
	ch, _ := channel.Build(&channel.Config{Webhook: &channel.Webhook{URL: h.URL, TLSConf: channel.TLS{CAFile: h.CAFile}}}, dir(), opts())
	m := message("x")
	m.To = "nobody-with-contacts"
	if err := ch["webhook"].Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if got := h.Hooks()[0].Header.Get("Authorization"); got != "" {
		t.Fatalf("header without header: = %q", got)
	}
}

func TestWebhookMissingSecretAndStatus(t *testing.T) {
	h := channeltest.NewWebhook(t)
	d := dir()
	delete(d.Secrets, "webhook.header")
	c := &channel.Config{Webhook: &channel.Webhook{URL: h.URL, Header: "X-Key", TLSConf: channel.TLS{CAFile: h.CAFile}}}
	ch, _ := channel.Build(c, d, opts())
	class(t, ch["webhook"].Send(context.Background(), message("x")), escalate.FailConfig)
	if len(h.Hooks()) != 0 {
		t.Fatal("sent without its secret")
	}
	ch, _ = channel.Build(c, dir(), opts())
	h.SetStatus(403)
	class(t, ch["webhook"].Send(context.Background(), message("x")), escalate.FailAuth)
}

func TestWebhookPlaintextWithHeaderRefused(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	// Build's Validate refuses this; a plaintext webhook with no header and
	// insecure: true is the one that goes out.
	_, err := channel.Build(&channel.Config{Webhook: &channel.Webhook{URL: srv.URL, Header: "X-Key", TLSConf: channel.TLS{Insecure: true}}}, dir(), opts())
	if err == nil {
		t.Fatal("plaintext + header accepted")
	}
	ch, err := channel.Build(&channel.Config{Webhook: &channel.Webhook{URL: srv.URL, TLSConf: channel.TLS{Insecure: true}}}, dir(), opts())
	if err != nil {
		t.Fatal(err)
	}
	if err := ch["webhook"].Send(context.Background(), message("x")); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatal("plaintext insecure webhook not sent")
	}
}

func TestBuildCAFile(t *testing.T) {
	for _, tc := range []struct {
		key string
		c   channel.Config
	}{
		{"alarms.channels.email.ca-file", channel.Config{Email: &channel.Email{SMTP: "mail.example.com:587", From: "a@example.com", TLSConf: channel.TLS{CAFile: "/nonexistent/ca.pem"}}}},
		{"alarms.channels.sms.ca-file", channel.Config{SMS: &channel.Provider{URL: "https://api.example.com", Account: "AC1", From: "+15550100", TLSConf: channel.TLS{CAFile: "/nonexistent/ca.pem"}}}},
		{"alarms.channels.webhook.ca-file", channel.Config{Webhook: &channel.Webhook{URL: "https://hook.example.com", TLSConf: channel.TLS{CAFile: "/nonexistent/ca.pem"}}}},
	} {
		_, err := channel.Build(&tc.c, dir(), opts())
		if err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s: err = %v", tc.key, err)
		}
	}
	junk := filepath.Join(t.TempDir(), "junk.pem")
	_ = os.WriteFile(junk, []byte("not a cert"), 0o600)
	_, err := channel.Build(&channel.Config{Webhook: &channel.Webhook{URL: "https://hook.example.com", TLSConf: channel.TLS{CAFile: junk}}}, dir(), opts())
	if err == nil || !strings.Contains(err.Error(), "alarms.channels.webhook.ca-file") {
		t.Errorf("junk ca: %v", err)
	}
}

func TestBuildKinds(t *testing.T) {
	ch, err := channel.Build(nil, dir(), opts())
	if err != nil || len(ch) != 0 {
		t.Fatalf("%v %v", ch, err)
	}
	ch, err = channel.Build(&channel.Config{Webhook: &channel.Webhook{URL: "https://hook.example.com"}}, dir(), opts())
	if err != nil || len(ch) != 1 || ch["webhook"] == nil {
		t.Fatalf("%v %v", ch, err)
	}
}

func TestUnreachableAndRetry(t *testing.T) {
	// A closed port: unreachable (the dispatcher retries it).
	h := channeltest.NewWebhook(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c := &channel.Config{Webhook: &channel.Webhook{URL: "https://" + addr, TLSConf: channel.TLS{CAFile: h.CAFile}}}
	ch, err := channel.Build(c, dir(), channel.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e := ch["webhook"].Send(context.Background(), message("x"))
	class(t, e, escalate.FailUnreachable)
	noLeak(t, e, "127.0.0.1", "https")
}

// A scheme in capitals is still https: Validate parses the URL, and so
// does the sender, so a manifest that checks clean also pages.
func TestProviderSchemeCase(t *testing.T) {
	ch, p := provider(t, opts(), func(c *channel.Config) {
		c.SMS.URL = "HTTPS://" + strings.TrimPrefix(c.SMS.URL, "https://")
	})
	if err := ch["sms"].Send(context.Background(), message("x")); err != nil {
		t.Fatalf("HTTPS:// provider: %v", err)
	}
	if p.Hits() != 1 {
		t.Fatalf("hits = %d", p.Hits())
	}
}
