// One realtime client (and one alarm client) for the whole app — the
// single page reads both. Started by +layout.svelte; the page just imports
// and reads.
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

// Write a tag back to the controller (same-origin via the dev proxy in
// dev, same-origin for real once the built app is served by `naut run`).
export async function writeTag(name: string, value: unknown): Promise<string | null> {
	return rt.writeTag(name, value);
}
