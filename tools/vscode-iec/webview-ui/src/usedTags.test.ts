/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fbdRefs, ldRefs, sfcRefs, stIdents, usedProjectTags, type ProjectTag } from './usedTags.ts';
import type { LdModel } from './ladder.ts';
import type { FbdModel } from './layout.ts';
import type { SfcModel } from './sfc.ts';

// The motor example: tags/motor.yaml declares six MTR101_* tags, the
// PROGRAM declares nothing, and its rungs use five of them plus a TON
// instance — the ldModel `naut ld graph - motor.ld` prints for it.
const MOTOR_TAGS: ProjectTag[] = [
	{ name: 'MTR101_HandSel', role: 'input', type: 'BOOL', desc: 'Hand selected at the MCC' },
	{ name: 'MTR101_RunCmd', role: 'output', type: 'BOOL', desc: 'Run command to the starter' },
	{ name: 'MTR101_RunFbk', role: 'input', type: 'BOOL', desc: 'Starter auxiliary contact' },
	{ name: 'MTR101_Overload', role: 'input', type: 'BOOL', desc: 'Overload relay tripped' },
	{ name: 'MTR101_FailToRun', role: 'memory', type: 'BOOL', desc: 'Commanded but no run feedback' },
	{ name: 'MTR101_Permissive', role: 'memory', type: 'BOOL', desc: 'Clear to run' }
];
const MOTOR: LdModel = {
	name: 'Motor',
	rungs: [
		{
			name: 'permissive',
			line: 3,
			endLine: 4,
			elements: [
				{ kind: 'contact', ref: 'MTR101_Overload', neg: true },
				{ kind: 'contact', ref: 'MTR101_FailToRun', neg: true }
			],
			coils: [{ kind: 'coil', ref: 'MTR101_Permissive' }]
		},
		{
			name: 'failtorun',
			line: 5,
			endLine: 6,
			elements: [
				{ kind: 'contact', ref: 'MTR101_RunCmd' },
				{ kind: 'contact', ref: 'MTR101_RunFbk', neg: true },
				{ kind: 'fb', args: 'PT := T#5S', inst: 'ftrTmr', type: 'TON', powerIn: 'IN', powerOut: 'Q' }
			],
			coils: [{ kind: 'coil', ref: 'MTR101_FailToRun', mode: 'S' }]
		}
	],
	tags: MOTOR_TAGS
};

const ld = (m: LdModel, tags = m.tags, declared = (m.vars ?? []).filter((v) => !v.pou)) => {
	const r = ldRefs(m);
	return usedProjectTags(r.refs, tags, declared, r.exclude).map((t) => t.name);
};

test('ladder: the motor example lists the five tags its rungs use, in manifest order; the unused one is not listed', () => {
	assert.deepEqual(ld(MOTOR), ['MTR101_RunCmd', 'MTR101_RunFbk', 'MTR101_Overload', 'MTR101_FailToRun', 'MTR101_Permissive']);
	const r = ldRefs(MOTOR);
	const got = usedProjectTags(r.refs, MOTOR_TAGS, [], r.exclude);
	assert.deepEqual(got[0], MOTOR_TAGS[1], 'the tag rides along whole: type, role, desc');
});

test('ladder: a header declaration (any case) is never listed again; nor is a block instance', () => {
	const m: LdModel = {
		...MOTOR,
		vars: [{ name: 'mtr101_runcmd', type: 'BOOL', section: 'VAR_EXTERNAL', line: 2 }]
	};
	assert.deepEqual(ld(m), ['MTR101_RunFbk', 'MTR101_Overload', 'MTR101_FailToRun', 'MTR101_Permissive']);
	// A tag that happens to share an instance's name is the instance on this diagram.
	assert.deepEqual(ld(MOTOR, [...MOTOR_TAGS, { name: 'FTRTMR', type: 'BOOL' }]).includes('FTRTMR'), false);
});

test('ladder: case-insensitive matching keeps the manifest spelling', () => {
	const m: LdModel = { name: 'P', rungs: [{ name: 'r', line: 1, elements: [{ kind: 'contact', ref: 'mtr101_overload' }], coils: [] }] };
	assert.deepEqual(ld(m, MOTOR_TAGS), ['MTR101_Overload']);
});

