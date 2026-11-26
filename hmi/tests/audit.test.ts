// The audit journal reader (#709, #708 PR 7b): the query it sends, and what
// it says for each answer. A 403 is "your role cannot read it", never an
// empty journal.
import { describe, it, expect } from './harness.js';
import { AuditClient, actorText, auditQuery, changeRaw, changeText, type AuditEntry } from '../src/lib/audit.js';

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status });

function client(answer: Response | Error) {
	const asked: string[] = [];
	const c = new AuditClient({
		fetch: async (i) => {
			asked.push(String(i));
			if (answer instanceof Error) throw answer;
			return answer;
		}
	});
	return { c, asked };
}

describe('the query', () => {
	it('sends only what is set, times as epoch ms', () => {
		expect(auditQuery({})).toBe('');
		expect(auditQuery({ user: ' user:akhan ', action: '', result: 'refused', from: new Date(1000), to: 2000.4, limit: 50 })).toBe(
			'user=user%3Aakhan&result=refused&from=1000&to=2000&limit=50'
		);
		expect(auditQuery({ limit: 0, from: NaN })).toBe('');
	});
	it('asks /api/audit with it', async () => {
		const { c, asked } = client(json({ entries: [], truncated: false }));
		await c.query({ action: 'force.set', limit: 10 });
		expect(asked).toEqual(['/api/audit?action=force.set&limit=10']);
	});
});

describe('the export', () => {
	it('asks /api/audit.csv with the filter and no row limit, and keeps the suggested name', async () => {
		const { c, asked } = client(new Response('ts,seq\n', { status: 200, headers: { 'Content-Disposition': 'attachment; filename="audit-1.csv"' } }));
		const r = await c.csv({ user: 'alice', limit: 200 });
		expect(asked).toEqual(['/api/audit.csv?user=alice']);
		expect(r.ok && r.filename).toBe('audit-1.csv');
	});
	it('says why when it is refused', async () => {
		const r = await client(json({ error: 'jsmith may not audit' }, 403)).c.csv();
		expect(r).toEqual({ ok: false, status: 403, message: 'jsmith may not audit' });
	});
});

describe('the answers', () => {
	it('entries and whether the controller cut the list', async () => {
		const e: AuditEntry = { id: '1', ts: '2026-11-20T09:00:00Z', user: 'user:akhan', source: 'hmi', action: 'force.set', target: 'Setpoint', after: 7, result: 'ok' };
		const r = await client(json({ entries: [e], truncated: true })).c.query();
		expect(r).toEqual({ ok: true, entries: [e], truncated: true });
	});
	it("a role without audit is told why, from the controller's JSON", async () => {
		const r = await client(json({ error: 'jsmith (operator) may not audit' }, 403)).c.query();
		expect(r).toEqual({ ok: false, status: 403, message: 'jsmith (operator) may not audit' });
	});
	it('an older controller has no journal; an unreachable one says so', async () => {
		expect(await client(new Response('404 page not found', { status: 404 })).c.query()).toEqual({ ok: false, status: 404, message: 'this controller has no audit journal' });
		expect(await client(new Error('down')).c.query()).toEqual({ ok: false, status: 0, message: 'controller unreachable' });
		const bad = await client(new Response('<html>', { status: 200 })).c.query();
		expect(bad.ok).toBe(false);
	});
});

describe('a row, for a person', () => {
	const base: AuditEntry = { id: '1', ts: '', user: 'user:akhan', source: 'hmi', action: 'tag.write', result: 'ok' };
	it('the change', () => {
		expect(changeText({ ...base, before: 50, after: 55 })).toBe('50 → 55');
		expect(changeText({ ...base, after: { until: '10:00' } })).toBe('until 10:00');
		expect(changeText({ ...base, before: 'on' })).toBe('on');
		expect(changeText(base)).toBe('');
	});
	const at = (ms: number) => `t${ms}`;
	it('a substitute start, stop and expiry read as short text, never raw JSON', () => {
		const start = {
			...base,
			action: 'substitute.start',
			before: { quality: 'good', value: 40 },
			after: { id: 3, value: 95, untilMs: 1000 }
		};
		expect(changeText(start, at)).toBe('95 until t1000 (live 40)');
		const stop = { ...base, action: 'substitute.stop', before: { id: 3, value: 95, live: 40 } };
		expect(changeText(stop, at)).toBe('95 → live 40');
		const expire = {
			...base,
			action: 'substitute.expire',
			before: { id: 3, value: 95, by: 'akhan' },
			after: { live: 41 },
			detail: { reason: 'expired', untilMs: 2000 }
		};
		expect(changeText(expire, at)).toBe('95 by akhan until t2000 → live 41');
		for (const e of [start, stop, expire]) expect(changeText(e, at).includes('{')).toBe(false);
	});
	it('an acknowledge-all names the alarms it took', () => {
		const ack = { ...base, action: 'alarm.ack', target: '*', after: ['HiTemp', 'LoFlow'] };
		expect(changeText(ack)).toBe('acknowledged HiTemp, LoFlow');
		expect(changeText({ ...ack, after: [] })).toBe('nothing to acknowledge');
		const many = Array.from({ length: 30 }, (_, i) => `Alarm${i}`);
		const t = changeText({ ...ack, after: many });
		expect(t.length < 80).toBe(true);
		expect(t.includes('(30)')).toBe(true);
	});
	it('another object is flattened and clipped, and the raw stays available', () => {
		const e = { ...base, action: 'x.y', after: { a: 1, b: 'two', c: 'z'.repeat(80) } };
		const t = changeText(e);
		expect(t.startsWith('a 1, b two, c zzz')).toBe(true);
		expect(t.length <= 48).toBe(true);
		expect(changeRaw(e).includes('"b": "two"')).toBe(true);
		expect(changeRaw({ ...base, before: 50, after: 55 })).toBe('');
	});
	it('who', () => {
		expect(actorText(base)).toBe('akhan');
		expect(actorText({ ...base, user: 'token:scada' })).toBe('token:scada');
	});
});
