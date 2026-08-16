// One realtime client for the whole app: the layout header (connection +
// alarm pills) and the overview page read the same SSE stream, so the
// client and its trend buffers live here instead of on a page. Started by
// +layout.svelte; pages just import and read.
import { createRealtimeClient, TrendBuffer, type NautilusFrame } from '@joyautomation/nautilus-hmi';

// Two rolling windows for the trends (3 minutes).
export const tempBuf = new TrendBuffer(180);
export const levelBuf = new TrendBuffer(180);

export const rt = createRealtimeClient<NautilusFrame>({
	url: '/api/stream',
	onFrame: (f) => {
		const t = f.tags ?? {};
		if (typeof t.TempC === 'number') tempBuf.push(f.ts, t.TempC);
		if (typeof t.LevelPct === 'number') levelBuf.push(f.ts, t.LevelPct);
	}
});

// Write a tag back to the controller (same-origin via the dev proxy, so
// the write is allowed without a token).
export async function writeTag(name: string, value: unknown) {
	try {
		await fetch('/api/tags', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ name, value })
		});
	} catch {
		/* offline — the next frame will show the unchanged value */
	}
}
