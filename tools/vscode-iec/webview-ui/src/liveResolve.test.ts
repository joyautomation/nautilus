// Live-value resolution on Logix rungs, headless: `naut logix serve` holds
// a program tag as MainProgram_Counts while the rung says Counts.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { resolveLabel, resolveScoped } from './liveResolve.ts';

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
