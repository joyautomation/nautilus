//go:build quorum_mut

package server

import "os"

// Mutations of the pair's operator-intent wiring (#710), switched on like
// package quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - ack_clear_off: an acknowledged alarm back to normal proposes no
//     ack-clear, so a takeover carries the ack to a new occurrence;
//   - alarm_view_ignored: a synced takeover restores the replica's own
//     retain instead of the log's alarm view;
//   - engine_not_reset: a takeover restores the view over the engine's
//     frozen operator state instead of resetting it first;
//   - reconcile_before_restore: the log counts as live from the lease on,
//     before the takeover restored the view, so Reconcile clears every
//     carried ack against the unrestored engine (#1033 CI);
//   - cluster_ops_proxied: the pair's operator commands go through the
//     standby proxy like a write, so a standby that knows no leader
//     refuses them with a 503 (#1029 3c-3a);
//   - redundancy_same_origin: without an auth: block, the pair's operator
//     commands take the write rule (same-origin with no credential);
//   - retained_stale_ignored: a takeover restores a superseded log's
//     retained substitutes (#1029 3c-3c-2a);
//   - adopt_ignores_log: a late committed ack adopts the standing
//     occurrence though the log shows its own ended (#715);
//   - adopt_under_mu: Reconcile asks the oplog node under AlarmLog.mu
//     (the inverse of the apply's lock order);
//   - engine_event_waits: the engine's hook takes AlarmLog.mu on the
//     scan instead of handing off (#1053);
//   - clear_any_later_ack: a late hand-off clears an ack of a later
//     occurrence;
//   - clear_ack_taken_after: a late hand-off clears an ack the view took
//     after its event fired;
//   - engine_event_blocks: the engine's hook blocks when the hand-off
//     is full;
//   - clear_while_engine_holds: a late hand-off clears an ack the engine holds;
//   - clear_without_livemu: a late hand-off is judged without waiting out an in-flight Apply;
//   - reconcile_without_livemu: Reconcile reads the engine's acks and the view's without liveMu;
//   - ack_clear_any_occurrence: an ack-clear naming one occurrence removes another occurrence's ack;
//   - engine_event_takes_livemu: the engine's hook waits on liveMu on the
//     scan;
//   - superseded_alarms_kept: a superseded log's acks and shelves are
//     carried by a takeover, and kept by a leader that learns it.
//   - link_replay_applied: a committed ack naming a spent ack link is applied again (#715);
//   - link_spent_unchecked: an ack link already used is not refused before it is proposed or applied (#715);
//   - link_ignores_standing: an ack link acts for a user who is not enabled or not in the users file (#715);
//   - link_sign_in_ignored: ack-from-notification: sign-in lets the link alone acknowledge (#715);
//   - link_needs_write_rule: an ack link without an auth: block needs NAUTILUS_TOKEN too (#715);
//   - link_not_forwarded: a standby answers an ack link itself instead of forwarding it to the leader (#715);
//   - link_reason_html: a refused ack link is journalled with its page's HTML as the reason (#715);
//   - link_acks_on_standby: a replica that does not lead acknowledges an ack link on its own engine (#715);
//   - link_session_cookie_refused: a signed-in browser from another origin is refused on a link in link mode instead of the link alone acting (#715);
//   - inbound_unsigned: a provider's reply or keypress is taken without its signature checking (#715);
//   - inbound_tries_unlimited: a person who sent too many wrong reply codes is not refused (#715);
//   - inbound_callee_unchecked: a keypress acknowledges though the call's number is not the person's (#715);
//   - inbound_any_key: a keypress other than 1 acknowledges (#715);
//   - inbound_status_on_wire: a refused reply or keypress answers the provider with its status instead of 200 TwiML (#715);
//   - inbound_not_forwarded: a standby answers a provider's reply itself instead of forwarding it to the leader (#715);
//   - voice_link_acks_here: a call's keypress URL acknowledges as an ack link (#715);
var mutOn = os.Getenv("QUORUM_MUT")

func mutServer(name string) bool { return name == mutOn }
