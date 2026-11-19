package users

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id's cost parameters. Each hash carries its own, so
// raising Default later leaves every stored hash verifiable.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// Default is RFC 9106's second recommended option: 64 MiB, three passes,
// four lanes. A sign-in costs that much memory for about a tenth of a
// second, which is why sign-ins are rate-limited (#708 PR 5b).
var Default = Params{Memory: 64 * 1024, Time: 3, Threads: 4}

const (
	saltLen = 16
	keyLen  = 32
)

var b64 = base64.RawStdEncoding

// HashPassword is password's argon2id hash in the PHC string format:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>.
func HashPassword(password string, p Params) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// phc is a parsed hash.
type phc struct {
	p    Params
	salt []byte
	key  []byte
}

// maxMemory bounds what a hash read from the file may ask for (4 GiB), so
// a hand-edited file cannot make one sign-in take the controller's memory.
const maxMemory = 4 * 1024 * 1024

func parsePHC(s string) (phc, error) {
	bad := errors.New("not an argon2id hash ($argon2id$v=19$m=…,t=…,p=…$salt$hash)")
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return phc{}, bad
	}
	var h phc
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.p.Memory, &h.p.Time, &h.p.Threads); err != nil || n != 3 {
		return phc{}, bad
	}
	if h.p.Memory < 8*uint32(h.p.Threads) || h.p.Memory > maxMemory || h.p.Time < 1 || h.p.Threads < 1 {
		return phc{}, fmt.Errorf("argon2id parameters out of range: %s", parts[3])
	}
	var err error
	if h.salt, err = b64.DecodeString(parts[4]); err != nil || len(h.salt) < 8 {
		return phc{}, bad
	}
	if h.key, err = b64.DecodeString(parts[5]); err != nil || len(h.key) < 16 {
		return phc{}, bad
	}
	return h, nil
}

// verify reports whether password matches hash, in time that does not
// depend on where they differ.
func verify(hash, password string) bool {
	h, err := parsePHC(hash)
	if err != nil {
		return false
	}
	key := argon2.IDKey([]byte(password), h.salt, h.p.Time, h.p.Memory, h.p.Threads, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(key, h.key) == 1
}

// dummyHash is verified against when the user does not exist or is
// disabled, so a refusal takes as long as a wrong password and the time
// taken does not say which names exist. Made on first use: every naut
// command imports this package, and few of them check a password.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword("nautilus-no-such-user", Default)
	if err != nil {
		panic(err)
	}
	return h
})
