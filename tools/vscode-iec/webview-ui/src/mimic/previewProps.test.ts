/// <reference types="node" />
// The props strip's value grammar — see previewProps.ts. Runs with
// `node --experimental-strip-types --test` (wired into the package's
// `npm test`).
import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { formatPropValue, isPropName, kindOf, parsePropInput } from './previewProps.ts';

test('parsePropInput reads JSON-looking text as JSON, the rest as a string', () => {
	assert.equal(parsePropInput('62'), 62);
	assert.equal(parsePropInput(' 0.5 '), 0.5);
	assert.equal(parsePropInput('true'), true);
	assert.equal(parsePropInput('false'), false);
	assert.equal(parsePropInput('null'), null);
	assert.deepEqual(parsePropInput('[4, 6, 5]'), [4, 6, 5]);
	assert.deepEqual(parsePropInput('{"a": 1}'), { a: 1 });
	assert.equal(parsePropInput('"P-101"'), 'P-101');
	assert.equal(parsePropInput('P-101'), 'P-101');
	assert.equal(parsePropInput('var(--s1)'), 'var(--s1)');
	assert.equal(parsePropInput(''), '');
	assert.equal(parsePropInput('   '), '');
});

test('formatPropValue round-trips through parsePropInput', () => {
	for (const v of [62, 0.5, true, null, [4, 6, 5], { a: 1, b: 'x' }, 'P-101', '']) {
		assert.deepEqual(parsePropInput(formatPropValue(v)), v);
	}
	assert.equal(formatPropValue(undefined), '');
});

test('kindOf names the badge', () => {
	assert.equal(kindOf(1), 'number');
	assert.equal(kindOf('x'), 'string');
	assert.equal(kindOf(true), 'boolean');
	assert.equal(kindOf(null), 'null');
	assert.equal(kindOf([1]), 'array');
	assert.equal(kindOf({}), 'object');
});

test('isPropName accepts identifiers only', () => {
	assert.ok(isPropName('levelPct'));
	assert.ok(isPropName('_x'));
	assert.ok(isPropName('$w'));
	assert.ok(!isPropName('level pct'));
	assert.ok(!isPropName('1st'));
	assert.ok(!isPropName(''));
	assert.ok(!isPropName('a-b'));
});
