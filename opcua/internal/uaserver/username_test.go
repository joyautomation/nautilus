package uaserver

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// secret builds a UserName secret as a client does (gopcua's
// EncryptUserPassword before it encrypts): length, password, nonce.
func secret(pw, nonce []byte, lie int) []byte {
	out := make([]byte, 4)
	binary.LittleEndian.PutUint32(out, uint32(len(pw)+len(nonce)+lie))
	out = append(out, pw...)
	return append(out, nonce...)
}

// A UserName token's secret is accepted only whole and only bound to the
// session's current server nonce: a token replayed from an earlier
// activation (its nonce is the one before), a truncated secret, or a
// length that runs past the bytes is Bad_IdentityTokenInvalid, never a
// password.
func TestPasswordSecretIsBoundToTheNonce(t *testing.T) {
	nonce := bytes.Repeat([]byte{7}, 32)
	old := bytes.Repeat([]byte{6}, 32)
	pw := []byte("password-jsmith")
	if got, ok := passwordSecret(secret(pw, nonce, 0), nonce); !ok || !bytes.Equal(got, pw) {
		t.Fatalf("a good secret: %q %v", got, ok)
	}
	// Padding after the counted bytes (a block cipher's) is ignored.
	if got, ok := passwordSecret(append(secret(pw, nonce, 0), 0, 0, 0), nonce); !ok || !bytes.Equal(got, pw) {
		t.Errorf("a padded secret: %q %v", got, ok)
	}
	if got, ok := passwordSecret(secret(nil, nonce, 0), nonce); !ok || len(got) != 0 {
		t.Errorf("an empty password: %q %v", got, ok)
	}
	for _, c := range []struct {
		what  string
		plain []byte
		nonce []byte
	}{
		{"replayed (an earlier nonce)", secret(pw, old, 0), nonce},
		{"no nonce at the server", secret(pw, nonce, 0), nil},
		{"length past the bytes", secret(pw, nonce, 1), nonce},
		{"length shorter than the nonce", secret(nil, nonce, -1), nonce},
		{"too short to hold a length", []byte{1, 0}, nonce},
		{"the nonce cut off", secret(pw, nonce, 0)[:4+len(pw)+10], nonce},
	} {
		if got, ok := passwordSecret(c.plain, c.nonce); ok {
			t.Errorf("%s: accepted, password %q", c.what, got)
		}
	}
}
