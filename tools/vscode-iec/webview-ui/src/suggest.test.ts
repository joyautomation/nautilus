/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { replaceTailWord, tailWord } from './suggest.ts';

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
