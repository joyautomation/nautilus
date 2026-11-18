// node --test --experimental-strip-types src/lib/driver.test.ts (npm test)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { driverLabel } from './driver.ts';

test('a field driver shows its kind and state', () => {
	assert.equal(driverLabel({ kind: 'optommp', state: 'connected' }, true), 'optommp connected');
});

test('a frame with no driver is the simulated build, not a screen still waiting', () => {
	assert.equal(driverLabel(undefined, true), 'none (simulated)');
});

test('before the first frame the row is blank', () => {
	assert.equal(driverLabel(undefined, false), '—');
});
