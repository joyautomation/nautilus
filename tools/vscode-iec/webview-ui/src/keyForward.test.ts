/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decideKey, isTextField, keyAction, type KeyLike } from './keyForward.ts';

const k = (key: string, mods: Partial<KeyLike> = {}): KeyLike => ({
	key,
	ctrlKey: false,
	metaKey: false,
	shiftKey: false,
	altKey: false,
	...mods
});

test('keyAction: undo, redo (both chords), save — Ctrl or Cmd', () => {
	assert.equal(keyAction(k('z', { ctrlKey: true })), 'undo');
	assert.equal(keyAction(k('z', { metaKey: true })), 'undo');
	assert.equal(keyAction(k('Z', { ctrlKey: true, shiftKey: true })), 'redo');
	assert.equal(keyAction(k('Z', { metaKey: true, shiftKey: true })), 'redo');
	assert.equal(keyAction(k('y', { ctrlKey: true })), 'redo');
	assert.equal(keyAction(k('s', { ctrlKey: true })), 'save');
	assert.equal(keyAction(k('S', { metaKey: true })), 'save');
});

test('keyAction: everything else is not ours', () => {
	assert.equal(keyAction(k('z')), undefined);
	assert.equal(keyAction(k('z', { ctrlKey: true, altKey: true })), undefined);
	assert.equal(keyAction(k('S', { ctrlKey: true, shiftKey: true })), undefined); // Save As
	assert.equal(keyAction(k('Y', { ctrlKey: true, shiftKey: true })), undefined);
	assert.equal(keyAction(k('c', { ctrlKey: true })), undefined);
	assert.equal(keyAction(k('v', { metaKey: true })), undefined);
	assert.equal(keyAction(k('Delete')), undefined);
});

test('decideKey: preview panel forwards undo/redo/save from the canvas', () => {
	assert.deepEqual(decideKey(k('z', { ctrlKey: true }), false, true), { kind: 'forward', action: 'undo' });
	assert.deepEqual(decideKey(k('y', { ctrlKey: true }), false, true), { kind: 'forward', action: 'redo' });
	assert.deepEqual(decideKey(k('s', { metaKey: true }), false, true), { kind: 'forward', action: 'save' });
});

test('decideKey: a focused text field keeps its own undo/redo', () => {
	assert.deepEqual(decideKey(k('z', { ctrlKey: true }), true, true), { kind: 'native' });
	assert.deepEqual(decideKey(k('Z', { ctrlKey: true, shiftKey: true }), true, true), { kind: 'native' });
	// ... in a custom editor too, where the workbench would undo the document.
	assert.deepEqual(decideKey(k('z', { ctrlKey: true }), true, false), { kind: 'native' });
	// Save means nothing to a field: it still saves the document.
	assert.deepEqual(decideKey(k('s', { ctrlKey: true }), true, true), { kind: 'forward', action: 'save' });
	assert.deepEqual(decideKey(k('s', { ctrlKey: true }), true, false), { kind: 'pass' });
});

test('decideKey: custom editor leaves canvas keys to the workbench', () => {
	assert.deepEqual(decideKey(k('z', { ctrlKey: true }), false, false), { kind: 'pass' });
	assert.deepEqual(decideKey(k('s', { ctrlKey: true }), false, false), { kind: 'pass' });
	assert.deepEqual(decideKey(k('c', { ctrlKey: true }), false, true), { kind: 'pass' });
});

test('isTextField', () => {
	assert.equal(isTextField(null), false);
	assert.equal(isTextField({ tagName: 'DIV' }), false);
	assert.equal(isTextField({ tagName: 'BUTTON' }), false);
	assert.equal(isTextField({ tagName: 'input' }), true);
	assert.equal(isTextField({ tagName: 'TEXTAREA' }), true);
	assert.equal(isTextField({ tagName: 'SELECT' }), true);
	assert.equal(isTextField({ tagName: 'DIV', isContentEditable: true }), true);
});
