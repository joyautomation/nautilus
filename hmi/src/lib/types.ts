// Public, app-agnostic types for the nautilus HMI kit. The realtime client is
// generic over the frame shape, so nothing here is tied to any specific
// runtime snapshot — only the primitives the visual components consume.
import type { AlarmSummary } from './alarms.svelte.js';

/** A single timestamped sample. `t` is epoch milliseconds, `v` the value. */
export interface TrendPoint {
	t: number;
	v: number;
}

/**
 * One pen (series) on a `TrendChart`. `data` may be a static, pre-sorted
 * `TrendPoint[]` for historical playback, or a `TrendBuffer`'s reactive
 * `.points` for live streaming — the chart treats both identically, so
 * there's no separate "live mode" prop to flip.
 */
export interface TrendPen {
	/** Stable identity — also the join key for `TrendThreshold.penId`. */
	id: string;
	label: string;
	/** CSS color; omit to draw from the kit's default categorical palette. */
	color?: string;
	units?: string;
	/** Fixed bounds. In `percent` axis mode these define that pen's own 0–100%
	 * range; in `shared` mode use the chart-level `yMin`/`yMax` instead.
	 * Omit either to auto-scale that side from the currently visible data. */
	min?: number;
	max?: number;
	/** Force the dashed stroke style (e.g. to mark a setpoint pen). */
	dashed?: boolean;
	/** Start hidden; the legend can still toggle it back on. */
	hidden?: boolean;
	data: TrendPoint[];
}

/**
 * A threshold line (`value`) or shaded band (`lo`..`hi`) drawn behind one
 * pen's trace, in that pen's own engineering units — converted through
 * whichever axis mode the chart is using. Uses the theme's reserved
 * `--crit`/`--warn` roles, never a series color.
 *
 * `penId` is optional: omit it for a CHART-LEVEL threshold, drawn across the
 * whole chart against the shared y-domain rather than one pen's range. Only
 * meaningful in `axisMode="shared"` — a chart in `percent` mode has no single
 * engineering-units axis for it to sit on, so a penless threshold is simply
 * not drawn there.
 */
export interface TrendThreshold {
	penId?: string;
	kind: 'crit' | 'warn';
	value?: number;
	lo?: number;
	hi?: number;
	label?: string;
}

/**
 * How much a tag's value is worth believing, as the controller reports it
 * (`frame.quality`, `RealtimeClient.quality()`). Mirrors io.Quality in Go.
 *
 * This is the axis the kit could not see before. `tags.ts`'s helpers detect
 * a tag's ABSENCE — a screen bound to a point the controller does not
 * publish shows "—" rather than a confident zero — but a tag that IS
 * published and merely old was indistinguishable from a live one: the
 * number is finite, the binding resolves, and the gauge renders it with
 * full confidence. `quality` is the missing half.
 *
 * - `good` — current value from a healthy source. The default: a tag the
 *   controller says nothing about is good.
 * - `stale` — was good, is no longer being refreshed. Show the number and
 *   its age; do not blank it. It is what the plant last was.
 * - `bad` — the source itself calls this reading untrustworthy (sensor
 *   fault, conversion out of range). The link is fine; the value is not.
 * - `notConnected` — the source has never delivered. There may be no value
 *   at all. A Sparkplug node that never birthed, an address that never
 *   answered.
 */
export type Quality = 'good' | 'stale' | 'bad' | 'notConnected';

/** Reserved status roles used by StatusPill and other indicators. */
export type StatusKind = 'good' | 'warning' | 'serious' | 'critical' | 'off';

/**
 * Scan-loop diagnostics as the nautilus server reports them in every frame
 * (`frame.scan`) — the numbers behind a PLC-style runtime diagnostics page.
 * Mirrors runtime.ScanStats in Go.
 */
