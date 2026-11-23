// Built-in cameras (#724): live video starts only on a click and stops on
// everything else, no cameras shows nothing and no error, and a failure shows
// the controller's own words.
import { describe, it, expect } from './harness.js';
import { CameraClient, ptzMove, PTZ_HOLD_MS, formatBps, camerasFor, watchingText, nextState, INITIAL, isLive, type Camera, type CameraState } from '../src/lib/camera.js';

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status });
function fake(answers: Array<Response | Error>) {
	const sent: Array<{ url: string; signal?: AbortSignal | null; method?: string; body?: string }> = [];
	const f = (async (url: string, init?: RequestInit) => {
		sent.push({ url, signal: init?.signal, method: init?.method, body: init?.body as string | undefined });
		const a = answers.shift();
		if (!a) throw new Error('no more answers');
		if (a instanceof Error) throw a;
		return a;
	}) as unknown as typeof fetch;
	return { sent, client: new CameraClient({ fetch: f }) };
}
const cam = (o: Partial<Camera> = {}): Camera => ({ id: 'c1', label: 'Wet well', asset: 'p1', kind: 'mjpeg', interval: 5, maxViewers: 2, view: true, live: true, hls: false, ptz: false, snapshot: true, watching: 0, bps: 0, ...o });

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
		const { client } = fake([json({ cameras: [{ id: 'c1', label: 'A', asset: 'p1', live: true, hls: false, snapshot: true, watching: 1, bps: 1500, maxViewers: 2 }, { nope: 1 }] })]);
		const l = await client.list();
		expect(l.length).toBe(1);
		expect(l[0].interval).toBe(5);
		expect(l[0].bps).toBe(1500);
		expect(l[0].live).toBe(true);
	});
	it('view is false only when the controller says so', async () => {
		const { client } = fake([json({ cameras: [{ id: 'a', view: false }, { id: 'b', view: true }, { id: 'c' }] })]);
		const l = await client.list();
		expect(l.map((c) => c.view)).toEqual([false, true, true]);
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

describe('pan, tilt and zoom', () => {
	it('a direction is a bounded velocity, held no longer than the controller allows', () => {
		expect(ptzMove('left', 400)).toEqual({ action: 'move', x: -0.5, y: 0, zoom: 0, ms: 400 });
		expect(ptzMove('up', 400)).toEqual({ action: 'move', x: 0, y: 0.5, zoom: 0, ms: 400 });
		expect(ptzMove('out', 400)).toEqual({ action: 'move', x: 0, y: 0, zoom: -0.5, ms: 400 });
		expect((ptzMove('right', 1e9) as any).ms).toBe(PTZ_HOLD_MS);
		expect((ptzMove('right', 1) as any).ms).toBe(100);
	});
	it('posts the command, and says the controller\'s words when it refuses', async () => {
		const a = fake([json({ ok: true }), json({ error: 'too fast: one move at a time' }, 429), json({ error: 'no permission' }, 403), new Error('offline')]);
		expect(await a.client.ptz('c 1', ptzMove('up', 400))).toEqual({ ok: true, status: 200, message: '' });
		expect(a.sent[0].url).toBe('/api/cameras/c%201/ptz');
		expect(a.sent[0].method).toBe('POST');
		expect(JSON.parse(a.sent[0].body!)).toEqual({ action: 'move', x: 0, y: 0.5, zoom: 0, ms: 400 });
		expect(await a.client.ptz('c1', ptzMove('left', 400))).toEqual({ ok: false, status: 429, message: 'too fast: one move at a time' });
		expect((await a.client.ptz('c1', { action: 'preset', preset: '1' })).message).toBe('no permission');
		expect(await a.client.ptzStop('c1')).toEqual({ ok: false, status: 0, message: 'The controller did not answer.' });
	});
	it('a stop goes to its own route, with keepalive, and a move can be cancelled before it is sent', async () => {
		const seen: any[] = [];
		const f = (async (url: string, init?: RequestInit) => {
			seen.push({ url, keepalive: init?.keepalive, body: init?.body, signal: init?.signal });
			if (init?.signal?.aborted) throw new DOMException('aborted', 'AbortError');
			return new Response('{}', { status: 200 });
		}) as unknown as typeof fetch;
		const c = new CameraClient({ fetch: f });
		expect((await c.ptzStop('c1')).ok).toBe(true);
		expect(seen[0].url).toBe('/api/cameras/c1/ptz/stop');
		expect(seen[0].keepalive).toBe(true);
		expect(seen[0].body).toBe(undefined);
		const ctl = new AbortController();
		ctl.abort();
		const r = await c.ptz('c1', ptzMove('left', 400), ctl.signal);
		expect(r.ok).toBe(false);
	});
	it('lists presets, and none on any failure', async () => {
		const a = fake([json({ presets: [{ token: '1', name: 'Home' }, { token: '2' }, { name: 'no token' }, 5] }), json({}, 404), new Error('x')]);
		expect(await a.client.ptzPresets('c1')).toEqual([{ token: '1', name: 'Home' }, { token: '2', name: '2' }]);
		expect(await a.client.ptzPresets('c1')).toEqual([]);
		expect(await a.client.ptzPresets('c1')).toEqual([]);
	});
	it('reads whether a camera pans', async () => {
		const a = fake([json({ cameras: [{ id: 'a', ptz: true }, { id: 'b' }] })]);
		expect((await a.client.list()).map((c) => c.ptz)).toEqual([true, false]);
	});
});
