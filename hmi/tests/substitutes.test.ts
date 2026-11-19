// The substitutes client's contract (#707): one requestId per action reused
// on retry, an unknown outcome that is neither success nor failure, 409s that
// are never blindly retried, and kept:false that is never swallowed.
import { describe, it, expect } from './harness.js';
import {
	SubstituteClient,
	classify,
	failureText,
	keptText,
	formatRemaining,
	unacked,
	validateStart,
	type SubstituteResult
} from '../src/lib/substitutes.js';

type Sent = { url: string; method: string; body: Record<string, unknown> | undefined };

function fake(answers: Array<Response | Error>) {
	const sent: Sent[] = [];
	const f = (async (url: string, init?: RequestInit) => {
		sent.push({ url, method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined });
		const a = answers.shift();
		if (!a) throw new Error('no more answers');
		if (a instanceof Error) throw a;
		return a;
	}) as unknown as typeof fetch;
	let n = 0;
	return { sent, client: new SubstituteClient({ fetch: f, uuid: () => `id-${++n}`, token: 't' }) };
}
const json = (b: unknown, status = 200, headers: Record<string, string> = {}) =>
	new Response(JSON.stringify(b), { status, headers });
const text = (b: string, status: number, headers: Record<string, string> = {}) => new Response(b, { status, headers });

describe('start — requestId', () => {
	it('sends a fresh requestId per action and returns it', async () => {
		const { sent, client } = fake([json({ started: true, kept: true }), json({ started: true, kept: true })]);
		const a = await client.start('T1', 5, 60);
		const b = await client.start('T1', 5, 60);
		expect(sent[0].body?.requestId).toBe('id-1');
		expect(sent[1].body?.requestId).toBe('id-2');
		expect(a.requestId).toBe('id-1');
		expect(b.requestId).toBe('id-2');
	});
	it('a retry reuses the id it is given, never a new one', async () => {
		const { sent, client } = fake([text('outcome unknown: re-read', 503, { 'X-Nautilus-Outcome': 'unknown' }), json({ started: true })]);
		const first = await client.start('T1', 5, 60);
		expect(first.ok).toBe(false);
		await client.start('T1', 5, 60, first.requestId);
		expect(sent[1].body?.requestId).toBe(sent[0].body?.requestId);
	});
	it('omits value and seconds when not given (the rule decides)', async () => {
		const { sent, client } = fake([json({})]);
		await client.start('T1', undefined, undefined);
		expect(Object.keys(sent[0].body ?? {}).sort()).toEqual(['name', 'requestId']);
	});
});

describe('start — answers', () => {
	const fail = (r: SubstituteResult) => {
		if (r.ok) throw new Error('expected a failure');
		return r;
	};
	it('503 outcome unknown (header) is unknown: maybe applied, retry the same id', async () => {
		const { client } = fake([text('whatever', 503, { 'X-Nautilus-Outcome': 'unknown' })]);
		const r = fail(await client.start('T', 1, 1));
		expect(r.kind).toBe('unknown');
		expect(r.maybeApplied).toBe(true);
		expect(r.retrySameId).toBe(true);
	});
	it('503 outcome unknown (text only) is unknown too', async () => {
		const { client } = fake([text('outcome unknown; re-read GET /api/substitutes', 503)]);
		expect(fail(await client.start('T', 1, 1)).kind).toBe('unknown');
	});
	it('503 no leader and 503 not saved are certain: nothing started', async () => {
		const { client } = fake([text('no leader', 503), text('substitute not saved: store refused', 503)]);
		const a = fail(await client.start('T', 1, 1));
		const b = fail(await client.start('T', 1, 1));
		expect(a.kind).toBe('noLeader');
		expect(b.kind).toBe('notSaved');
		expect(a.maybeApplied).toBe(false);
		expect(b.maybeApplied).toBe(false);
	});
	it('409 is a conflict: never retried blind, never "maybe applied"', async () => {
		const { client } = fake([text('requestId reused with a different request', 409)]);
		const r = fail(await client.start('T', 1, 1));
		expect(r.kind).toBe('conflict');
		expect(r.retrySameId).toBe(false);
		expect(r.maybeApplied).toBe(false);
	});
	it('a network error is an unknown outcome, not a plain failure', async () => {
		const { client } = fake([new Error('down')]);
		const r = fail(await client.start('T', 1, 1));
		expect(r.kind).toBe('unreachable');
		expect(r.maybeApplied).toBe(true);
		expect(r.retrySameId).toBe(true);
		expect(failureText(r).includes('may still take effect')).toBe(true);
	});
	it('400 and 403 map to invalid and forbidden', () => {
		expect(classify(400, 'bad', null).kind).toBe('invalid');
		expect(classify(403, 'operator value not allowed', null).kind).toBe('forbidden');
		expect(classify(404, 'not active', null).kind).toBe('notFound');
	});
});

