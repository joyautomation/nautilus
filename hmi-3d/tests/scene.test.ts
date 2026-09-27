// Unit tests for the scene document: validation and the subscription list.
//
//	npm test
//
// Same harness as the kit's: ./harness.ts, a strict subset of vitest's API.
import { describe, expect, it } from './harness.js';
import { validateScene, sceneTags, isBindingRef, refRoot, type SceneDoc } from '../src/lib/scene.js';

const rig: SceneDoc = {
	name: 'Office rig',
	camera: { pos: [2.2, 1.6, 2.6], target: [0.9, 0.1, 0] },
	nodes: [
		{ id: 'T101', kind: 'tank', tag: 'T101', label: 'T-101', pos: [1, -0.75, 0.4] },
		{ id: 'P101', kind: 'pump', tag: 'P101', pos: [0.3, 0, 0.2], rot: [0, 90, 0] },
		{ id: 'XV101', kind: 'valve', tag: 'XV101', pos: [1.8, 0.85, 0], bind: { cmd: 'Demand' } }
	],
	pipes: [
		{ points: [[0.46, 0.08, 0.2], [1, 0.08, 0.4]], bind: { flowing: 'P101.Running' } },
		{ points: [[1.2, -0.7, 0.4], [1.8, 0.75, 0]], bind: { flowing: '!XV101.Fault' } }
	]
};
const KINDS = ['tank', 'pump', 'valve'];

describe('validateScene', () => {
	it('accepts the rig scene', () => {
		expect(validateScene(rig, KINDS)).toEqual({ ok: true, errors: [] });
	});

	it('rejects a non-object', () => {
		expect(validateScene(null).ok).toBe(false);
		expect(validateScene('scene').errors[0].path).toBe('');
	});

	it('requires nodes', () => {
		const r = validateScene({});
		expect(r.ok).toBe(false);
		expect(r.errors).toEqual([{ path: '/nodes', message: 'must be an array' }]);
	});

	it('names the path of a bad position', () => {
		const r = validateScene({ nodes: [{ id: 'a', kind: 'tank', pos: [1, 2] }] });
		expect(r.errors.map((e) => e.path)).toEqual(['/nodes/0/pos']);
	});

	it('rejects a duplicate id', () => {
		const r = validateScene({
			nodes: [
				{ id: 'a', kind: 'tank', pos: [0, 0, 0] },
				{ id: 'a', kind: 'pump', pos: [1, 0, 0] }
			]
		});
		expect(r.errors).toEqual([{ path: '/nodes/1/id', message: 'duplicate id "a"' }]);
	});

	it('checks kinds only against a registry it is given', () => {
		const doc = { nodes: [{ id: 'sw', kind: 'switch', pos: [0, 0, 0] }] };
		expect(validateScene(doc).ok).toBe(true);
		const r = validateScene(doc, KINDS);
		expect(r.ok).toBe(false);
		expect(r.errors[0].path).toBe('/nodes/0/kind');
		expect(validateScene(doc, [...KINDS, 'switch']).ok).toBe(true);
	});

	it('rejects a malformed binding ref, with the prop in the path', () => {
		const r = validateScene({ nodes: [{ id: 'a', kind: 'tank', pos: [0, 0, 0], bind: { level: 'T101..Level', ok: '!' } }] });
		expect(r.errors.map((e) => e.path)).toEqual(['/nodes/0/bind/level', '/nodes/0/bind/ok']);
	});

	it('a pipe needs two points and binds only flowing', () => {
		const r = validateScene({
			nodes: [],
			pipes: [{ points: [[0, 0, 0]] }, { points: [[0, 0, 0], [1, 0, 0]], bind: { colour: 'X' } }]
		});
		expect(r.errors.map((e) => e.path)).toEqual(['/pipes/0/points', '/pipes/1/bind/colour']);
	});

	it('a non-empty writable list is an error until writes ship', () => {
		expect(validateScene({ nodes: [], writable: [] }).ok).toBe(true);
		const r = validateScene({ nodes: [], writable: ['Demand'] });
		expect(r.errors[0].path).toBe('/writable');
	});

	it('validates fixtures, grid and camera', () => {
		const r = validateScene({
			nodes: [],
			camera: { pos: [0, 0, 0], target: [0, 0], fov: 400 },
			grid: { cell: 0 },
			fixtures: [{ kind: 'sphere', pos: [0, 0, 0] }, { kind: 'box', pos: [0, 0, 0], opacity: 2 }]
		});
		expect(r.errors.map((e) => e.path)).toEqual([
			'/camera/target',
			'/camera/fov',
			'/grid/cell',
			'/fixtures/0/kind',
			'/fixtures/1/opacity'
		]);
	});
});

describe('isBindingRef / refRoot', () => {
	it('accepts a tag, a dotted path and a negation', () => {
		expect(isBindingRef('Demand')).toBe(true);
		expect(isBindingRef('P101.Speed')).toBe(true);
		expect(isBindingRef('!P101.Fault')).toBe(true);
	});
	it('rejects empties and empty segments', () => {
		expect(isBindingRef('')).toBe(false);
		expect(isBindingRef('!')).toBe(false);
		expect(isBindingRef('A..B')).toBe(false);
		expect(isBindingRef('.A')).toBe(false);
		expect(isBindingRef(3)).toBe(false);
	});
	it('roots', () => {
		expect(refRoot('Demand')).toBe('Demand');
		expect(refRoot('P101.Speed')).toBe('P101');
		expect(refRoot('!P101.Fault')).toBe('P101');
	});
});

describe('sceneTags', () => {
	it('lists every root tag once, in document order', () => {
		expect(sceneTags(rig)).toEqual(['T101', 'P101', 'XV101', 'Demand']);
	});
	it('includes a tagless node’s binding roots and a pipe’s', () => {
		const doc: SceneDoc = {
			nodes: [{ id: 'a', kind: 'tank', pos: [0, 0, 0], bind: { level: 'T101_Level' } }],
			pipes: [{ points: [[0, 0, 0], [1, 0, 0]], bind: { flowing: 'P1.Running' } }]
		};
		expect(sceneTags(doc)).toEqual(['T101_Level', 'P1']);
	});
	it('is empty for an empty scene', () => {
		expect(sceneTags({ nodes: [] })).toEqual([]);
	});
});
