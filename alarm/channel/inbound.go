package channel

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/url"
	"slices"
	"strings"
)

// SignatureHeader carries a Twilio-compatible provider's signature of a
// request it makes to the controller (an SMS reply, a keypress; #715).
const SignatureHeader = "X-Twilio-Signature"

// Signature is the provider's signature, under the account token, of a
// form POST to rawURL: base64(HMAC-SHA1(token, rawURL + each parameter's
// name and value, names sorted, a name's values sorted, duplicates
// once)), the provider's published scheme.
func Signature(token, rawURL string, form url.Values) string {
	return base64.StdEncoding.EncodeToString(signature(token, rawURL, signedParams(form)))
}

func signature(token, rawURL, params string) []byte {
	m := hmac.New(sha1.New, []byte(token))
	m.Write([]byte(rawURL))
	m.Write([]byte(params))
	return m.Sum(nil)
}

func signedParams(form url.Values) string {
	var params strings.Builder
	names := make([]string, 0, len(form))
	for k := range form {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		vs := slices.Clone(form[k])
		slices.Sort(vs)
		for _, v := range slices.Compact(vs) {
			params.WriteString(k)
			params.WriteString(v)
		}
	}
	return params.String()
}

// CheckSignature reports whether sig is Signature(token, rawURL, form).
//
// rawURL is the URL the controller is configured to be called at
// (alarms.oncall.inbound.url plus the path and query), never the request's
// Host: a proxy or a forged header cannot choose what is checked. The
// provider signs some requests with the URL's port and some without
// (urlSpellings), so each is tried.
func CheckSignature(token, rawURL string, form url.Values, sig string) bool {
	if token == "" || sig == "" {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(want) != sha1.Size {
		return false
	}
	params := signedParams(form)
	ok := false
	for _, u := range urlSpellings(rawURL) {
		// Every spelling is checked: the time does not say which matched.
		if hmac.Equal(signature(token, u, params), want) {
			ok = true
		}
	}
	return ok
}

// urlSpellings is rawURL as given, without its port, and with its
// scheme's default port when it names none: the spellings the provider's
// own validator accepts (a voice request over https is signed with the
// port dropped, any port; others keep it).
func urlSpellings(rawURL string) []string {
	out := []string{rawURL}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return out
	}
	add := func(host string) {
		alt := *u
		alt.Host = host
		if s := alt.String(); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	add(host)
	if u.Port() == "" {
		if def := map[string]string{"https": "443", "http": "80"}[u.Scheme]; def != "" {
			add(host + ":" + def)
		}
	}
	return out
}
