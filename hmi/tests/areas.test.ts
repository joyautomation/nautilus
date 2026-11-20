// What a control may do (#708 PR 7b). The table is the controller's: the
// same rows are checked against its own matcher in auth/areamatch_test.go,
// so the kit greys out exactly what the controller would refuse.
import { describe, it, expect } from './harness.js';
import { readFileSync } from 'node:fs';
import { areaMatch, can, deniedReason, pathMatch, tagOf, whoIs } from '../src/lib/areas.js';
import type { Me } from '../src/lib/auth.js';
import { asksPassword, deniedFor, setAuthView, type AuthView } from '../src/lib/authview.js';

const table = JSON.parse(readFileSync('tests/areamatch.json', 'utf8')) as {
	cases: { pattern: string; target: string; want: boolean }[];
	targets: { target: string; want: boolean }[];
};

describe('area patterns, as the controller matches them', () => {
	it('every row of the shared table', () => {
		if (table.cases.length < 30) throw new Error('the table was cut');
		const wrong = table.cases.filter((c) => areaMatch(c.pattern, c.target) !== c.want);
		if (wrong.length) throw new Error('disagrees with the controller on ' + JSON.stringify(wrong));
	});
	it('tagOf cuts as the controller does', () => {
		expect(tagOf(' Pump_1.Speed ')).toBe('pump_1');
		expect(tagOf('ARR[3].x')).toBe('arr');
		expect(tagOf('.member')).toBe('');
	});
	it('path.Match edge cases', () => {
		expect(pathMatch('a*', 'a/b')).toBe(false);
		expect(pathMatch('*', '')).toBe(true);
		expect(pathMatch('[', 'x')).toBe(false);
		expect(pathMatch('ab', 'a')).toBe(false);
	});
});

const operator: Me = {
	auth: true,
	secure: true,
	principal: 'user:jsmith',
	roles: ['operator'],
	permissions: { '*': ['read'], roasting: ['write', 'ack', 'shelve'] },
	areas: { roasting: ['rst_*', 'burner_*'] },
	signIn: true
};

describe('can: what a control may do', () => {
	it('in an area the user holds it in, and nowhere else', () => {
		expect(can(operator, 'write', 'RST_TEMP_SP')).toBe(true);
		expect(can(operator, 'write', 'Burner_2.Enable')).toBe(true);
		expect(can(operator, 'write', 'TANK1_SP')).toBe(false);
		expect(can(operator, 'force', 'RST_TEMP_SP')).toBe(false);
	});
	it('the whole controller only from an everywhere grant (ack all, config)', () => {
		expect(can(operator, 'ack')).toBe(false);
		expect(can(operator, 'read')).toBe(true);
		const engineer: Me = { ...operator, principal: 'user:akhan', roles: ['engineer'], permissions: { '*': ['read', 'write', 'ack', 'config'] }, areas: undefined };
		expect(can(engineer, 'ack')).toBe(true);
		expect(can(engineer, 'config')).toBe(true);
		expect(can(engineer, 'write', 'ANY_TAG')).toBe(true);
	});
	it('a target that names no tag is refused, as the controller refuses it', () => {
		const admin: Me = { ...operator, permissions: { '*': ['write'] } };
		for (const t of ['.x', '[0]', ' ', 'PUMP_*', 'PUMP_?']) expect(can(admin, 'write', t)).toBe(false);
	});
	it('without auth (or without /api/me) everything is allowed, as before', () => {
		expect(can(null, 'write', 'X')).toBe(true);
		expect(can({ ...operator, auth: false, permissions: {} }, 'force', 'X')).toBe(true);
	});
	it('anonymous holds nothing', () => {
		const anon: Me = { auth: true, secure: true, principal: 'anonymous', roles: [], permissions: {}, signIn: true };
		expect(can(anon, 'write', 'RST_1')).toBe(false);
	});
	it('says who and what, in words', () => {
		expect(deniedReason(operator, 'write', 'TANK1_SP')).toBe('jsmith (operator) does not hold write on TANK1_SP');
		expect(deniedReason({ ...operator, principal: 'anonymous', roles: [] }, 'ack')).toBe('sign in to ack');
		expect(whoIs({ ...operator, principal: 'token:scada' })).toBe('token scada');
		expect(whoIs({ ...operator, principal: 'token' })).toBe('the controller token');
	});
});

describe("what a target may name at all (the table's targets, as Policy.Can)", () => {
	it('every row, under an area that takes in every tag', () => {
		const every: Me = { auth: true, secure: true, principal: 'user:u', roles: ['operator'], permissions: { every: ['write'] }, areas: { every: ['*'] } };
		if (table.targets.length < 10) throw new Error('the targets table was cut');
		const wrong = table.targets.filter((c) => can(every, 'write', c.target) !== c.want);
		if (wrong.length) throw new Error('disagrees with the controller on ' + JSON.stringify(wrong));
	});
	it('and under an everywhere grant too (a pattern is never a tag)', () => {
		const admin: Me = { auth: true, secure: true, principal: 'token', roles: ['administrator'], permissions: { '*': ['write'] } };
		for (const c of table.targets) expect(can(admin, 'write', c.target)).toBe(c.want);
	});
});

describe('deniedFor and asksPassword: what a control asks (behaviour, not source)', () => {
	// One spec: the view is module state, and specs run concurrently.
	it('follows the installed view', () => {
		const view: { v: AuthView | null } = { v: null };
		setAuthView(() => view.v);
		try {
			// No createAuth() started: allowed, never asks.
			expect(deniedFor('write', 'TANK1_SP')).toBe('');
			expect(asksPassword('config')).toBe(false);

			view.v = { me: operator, supported: true, clock: 0, skew: 0 };
			expect(deniedFor('write', 'RST_TEMP_SP')).toBe('');
			expect(deniedFor('write', 'TANK1_SP')).toBe('jsmith (operator) does not hold write on TANK1_SP');
			expect(deniedFor('ack')).toBe('jsmith (operator) does not hold ack');
			expect(deniedFor('write', 'a]b')).toBe('jsmith (operator) does not hold write on a]b');

			// A controller without /api/me: the kit does nothing.
			view.v = { me: operator, supported: false, clock: 0, skew: 0 };
			expect(deniedFor('write', 'TANK1_SP')).toBe('');

			// The password badge, by the controller's clock.
			const eng: Me = {
				...operator,
				principal: 'user:akhan',
				permissions: { '*': ['read', 'config'] },
				session: { expires: '2026-11-20T20:00:00Z', elevatedUntil: '2026-11-20T09:15:00Z' },
				elevated: ['config']
			};
			const at = Date.parse('2026-11-20T09:00:00Z');
			view.v = { me: eng, supported: true, clock: at, skew: 0 };
			expect(asksPassword('config')).toBe(false);
			view.v = { me: eng, supported: true, clock: at + 16 * 60_000, skew: 0 };
			expect(asksPassword('config')).toBe(true);
			view.v = { me: eng, supported: true, clock: at + 16 * 60_000, skew: -20 * 60_000 }; // the browser runs fast
			expect(asksPassword('config')).toBe(false);
			view.v = { me: eng, supported: false, clock: at + 16 * 60_000, skew: 0 };
			expect(asksPassword('config')).toBe(false);
		} finally {
			setAuthView(() => null);
		}
	});
});
