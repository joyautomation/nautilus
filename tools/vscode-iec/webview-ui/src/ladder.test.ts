/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { argIdents, annotate, useTypes, offerType, type LdRung } from './ladder.ts';

test('argIdents: the right side of := and =>, never the pin names', () => {
	assert.deepEqual(argIdents('Reset := ResetFaults, Run => MotorRun, Fault => P101_Fault'), ['ResetFaults', 'MotorRun', 'P101_Fault']);
});

test('argIdents: positional args, members, indexes and expressions', () => {
	assert.deepEqual(argIdents('Level, 3.0'), ['Level']);
	assert.deepEqual(argIdents('IN := t1.Q AND NOT Stop, PV := Levels[i] * 2'), ['t1', 'Stop', 'Levels', 'i']);
});

test('argIdents: literals, function names, placeholders and keywords are not variables', () => {
	assert.deepEqual(argIdents("PT := T#5S, PV := 16#FF, SP := LIMIT(0.0, Sp, 100.0), X := _, B := TRUE, S := 'txt'"), ['Sp']);
	assert.deepEqual(argIdents(''), []);
});

const rung = (elements: LdRung['elements'], coils: LdRung['coils'] = []): LdRung => ({ name: 'r', line: 1, elements, coils });
const CTU = { name: 'CTU', prefix: 'c', pins: [{ name: 'CU', type: 'BOOL', dir: 'in' as const }, { name: 'PV', type: 'INT', dir: 'in' as const }, { name: 'CV', type: 'INT', dir: 'out' as const }] };

test('useTypes: a counter pin types what it binds; a contact or coil is BOOL (#219)', () => {
	const t = useTypes(
		[rung([{ kind: 'contact', ref: 'Run' }, { kind: 'fb', inst: 'c1', type: 'CTU', args: 'R := Reset, PV := Preset, CV => Starts' }], [{ kind: 'coil', ref: 'Done' }])],
		[CTU]
	);
	assert.equal(t.get('starts'), 'INT');
	assert.equal(t.get('preset'), 'INT');
	assert.equal(t.get('run'), 'BOOL');
	assert.equal(t.get('done'), 'BOOL');
	assert.equal(t.get('reset'), undefined, 'R is not in this catalog');
});

test('useTypes: a comparison types a name from its other operand, a pin wins over a contact', () => {
	const t = useTypes(
		[
			rung([{ kind: 'fn', fn: 'GE', args: 'State, INT#3' }, { kind: 'fn', fn: 'GT', args: 'Temp, 90.0' }, { kind: 'fn', fn: 'EQ', args: 'Mode, Want' }, { kind: 'fn', fn: 'EQ', args: 'Any, 3' }]),
			rung([{ kind: 'contact', ref: 'Count' }, { kind: 'fb', inst: 'c', type: 'CTU', args: 'CV => Count' }])
		],
		[CTU],
		[{ name: 'Want', type: 'DINT', section: 'VAR', line: 1 }]
	);
	assert.equal(t.get('state'), 'INT');
	assert.equal(t.get('temp'), 'REAL');
	assert.equal(t.get('mode'), 'DINT');
	assert.equal(t.get('any'), undefined, 'an untyped integer says nothing');
	assert.equal(t.get('count'), 'INT');
});

test('offerType: a use beats a seed-inferred REAL; a UDT or BOOL from the manifest stands', () => {
	assert.equal(offerType('INT', 'REAL'), 'INT');
	assert.equal(offerType(undefined, 'REAL'), 'REAL');
	assert.equal(offerType('BOOL', 'REAL'), 'REAL');
	assert.equal(offerType('INT', 'MotorData'), 'MotorData');
	assert.equal(offerType('INT', undefined), 'INT');
	assert.equal(offerType(undefined, undefined), 'BOOL');
});

test('annotate: an edge contact is its one-shot Q when that streams, else ruled out by its input (#212)', () => {
	const P = { kind: 'edge' as const, ref: 'Btn', mode: 'P', trig: 'rt_r_Btn' };
	const N = { kind: 'edge' as const, ref: 'Btn', mode: 'N', trig: 'ft_r_Btn' };
	const vals = (v: Record<string, unknown>) => (l: string) => v[l];
	assert.equal(annotate([P], true, vals({ 'rt_r_Btn.Q': true, Btn: true }))[0].out, true);
	assert.equal(annotate([P], true, vals({ 'rt_r_Btn.Q': false, Btn: true }))[0].out, false);
	assert.equal(annotate([P], true, vals({ Btn: false }))[0].out, false, 'a rising edge cannot fire on FALSE');
	assert.equal(annotate([P], true, vals({ Btn: true }))[0].out, undefined, 'held TRUE: unknown without Q');
	assert.equal(annotate([N], true, vals({ Btn: true }))[0].out, false, 'a falling edge cannot fire on TRUE');
	assert.equal(annotate([P], false, vals({ 'rt_r_Btn.Q': true }))[0].out, false, 'no power in, none out');
});
