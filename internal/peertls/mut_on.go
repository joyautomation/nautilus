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
//   - peer_tls_client_any_server: a peer call accepts any server key.
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
