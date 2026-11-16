// Live-value resolution on Logix rungs, headless: `naut logix serve` holds
// a program tag as MainProgram_Counts while the rung says Counts.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fbAddress, forcedLabel, resolveLabel, resolveScoped } from './liveResolve.ts';
import { annotate, type LdElement } from './ladder.ts';

// Top-level keys arrive lowercased, as the extension sends them.
const values: Record<string, unknown> = {
	counts: 1,
	mainprogram_counts: 17,
	levelpct: 42.5,
	p101: { Run: true, Speed: 30 },
	mainprogram_recipe: [10, 20, 30],
	mainprogram_cfg: { Gains: [0.5, 1.5] }
};
const main = 'Program:MainProgram';

test('a program tag is found by the name the rung uses', () => {
	assert.equal(resolveScoped(values, main, 'Counts'), 17);
});

test('a program tag shadows the controller tag of the same name', () => {
	assert.equal(resolveScoped(values, main, 'Counts'), 17);
	assert.equal(resolveScoped(values, 'Program:Other', 'Counts'), 1);
});

test('controller tags and their members resolve from a program routine', () => {
	assert.equal(resolveScoped(values, main, 'LevelPct'), 42.5);
	assert.equal(resolveScoped(values, main, 'P101.Run'), true);
});

test('Logix arrays are zero-based, with or without a member in between', () => {
	assert.equal(resolveScoped(values, main, 'Recipe[0]'), 10);
	assert.equal(resolveScoped(values, main, 'Recipe[2]'), 30);
	assert.equal(resolveScoped(values, main, 'Cfg.Gains[1]'), 1.5);
	assert.equal(resolveScoped(values, main, 'Recipe[3]'), undefined);
});

test('a bad member on a program tag does not fall through to a controller tag', () => {
	const v = { ...values, mainprogram_p101: { Other: 1 } };
	assert.equal(resolveScoped(v, main, 'P101.Run'), undefined);
});

test('an AOI routine resolves nothing: its operands are per-instance', () => {
	assert.equal(resolveScoped(values, 'AOI:Valve', 'LevelPct'), undefined);
});

test('nautilus source keeps unknown bounds unresolved', () => {
	assert.equal(resolveLabel(values, {}, 'MainProgram_Recipe[0]'), undefined);
	assert.equal(resolveLabel(values, { mainprogram_recipe: [1] }, 'MainProgram_Recipe[1]'), 10);
});

test('forcedLabel: exact, under a whole-tag force, and a struct with a forced member', () => {
	const forced = { startpb: true, 'p101.speed': 55, tbl: [1, 2] };
	assert.equal(forcedLabel(forced, 'StartPB'), true);
	assert.equal(forcedLabel(forced, 'StartPBX'), false);
	assert.equal(forcedLabel(forced, 'P101.Speed'), true);
	assert.equal(forcedLabel(forced, 'P101'), true);
	assert.equal(forcedLabel(forced, 'P101.Run'), false);
	assert.equal(forcedLabel(forced, 'Tbl[1]'), true);
	assert.equal(forcedLabel({}, 'StartPB'), false);
});

// ── FUNCTION_BLOCK bodies: names resolve inside the monitored instance ───
// The PLC-Lab example's shape (examples/plclab): the ladder is TankCycle, a
// FUNCTION_BLOCK called once as `tank`, and the stream carries the instance
// whole, pins AND locals. A global that shares a pin's name (StartPB here,
// Running below) must never stand in for the instance's own value.
const plclab: Record<string, unknown> = {
	startpb: true,
	running: false,
	tank: { Start: true, Stop: true, Running: true, Fill: false, Drain: false, High: true, dwell: { IN: true, Q: true, ET: 3000, PT: 3000 }, hist: [1, 2, 3], i: 2 },
	idx: 0
};
const none = () => false;

test('a FUNCTION_BLOCK name is re-rooted at the monitored instance', () => {
	assert.equal(fbAddress('Running', 'tank', none), 'tank.Running');
	assert.equal(fbAddress('dwell.Q', 'tank', none), 'tank.dwell.Q');
	assert.equal(fbAddress('hist[i]', 'tank', none), 'tank.hist[tank.i]');
	assert.equal(fbAddress('hist[ 2 ]', 'tank', none), 'tank.hist[ 2 ]');
});

test('a VAR_EXTERNAL name in a FUNCTION_BLOCK body is the controller tag', () => {
	const ext = (n: string) => n.toLowerCase() === 'startpb' || n.toLowerCase() === 'idx';
	assert.equal(fbAddress('StartPB', undefined, ext), 'StartPB');
	assert.equal(fbAddress('hist[idx]', 'tank', ext), 'tank.hist[idx]');
});

test('with no monitored instance, a FUNCTION_BLOCK name has no value', () => {
	assert.equal(fbAddress('Running', undefined, none), undefined);
	assert.equal(fbAddress('dwell.Q', undefined, none), undefined);
});

test('TankCycle rungs light from the instance, internals included', () => {
	const resolve = (inst: string | undefined) => (label: string) => {
		const a = fbAddress(label, inst, none);
		return a === undefined ? undefined : resolveLabel(plclab, { 'tank.hist': [0] }, a);
	};
	// RUNG run: [ Start | Running ] Stop ( Running )
	const seal: LdElement[] = [
		{ kind: 'branch', legs: [[{ kind: 'contact', ref: 'Start' }], [{ kind: 'contact', ref: 'Running' }]] },
		{ kind: 'contact', ref: 'Stop' }
	];
	const lit = annotate(seal, true, resolve('tank'));
	assert.equal(lit[1].val, true, 'Stop is the instance pin');
	assert.equal(lit[1].out, true, 'power reaches the coil');
	// RUNG hold: High dwell:TON(PT := T#3S): the timer is a LOCAL of the block.
	const hold = annotate([{ kind: 'contact', ref: 'High' }, { kind: 'fb', inst: 'dwell', fbType: 'TON', powerOut: 'Q' } as LdElement], true, resolve('tank'));
	assert.equal(hold[0].val, true);
	assert.equal(hold[1].val, true, 'dwell.Q read from tank.dwell');
	// An array local, its index another local: hist[i] → tank.hist[tank.i].
	assert.equal(resolve('tank')('hist[i]'), 3);
	// Unmonitored, nothing shows — not the global Running (FALSE), not anything.
	const dark = annotate(seal, true, resolve(undefined));
	assert.equal(dark[1].val, undefined);
	assert.equal(resolve(undefined)('Running'), undefined);
});
