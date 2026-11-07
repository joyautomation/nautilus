/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fbdNodeTag, isToggleKey, ldElementTag, plainTag, sfcNameTag, tagContext } from './liveTag.ts';

test('plainTag: identifiers only, trimmed; no member, element, expression or literal', () => {
	assert.equal(plainTag('StartPB'), 'StartPB');
	assert.equal(plainTag('  Run_1 '), 'Run_1');
	for (const r of ['Pump.Ready', 'Arr[2]', 'NOT Run', 'A AND B', 'TRUE AND x', '3', 'T#5S', '', undefined, null]) {
		assert.equal(plainTag(r as string | undefined), undefined, String(r));
	}
});

test('ldElementTag: contacts, edge contacts and coils of a PROGRAM rung', () => {
	assert.equal(ldElementTag({}, { kind: 'contact', ref: 'StartPB' }), 'StartPB');
	assert.equal(ldElementTag({}, { kind: 'edge', ref: 'Trip' }), 'Trip');
	assert.equal(ldElementTag({}, { kind: 'coil', ref: 'Motor' }), 'Motor');
	assert.equal(ldElementTag({}, { kind: 'contact', ref: 'Pump.Ready' }), undefined);
	assert.equal(ldElementTag({}, { kind: 'block', ref: 'T1' }), undefined);
	assert.equal(ldElementTag({ scope: 'Seal' }, { kind: 'contact', ref: 'In' }), undefined);
});

test('fbdNodeTag: variable chips of a PROGRAM; not literals, blocks, FB instances or a FUNCTION_BLOCK body', () => {
	assert.equal(fbdNodeTag({ kind: 'input', label: 'StartPB' }, false), 'StartPB');
	assert.equal(fbdNodeTag({ kind: 'coil', label: 'Motor' }, undefined), 'Motor');
	assert.equal(fbdNodeTag({ kind: 'input', label: 'TRUE', src: 'TRUE' }, false), undefined, 'a literal');
	assert.equal(fbdNodeTag({ kind: 'input', label: '5', src: 5 }, false), undefined, 'a literal');
	assert.equal(fbdNodeTag({ kind: 'input', label: 'Pump.Ready' }, false), undefined, 'a member');
	assert.equal(fbdNodeTag({ kind: 'block', label: 'AND' }, false), undefined);
	assert.equal(fbdNodeTag({ kind: 'fb', label: 'T1' }, false), undefined);
	assert.equal(fbdNodeTag({ kind: 'comment', label: 'note' }, false), undefined);
	assert.equal(fbdNodeTag({ kind: 'input', label: 'In' }, true), undefined, "a FUNCTION_BLOCK's pin");
});

test('sfcNameTag: declared variables and manifest tags; not constants, steps, ACTIONs or new actions', () => {
	const m = {
		vars: [
			{ name: 'FillValve', section: 'VAR_EXTERNAL' },
			{ name: 'Count', section: 'VAR' },
			{ name: 'Limit', section: 'VAR CONSTANT' }
		],
		tags: [{ name: 'Level_OK' }],
		steps: [{ name: 'Fill' }],
		actions: [{ name: 'Wash' }]
	};
	assert.equal(sfcNameTag('FillValve', m), 'FillValve');
	assert.equal(sfcNameTag('fillvalve', m), 'fillvalve', 'names match without case, as IEC does');
	assert.equal(sfcNameTag('Count', m), 'Count');
	assert.equal(sfcNameTag(' Level_OK ', m), 'Level_OK', 'a manifest tag, in scope undeclared');
	assert.equal(sfcNameTag('Limit', m), undefined, 'a constant');
	assert.equal(sfcNameTag('Fill', m), undefined, 'a step');
	assert.equal(sfcNameTag('Wash', m), undefined, 'an ACTION');
	assert.equal(sfcNameTag('Rinse', m), undefined, 'a new action waiting for its body');
	assert.equal(sfcNameTag('Level_OK AND FillValve', m), undefined, 'a condition expression');
	assert.equal(sfcNameTag('Fill.X', m), undefined, 'a step flag member');
	assert.equal(sfcNameTag('TRUE', { ...m, vars: [] }), undefined);
});

test('tagContext: the tag merged into an element context; nothing when neither', () => {
	assert.equal(tagContext('StartPB'), '{"nautilusTag":"StartPB"}');
	assert.equal(tagContext(undefined), undefined);
	assert.deepEqual(JSON.parse(tagContext('Run', { nautilusSfcTransition: 't1' })!), { nautilusSfcTransition: 't1', nautilusTag: 'Run' });
	assert.deepEqual(JSON.parse(tagContext(undefined, { nautilusSfcTransition: 't1' })!), { nautilusSfcTransition: 't1' });
});

test('isToggleKey: T or t alone; never with Ctrl, Cmd or Alt', () => {
	const k = (key: string, m: Partial<{ ctrlKey: boolean; metaKey: boolean; altKey: boolean }> = {}) => ({ key, ctrlKey: false, metaKey: false, altKey: false, ...m });
	assert.equal(isToggleKey(k('t')), true);
	assert.equal(isToggleKey(k('T')), true);
	assert.equal(isToggleKey(k('t', { ctrlKey: true })), false);
	assert.equal(isToggleKey(k('t', { metaKey: true })), false);
	assert.equal(isToggleKey(k('t', { altKey: true })), false);
	assert.equal(isToggleKey(k('y')), false);
});
