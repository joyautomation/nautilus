package node

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// The box a controller's voter state belongs to (#1029). A cloned disk
// (or VM) carries a copy of the original's voter: four voters under three
// names, each copy of the original granting itself, and any single
// partition of the original from one voter then elects the clone beside
// it. So a controller's state file records the box that wrote it
// (Options.Box), and a controller started on another box treats the file
// as lost: it starts uncommissioned and re-adopts the pair's witness
// incarnation from its peer (#976), which a real box change (a NIC swap,
// a migration) does at once and a clone never does (the peer's Adopt goes
// to the original's address).
//
// The fingerprint is the machine-id and the MACs of the interfaces that
// carry the pair's links, hashed separately, so the event says which
// changed. Either one changed is a changed box: a cloned VM usually keeps
// its machine-id and gets a new MAC.

// BoxFingerprint is a controller's Options.Box: machineID, and the MACs of
// the interfaces its peer and witness links leave by (any order).
func BoxFingerprint(machineID string, macs []string) string {
	m := slices.Clone(macs)
	slices.Sort(m)
	m = slices.Compact(m)
	return "m=" + short(strings.TrimSpace(machineID)) + " n=" + short(strings.Join(m, ","))
}

func short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// boxChanges names what differs between the box a file was written on and
// this one: "machine-id", "peer-link MAC", or both. Nil when either is
// unknown ("") or they match.
func boxChanges(written, here string) []string {
	if written == "" || here == "" || written == here || mut("box_unchecked") {
		return nil
	}
	part := func(fp, key string) string {
		for _, f := range strings.Fields(fp) {
			if v, ok := strings.CutPrefix(f, key+"="); ok {
				return v
			}
		}
		return ""
	}
	var out []string
	if part(written, "m") != part(here, "m") {
		out = append(out, "machine-id")
	}
	if part(written, "n") != part(here, "n") {
		out = append(out, "peer-link MAC")
	}
	if out == nil {
		out = []string{"fingerprint"}
	}
	return out
}
