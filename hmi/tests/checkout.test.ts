// The I/O checkout client's contract (#719): a force value is typed the way the
// controller reads it, a standing force's countdown never claims a release the
// controller did not announce, a write whose answer never came is "unknown",
// and the page offers only what the point and the person allow.
import { describe, it, expect } from './harness.js';
import { CheckoutClient, forceValue, canIn, offersForce, countdown, clock, forceBanner, resultLine, needsNote, type CheckoutForce, type CheckoutPoint } from '../src/lib/checkout.js';
import type { Me } from '../src/lib/auth.js';

type Sent = { url: string; method: string; body: any };
function fake(answers: Array<Response | Error>) {
	const sent: Sent[] = [];
	const f = (async (url: string, init?: RequestInit) => {
		sent.push({ url, method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined });
		const a = answers.shift();
		if (!a) throw new Error('no more answers');
		if (a instanceof Error) throw a;
		return a;
	}) as unknown as typeof fetch;
	return { sent, client: new CheckoutClient({ fetch: f }) };
}
const json = (b: unknown, status = 200, headers: Record<string, string> = {}) => new Response(JSON.stringify(b), { status, headers });
const text = (b: string, status: number) => new Response(b, { status });

describe('forceValue', () => {
	it('sends a boolean for a digital output', () => {
		expect(forceValue('DO', 'on')).toEqual({ ok: true, value: true });
		expect(forceValue('DO', ' OFF ')).toEqual({ ok: true, value: false });
		expect(forceValue('DO', '1')).toEqual({ ok: true, value: true });
		expect(forceValue('DO', '0')).toEqual({ ok: true, value: false });
	});
	it('refuses what is not on or off, and never sends the text', () => {
		for (const t of ['', 'maybe', '5', 'truee']) expect(forceValue('DO', t).ok).toBe(false);
	});
	it('sends a finite number for an analog output', () => {
		expect(forceValue('AO', '5.5')).toEqual({ ok: true, value: 5.5 });
		expect(forceValue('AO', ' -3 ')).toEqual({ ok: true, value: -3 });
		expect(forceValue('AO', '0')).toEqual({ ok: true, value: 0 });
	});
	it('refuses a blank, NaN or infinite analog value (Number("") is 0)', () => {
		for (const t of ['', '  ', 'abc', 'NaN', 'Infinity', '-Infinity', '1e999']) expect(forceValue('AO', t).ok).toBe(false);
	});
	it('never forces an input', () => {
		expect(forceValue('DI', '1').ok).toBe(false);
		expect(forceValue('AI', '1').ok).toBe(false);
	});
});

describe('countdown', () => {
	const f = (remainingMs: number): CheckoutForce => ({ run: 'r', point: 'OUT', tag: 'Out', by: 'alice', remainingMs });
	it('counts down from the controller\'s figure by the time since that frame', () => {
		expect(countdown(f(10_000), 1000, 3000)).toEqual({ state: 'standing', remainingMs: 8000, ending: false });
	});
	it('says ending, never released, when the timer reaches zero but frames still say it stands', () => {
		expect(countdown(f(1000), 0, 2000)).toEqual({ state: 'standing', remainingMs: 0, ending: true });
	});
	it('says none only for a fresh frame without the field', () => {
		expect(countdown(undefined, 100, 200)).toEqual({ state: 'none' });
		expect(countdown(null, 100, 200)).toEqual({ state: 'none' });
	});
	it('says unknown when the stream has gone quiet, with or without a force', () => {
		expect(countdown(f(60_000), 0, 5000).state).toBe('unknown');
		expect(countdown(undefined, 0, 5000).state).toBe('unknown');
	});
	it('resyncs from every new frame (a longer figure puts time back)', () => {
		expect(countdown(f(30_000), 9000, 9500)).toEqual({ state: 'standing', remainingMs: 29_500, ending: false });
	});
	it('does not go negative when the clock reads before the frame', () => {
		expect(countdown(f(5000), 2000, 1000)).toEqual({ state: 'standing', remainingMs: 5000, ending: false });
	});
});

describe('clock and banner', () => {
	it('rounds up so it never reads 0:00 with time left', () => {
		expect(clock(65_000)).toBe('1:05');
		expect(clock(100)).toBe('0:01');
		expect(clock(0)).toBe('0:00');
	});
	it('names the tag, the person and the time; says unknown honestly; is empty when none', () => {
		const force = { run: 'r', point: 'OUT', tag: 'Out', by: 'alice', remainingMs: 61_000 };
		expect(forceBanner({ state: 'standing', remainingMs: 61_000, ending: false }, force)).toBe('Out is forced by alice for the I/O checkout: 1:01 left');
		expect(forceBanner({ state: 'standing', remainingMs: 0, ending: true }, force).includes('ending now')).toBe(true);
		expect(forceBanner({ state: 'unknown', remainingMs: 5000 }, force).includes('not known')).toBe(true);
		expect(forceBanner({ state: 'unknown', remainingMs: 0 }, null).includes('may be standing')).toBe(true);
		expect(forceBanner({ state: 'none' }, null)).toBe('');
	});
});