test('ladder: pin names, literals and members are not tags; arguments, indexes and assignment bodies are', () => {
	const tags: ProjectTag[] = ['PT', 'T', 'S', 'Q', 'Preset', 'Tank', 'Level', 'Idx', 'Total', 'Flow', 'Stop'].map((name) => ({ name }));
	const m: LdModel = {
		name: 'P',
		rungs: [
			{
				name: 'r1',
				line: 1,
				elements: [
					{ kind: 'contact', ref: 'Tank.Level' },
					{ kind: 'fb', inst: 't1', type: 'TON', args: 'PT := Preset, Q => _' },
					{ kind: 'fn', fn: 'GT', args: 'Hist[Idx], 3.0' }
				],
				coils: [{ kind: 'assign', text: 'Total := Total + Flow; (* Stop is only in a comment *)' }]
			}
		]
	};
	assert.deepEqual(ld(m, tags), ['Preset', 'Tank', 'Idx', 'Total', 'Flow']);
});

test('ladder: a FUNCTION_BLOCK body\'s rungs are its pins and locals, never tags; a program-scope declaration only shadows the program', () => {
	const m: LdModel = {
		name: 'P',
		blocks: [{ name: 'Starter', line: 10, endLine: 20 }],
		vars: [{ name: 'MTR101_Overload', type: 'BOOL', section: 'VAR_INPUT', line: 11, pou: 'Starter' }],
		rungs: [
			{ name: 'b1', line: 12, pou: 'Starter', elements: [{ kind: 'contact', ref: 'MTR101_RunFbk' }], coils: [] },
			{ name: 'p1', line: 2, elements: [{ kind: 'contact', ref: 'MTR101_Overload' }], coils: [] }
		]
	};
	assert.deepEqual(ld(m, MOTOR_TAGS), ['MTR101_Overload']);
});

test('ladder: branch legs are walked; a missing model, rungs or tags yield nothing', () => {
	const m: LdModel = {
		name: 'P',
		rungs: [{ name: 'r', line: 1, elements: [{ kind: 'branch', legs: [[{ kind: 'contact', ref: 'MTR101_HandSel' }], [{ kind: 'contact', ref: 'MTR101_RunFbk' }]] }], coils: [] }]
	};
	assert.deepEqual(ld(m, MOTOR_TAGS), ['MTR101_HandSel', 'MTR101_RunFbk']);
	assert.deepEqual(ld({ name: 'P', rungs: [] }, MOTOR_TAGS), []);
	assert.deepEqual(usedProjectTags(ldRefs(MOTOR).refs, undefined, []), []);
	assert.deepEqual(ldRefs(null), { refs: [], exclude: [] });
});

test('stIdents: assignment targets and member bases count; call pins, callees, members, literals and comments do not', () => {
	assert.deepEqual(stIdents('y := a + b; z := 0'), ['y', 'a', 'b', 'z']);
	assert.deepEqual(stIdents("tmr(IN := Run AND NOT Stop, PT := T#5S); Done := tmr.Q; s := 'Lit'"), ['Run', 'AND', 'NOT', 'Stop', 'Done', 'tmr', 's']);
	assert.deepEqual(stIdents('Tank.Level > 16#FF (* Hidden *) // Gone'), ['Tank']);
	assert.deepEqual(stIdents('LIMIT(0.0, Sp, 100.0) > 1.5E3'), ['Sp']);
	assert.deepEqual(stIdents(''), []);
});

test('fbd: input chips and coils are refs; instances and wires are not; a FUNCTION_BLOCK body offers none', () => {
	const m: FbdModel = {
		nodes: [
			{ id: 'a', kind: 'input', label: 'mtr101_runcmd', layer: 0 },
			{ id: 'b', kind: 'input', label: 'Hist[2]', layer: 0 },
			{ id: 'c', kind: 'fb', label: 'MTR101_HandSel', type: 'TON', layer: 1 },
			{ id: 'd', kind: 'coil', label: 'MTR101_Permissive', layer: 2 },
			{ id: 'e', kind: 'input', label: 'T#5S', layer: 0 },
			// A named wire's driving block, and a chip naming it in another network.
			{ id: 'f', kind: 'block', label: 'AND', wire: 'MTR101_Overload', layer: 1 },
			{ id: 'g', kind: 'input', label: 'MTR101_Overload', layer: 0 }
		],
		edges: []
	} as unknown as FbdModel;
	const tags = [...MOTOR_TAGS, { name: 'Hist', type: 'ARRAY' }];
	const r = fbdRefs(m);
	assert.deepEqual(usedProjectTags(r.refs, tags, [], r.exclude).map((t) => t.name), ['MTR101_RunCmd', 'MTR101_Permissive', 'Hist']);
	assert.deepEqual(fbdRefs({ ...m, block: true }), { refs: [], exclude: [] });
});