describe('kept:false', () => {
	it('a success with kept:false carries its warning to the operator', async () => {
		const { client } = fake([json({ kept: false, warning: 'may still be active on the next leader' })]);
		const r = await client.stop('abc');
		expect(r.ok).toBe(true);
		if (!r.ok) return;
		expect(r.kept).toBe(false);
		expect(keptText(r)).toBe('may still be active on the next leader');
	});
	it('kept absent or true is silent', async () => {
		const { client } = fake([json({ started: true }), json({ kept: true })]);
		for (let i = 0; i < 2; i++) {
			const r = await client.start('T', 1, 1);
			if (!r.ok) throw new Error('ok expected');
			expect(r.kept).toBe(true);
			expect(keptText(r)).toBe(undefined);
		}
	});
	it('kept:false without a warning still says something', async () => {
		const { client } = fake([json({ kept: false })]);
		const r = await client.stop('abc');
		if (!r.ok) throw new Error('ok expected');
		expect(keptText(r)?.includes('not saved')).toBe(true);
	});
});

describe('stop / ack / list', () => {
	it('stop DELETEs the escaped id; ack POSTs ids', async () => {
		const { sent, client } = fake([json({ kept: true }), json({ kept: true })]);
		await client.stop('a/b');
		await client.ack(['x', 'y']);
		expect(sent[0].url).toBe('/api/substitutes/a%2Fb');
		expect(sent[0].method).toBe('DELETE');
		expect(sent[1].url).toBe('/api/substitutes/ack');
		expect(sent[1].body).toEqual({ ids: ['x', 'y'] });
	});
	it('list tolerates a missing array and reports a read error', async () => {
		const a = fake([json({ rules: [{ tag: 'T' }] })]);
		expect(await a.client.list()).toEqual({ substitutes: [], rules: [{ tag: 'T' }] });
		const b = fake([text('nope', 500)]);
		expect(await b.client.list()).toEqual({ error: 'nope' });
	});
});

describe('display rules', () => {
	it('formats a countdown and never goes negative', () => {
		expect(formatRemaining(45_000)).toBe('45s');
		expect(formatRemaining(125_000)).toBe('2m 05s');
		expect(formatRemaining(3_720_000)).toBe('1h 02m');
		expect(formatRemaining(-5)).toBe('ending');
		expect(formatRemaining(undefined)).toBe('—');
	});
	it('lists the substitutes still waiting for an ack', () => {
		const e = (id: string, ackOnStart: boolean, acked: boolean) => ({ id, name: id, value: 1, startMs: 0, untilMs: 0, remainingMs: 0, ackOnStart, acked });
		expect(unacked([e('a', true, false), e('b', true, true), e('c', false, false)]).map((s) => s.id)).toEqual(['a']);
	});
	it('validates what the server would refuse', () => {
		const rule = { tag: 'T', maxMs: 600_000, operatorValue: true };
		expect(validateStart(undefined, 1, 5)).toBe('Pick a tag.');
		expect(validateStart(rule, 1, 0)).toBe('Enter a duration in seconds.');
		expect(validateStart(rule, 1, 601)?.startsWith('At most')).toBe(true);
		expect(validateStart(rule, '', 5)).toBe('Enter the value to hold.');
		expect(validateStart(rule, 1, 600)).toBeNull();
		expect(validateStart({ tag: 'T' }, undefined, 5)).toBeNull();
	});
});
