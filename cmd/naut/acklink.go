package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/internal/pairkey"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/securefile"
	"github.com/joyautomation/nautilus/server"
)

// AckLinkKeyName is the file, in the controller's state directory, that
// holds a controller's own ack-link key when it is not half of a pair
// (#715): 32 random bytes, hex, mode 0600, made at the first start that
// needs it. A pair derives its key from the peer token instead
// (internal/pairkey), so both members check each other's links.
const AckLinkKeyName = "alarm-link.key"

// minPeerToken is the shortest peer token naut run derives ack links
// from without a warning.
const minPeerToken = 32

// linkKinds are the channel kinds whose pages carry an ack link: what a
// person reads and can tap. A voice call cannot (3b-2 answers by
// keypress); a webhook goes to a system, not a person, and the link would
// let that system act as them. Push mints its own (server.AckLinkURL).
var linkKinds = map[string]bool{channel.KindEmail: true, channel.KindSMS: true}

// ackLinkKeyring is the keyring for the project's ack links, or nil when
// it sends none (no alarms.oncall.ack-url). peerToken, when set, is the
// pair's (NAUTILUS_PEER_TOKEN): the key is derived from it. Else the key
// is the state directory's AckLinkKeyName, made when absent.
func ackLinkKeyring(proj *project.Project, stateDir, peerToken string, stderr io.Writer) (*acklink.Keyring, error) {
	if proj.Alarms == nil || proj.Alarms.Oncall.AckURL == "" || len(proj.Alarms.Channels.Kinds()) == 0 {
		return nil, nil // no link to send, or nothing that pages a person
	}
	if proj.Alarms.Oncall.AckFrom() == escalate.AckBySignIn && !signInPossible(proj) {
		return nil, errors.New("alarms.oncall.ack-from-notification: sign-in, but nobody can sign in here (no auth: users or auth.oidc): add users (naut users add) or use the default (link)")
	}
	if peerToken != "" {
		if len(peerToken) < minPeerToken {
			// Every link carries a MAC under a key derived from the token,
			// so anyone holding a page can test guesses at it offline.
			fmt.Fprintf(stderr, "naut run: warning: NAUTILUS_PEER_TOKEN is %d characters: ack links let anyone who holds a page test guesses at it offline; use a random token of at least %d (naut redundancy new-pair makes one)\n", len(peerToken), minPeerToken)
		}
		k, err := pairkey.Derive([]byte(peerToken), pairkey.AckLink)
		if err != nil {
			return nil, err
		}
		return acklink.New(k)
	}
	if proj.Runtime.Coordinator != nil {
		fmt.Fprintln(stderr, "naut run: warning: alarms.oncall.ack-url: this controller has no NAUTILUS_PEER_TOKEN, so its ack links are signed with its own key: a link it sends does not check on another replica after a takeover")
	}
	if stateDir == "" {
		return nil, errors.New("alarms.oncall.ack-url: the links' key lives in the controller's state directory, and this controller has none: set auth.state-dir, or run under systemd with StateDirectory=")
	}
	k, err := loadAckLinkKey(filepath.Join(stateDir, AckLinkKeyName))
	if err != nil {
		return nil, err
	}
	return acklink.New(k)
}

// loadAckLinkKey reads the key at path, making it first when absent. It
// reads only a plain file, mode 0600, that this process's user owns (a
// key another local user planted would let them forge any link). Two
// starts at once make one key: the file appears whole (a hard link of a
// finished temporary file) or not at all, and the second start reads the
// first's.
func loadAckLinkKey(path string) ([]byte, error) {
	b, err := readAckLinkKey(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := makeAckLinkKey(path); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		b, err = readAckLinkKey(path)
	}
	if err != nil {
		return nil, err
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(k) < acklink.MinKey {
		return nil, fmt.Errorf("%s does not hold an ack-link key: remove it and start again (links already sent stop working)", path)
	}
	return k, nil
}

// ackLinkEuid is os.Geteuid, a variable so a test can be another user.
var ackLinkEuid = os.Geteuid

// readAckLinkKey reads path after the mode and owner checks.
func readAckLinkKey(path string) ([]byte, error) {
	const noun, secret = "the ack-link key file", "the key that signs alarm acknowledgement links"
	if err := securefile.CheckMode(path, noun, secret); err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := securefile.CheckOwner(path, st, ackLinkEuid(), "naut run"); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// makeAckLinkKey writes a new random key to a temporary file in path's
// directory, then links it to path: os.ErrExist when another start made
// one first.
func makeAckLinkKey(path string) error {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".alarm-link-*.key")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(hex.EncodeToString(k) + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), path)
}

// ackLinkHook is the dispatcher's AckLink: the server mints (it holds the
// key and answers the link), for the kinds whose pages carry one.
func ackLinkHook(srv *atomic.Pointer[server.Server]) func(escalate.Message, string) string {
	return func(m escalate.Message, via string) string {
		s := srv.Load()
		// An alarm that needs no acknowledgement gets no link: it would
		// open a page that can only say there is nothing to acknowledge.
		if s == nil || !linkKinds[via] || !m.Alarm.AckRequired {
			return ""
		}
		return s.AckLinkURL(m.Alarm.ID, m.Alarm.Occurrence, m.To, via, m.Level)
	}
}

// ackLinkChecks are naut check's findings on alarms.oncall's ack links.
func ackLinkChecks(proj *project.Project) (errs, warns []string) {
	if proj.Alarms == nil || proj.Alarms.Oncall.AckURL == "" {
		return nil, nil
	}
	o := proj.Alarms.Oncall
	if o.AckURLInsecure {
		warns = append(warns, "alarms.oncall.ack-url-insecure: true: an ack link acknowledges as the person paged, and it crosses the network in plaintext; serve the HMI over https (server.tls) and drop the option")
	}
	if o.AckFrom() == escalate.AckBySignIn && !signInPossible(proj) {
		errs = append(errs, "alarms.oncall.ack-from-notification: sign-in, but nobody can sign in here (no auth: users or auth.oidc), so no link could ever acknowledge: add users (naut users add) or use the default (link)")
	}
	if u, err := url.Parse(o.AckURL); err == nil && u.Hostname() != "" && net.ParseIP(u.Hostname()) == nil &&
		u.Hostname() != "localhost" && !slices.ContainsFunc(proj.Server.AllowedHosts, func(h string) bool { return strings.EqualFold(h, u.Hostname()) }) {
		warns = append(warns, fmt.Sprintf("alarms.oncall.ack-url: the controller answers %s only if it is an allowed host: add it to server.allowed-hosts (or NAUTILUS_ALLOWED_HOSTS on the box), or a link opens a refusal", u.Hostname()))
	}
	if !slices.ContainsFunc(proj.Alarms.Channels.Kinds(), func(k string) bool { return linkKinds[k] || k == channel.KindPush }) {
		warns = append(warns, "alarms.oncall.ack-url is set, but no channel here carries a link (email, sms or push): no page will have one")
	}
	return errs, warns
}

// signInPossible reports whether anyone can sign in to the project's
// controller: local users or OIDC.
func signInPossible(proj *project.Project) bool {
	return proj.Auth != nil && (proj.Auth.HasUsers() || proj.Auth.OIDC())
}
