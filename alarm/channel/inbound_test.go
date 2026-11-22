package channel

import (
	"net/url"
	"testing"
)

// The provider's published vectors (Twilio's security docs and its
// Python helper's tests), token "12345".
func TestCheckSignatureVectors(t *testing.T) {
	docs := url.Values{
		"CallSid": {"CA1234567890ABCDE"}, "Caller": {"+14158675310"}, "Digits": {"1234"},
		"From": {"+14158675310"}, "To": {"+18005551212"},
	}
	if !CheckSignature("12345", "https://example.com/myapp.php?foo=1&bar=2", docs, "L/OH5YylLD5NRKLltdqwSvS0BnU=") {
		t.Fatal("docs vector refused")
	}
	py := url.Values{
		"CallSid": {"CA1234567890ABCDE"}, "Digits": {"1234"}, "From": {"+14158675309"},
		"To": {"+18005551212"}, "Caller": {"+14158675309"},
	}
	const pyURL = "https://mycompany.com/myapp.php?foo=1&bar=2"
	if !CheckSignature("12345", pyURL, py, "RSOYDt4T1cUTdK1PDd93/VVr8B8=") {
		t.Fatal("helper vector refused")
	}
	// Names sorted, a name's values sorted, duplicates once.
	dup := url.Values{"Sid": {"CA123"}, "SidAccount": {"AC123"}, "Digits": {"5678", "1234", "1234"}}
	if !CheckSignature("12345", pyURL, dup, "IK+Dwps556ElfBT0I3Rgjkr1wJU=") {
		t.Fatal("duplicates vector refused")
	}
	// The port either way, as the provider's own validator: a URL
	// configured with :443 or any other port takes a signature over the
	// URL without it (a voice request over https is signed so)...
	for _, u := range []string{"https://mycompany.com:443/myapp.php?foo=1&bar=2", "https://mycompany.com:1234/myapp.php?foo=1&bar=2"} {
		if !CheckSignature("12345", u, py, "RSOYDt4T1cUTdK1PDd93/VVr8B8=") {
			t.Fatalf("%s refused the portless signature", u)
		}
	}
	// ...and a URL configured without one takes a signature over it with
	// the scheme's default port.
	sig443 := Signature("12345", "https://mycompany.com:443/myapp.php?foo=1&bar=2", py)
	if !CheckSignature("12345", pyURL, py, sig443) {
		t.Fatal("a signature with :443 refused for the portless URL")
	}
	if got := Signature("12345", pyURL, py); got != "RSOYDt4T1cUTdK1PDd93/VVr8B8=" {
		t.Fatalf("Signature = %s", got)
	}
}

func TestCheckSignatureRefuses(t *testing.T) {
	py := url.Values{
		"CallSid": {"CA1234567890ABCDE"}, "Digits": {"1234"}, "From": {"+14158675309"},
		"To": {"+18005551212"}, "Caller": {"+14158675309"},
	}
	const u, sig = "https://mycompany.com/myapp.php?foo=1&bar=2", "RSOYDt4T1cUTdK1PDd93/VVr8B8="
	changed := url.Values{}
	for k, v := range py {
		changed[k] = v
	}
	changed["Digits"] = []string{"1"}
	for name, ok := range map[string]bool{
		"other token":                  CheckSignature("12346", u, py, sig),
		"no token":                     CheckSignature("", u, py, sig),
		"other url":                    CheckSignature("12345", "https://mycompany.com/other.php?foo=1&bar=2", py, sig),
		"other host":                   CheckSignature("12345", "https://evil.example/myapp.php?foo=1&bar=2", py, sig),
		"other port":                   CheckSignature("12345", "https://mycompany.com:9443/myapp.php?foo=1&bar=2", py, Signature("12345", "https://mycompany.com:8443/myapp.php?foo=1&bar=2", py)),
		"port 443 for a port 8443 url": CheckSignature("12345", "https://mycompany.com:8443/myapp.php?foo=1&bar=2", py, Signature("12345", "https://mycompany.com:443/myapp.php?foo=1&bar=2", py)),
		"other param":                  CheckSignature("12345", u, changed, sig),
		"no signature":                 CheckSignature("12345", u, py, ""),
		"not base64":                   CheckSignature("12345", u, py, "!!!"),
		"short":                        CheckSignature("12345", u, py, "AAAA"),
	} {
		if ok {
			t.Errorf("%s: accepted", name)
		}
	}
}
