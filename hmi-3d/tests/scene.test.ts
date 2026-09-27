// Unit tests for the scene document: validation and the subscription list.
//
//	npm test
//
// Same harness as the kit's: ./harness.ts, a strict subset of vitest's API.
import { describe, expect, it } from './harness.js';
import { validateScene, sceneTags, isBindingRef, refRoot, kindMembers, type SceneDoc } from '../src/lib/scene.js';

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

describe('validateScene: kinds', () => {
	it('accepts a re-pointed built-in and a declared custom kind', () => {
		const doc = { kinds: { pump: { type: 'VfdPump' }, switch: { type: 'Switch', members: ['PortsUp'] } }, nodes: [] };
		expect(validateScene(doc, [...KINDS, 'switch'])).toEqual({ ok: true, errors: [] });
	});
	it('a declared kind the app does not register is an error only against a registry', () => {
		const doc = { kinds: { switch: { type: 'Switch' } }, nodes: [] };
		expect(validateScene(doc).ok).toBe(true);
		expect(validateScene(doc, KINDS).errors.map((e) => e.path)).toEqual(['/kinds/switch']);
	});
	it('checks the shape', () => {
		const r = validateScene({ kinds: { a: 'Tank', b: { type: 3 }, c: { members: 'Level' } }, nodes: [] });
		expect(r.errors.map((e) => e.path)).toEqual(['/kinds/a', '/kinds/b/type', '/kinds/c/members']);
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

describe('validateScene: kinds as data, environment, textures (§3c)', () => {
	const kinds = {
		pump: { model: 'models/pump.glb', drive: [{ mesh: 'Coupling', spin: { axis: 'x', revPerS: { bind: 'Speed', scale: 0.02 } } }] },
		beacon: { type: 'Switch', model: 'models/beacon.glb', status: '{Fault?FAULT:ok}', bounds: 'auto' }
	};
	it('a data kind under a new name is a kind the document brings', () => {
		const doc = { kinds, nodes: [{ id: 'b', kind: 'beacon', pos: [0, 0, 0] }] };
		expect(validateScene(doc, KINDS)).toEqual({ ok: true, errors: [] });
	});
	it('a kind without a model still needs the app to register it', () => {
		const r = validateScene({ kinds: { switch: { type: 'Switch' } }, nodes: [] }, KINDS);
		expect(r.errors.map((e) => e.path)).toEqual(['/kinds/switch']);
	});
	it('names the path of each problem', () => {
		const r = validateScene(
			{
				kinds: {
					a: { model: 'http://x/a.glb' },
					b: { model: 'models/b.glb', bounds: 'big', labelAt: [0, 1], status: '{Level %' },
					c: { drive: [] },
					d: { model: 'models/d.glb', drive: [{ mesh: 'M', tint: { bind: 'Running' } }] }
				},
				environment: { hdri: 'env/x.png', backdrop: 'env/x.hdr.txt', background: 'wall', floor: 'low', intensity: -1, shadows: 'yes', fog: { near: 5, far: 2 } },
				fixtures: [
					{ kind: 'box', pos: [0, 0, 0], texture: { map: 'a.jpg' } },
					{ kind: 'plane', pos: [0, 0, 0], texture: { map: '../a.jpg', repeat: [1] } }
				],
				nodes: []
			},
			KINDS
		);
		expect(r.errors.map((e) => e.path)).toEqual([
			'/fixtures/0/texture',
			'/fixtures/1/texture/map',
			'/fixtures/1/texture/repeat',
			'/kinds/a/model',
			'/kinds/b/bounds',
			'/kinds/b/labelAt',
			'/kinds/b/status',
			'/kinds/c/drive',
			'/kinds/c',
			'/kinds/d/drive/0/tint/on',
			'/environment/hdri',
			'/environment/backdrop',
			'/environment/fog/far',
			'/environment/background',
			'/environment/floor',
			'/environment/intensity',
			'/environment/shadows'
		]);
	});
	it('kindMembers unions members, drives and the status template', () => {
		expect(kindMembers(kinds.pump as never)).toEqual(['Speed']);
		expect(kindMembers({ members: ['PortsUp'], status: '{Fault?FAULT:ok} {PortsUp}' })).toEqual(['PortsUp', 'Fault']);
	});
});
