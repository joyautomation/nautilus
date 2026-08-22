// Public, app-agnostic types for the nautilus HMI kit. The realtime client is
// generic over the frame shape, so nothing here is tied to any specific
// runtime snapshot — only the primitives the visual components consume.
import type { AlarmSummary } from './alarms.js';

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
 */
export interface TrendThreshold {
	penId: string;
	kind: 'crit' | 'warn';
	value?: number;
	lo?: number;
	hi?: number;
	label?: string;
}

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
	scan: ScanStats;
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
}

/** One labeled readout on a driver-status card. `text` overrides `value`. */
export interface DriverMetric {
	label: string;
	value: number;
	unit?: string;
	text?: string;
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