export interface ScanStats {
	count: number;
	targetMs: number;
	lastMs: number;
	minMs: number;
	maxMs: number;
	avgMs: number;
	/** Last-scan phase breakdown: input read / logic execute / output write. */
	readMs: number;
	execUs: number;
	writeMs: number;
	periodMs: number;
	jitterMs: number;
	ioErrors: number;
	logicErrors: number;
	ioHealthy: boolean;
	lastIOError?: string;
	/** Last 180 scan times, ms. */
	recent: number[];
	/** Last 180 actual scan periods, ms. */
	periods: number[];
	/** Scan-time distribution, 2 ms buckets. */
	histogram: number[];
	/** Additional tasks' health — present when the resource runs more than
	 * one program (the main task owns field I/O; tasks compute against the
	 * tag store at their own rates). Mirrors runtime.TaskStats. */
	tasks?: TaskStats[];
}

/** One additional task's health, riding inside ScanStats. */
export interface TaskStats {
	name: string;
	targetMs: number;
	count: number;
	lastMs: number;
	logicErrors: number;
	lastError?: string;
}

/** The nautilus server's frame shape (GET /api/state, SSE /api/stream). */
export interface NautilusFrame {
	ts: number;
	scans: number;
	tags: Record<string, unknown>;
	/** Retained program locals (integrals, latches, FB instances with their
	 * pins) — the watch inside the POU, read-only. */
	locals?: Record<string, unknown>;
	/**
	 * Scan-loop diagnostics. Always present on a frame published by
	 * `RealtimeClient` — but on the WIRE a delta frame carries the block
	 * only on its cadence (~2 s), and `drivers` / `alarms` only when they
	 * change, so code that parses the SSE stream itself must merge them the
	 * way `mergeDelta` does rather than assume every frame is complete.
	 */
	scan: ScanStats;
	/**
	 * Tags whose value is NOT to be trusted — only the non-good ones, so a
	 * healthy controller omits the field entirely and an absent name means
	 * `good`. A name here need not appear in `tags`: `notConnected` is
	 * precisely the source that has never delivered a value. Read it
	 * through `RealtimeClient.quality()` / `.isGood()` rather than by hand,
	 * which also resolves a dotted member path to its root tag.
	 *
	 * Present only on controllers whose `/api/meta` reports `quality: true`
	 * — an empty map on an older runtime means "no idea", not "all good".
	 */
	quality?: Record<string, Quality>;
	/**
	 * Frame counter for ONE delta-stream client, from 1. Absent on a plain
	 * stream. The client's gap detector: frames are built from a per-client
	 * generation and are never dropped mid-stream, so a skipped `seq` means
	 * the connection broke and the merged state can no longer be vouched
	 * for — `RealtimeClient` reconnects rather than merging on.
	 */
	seq?: number;
	/**
	 * This frame is a COMPLETE snapshot: replace the merged tag state, do
	 * not merge into it. True on a delta stream's first frame, on each
	 * periodic resync, and whenever the controller's tag set changed (a
	 * delta cannot express a deletion). Absent on a plain stream, whose
	 * every frame is full by definition.
	 */
	full?: boolean;
	/** Field-driver / publisher status, present when the controller runs a
	 * driver that reports it (also served standalone at GET /api/drivers).
	 * Mirrors server.DriverStatus in Go. */
	drivers?: DriverStatus[];
	/** Alarm counts, present when the controller runs an alarm engine
	 * (`alarm/` package — also served in full at GET /api/alarms). Counts
	 * only, never the alarm list itself; `rev` bumps on any change so the
	 * HMI can refetch /api/alarms exactly when something moved. Mirrors
	 * alarm.Summary in Go. See ./alarms.ts — contract: docs/design/alarms.md */
	alarms?: AlarmSummary;
}

/** The connection lifecycle a driver-status card renders. Maps to a
 * reserved color and never stands on color alone. */
export type DriverState =
	| 'connected'
	| 'connecting'
	| 'waiting'
	| 'degraded'
	| 'error'
	| 'offline';

/** One field driver's or publisher's health (GET /api/drivers, or
 * frame.drivers). Mirrors server.DriverStatus in Go. */
