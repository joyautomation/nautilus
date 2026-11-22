package channel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/joyautomation/nautilus/alarm/escalate"
)

// newHTTPClient never follows a redirect: a 3xx is a refusal, and the
// credential never leaves for a host the manifest did not name.
func newHTTPClient(cfg *tls.Config) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyFromEnvironment,
			TLSClientConfig:   cfg,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// post sends body and reduces the outcome to a SendError: the status
// decides the class, the response body is read (bounded) and dropped.
func post(ctx context.Context, c *http.Client, rawURL string, hdr http.Header, user, token string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return escalate.Failed(escalate.FailConfig)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if user != "" {
		req.SetBasicAuth(user, token)
	}
	resp, err := c.Do(req)
	if err != nil {
		// err is a *url.Error carrying the URL: only its class survives.
		return escalate.Failed(classify(ctx, err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return statusError(resp.StatusCode)
}

func statusError(code int) error {
	se := &escalate.SendError{Code: code, Proto: "http"}
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == 401 || code == 403:
		se.Class = escalate.FailAuth
	case code == 408 || code == 429:
		// A timeout or a rate limit at the provider: worth another try.
		se.Class = escalate.FailProvider
	case code >= 300 && code < 500:
		se.Class = escalate.FailRejected
	case code >= 500:
		se.Class = escalate.FailProvider
	default:
		se.Class = escalate.FailUnknown
	}
	return se
}

type providerSender struct {
	kind   string
	cfg    Provider
	secret string
	dir    Directory
	client *http.Client
}

func newProviderSender(kind string, p Provider, secretKey string, dir Directory, pool *x509.CertPool) *providerSender {
	// No ServerName: the transport sets it per connection, to the
	// provider's host, or to the proxy's for an https proxy hop.
	return &providerSender{kind: kind, cfg: p, secret: secretKey, dir: dir,
		client: newHTTPClient(tlsConfig("", p.TLSConf, pool))}
}

func (p *providerSender) attempt(ctx context.Context, m escalate.Message) error {
	to, err := address(p.dir, m.To, p.kind)
	if err != nil {
		return err
	}
	if ValidPhone(to) != nil {
		return escalate.Failed(escalate.FailNoContact)
	}
	token, err := secret(p.dir, p.secret)
	if err != nil {
		return err
	}
	if u, err := url.Parse(p.cfg.URL); err != nil || u.Scheme != "https" {
		return escalate.Failed(escalate.FailTLS) // the token never rides plaintext
	}
	endpoint, form := "Messages.json", url.Values{"To": {to}, "From": {p.cfg.From}}
	if p.kind == KindVoice {
		endpoint = "Calls.json"
		var esc bytes.Buffer
		_ = xml.EscapeText(&esc, []byte(voiceText(m)))
		form.Set("Twiml", `<Response><Say loop="2">`+esc.String()+`</Say></Response>`)
	} else {
		form.Set("Body", smsText(m))
	}
	u := strings.TrimRight(p.cfg.URL, "/") + "/2010-04-01/Accounts/" + url.PathEscape(p.cfg.Account) + "/" + endpoint
	hdr := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	return post(ctx, p.client, u, hdr, p.cfg.Account, token, []byte(form.Encode()))
}
