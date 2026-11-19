// The substitutes client's contract (#707): one requestId per action reused
// on retry, an unknown outcome that is neither success nor failure, 409s that
// are never blindly retried, and kept:false that is never swallowed.
import { describe, it, expect } from './harness.js';
import {
	SubstituteClient,
	SubstituteSession,
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
	const R = { 'X-Nautilus-Outcome': 'refused' };
	it('503 outcome unknown (header) is unknown: maybe applied, retry the same id', async () => {
		const { client } = fake([text('whatever', 503, { 'X-Nautilus-Outcome': 'unknown' })]);
		const r = fail(await client.start('T', 1, 1));
		expect(r.kind).toBe('unknown');
		expect(r.maybeApplied).toBe(true);
		expect(r.retrySameId).toBe(true);
	});
	it('ANY failure without the refused header is unknown, whatever its status or text', async () => {
		for (const [status, body] of [[503, 'no leader'], [503, 'substitute not saved'], [504, 'timeout'], [502, 'bad gateway'], [500, 'boom'], [409, 'x'], [400, 'x']] as const) {
			const { client } = fake([text(body, status)]);
			const r = fail(await client.start('T', 1, 1));
			expect(r.kind).toBe('unknown');
			expect(r.retrySameId).toBe(true);
			expect(r.maybeApplied).toBe(true);
		}
	});
	it('503 no leader and 503 not saved, WITH the refused header, are certain', async () => {
		const { client } = fake([text('no leader', 503, R), text('substitute not saved: store refused', 503, R)]);
		const a = fail(await client.start('T', 1, 1));
		const b = fail(await client.start('T', 1, 1));
		expect(a.kind).toBe('noLeader');
		expect(b.kind).toBe('notSaved');
		expect(a.maybeApplied).toBe(false);
		expect(b.maybeApplied).toBe(false);
	});
	it('503 no majority WITH the refused header is certain: nothing changed', async () => {
		const msg = 'operator-intent log has no majority: the peer is down; substitutes cannot be started, stopped or acknowledged until it returns';
		const { client } = fake([text(msg, 503, R)]);
		const r = fail(await client.start('T', 1, 1));
		expect(r.kind).toBe('noMajority');
		expect(r.maybeApplied).toBe(false);
		expect(failureText(r).includes('Nothing was changed')).toBe(true);
		expect(failureText(r).includes(msg)).toBe(true);
	});
	it('409 refused is a conflict: never retried blind, never "maybe applied"', async () => {
		const { client } = fake([text('requestId reused with a different request', 409, R)]);
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
	it('a stop or ack with an unknown outcome has no requestId to retry: re-read and decide', async () => {
		const { client } = fake([text('bad gateway', 502), new Error('down')]);
		const a = fail(await client.stop(1));
		const b = fail(await client.ack([1]));
		for (const r of [a, b]) {
			expect(r.maybeApplied).toBe(true);
			expect(r.retrySameId).toBe(false);
		}
	});
	it('refused 400, 403, 404 map to invalid, forbidden, notFound', () => {
		expect(classify(400, 'bad', 'refused').kind).toBe('invalid');
		expect(classify(403, 'operator value not allowed', 'refused').kind).toBe('forbidden');
		expect(classify(404, 'not active', 'refused').kind).toBe('notFound');
		expect(classify(404, 'proxy', null).kind).toBe('unknown');
	});
});

describe('SubstituteSession — the start state machine', () => {
	const U = { 'X-Nautilus-Outcome': 'unknown' };
	it('an unknown start is retried under the SAME requestId, never a new one', async () => {
		const { sent, client } = fake([text('x', 504), json({ started: true })]);
		const s = new SubstituteSession(client);
		await s.start('T1', 5, 60);
		expect(s.pending().length).toBe(1);
		const r = await s.retry('T1');
		expect(r?.ok).toBe(true);
		expect(sent[1].body?.requestId).toBe(sent[0].body?.requestId);
		expect(s.pending().length).toBe(0);
	});
	it('a fresh start of a tag with an unknown start pending is blocked and sends nothing', async () => {
		const { sent, client } = fake([text('x', 503, U)]);
		const s = new SubstituteSession(client);
		await s.start('T1', 5, 60);
		const r = await s.start('t1', 5, 60);
		expect(r.ok).toBe(false);
		if (!r.ok) expect(r.kind).toBe('pending');
		expect(sent.length).toBe(1);
	});
	it('another tag is not blocked', async () => {
		const { sent, client } = fake([text('x', 503, U), json({})]);
		const s = new SubstituteSession(client);
		await s.start('T1', 5, 60);
		const r = await s.start('T2', 5, 60);
		expect(r.ok).toBe(true);
		expect(sent.length).toBe(2);
	});
	it('a definite answer, a dismissal or seeing the entry clears the block', async () => {
		const { client } = fake([text('x', 503, U), text('requestId reused', 409, { 'X-Nautilus-Outcome': 'refused' }), text('x', 503, U), text('x', 503, U)]);
		const s = new SubstituteSession(client);
		await s.start('T1', 5, 60);
		await s.retry('T1'); // a certain 409 on the retry
		expect(s.pending().length).toBe(0);
		await s.start('T1', 5, 60);
		s.dismiss('T1');
		expect(s.pending().length).toBe(0);
		await s.start('T1', 5, 60);
		const id = s.pending()[0].requestId;
		s.reconcile([{ id: 9, name: 'T1', value: 5, startMs: 0, untilMs: 0, remainingMs: 0, requestId: id }]);
		expect(s.pending().length).toBe(0);
	});
	it('a re-read that shows the pending start LAPSED clears the block and reports it (#710)', async () => {
		const { client } = fake([text('x', 503, U), text('x', 503, U)]);
		const s = new SubstituteSession(client);
		await s.start('T1', 5, 60);
		await s.start('T2', 5, 60);
		const id = s.pending().find((p) => p.name === 'T1')!.requestId;
		const gone = s.reconcile([], [{ id: 7, name: 'T1', value: 5, requestId: id, proposedMs: 0, atMs: 0, note: 'lapsed (outcome was unknown; not started)' }]);
		expect(gone.length).toBe(1);
		expect(gone[0].name).toBe('T1');
		expect(s.pending().length).toBe(1);
		expect(s.pending()[0].name).toBe('T2');
		// A lapse with another requestId, or none, clears nothing.
		expect(s.reconcile([], [{ id: 8, name: 'T2', value: 5, proposedMs: 0, atMs: 0, note: 'x' }]).length).toBe(0);
		expect(s.pending().length).toBe(1);
	});
	it('a fresh action after a certain refusal gets a new requestId', async () => {
		const { sent, client } = fake([text('no leader', 503, { 'X-Nautilus-Outcome': 'refused' }), json({})]);
		const s = new SubstituteSession(client);
		await s.start('T1', 5, 60);
		await s.start('T1', 5, 60);
		expect(sent[0].body?.requestId).not.toBe(sent[1].body?.requestId);
	});
});

describe('kept:false', () => {
	it('a success with kept:false carries its warning to the operator', async () => {
		const { client } = fake([json({ kept: false, warning: 'may still be active on the next leader' })]);
		const r = await client.stop(1);
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
		const r = await client.stop(1);
		if (!r.ok) throw new Error('ok expected');
		expect(keptText(r)?.includes('not saved')).toBe(true);
	});
});

describe('stop / ack / list', () => {
	it('stop DELETEs the escaped id; ack POSTs ids', async () => {
		const { sent, client } = fake([json({ kept: true }), json({ kept: true })]);
		await client.stop(7);
		await client.ack([7, 8]);
		expect(sent[0].url).toBe('/api/substitutes/7');
		expect(sent[0].method).toBe('DELETE');
		expect(sent[1].url).toBe('/api/substitutes/ack');
		expect(sent[1].body).toEqual({ ids: [7, 8] });
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
		const e = (id: number, ackOnStart: boolean, acked: boolean) => ({ id, name: String(id), value: 1, startMs: 0, untilMs: 0, remainingMs: 0, ackOnStart, acked });
		expect(unacked([e(1, true, false), e(2, true, true), e(3, false, false)]).map((s) => s.id)).toEqual([1]);
	});
	it('validates what the server would refuse', () => {
		const rule = { tag: 'T', maxMs: 600_000, operatorValue: true };
		expect(validateStart(undefined, 1, 5)).toBe('Pick a tag.');
		expect(validateStart(rule, 1, 0)).toBe('Enter a duration in seconds.');
		expect(validateStart(rule, 1, 601)?.startsWith('At most')).toBe(true);
		// No value on an operator-value rule is fine: the rule's own is held.
		expect(validateStart(rule, undefined, 5)).toBeNull();
		expect(validateStart(rule, 1, 600)).toBeNull();
		expect(validateStart({ tag: 'T' }, undefined, 5)).toBeNull();
	});
});
