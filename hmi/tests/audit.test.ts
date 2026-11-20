// The audit journal reader (#709, #708 PR 7b): the query it sends, and what
// it says for each answer. A 403 is "your role cannot read it", never an
// empty journal.
import { describe, it, expect } from './harness.js';
import { AuditClient, actorText, auditQuery, changeText, type AuditEntry } from '../src/lib/audit.js';

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
		expect(changeText({ ...base, after: { until: '10:00' } })).toBe('{"until":"10:00"}');
		expect(changeText({ ...base, before: 'on' })).toBe('on');
		expect(changeText(base)).toBe('');
	});
	it('who', () => {
		expect(actorText(base)).toBe('akhan');
		expect(actorText({ ...base, user: 'token:scada' })).toBe('token:scada');
	});
});
