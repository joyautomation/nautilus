//go:build quorum_mut

package runtime

import "os"

// Mutations of the runtime's side of a redundant pair, switched on like
// package quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - stale_retained_alarms_kept: a start after a superseded log keeps the
//     retain store's alarm acks and shelves (the old history's), so a
//     later unsynced takeover carries its shelves (#1177 review).
//   - takeover_keeps_first_scan: a takeover resets the frames but not the
//     first-scan flag, so FIRST_SCAN() stays FALSE on a takeover by a box
//     that led before (#710).
//   - image_copy_in_scan_encodes: the copy a scan makes of its frame for
//     the state stream also hashes the layout, work guard (a) keeps out of
//     the scan (#710 S1).
//   - image_seq_from_scans: an image is numbered by its program's scan
//     count, which an online edit resets, so the standby refuses the
//     images after an edit as older (#710 S1).
//   - image_crc_unchecked: a state image's checksum is neither written nor
//     checked (#710 S1).
//   - shadow_older_image_applied: the standby's shadow applies an image of
//     an older (term, seq) (#710 S1).
//   - shadow_layout_unchecked: the shadow applies a frame to a program of
//     another layout (#710 S1).
//   - shadow_stale_frame_kept: a task an image did not apply keeps
//     offering the older image's frame as current (#710 S1).
//   - image_size_unbounded: images over the size limits are encoded and
//     decoded (#710 S1).
//   - shadow_kept_after_leading: Shadow.Reset keeps the frames and the
//     position (#710 S1).
//   - stream_takeover_no_flush: a takeover reads the retain store without
//     flushing the newest state image first (#710 S1b).
//   - stream_leader_carries: a controller that leads still carries an image
//     into its retain store (#710 S1b).
//   - stream_old_image_carried: a standby carries an image older than the
//     position its retain store records (#710 S1b).
//   - stream_leader_saves_unmarked: a pair leader's saves do not record its
//     term in the retain store (#710 S1b).
//   - stream_stale_capture_sent: a frame copy made before a takeover
//     survives its reset, for the new leadership's stream (#710 S1b).
//   - stream_flush_after_io_read: a takeover's store read for live I/O comes before the flush, and the takeover loads that pre-flush read (#710 S1b review).
//   - stream_flush_unbounded: a takeover waits for a flush that never ends (#710 S1b review).
//   - stream_flush_term_unkeyed: a flush that ran for an earlier term counts for the new one (#710 S1b review).
//   - stream_failed_load_streams: a takeover whose retain store could not be read is not flagged, so its leader streams initial values (#710 S1b review).
//   - stream_store_bad_forgotten: a store read that failed before the takeover (the file moved aside) is not counted as its failed load (#710 S1b review).
//   - image_tags_carry_io: an image carries the inputs and outputs, which the field gives back and the first scan recomputes (#710 S2a).
//   - image_tags_written_only: an image carries only the tags a program writes, not an operator's (#710 S2a).
//   - image_tag_forced_value: an image carries a forced tag's forced value, not its actual one (#710 S2a).
//   - image_tags_at_take: a task's tags are copied when the image is taken, not at the end of the scan its frame was copied at (#710 S2a).
//   - image_source_unhashed: a task's image names no running source (#710 S2a).
//   - shadow_tags_layout_unchecked: the shadow applies the tags of a leader that carries another set (#710 S2a).
//   - image_tags_retained_twice: an image's store section carries the retained tags the retain section already carries (#710 S2a).
//   - copy_first_scan_only: an image is taken from the first copies, with no second round for the faster tasks, so a fast task's state can be as old as the slowest task's period (#710 S2c).
//   - copy_from_older_round: an image takes a copy made for an earlier round, whose image was never taken (#710 S2c).
//   - hot_no_age_check: a takeover goes hot on an image older than hot-max-age, or with hot-max-age 0 (#710 S2b).
//   - hot_source_unchecked: a takeover goes hot with a task running another source than the image's (#710 S2b).
//   - hot_partial_tasks: a takeover installs the tasks that fit and resets the rest, not all or none (#710 S2b).
//   - hot_globals_not_carried: a hot takeover installs the frames but not the image's tags (#710 S2b).
//   - hot_timer_not_rebased: a hot takeover keeps the timers on the old leader's clock (#710 S2b).
//   - hot_age_not_elapsed: a hot takeover rebases the timers to the image's arrival, not counting its age and the inputs wait (#710 S2b).
//   - hot_late_flush_installs: a takeover waits out a late flush and installs what it hands over (#710 S2b).
//   - hot_failed_flush_installs: a takeover whose flush failed (not on an unreadable own store) still goes hot (#710 S2b).
//   - hot_ticket_carried: a hot takeover keeps a READ/WRITE ticket of the old controller (#710 S2b).
//   - hot_retain_before_checks: a takeover over an unreadable own store loads the image's retained values before its checks, and keeps them when one fails (#710 S2b).
//   - hot_rebase_per_task: a hot takeover rebases each task from its own copy time, not the image's one leader reference, so a fast task beside a slow one loses the gap (#710 S2b, Fable).
//   - retain_memory_only_forgotten: a hot takeover over an unreadable own store does not say its retained values are only in memory, or a failed save clears it (#710 S2b).
//   - handback_on_readable_store: a takeover asks for a hand-back with its own store readable (#710 S2c-3).
//   - handback_when_hot: a takeover that will go hot asks for a hand-back (#710 S2c-3).
//   - handback_store_bad_forgotten: an edge's failed store read is forgotten once a hand-back closes the gate (#710 S2c-3, Fable M1).
//   - hot_first_scan_true: a hot takeover makes FIRST_SCAN() TRUE (#710 (b)).
//   - takeover_unmarked: a takeover sets no TAKEOVER_SCAN()/HOT_TAKEOVER() and counts nothing (#710 (b)).
//   - takeover_flag_kept: TAKEOVER_SCAN()/HOT_TAKEOVER() stay TRUE past the first scan (#710 (b)).
//   - peer_ready_never_set: PEER_READY() never takes what the pair sets (#710 (b)).
//   - takeover_flag_survives_step_down: a takeover flag a program never scanned after survives a step-down (#710 (b)).
//   - hold_never_starts: a takeover never starts the takeover hold (#710 (b2)).
//   - hold_release_tag_kept: a hold's start leaves a carried TRUE in its release tag (#710 (b2)).
//   - hold_drivers_remember: a hold's start does not make the drivers forget what they sent (#710 (b2)).
//   - hold_safe_unsent: a hold never writes its safe values (#710 (b2)).
//   - hold_not_inherited: a hot takeover from a holding leader does not inherit its hold (#710 (b2)).
//   - hold_age_restarts: an inherited hold's age starts over at the takeover (#710 (b2)).
//   - hold_no_max: a hold never reaches its max (no alarm) (#710 (b2)).
//   - hold_release_tag_writable: a write or a force from outside the program may set the release tag (#710 (b2)).
//   - hold_channel_writes: a channel WRITE goes out while the hold holds (#710 (b2)).
//   - hold_not_imaged: a holding leader's image says it is not holding (#710 (b2)).
//   - hold_release_at_max_ignored: on-max: release never releases at max (#710 (b2), Fable).
//   - hold_replace_unsafe: a driver swapped in during a hold never gets the safe values (#710 (b2), Fable).
//   - hold_channel_held_at_post: a channel WRITE posted while held goes out when it comes due after the release (#710 (b2), Fable).
//   - hold_safe_wait_restarts: every safe re-send restarts the 2 s wait, so retries keep the alarm down (#710 (b2), Fable re-check).
//   - switch_writes_allowed, switch_scans_on, switch_no_wait, switch_ignores_forces,
//     switch_ignores_hold, abort_keeps_switching, resume_ends_switching,
//     takeover_keeps_switching, final_keeps_round: a planned switchover's
//     switching state (#710 d1c, switch.go).
//   - flush_next_tick, catchup_next_tick, edge_wait_blocks_others,
//     leading_term_locks, wait_keeps_lead_sample,
//     beginswitch_reads_snapshot, carry_snapshot_any_wait,
//     save_waits_on_edge: a takeover edge's
//     same-tick wait for its steps (#710 d1e, stream.go).
var mutOn = os.Getenv("QUORUM_MUT")

func mutRuntime(name string) bool { return name == mutOn }
