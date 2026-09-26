/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { argIdents } from './ladder.ts';

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
