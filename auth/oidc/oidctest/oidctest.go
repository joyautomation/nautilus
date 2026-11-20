// Package oidctest is a fake OpenID Connect provider for tests (#708 PR
// 6): discovery, a JWKS, and a token endpoint that checks the client's
// secret, the redirect URI and the PKCE verifier, and signs ID tokens
// with the claims a test gives. Only test files import it.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// IdP is a fake provider on a loopback httptest server.
type IdP struct {
	URL      string // the issuer
	ClientID string
	Secret   string

	srv *httptest.Server
	key *rsa.PrivateKey

	mu sync.Mutex
	// BadKey signs ID tokens with a key the JWKS does not hold.
	BadKey bool
	// Tokens counts token requests answered.
	Tokens int
	codes  map[string]grant
	other  *rsa.PrivateKey
}

type grant struct {
	claims    map[string]any
	nonce     string
	redirect  string
	challenge string
}

// New starts a provider for one client; it stops when t ends.
func New(t testing.TB, clientID, secret string) *IdP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	o, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &IdP{ClientID: clientID, Secret: secret, key: k, other: o, codes: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /keys", p.keys)
	mux.HandleFunc("POST /token", p.token)
	p.srv = httptest.NewServer(mux)
	p.URL = p.srv.URL
	t.Cleanup(p.srv.Close)
	return p
}

// Close stops the provider: it is then unreachable.
func (p *IdP) Close() { p.srv.Close() }

func (p *IdP) discovery(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/keys",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *IdP) keys(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
}

// Authorize is a person signing in at the provider: authURL is where the
// controller sent the browser. It returns the URL the provider sends the
// browser back to (redirect_uri with code and state), and the ID token it
// will issue holds claims, over the defaults (iss, aud, exp, iat, the
// nonce from authURL, and sub "sub-1" when claims has none). A claim set
// to nil is left out.
func (p *IdP) Authorize(t testing.TB, authURL string, claims map[string]any) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorize: unexpected request %s", authURL)
	}
	code := randomString()
	p.mu.Lock()
	p.codes[code] = grant{claims: claims, nonce: q.Get("nonce"), redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge")}
	p.mu.Unlock()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	return back.String()
}

func (p *IdP) token(w http.ResponseWriter, r *http.Request) {
	refuse := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	if err := r.ParseForm(); err != nil {
		refuse("invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.Secret {
		refuse("invalid_client")
		return
	}
	p.mu.Lock()
	g, found := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.Tokens++
	bad := p.BadKey
	p.mu.Unlock()
	if !found || r.PostForm.Get("redirect_uri") != g.redirect {
		refuse("invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		refuse("invalid_grant")
		return
	}
	now := time.Now()
	c := map[string]any{"iss": p.URL, "aud": p.ClientID, "sub": "sub-1", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": g.nonce}
	for k, v := range g.claims {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	key := p.key
	if bad {
		key = p.other
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + randomString(), "token_type": "Bearer", "expires_in": 300, "id_token": sign(key, c)})
}

func sign(key *rsa.PrivateKey, claims map[string]any) string {
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		panic(err)
	}
	b, _ := json.Marshal(claims)
	obj, err := s.Sign(b)
	if err != nil {
		panic(err)
	}
	out, err := obj.CompactSerialize()
	if err != nil {
		panic(err)
	}
	return out
}

func randomString() string {
	var b [16]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
