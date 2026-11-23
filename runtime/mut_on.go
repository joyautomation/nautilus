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
var mutOn = os.Getenv("QUORUM_MUT")

func mutRuntime(name string) bool { return name == mutOn }