describe('who is offered what', () => {
	const me = (permissions: Record<string, string[]>): Me => ({ auth: true, secure: true, principal: 'user:a', roles: [], permissions });
	it('an area-scoped checkout holder is offered that area only', () => {
		const m = me({ RCV: ['checkout'] });
		expect(canIn(m, 'checkout', 'RCV')).toBe(true);
		expect(canIn(m, 'checkout', 'SHP')).toBe(false);
		expect(canIn(m, 'checkout', undefined)).toBe(false);
	});
	it('a wildcard holder is offered every area; no /api/me offers nothing', () => {
		expect(canIn(me({ '*': ['checkout'] }), 'checkout', 'SHP')).toBe(true);
		expect(canIn(null, 'checkout', 'RCV')).toBe(false);
	});
	it('offers a force on a forced output only', () => {
		const p = (kind: any, method: any): CheckoutPoint => ({ id: 'x', tag: 'x', kind, method });
		expect(offersForce(p('DO', 'force'))).toBe(true);
		expect(offersForce(p('AO', 'force'))).toBe(true);
		expect(offersForce(p('DO', 'observe'))).toBe(false);
		expect(offersForce(p('DI', 'force'))).toBe(false);
	});
});

describe('results', () => {
	it('shows the controller\'s reading and quality, not a page value', () => {
		const line = resultLine({ run: 'r', seq: 1, at: 0, point: 'P', outcome: 'pass', seen: 42.5, quality: 'bad', by: 'alice' });
		expect(line).toBe('Pass by alice, read 42.5 (bad)');
		expect(resultLine({ run: 'r', seq: 1, at: 0, point: 'P', outcome: 'fail', by: 'bob', note: 'stuck' })).toBe('Fail by bob, no reading: stuck');
	});
	it('a fail or a note says why', () => {
		expect(needsNote('pass')).toBe(false);
		expect(needsNote('fail')).toBe(true);
		expect(needsNote('note')).toBe(true);
	});
});

describe('CheckoutClient', () => {
	it('sends the force typed, with the typed confirmation', async () => {
		const { sent, client } = fake([json({ ok: true }, 201)]);
		const a = await client.force('r1', 'OUT', 5.5, 30, 'OUT');
		expect(a.ok).toBe(true);
		expect(sent[0].url).toBe('/api/checkout/runs/r1/force');
		expect(sent[0].body).toEqual({ point: 'OUT', value: 5.5, seconds: 30, confirm: 'OUT' });
		expect(typeof sent[0].body.value).toBe('number');
	});
	it('says a write whose answer never came may have been recorded', async () => {
		const { client } = fake([new Error('network')]);
		const a = await client.record('r1', 'P', 'pass');
		expect(a.ok).toBe(false);
		if (!a.ok) {
			expect(a.outcome).toBe('unknown');
			expect(a.message.includes('may have been recorded')).toBe(true);
		}
	});
	it('a read that failed is down, not unknown', async () => {
		const { client } = fake([new Error('network')]);
		const a = await client.view();
		if (!a.ok) expect(a.outcome).toBe('down');
		else throw new Error('should fail');
	});
	it('carries the controller\'s own words for a refusal', async () => {
		const { client } = fake([json({ error: 'take Pump1 out of service first' }, 409)]);
		const a = await client.force('r1', 'OUT', true, 30, 'OUT');
		expect(a.ok).toBe(false);
		if (!a.ok) {
			expect(a.status).toBe(409);
			expect(a.outcome).toBe('refused');
			expect(a.message).toBe('take Pump1 out of service first');
		}
	});
	it('uses the fixed texts for a bare 401 and 403', async () => {
		const { client } = fake([text('', 403), text('', 401)]);
		const a = await client.begin(false);
		const b = await client.begin(false);
		if (!a.ok) expect(a.message).toBe('not permitted');
		if (!b.ok) expect(b.message).toBe('sign in, or send a token');
	});
	it('refuses a body it does not understand rather than showing blanks', async () => {
		const { client } = fake([json({ nope: 1 })]);
		const a = await client.view();
		expect(a.ok).toBe(false);
	});
	it('a release posts to the run\'s release route', async () => {
		const { sent, client } = fake([json({ ok: true })]);
		await client.release('r1');
		expect(sent[0]).toEqual({ url: '/api/checkout/runs/r1/force/release', method: 'POST', body: {} });
	});
});
