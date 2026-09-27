// Unit tests for binding evaluation (src/lib/bindings.ts).
import { describe, expect, it } from './harness.js';
import { resolveNodeBindings, bindingsGood, readPath, member, num, flowing } from '../src/lib/bindings.js';

const tags = {
	P101: { Running: true, Fault: false, Speed: 62.5 },
	T101: { Level: 48.2, TempC: 21 },
	Demand: 50,
	Flat_Bool: false
};

describe('readPath', () => {
	it('reads a top-level tag and a member', () => {
		expect(readPath(tags, 'Demand')).toBe(50);
		expect(readPath(tags, 'P101.Speed')).toBe(62.5);
	});
	it('is undefined for a missing tag, a missing member, or a path through a scalar', () => {
		expect(readPath(tags, 'P999')).toBe(undefined);
		expect(readPath(tags, 'P101.Amps')).toBe(undefined);
		expect(readPath(tags, 'Demand.X')).toBe(undefined);
		expect(readPath(undefined, 'Demand')).toBe(undefined);
	});
});

describe('resolveNodeBindings', () => {
	it('is the mimic grammar: prop -> tag, absent tag leaves the prop unset', () => {
		expect(resolveNodeBindings({ cmd: 'Demand', amps: 'P101.Amps' }, tags)).toEqual({ cmd: 50 });
	});
	it('walks dotted paths', () => {
		expect(resolveNodeBindings({ speed: 'P101.Speed', level: 'T101.Level' }, tags)).toEqual({ speed: 62.5, level: 48.2 });
	});
	it('negates with a leading !', () => {
		expect(resolveNodeBindings({ ok: '!P101.Fault', off: '!P101.Running', flat: '!Flat_Bool' }, tags)).toEqual({
			ok: true,
			off: false,
			flat: true
		});
	});
	it('negation of a non-boolean is true (not === true), as in the mimic', () => {
		expect(resolveNodeBindings({ x: '!Demand' }, tags)).toEqual({ x: true });
	});
	it('handles no map and no tags', () => {
		expect(resolveNodeBindings(undefined, tags)).toEqual({});
		expect(resolveNodeBindings({ a: 'Demand' }, undefined)).toEqual({});
	});
});

describe('bindingsGood', () => {
	const good = (t: string) => t !== 'P101';
	it('is good when every root is good', () => {
		expect(bindingsGood({ a: 'Demand', b: 'T101.Level' }, good)).toBe(true);
		expect(bindingsGood(undefined, good)).toBe(true);
	});
	it('is bad when any root is bad, through a dotted or negated ref', () => {
		expect(bindingsGood({ a: 'Demand', b: 'P101.Speed' }, good)).toBe(false);
		expect(bindingsGood({ a: '!P101.Fault' }, good)).toBe(false);
	});
});

describe('member / num / flowing', () => {
	it('member reads off an object only', () => {
		expect(member(tags.P101, 'Speed')).toBe(62.5);
		expect(member(50, 'Speed')).toBe(undefined);
		expect(member(undefined, 'Speed')).toBe(undefined);
	});
	it('num falls back on anything not a finite number', () => {
		expect(num(3)).toBe(3);
		expect(num('3')).toBe(0);
		expect(num(NaN, 7)).toBe(7);
		expect(num(undefined, 1)).toBe(1);
	});
	it('flowing: booleans as-is, numbers above 2', () => {
		expect(flowing(true)).toBe(true);
		expect(flowing(false)).toBe(false);
		expect(flowing(2)).toBe(false);
		expect(flowing(2.5)).toBe(true);
		expect(flowing('yes')).toBe(false);
	});
});
