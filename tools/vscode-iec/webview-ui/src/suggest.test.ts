/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fbCatalog, fbOutputRefs, filterItems, functionItems, openArgs, replaceTailWord, tailWord } from './suggest.ts';

test('tailWord: the word after an SFC action qualifier', () => {
	assert.equal(tailWord('N Pu'), 'Pu');
	assert.equal(tailWord('N '), '');
	assert.equal(tailWord('Pump'), 'Pump');
});

test('replaceTailWord: keeps the qualifier, defaults one when missing', () => {
	assert.equal(replaceTailWord('N Pu', 'PumpRun'), 'N PumpRun');
	assert.equal(replaceTailWord('S  ', 'AlarmHorn'), 'S  AlarmHorn');
	assert.equal(replaceTailWord('Pu', 'PumpRun'), 'N PumpRun');
});

// ── the FBD palette's block catalog ─────────────────────────────────────────

test('fbCatalog: the model catalog wins; an older CLI falls back to the standard names (PID too)', () => {
	const pid = { name: 'PID', prefix: 'pid', pins: [{ name: 'AUTO', type: 'BOOL', dir: 'in' as const }] };
	assert.deepEqual(fbCatalog([pid]), [pid]);
	const fallback = fbCatalog(undefined);
	assert.ok(fallback.some((t) => t.name === 'PID' && t.prefix === 'pid'));
	assert.ok(fallback.some((t) => t.name === 'TON'));
});

test('openArgs: every input and in-out open, outputs never bound', () => {
	assert.equal(
		openArgs({
			name: 'Starter',
			prefix: 's',
			pins: [
				{ name: 'Req', type: 'BOOL', dir: 'in' },
				{ name: 'Hours', type: 'REAL', dir: 'inout' },
				{ name: 'Run', type: 'BOOL', dir: 'out' }
			]
		}),
		'Req := _, Hours := _'
	);
	assert.equal(openArgs({ name: 'PID', prefix: 'pid', args: 'AUTO := _, PV := _' }), 'AUTO := _, PV := _');
});

test('fbOutputRefs: each instance output is a source (lic.CV)', () => {
	const refs = fbOutputRefs([{ name: 'lic', type: 'PID', outs: ['CV', 'SAT_HI'] }]).map((r) => r.name);
	assert.deepEqual(refs, ['lic.CV', 'lic.SAT_HI']);
	assert.deepEqual(filterItems(fbOutputRefs([{ name: 'lic', outs: ['CV', 'ERR'] }]), 'lic.C').map((r) => r.name), ['lic.CV']);
});

test('openArgs: an input with a declared initial value stays unbound (#205)', () => {
	assert.equal(
		openArgs({
			name: 'Dosing',
			prefix: 'd',
			pins: [
				{ name: 'Start', type: 'BOOL', dir: 'in' },
				{ name: 'NoFlowTime', type: 'TIME', dir: 'in', init: 'T#5S' },
				{ name: 'Recipe', type: 'DoseRecipe', dir: 'inout' },
				{ name: 'Done', type: 'BOOL', dir: 'out' }
			]
		}),
		'Start := _, Recipe := _'
	);
});

test('functionItems: the project FUNCTIONs by declared name, then the standard ones (#204)', () => {
	const items = functionItems([{ name: 'ScaleAnalog', prefix: 's', detail: '(Raw, EngLo, EngHi) → REAL', result: 'REAL' }]);
	assert.deepEqual(items[0], { name: 'ScaleAnalog', detail: '(Raw, EngLo, EngHi) → REAL' });
	assert.ok(items.some((i) => i.name === 'LIMIT'));
	assert.deepEqual(filterItems(items, 'Sca').map((i) => i.name), ['ScaleAnalog']);
	assert.equal(functionItems(undefined).length, items.length - 1);
});
