// trust.go pins each server's certificate on first use. gopcua v0.9.1 has
// no trust list: the server certificate it encrypts to and verifies
// against comes from the GetEndpoints reply, which is not authenticated.
// Without a pin, a machine in the middle could substitute its own and read
// the session, credentials included. The first secured connection to a
// server records its certificate's SHA-256 thumbprint in pki-dir; every
// later dial refuses a different certificate until the pin is removed by
// hand. Trust on first use: the first connection is trusted as found.
package opcua

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// pinDir is the subdirectory of pki-dir holding one file per server.
const pinDir = "trusted-servers"

// PinFile is where the thumbprint for endpoint's server is kept: one file
// per host:port under <pki-dir>/trusted-servers.
func PinFile(pkiDir, endpoint string) string {
	host := endpoint
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	b := []byte(strings.ToLower(host))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			b[i] = '_'
		}
	}
	return filepath.Join(pkiDir, pinDir, string(b)+".sha256")
}

// thumbprint is the hex SHA-256 of a DER certificate.
func thumbprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// checkPin compares the server certificate offered for endpoint against
// the pin. pinned is false when there is no pin yet (the caller records
// one once the session is up); a different certificate is an error naming
// both thumbprints and the file to delete to trust the new one.
func checkPin(pkiDir, endpoint string, der []byte) (pinned bool, err error) {
	if len(der) == 0 {
		return false, fmt.Errorf("the server offered no certificate for a secured endpoint")
	}
	file := PinFile(pkiDir, endpoint)
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	want := string(bytes.TrimSpace(firstLine(raw)))
	if got := thumbprint(der); got != want {
		return true, fmt.Errorf("server certificate changed: pinned sha256 %s, offered %s — refused. "+
			"If the server's certificate was replaced on purpose, delete %s to trust the new one", want, got, file)
	}
	return true, nil
}

// recordPin writes the pin for a certificate first seen on endpoint.
func recordPin(pkiDir, endpoint string, der []byte) error {
	file := PinFile(pkiDir, endpoint)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	body := thumbprint(der) + "\n# server certificate for " + endpoint + ", trusted on first use; delete to re-trust\n"
	return os.WriteFile(file, []byte(body), 0o600)
}

func firstLine(b []byte) []byte {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i]
	}
	return b
}
