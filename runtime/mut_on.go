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
var mutOn = os.Getenv("QUORUM_MUT")

func mutRuntime(name string) bool { return name == mutOn }
