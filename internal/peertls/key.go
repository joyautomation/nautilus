package peertls

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Box is the box a key belongs to (#710 per-box keys): its machine-id and
// the MACs of its physical NICs (a bridge, a VLAN, a bond, a tun, a veth
// or docker's are not physical). A box with no physical NIC (a container)
// gives the MACs of the interfaces its links leave by instead.
//
// A key file is this box's while the machine-id matches and at least one
// NIC it was made with is still here. Adding anything (docker, a bridge, a
// USB NIC for maintenance, a VPN) keeps the key, and so does moving a NIC
// into a bridge; a disk cloned onto other hardware, or a cloned VM with a
// new MAC, sees none of its NICs and makes a new key, which the other
// members refuse until an operator unpins its name. So does replacing a
// box's only NIC: a real change of box.
type Box struct {
	MachineID string
	NICs      []string
}

// Known reports whether the box could be read: a Box that is not known
// yet never makes or replaces a key.
func (b Box) Known() bool { return strings.TrimSpace(b.MachineID) != "" }

// boxFile is the box as a key file records it: hashed, so the file holds
// no machine-id or MAC.
type boxFile struct {
	MachineID string   `json:"machineId"`
	NICs      []string `json:"nics,omitempty"`
}

func hashed(b Box) boxFile {
	f := boxFile{MachineID: hash12(strings.TrimSpace(b.MachineID))}
	for _, n := range b.NICs {
		f.NICs = append(f.NICs, hash12(strings.ToLower(strings.TrimSpace(n))))
	}
	slices.Sort(f.NICs)
	f.NICs = slices.Compact(f.NICs)
	return f
}

func hash12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// sameBox reports whether a key written on box written is this box's.
func sameBox(written, here boxFile) bool {
	if mut("box_key_unbound") {
		return true
	}
	if written.MachineID != here.MachineID {
		return false
	}
	if len(written.NICs) == 0 {
		return true // made where no NIC could be read: the machine-id is all there was
	}
	for _, n := range written.NICs {
		if slices.Contains(here.NICs, n) {
			return true
		}
	}
	return false
}

// keyFile is the box key's file.
type keyFile struct {
	V    int     `json:"v"`
	Seed string  `json:"seed"` // base64 Ed25519 seed
	Box  boxFile `json:"box"`
}

// boxKey reads this box's key, or makes one: at the first start, when the
// file was written on another box (moved aside), or when it can't be read
// (moved aside). Never while the box is unknown: the handshake waits.
func (p *Peer) boxKey() (ed25519.PrivateKey, error) {
	box, err := p.o.Box()
	if err != nil {
		return nil, fmt.Errorf("this box can't be read, so its peer key is not used yet: %w", err)
	}
	if !box.Known() && !mut("key_on_unknown_box") {
		return nil, errors.New("this box is not known yet (a peer or witness address doesn't resolve): its peer key is used once it is")
	}
	here := hashed(box)
	path := p.o.KeyFile
	b, err := os.ReadFile(path)
	why := "this box's first start with per-box keys"
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil && !mut("key_read_error_remakes"):
		// Not "no key": a key that exists and can't be read now (its
		// mode, an I/O error) is never replaced, which would lock this
		// box out of the pair until an unpin. The handshakes wait.
		return nil, fmt.Errorf("this box's peer key %s could not be read (%v): its handshakes wait until it can be; fix its owner and mode (0600, the user naut runs as), never delete it", path, err)
	case err != nil:
		why = p.moveAside(path, "unreadable", fmt.Sprintf("the key file could not be read (%v)", err))
	default:
		var f keyFile
		seed, derr := []byte(nil), json.Unmarshal(b, &f)
		if derr == nil {
			seed, derr = base64.StdEncoding.DecodeString(f.Seed)
		}
		switch {
		case derr == nil && f.V > 1 && !mut("key_newer_version_remakes"):
			// Written by a newer release (a downgrade): never replaced.
			return nil, fmt.Errorf("this box's peer key %s is of version %d, written by a newer release: this one keeps its hands off it and its handshakes wait; run the newer release", path, f.V)
		case derr != nil || f.V != 1 || len(seed) != ed25519.SeedSize:
			why = p.moveAside(path, "unreadable", "the key file is damaged")
		case len(f.Box.NICs) > 0 && len(here.NICs) == 0 && !mut("key_no_nics_other_box"):
			// None of the box's NICs reads yet (a boot before its
			// interfaces show): not known, never another box.
			return nil, errors.New("none of this box's network interfaces can be read yet: its peer key is used once one can")
		case !sameBox(f.Box, here):
			why = p.moveAside(path, "other-box", "the key file was written on another box (a cloned disk or VM, or this box's machine-id and every NIC it had changed)")
		default:
			return ed25519.NewKeyFromSeed(seed), nil
		}
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := crand.Read(seed); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(keyFile{V: 1, Seed: base64.StdEncoding.EncodeToString(seed), Box: here}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if err := writeFileSync(path, append(out, '\n')); err != nil {
		return nil, fmt.Errorf("writing this box's peer key: %w", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	p.event(Event{Kind: EventNewKey, Name: p.o.Self, Fingerprint: Fingerprint(priv.Public().(ed25519.PublicKey)),
		Detail: why + ": the other members pin it at their first contact, or refuse it where another key is pinned for " + p.o.Self + " (naut redundancy unpin " + p.o.Self + " on the surviving controller, if this box really changed)"})
	return priv, nil
}

// moveAside keeps a key file that is not used, and says why.
func (p *Peer) moveAside(path, tag, why string) string {
	aside := path + "." + tag + "-" + strconv.FormatInt(p.o.Now().Unix(), 10)
	if err := os.Rename(path, aside); err != nil {
		return why + "; it could not be moved aside (" + err.Error() + ") and is replaced"
	}
	return why + "; kept at " + aside
}
