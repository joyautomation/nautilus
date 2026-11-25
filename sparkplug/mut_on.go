//go:build quorum_mut

package sparkplug

import "os"

// Mutations of the edge node under a redundant pair (#798), switched on
// like package quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - spark_publish_after_gate: the publish gate is always open, so a
//     session the gate closed on still births, samples and publishes
//     until its stop;
//   - spark_send_uncounted: a fenced publish is not counted for a
//     switchover's drain, so a drain can end with one still leaving;
//   - spark_bdseq_clash: a new session's bdSeq may be the peer's last;
//   - spark_death_watch_off: the node does not listen to its own NDEATH
//     topic, so another session's late death leaves it offline at a host
//     that matches deaths without their bdSeq.
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
