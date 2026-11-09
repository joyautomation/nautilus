// One realtime client (and one alarm client) for the whole app: every
// page and panel reads them. Started by +layout.svelte; the rest import
// and read.
import { createRealtimeClient, createAlarmClient, TrendBuffer, type NautilusFrame } from '@joyautomation/nautilus-hmi';

// Rolling windows for the level trend (10 minutes at ~1 sample/s).
export const levelBuf = new TrendBuffer(600);

export const rt = createRealtimeClient<NautilusFrame>({
	url: '/api/stream',
	onFrame: (f) => {
		const t = f.tags ?? {};
		if (typeof t.LIT101_Level === 'number') levelBuf.push(f.ts, t.LIT101_Level);
	}
});

// Alarm state (active/journal, ack/shelve) — refetches off frame.alarms.rev.
export const alarms = createAlarmClient(rt);

// The last write that failed, for the panels to show: a refused write
// must not look like one that worked. Cleared by the next one that works.
export const writes = $state({ failed: '' });

// Write a tag back to the controller (same-origin via the dev proxy in
// dev, same-origin for real once the built app is served by `naut run`).
export async function writeTag(name: string, value: unknown): Promise<string | null> {
	const err = await rt.writeTag(name, value);
	writes.failed = err ? `Could not set ${name}: ${err}` : '';
	return err;
}

// The latest frame's tags, read by name. Each read is reactive: a page or
// panel that calls these in its markup re-renders as the frame changes.
const tagsNow = () => (rt.frame?.tags ?? {}) as Record<string, unknown>;
export const num = (k: string, d = 0) => {
	const v = tagsNow()[k];
	return typeof v === 'number' ? v : d;
};
export const bool = (k: string) => tagsNow()[k] === true;
/** Whether the controller publishes the tag at all (an older project may not). */
export const has = (k: string) => k in tagsNow();
export const str = (k: string) => {
	const v = tagsNow()[k];
	return typeof v === 'string' ? v : '';
};

// ResetFaults is a PULSE input, shared by both pumps (motor.ld's `reset`
// rung, and the `lockout` rung's own gated reset): hold it true just long
// enough for one scan to see it, then drop it, so the button behaves like a
// momentary pushbutton rather than a maintained switch left ON.
export async function pulseReset() {
	await writeTag('ResetFaults', true);
	setTimeout(() => writeTag('ResetFaults', false), 300);
}
