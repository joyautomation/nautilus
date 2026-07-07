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
