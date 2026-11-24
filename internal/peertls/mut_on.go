//go:build quorum_mut

package peertls

import "os"

// Mutations of peer TLS (#710), switched on like package quorum's (build
// tag quorum_mut, QUORUM_MUT=<name>; the names are in quorum.Mutations,
// whose init refuses an unknown one, and each has a row in
// tools/quorum-mutants):
//
//   - peer_tls_short_token: New takes a token under MinToken;
//   - peer_tls_server_any_client: a peer listener asks for no client
//     certificate, so anyone who reaches it gets a TLS session;
//   - peer_tls_client_any_server: a peer call accepts any server;
//   - pin_accepts_changed_key: a pinned name's other key is accepted;
//   - pin_unsigned_accepted: a certificate the token's key did not sign
//     is accepted;
//   - pin_any_name: a certificate naming another member than the one
//     dialed (or one a listener takes no calls from) is accepted;
//   - pins_unreadable_silent: an unreadable pins file is read as none,
//     with no event and not moved aside;
//   - pin_checked_once: Bind checks the pin only at a name's first
//     contact, not on every request;
//   - box_key_unbound: a key file written on another box is used;
//   - key_on_unknown_box: a key is made while the box is not known;
//   - key_read_error_remakes: a key file that exists but can't be read
//     is replaced by a new key;
//   - key_no_nics_other_box: a box whose NICs can't be read yet counts
//     as another box;
//   - key_newer_version_remakes: a key file of a newer version is
//     replaced;
//   - drop_key_repins: the key whose pin was dropped is pinned again (by
//     a connection kept from before the drop);
//   - refusals_undeduped: every refusal of one key is an event (a
//     replayed certificate floods the journal).
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