export interface DriverStatus {
	/** Protocol discriminator, e.g. "ethernet-ip" | "sparkplug". */
	kind: string;
	/** Display name — the host, or a Sparkplug group/node. */
	name: string;
	/** One-line address/target. */
	detail: string;
	state: DriverState;
	/** Human sentence describing the current state. */
	message: string;
	/** Epoch ms the current state began (0/absent = unknown). */
	sinceMs?: number;
	lastError?: string;
	metrics?: DriverMetric[];
	devices?: DriverDevice[];
	/** Protocol-specific structured fields (born, primaryHost, …). */
	extra?: Record<string, unknown>;
	/**
	 * Epoch ms this status was OBSERVED — which on a delta stream is not
	 * when the frame carrying it arrived: the block is sent only when it
	 * changes, so a healthy driver's status can be up to one resync old.
	 * Render every age in it (a metric's `atMs`) against THIS, not against
	 * the clock, or a perfectly healthy "last publish 0.2s" creeps up to
	 * 30s and snaps back on every resync. Uptime from `sinceMs` is
	 * different — that is an absolute start time and grows honestly.
	 */
	asOfMs?: number;
}

/** One labeled readout on a driver-status card. `text` overrides `value`,
 * and `atMs` overrides both. */
export interface DriverMetric {
	label: string;
	value: number;
	unit?: string;
	text?: string;
	/**
	 * A moment in time (epoch ms) to render as an age — "last poll", "last
	 * publish". The controller sends the moment rather than a pre-rendered
	 * age because a rendered age changes on every build, which would put
	 * the whole driver block on the wire four times a second. Measure it
	 * against `DriverStatus.asOfMs`.
	 */
	atMs?: number;
}

/** One sub-device under a driver (a Sparkplug device). */
export interface DriverDevice {
	id: string;
	online: boolean;
	detail?: string;
}

/** Per-tag HMI documentation from GET /api/meta. */
export interface TagMeta {
	desc?: string;
	unit?: string;
}

/** The nautilus server's static controller description (GET /api/meta). */
export interface ControllerMeta {
	tags: Record<string, TagMeta>;
	inputs: string[];
	outputs: string[];
	scanTargetMs: number;
	/**
	 * True when the controller accepts a dotted member path (or an object
	 * payload) on `POST /api/tags` — i.e. UDT members are writable. Absent on
	 * runtimes older than that, where a dotted name silently created a junk
	 * tag, so treat `undefined` as false and disable member controls.
	 * Writability is still the ROOT tag's: a member of an `inputs` tag is
	 * refused like the input itself.
	 */
	memberWrites?: boolean;
	/**
	 * True when this controller can report per-tag data quality at all —
	 * it runs a driver that knows, or has driver-bound inputs the runtime
	 * can mark stale on a read failure.
	 *
	 * This flag matters more than the others because the false case is
	 * INVISIBLE: an empty `frame.quality` looks exactly like a healthy
	 * plant. A screen that renders a quality badge must check this first,
	 * or it paints a confident green on a controller that has no idea.
	 */
	quality?: boolean;
	/**
	 * True when `GET /api/stream` understands `?delta=1` and `?tags=`.
	 * Absent on older runtimes, which ignore both and send whole frames —
	 * harmless to merge, but the client never sees `full` and cannot tell a
	 * resync from steady state, so `RealtimeClient` falls back to plain
	 * pass-through when a frame arrives without a `seq`.
	 */
	deltas?: boolean;
	/**
	 * True when `GET /api/stream` understands `?blocks=delta`: the non-tag
	 * blocks (`scan`, `drivers`, `alarms`) sent only when they change
	 * rather than on every frame — ~18 kB a frame on the controller this
	 * was measured against. Absent on older runtimes, which ignore the
	 * parameter and keep sending every block; `mergeDelta` handles both,
	 * so a client never has to branch on this. It is here for a diagnostics
	 * page that wants to say which reductions a controller supports.
	 */
	blockDeltas?: boolean;
}

/** One link in a Nav sidebar section. */
export interface NavItem {
	label: string;
	href: string;
	/** Icon name from ./icons.ts. */
	icon?: string;
	badge?: string | number;
	disabled?: boolean;
}

/** A labeled (or unlabeled) group of NavItems. */
export interface NavSection {
	/** Section heading; omit for an unlabeled group. */
	label?: string;
	items: NavItem[];
}
