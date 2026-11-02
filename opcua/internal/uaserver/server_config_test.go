package uaserver

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"
	"time"
)

// An explicit ApplicationURI option wins over the URI a Certificate
// option reads from its certificate, in either order; with no explicit
// option the certificate's URI is the ApplicationURI. gopcua's options
// were last-one-wins, so a Certificate given after ApplicationURI
// silently replaced it.
func TestApplicationURIOptionWinsOverTheCertificate(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certURI, _ := url.Parse("urn:cert:uri")
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{certURI},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		opts []Option
		want string
	}{
		"certificate only":          {[]Option{Certificate(der)}, "urn:cert:uri"},
		"explicit then certificate": {[]Option{ApplicationURI("urn:explicit"), Certificate(der)}, "urn:explicit"},
		"certificate then explicit": {[]Option{Certificate(der), ApplicationURI("urn:explicit")}, "urn:explicit"},
	} {
		cfg := &serverConfig{}
		for _, o := range tc.opts {
			o(cfg)
		}
		if cfg.applicationURI != tc.want {
			t.Errorf("%s: applicationURI %q, want %q", name, cfg.applicationURI, tc.want)
		}
	}
}
