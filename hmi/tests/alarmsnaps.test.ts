// Pictures kept at alarm activation (#724 slice 2): a 404 is remembered (one
// request, not one per row), a 403 too, results are cached per occurrence and
// no more than three requests run at once.
import { describe, it, expect } from './harness.js';
import { SnapshotClient, parseSnaps, thumbSplit } from '../src/lib/alarmsnaps.js';

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status });
const snap = (n: number) => ({ file: `f${n}.jpg`, ts: n, camera: `c${n}`, alarm: 'a', url: `/api/camera-snapshots/f${n}.jpg` });

function fake(answer: (url: string) => Response | Error | Promise<Response>) {
	const sent: string[] = [];
	const f = (async (url: string) => {
		sent.push(url);
		const a = await answer(url);
		if (a instanceof Error) throw a;
		return a;
	}) as unknown as typeof fetch;
	return { sent, client: new SnapshotClient({ fetch: f }) };
}

describe('parseSnaps', () => {
	it('reads the list and drops rows that are not a path on this controller', () => {
		const l = parseSnaps({ snapshots: [snap(1), { url: 'http://elsewhere/x.jpg' }, { url: '//x/y' }, null, snap(2)] });
		expect(l.map((s) => s.file)).toEqual(['f1.jpg', 'f2.jpg']);
		expect(parseSnaps({})).toEqual([]);
		expect(parseSnaps(null)).toEqual([]);
	});
});

describe('thumbSplit', () => {
	it('shows up to 3 and counts the rest', () => {
		expect(thumbSplit([1, 2])).toEqual({ shown: [1, 2], more: 0 });
		expect(thumbSplit([1, 2, 3])).toEqual({ shown: [1, 2, 3], more: 0 });
		expect(thumbSplit([1, 2, 3, 4, 5])).toEqual({ shown: [1, 2, 3], more: 2 });
	});
});

describe('SnapshotClient', () => {
	it('lists the pictures of an occurrence', async () => {
		const { client, sent } = fake(() => json({ snapshots: [snap(1), snap(2)] }));
		const r = await client.list('o 1');
		expect(r.state).toBe('ok');
		expect(r.snaps.length).toBe(2);
		expect(sent[0]).toBe('/api/camera-snapshots?occurrence=o%201');
	});
	it('an empty list or a failure is none, not an error', async () => {
		expect((await fake(() => json({ snapshots: [] })).client.list('a')).state).toBe('none');
		expect((await fake(() => json({}, 500)).client.list('a')).state).toBe('none');
		expect((await fake(() => new Error('down')).client.list('a')).state).toBe('none');
	});
	it('remembers a 404: no other occurrence is asked', async () => {
		const { client, sent } = fake(() => json({}, 404));
		expect((await client.list('a')).state).toBe('none');
		expect(client.keepsNone).toBe(true);
		expect((await client.list('b')).state).toBe('none');
		expect(sent.length).toBe(1);
	});
	it('remembers a 403: denied, and no more is asked', async () => {
		const { client, sent } = fake(() => json({ error: 'no permission to view cameras' }, 403));
		expect((await client.list('a')).state).toBe('denied');
		expect(client.denied).toBe(true);
		expect((await client.list('b')).state).toBe('denied');
		expect(sent.length).toBe(1);
	});
	it('a network error is not remembered as "keeps none"', async () => {
		let n = 0;
		const { client, sent } = fake(() => (n++ === 0 ? new Error('down') : json({ snapshots: [snap(1)] })));
		expect((await client.list('a')).state).toBe('none');
		expect((await client.list('b')).state).toBe('ok');
		expect(sent.length).toBe(2);
	});
	it('caches per occurrence, including a request still in flight', async () => {
		const { client, sent } = fake(() => json({ snapshots: [snap(1)] }));
		const [a, b] = await Promise.all([client.list('a'), client.list('a')]);
		await client.list('a');
		expect(a).toBe(b);
		expect(sent.length).toBe(1);
	});
	it('runs at most 3 requests at once, and a 404 that returns settles the waiting ones', async () => {
		let open = 0;
		let peak = 0;
		const gates: Array<() => void> = [];
		const { client, sent } = fake(async () => {
			open++;
			peak = Math.max(peak, open);
			await new Promise<void>((r) => gates.push(r));
			open--;
			return json({}, 404);
		});
		const all = Promise.all(['1', '2', '3', '4', '5', '6'].map((o) => client.list(o)));
		await new Promise((r) => setTimeout(r, 5));
		expect(sent.length).toBe(3);
		gates.splice(0).forEach((g) => g());
		const rs = await all;
		expect(peak).toBe(3);
		expect(rs.every((r) => r.state === 'none')).toBe(true);
		// the three that waited saw the 404 and never went out
		expect(sent.length).toBe(3);
	});
	it('limit holds while answers are good', async () => {
		let open = 0;
		let peak = 0;
		const gates: Array<() => void> = [];
		const { client, sent } = fake(async () => {
			open++;
			peak = Math.max(peak, open);
			await new Promise<void>((r) => gates.push(r));
			open--;
			return json({ snapshots: [snap(1)] });
		});
		const all = Promise.all(['1', '2', '3', '4', '5'].map((o) => client.list(o)));
		for (let i = 0; i < 10; i++) {
			await new Promise((r) => setTimeout(r, 2));
			gates.splice(0).forEach((g) => g());
		}
		await all;
		expect(peak).toBe(3);
		expect(sent.length).toBe(5);
	});
});
