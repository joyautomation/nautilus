// Equipment out of service in the HMI kit (#715 PR 5, #616): the badge, the
// request rules, the order of the list, and what goes over the wire.
import { describe, it, expect } from './harness.js';
import {
	OOS_MAX_SECONDS,
	badgeText,
	checkOutOfService,
	equipmentChoices,
	listOutOfService,
	returnToService,
	soonestFirst,
	suppressionBadge,
	takeOutOfService,
	timeLeft,
	type OutOfService
} from '../src/lib/outofservice.js';

const win = (equipment: string, untilMs: number): OutOfService => ({ equipment, by: 'alice', reason: 'work', startMs: 0, untilMs, alarms: [] });

describe('the badge on a suppressed alarm', () => {
	it('says out of service with why, who and until', () => {
		const b = suppressionBadge({ state: 'suppressed', suppression: 'out-of-service', reason: 'seal job', suppressedBy: 'alice', suppressUntilMs: 5000 });
		expect(b?.label).toBe('Out of service');
		expect(b?.why).toBe('seal job');
		expect(b?.who).toBe('alice');
		expect(b?.untilMs).toBe(5000);
		expect(badgeText(b!, (ms) => `T${ms}`)).toBe('Out of service · seal job · by alice · until T5000');
	});
	it('keeps only the person\'s words from the controller\'s reason line', () => {
		const reason = 'out of service until 2026-11-22T12:58:18Z by alice: probe swap: WO 12';
		expect(suppressionBadge({ state: 'suppressed', suppression: 'out-of-service', reason })?.why).toBe('probe swap: WO 12');
		expect(suppressionBadge({ state: 'suppressed', suppression: 'out-of-service', reason: 'plain' })?.why).toBe('plain');
	});
	it('names the other kinds without a who or an until', () => {
		const b = suppressionBadge({ state: 'suppressed', suppression: 'substituted', reason: 'signal substituted: P1.Run', suppressedBy: 'x', suppressUntilMs: 9 });
		expect(b?.label).toBe('Signal substituted');
		expect(b?.who).toBe('');
		expect(b?.untilMs).toBe(0);
	});
	it('is not there for an alarm that is not suppressed', () => {
		expect(suppressionBadge({ state: 'unack-active', suppression: 'out-of-service' })).toBe(null);
	});
});

describe('the request', () => {
	it('needs equipment, a duration within a week and a one-line reason', () => {
		expect(checkOutOfService('', 60, 'x')).toBe('choose the equipment');
		expect(checkOutOfService('P1', 0, 'x')).toBe('choose how long');
		expect(checkOutOfService('P1', OOS_MAX_SECONDS + 1, 'x').includes('7 days')).toBe(true);
		expect(checkOutOfService('P1', 60, '  ')).toBe('a reason is required');
		expect(checkOutOfService('P1', 60, 'a\nb')).toBe('the reason is one line');
		expect(checkOutOfService('P1', 60, 'x'.repeat(201)).includes('200')).toBe(true);
		expect(checkOutOfService('P1', OOS_MAX_SECONDS, 'x'.repeat(200))).toBe('');
	});
});

describe('the lists', () => {
	it('puts the soonest to end first', () => {
		expect(soonestFirst([win('B', 30), win('A', 30), win('C', 10)]).map((w) => w.equipment).join()).toBe('C,A,B');
	});
	it('chooses equipment once, skipping the controller\'s own', () => {
		const got = equipmentChoices([{ equipment: 'Pump_1' }, { equipment: 'pump_1' }, { equipment: 'Fan' }, { equipment: '__nautilus' }, { equipment: 'nautilus.audit' }, {}]);
		expect(got.join()).toBe('Fan,Pump_1');
	});
	it('says how long is left', () => {
		expect(timeLeft(90_000, 0)).toBe('1m');
		expect(timeLeft(3 * 3600_000 + 5 * 60_000, 0)).toBe('3h 5m');
		expect(timeLeft(2 * 86400_000 + 3 * 3600_000, 0)).toBe('2d 3h');
		expect(timeLeft(0, 10)).toBe('0s');
	});
});

describe('what goes over the wire', () => {
	// each test gets its own recorder: the tests run side by side
	const reply = (status: number, body: unknown, seen: { url: string; init?: RequestInit }[] = []) => async (url: RequestInfo | URL, init?: RequestInit) => {
		seen.push({ url: String(url), init });
		return new Response(typeof body === 'string' ? body : JSON.stringify(body), { status });
	};
	it('posts the equipment, seconds and a trimmed reason to out-of-service', async () => {
		const seen: { url: string; init?: RequestInit }[] = [];
		const r = await takeOutOfService('/api/alarms', { equipment: ' P1 ', seconds: 3600, reason: ' seal ', by: 'alice' }, undefined, reply(200, {}, seen));
		expect(r.ok).toBe(true);
		expect(seen[0].url).toBe('/api/alarms/out-of-service');
		expect(seen[0].init?.method).toBe('POST');
		expect(JSON.parse(String(seen[0].init?.body))).toEqual({ equipment: 'P1', seconds: 3600, reason: 'seal', by: 'alice' });
	});
	it('does not ask the controller what it would refuse', async () => {
		const seen: { url: string; init?: RequestInit }[] = [];
		const r = await takeOutOfService('/api/alarms', { equipment: 'P1', seconds: 3600, reason: '', by: 'a' }, undefined, reply(200, {}, seen));
		expect(r.ok).toBe(false);
		expect(seen.length).toBe(0);
	});
	it('carries a refusal in the controller\'s words', async () => {
		const r = await takeOutOfService('/api/alarms', { equipment: 'P1', seconds: 60, reason: 'x', by: 'a' }, undefined, reply(403, { error: 'not permitted over Pump_2.Fault' }));
		expect(r.ok ? '' : r.message).toBe('not permitted over Pump_2.Fault');
	});
	it('posts in-service with the equipment', async () => {
		const seen: { url: string; init?: RequestInit }[] = [];
		await returnToService('/api/alarms', { equipment: 'P1', by: 'alice' }, undefined, reply(200, {}, seen));
		expect(seen[0].url).toBe('/api/alarms/in-service');
		expect(JSON.parse(String(seen[0].init?.body))).toEqual({ equipment: 'P1', by: 'alice' });
	});
	it('lists the windows soonest first, and null when the read fails', async () => {
		const l = await listOutOfService('/api/alarms', {}, reply(200, [win('B', 20), win('A', 10)]));
		expect(l?.map((w) => w.equipment).join()).toBe('A,B');
		expect(await listOutOfService('/api/alarms', {}, reply(500, 'x'))).toBe(null);
		expect((await listOutOfService('/api/alarms', {}, reply(200, 'null')))?.length).toBe(0);
	});
});