test('sfc: association targets, conditions and action bodies; steps, actions and declarations are not listed', () => {
	const m: SfcModel = {
		name: 'Fill',
		vars: [{ name: 'MTR101_RunFbk', type: 'BOOL', section: 'VAR_EXTERNAL', line: 2 }],
		steps: [
			{ id: 's1', name: 'Idle', initial: true, line: 4, endLine: 5 },
			{ id: 's2', name: 'Run', initial: false, line: 6, endLine: 8, actions: [{ qualifier: 'N', target: 'MTR101_RunCmd', line: 7 }, { qualifier: 'P', target: 'Latch', line: 7 }] }
		],
		trans: [{ id: 't1', from: ['s1'], to: ['s2'], cond: 'MTR101_Permissive AND NOT MTR101_RunFbk AND Idle.T > T#1S', kind: 'normal', line: 9, endLine: 9 }],
		actions: [{ id: 'a1', name: 'Latch', body: 'MTR101_FailToRun := FALSE;', line: 10, endLine: 12 }]
	};
	const tags = [...MOTOR_TAGS, { name: 'Idle' }, { name: 'Latch' }];
	const r = sfcRefs(m);
	assert.deepEqual(usedProjectTags(r.refs, tags, m.vars, r.exclude).map((t) => t.name), ['MTR101_RunCmd', 'MTR101_FailToRun', 'MTR101_Permissive']);
});

// Review of #651: the cases the first cut missed.
test('stIdents: a comment marker inside a string does not cut the code after it', () => {
	assert.deepEqual(stIdents("msg := 'see http://x'; Alarm := TRUE;"), ['msg', 'Alarm', 'TRUE']);
	assert.deepEqual(stIdents("msg := 'a (* b'; Pump := 1; (* c *) Valve := 0;"), ['msg', 'Pump', 'Valve']);
	// and a quote inside a comment is not a string
	assert.deepEqual(stIdents("// it's a note\nLevel := 1;"), ['Level']);
});

test('ldRefs: an instance array index is read; the array is not a tag', () => {
	const m = {
		rungs: [{ elements: [{ kind: 'fb', inst: 'tmrs[Idx]', type: 'TON' }], coils: [] }]
	} as unknown as LdModel;
	assert.deepEqual(ldRefs(m), { refs: ['Idx'], exclude: ['tmrs'] });
});

test('ldRefs: a FUNCTION_BLOCK\'s own instances do not shadow a project tag in the PROGRAM', () => {
	const m = {
		rungs: [
			{ pou: 'Starter', elements: [{ kind: 'fb', inst: 'Delay', type: 'TON' }], coils: [] },
			{ elements: [{ kind: 'contact', ref: 'Delay' }], coils: [] }
		]
	} as unknown as LdModel;
	const r = ldRefs(m);
	assert.deepEqual(r.exclude, []);
	assert.deepEqual(usedProjectTags(r.refs, [{ name: 'Delay', type: 'BOOL' }], [], r.exclude).map((t) => t.name), ['Delay']);
});

test('sfcRefs: an association\'s duration variable is used', () => {
	const m = {
		steps: [{ name: 'Fill', actions: [{ qualifier: 'L', target: 'FillValve', time: 'FillTime', line: 3 }] }],
		trans: [],
		actions: []
	} as unknown as SfcModel;
	assert.deepEqual(sfcRefs(m).refs, ['FillValve', 'FillTime']);
	// a literal duration is not
	const lit = { ...m, steps: [{ name: 'Fill', actions: [{ qualifier: 'L', target: 'FillValve', time: 'T#5S', line: 3 }] }] } as unknown as SfcModel;
	assert.deepEqual(sfcRefs(lit).refs, ['FillValve']);
});
