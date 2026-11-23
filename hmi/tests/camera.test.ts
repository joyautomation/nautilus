// Built-in cameras (#724): live video starts only on a click and stops on
// everything else, no cameras shows nothing and no error, and a failure shows
// the controller's own words.
import { describe, it, expect } from './harness.js';
import { CameraClient, formatBps, camerasFor, watchingText, nextState, INITIAL, isLive, type Camera, type CameraState } from '../src/lib/camera.js';

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status });
function fake(answers: Array<Response | Error>) {
	const sent: Array<{ url: string; signal?: AbortSignal | null }> = [];
	const f = (async (url: string, init?: RequestInit) => {
		sent.push({ url, signal: init?.signal });
		const a = answers.shift();
		if (!a) throw new Error('no more answers');
		if (a instanceof Error) throw a;
		return a;
	}) as unknown as typeof fetch;
	return { sent, client: new CameraClient({ fetch: f }) };
}
const cam = (o: Partial<Camera> = {}): Camera => ({ id: 'c1', label: 'Wet well', asset: 'p1', kind: 'mjpeg', interval: 5, maxViewers: 2, live: true, snapshot: true, watching: 0, bps: 0, ...o });

describe('formatBps', () => {
	it('names the unit', () => {
		expect(formatBps(0)).toBe('0');
		expect(formatBps(-5)).toBe('0');
		expect(formatBps(NaN)).toBe('0');
		expect(formatBps(800)).toBe('800 bit/s');
		expect(formatBps(1500)).toBe('1.5 kbit/s');
		expect(formatBps(2_400_000)).toBe('2.4 Mbit/s');
	});
});

describe('camerasFor / watchingText', () => {
	it('keeps only the cameras of the asset', () => {
		const l = [cam({ id: 'a', asset: 'p1' }), cam({ id: 'b', asset: 'p2' }), cam({ id: 'c', asset: 'p1' })];
		expect(camerasFor(l, 'p1').map((c) => c.id)).toEqual(['a', 'c']);
		expect(camerasFor(l, 'nope')).toEqual([]);
	});
	it('says how many are watching, with the cap when there is one', () => {
		expect(watchingText({ watching: 1, maxViewers: 3 })).toBe('Live: 1 of 3 watching');
		expect(watchingText({ watching: 2, maxViewers: 0 })).toBe('Live: 2 watching');
	});
});

describe('nextState', () => {
	const snap: CameraState = INITIAL;
	const live: CameraState = { mode: 'live', message: '' };
	it('starts snapshots, never live', () => {
		expect(snap.mode).toBe('snapshot');
		expect(isLive(snap)).toBe(false);
	});
	it('goes live only on watch, and only where there is a live stream', () => {
		expect(nextState(snap, { type: 'watch' }).mode).toBe('live');
		expect(nextState(snap, { type: 'watch' }, false).mode).toBe('snapshot');
	});
	it('stops live on stop, hidden and destroyed', () => {
		for (const t of ['stop', 'hidden', 'destroyed'] as const) expect(nextState(live, { type: t }).mode).toBe('snapshot');
	});
	it('a stream failure returns to snapshots with the controller message: busy for the cap', () => {
		expect(nextState(live, { type: 'streamError', status: 429, message: 'viewer cap reached' })).toEqual({ mode: 'busy', message: 'viewer cap reached' });
		expect(nextState(live, { type: 'streamError', status: 502, message: 'no answer' })).toEqual({ mode: 'error', message: 'no answer' });
		expect(isLive(nextState(live, { type: 'streamError', status: 502, message: 'x' }))).toBe(false);
	});
	it('a late stream error after stopping changes nothing', () => {
		expect(nextState(snap, { type: 'streamError', status: 502, message: 'x' })).toEqual(snap);
	});
	it('a still failing shows the message and a still arriving clears it', () => {
		const e = nextState(snap, { type: 'snapshotError', message: 'refused' });
		expect(e).toEqual({ mode: 'error', message: 'refused' });
		expect(nextState(e, { type: 'snapshotOk' })).toEqual(INITIAL);
		expect(nextState(live, { type: 'snapshotError', message: 'x' })).toEqual(live);
	});
	it('can watch again after an error or the cap', () => {
		expect(nextState({ mode: 'busy', message: 'cap' }, { type: 'watch' }).mode).toBe('live');
		expect(nextState({ mode: 'error', message: 'x' }, { type: 'watch' }).mode).toBe('live');
	});
});

describe('CameraClient', () => {
	it('lists the cameras and fills defaults', async () => {
		const { client } = fake([json({ cameras: [{ id: 'c1', label: 'A', asset: 'p1', live: true, snapshot: true, watching: 1, bps: 1500, maxViewers: 2 }, { nope: 1 }] })]);
		const l = await client.list();
		expect(l.length).toBe(1);
		expect(l[0].interval).toBe(5);
		expect(l[0].bps).toBe(1500);
		expect(l[0].live).toBe(true);
	});
	it('no route, an empty list, a bad body or no answer is no cameras', async () => {
		expect(await fake([json({ error: 'not found' }, 404)]).client.list()).toEqual([]);
		expect(await fake([json({ cameras: [] })]).client.list()).toEqual([]);
		expect(await fake([json({ something: 1 })]).client.list()).toEqual([]);
		expect(await fake([new Response('<html>', { status: 200 })]).client.list()).toEqual([]);
		expect(await fake([new Error('down')]).client.list()).toEqual([]);
	});
	it('puts the nonce on the still and never on the stream', () => {
		const { client } = fake([]);
		expect(client.snapshotUrl('c 1', 7)).toBe('/api/cameras/c%201/snapshot?n=7');
		expect(client.snapshotUrl('c1', 8)).not.toBe(client.snapshotUrl('c1', 7));
		expect(client.streamUrl('c1')).toBe('/api/cameras/c1/stream');
	});
	it('explain reads the controller JSON error, aborts, and falls back on anything else', async () => {
		const a = fake([json({ error: 'viewer cap reached: 2 of 2' }, 429)]);
		expect(await a.client.explain('/x')).toBe('viewer cap reached: 2 of 2');
		expect(a.sent[0].signal?.aborted).toBe(true);
		const d = fake([json({ error: 'busy' }, 429)]);
		expect((await d.client.describe('/x')).status).toBe(429);
		expect(await fake([new Response('<h1>oops</h1>', { status: 502 })]).client.explain('/x')).toBe('the camera did not answer (502)');
		expect(await fake([new Error('down')]).client.explain('/x')).toBe('the camera did not answer');
	});
});
