// Public, app-agnostic types for the nautilus HMI kit. The realtime client is
// generic over the frame shape, so nothing here is tied to any specific
// runtime snapshot — only the primitives the visual components consume.

/** A single timestamped sample. `t` is epoch milliseconds, `v` the value. */
export interface TrendPoint {
	t: number;
	v: number;
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
}
