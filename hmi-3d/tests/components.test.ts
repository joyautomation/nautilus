// Unit tests for item 1b's pure parts (docs/design/spatial-hmi.md §3d):
// component kinds from the app's modules, assemblies, and how a part
// reads its struct.
import { describe, expect, it } from './harness.js';
import { validateScene, kindMembers, assemblyMembers, kindDefinedBy, isComponentPath, type SceneDoc, type SceneKind } from '../src/lib/scene.js';
import { matchModule, componentRegistry, assemblyBounds, registryFor, kindOf, type NodeRegistry, type NodeProps, type KindMeta } from '../src/lib/defs.js';
import { readPart, resolveRefs } from '../src/lib/bindings.js';
import { BUILTIN_CONTRACT } from '../src/lib/kinds.js';

const KINDS = Object.keys(BUILTIN_CONTRACT);
// A registry of the built-ins' contracts and boxes, with no components,
// which is all these specs need (the Svelte entries never load here).
const fake = () => (() => {}) as unknown as import('svelte').Component<NodeProps>;
const registry: NodeRegistry = {
	tank: { component: fake(), bounds: { size: [0.5, 0.52, 0.5], center: [0, 0.2, 0] } },
	pump: { component: fake(), bounds: { size: [0.42, 0.24, 0.2], center: [0, 0.09, 0] } },
	valve: { component: fake(), bounds: { size: [0.16, 0.22, 0.22], center: [0, 0, 0.03] } }
};

const skid: SceneKind = {
	type: 'Skid',
	members: ['Fault'],
	status: '{Fault?FAULT:ok}',
	assembly: {
		nodes: [
			{ id: 'pump', kind: 'pump', tag: 'Pump', pos: [0, 0.02, 0] },
			{ id: 'valve', kind: 'valve', tag: 'Valve', pos: [0.42, 0.14, 0], rot: [0, 0, 90] }
		],
		pipes: [{ points: [[0.16, 0.1, 0], [0.42, 0.1, 0]], bind: { flowing: 'Pump.Running' } }]
	}
};

describe('a kind defined by a component file', () => {
	it('is a source path relative to the scene file, ending in .svelte', () => {
		expect(isComponentPath('hmi/src/lib/Skid.svelte')).toBe(true);
		expect(isComponentPath('Skid.svelte')).toBe(true);
		expect(isComponentPath('/abs/Skid.svelte')).toBe(false);
		expect(isComponentPath('../Skid.svelte')).toBe(false);
		expect(isComponentPath('http://x/Skid.svelte')).toBe(false);
		expect(isComponentPath('hmi/src/lib/Skid.ts')).toBe(false);
	});

	it('matches the module whose key the path ends with, longest first', () => {
		const keys = ['/src/lib/Skid.svelte', '/src/lib/parts/Skid.svelte', '/src/routes/+page.svelte'];
		expect(matchModule('hmi/src/lib/Skid.svelte', keys)).toBe('/src/lib/Skid.svelte');
		expect(matchModule('hmi/src/lib/parts/Skid.svelte', keys)).toBe('/src/lib/parts/Skid.svelte');
		expect(matchModule('src/lib/Skid.svelte', keys)).toBe('/src/lib/Skid.svelte');
		expect(matchModule('./src/lib/Skid.svelte', ['./src/lib/Skid.svelte'])).toBe('./src/lib/Skid.svelte');
		expect(matchModule('hmi/src/lib/Other.svelte', keys)).toBe(undefined);
	});

	const meta: KindMeta = { type: 'Skid', members: ['Pump', 'Valve'], bounds: { size: [1, 1, 1], center: [0, 0, 0] } };
	const modules = { '/src/lib/Skid.svelte': { default: fake(), kind: meta } };

	it('joins the registry from the module, the document winning on type', () => {
		const doc: SceneDoc = { kinds: { skid: { type: 'TransferSkid', members: ['Pump', 'Valve', 'Fault'], component: 'hmi/src/lib/Skid.svelte' } }, nodes: [] };
		const r = componentRegistry(doc, modules, registry);
		expect(r.errors).toEqual([]);
		expect(r.registry.skid.type).toBe('TransferSkid');
		expect(r.registry.skid.members).toEqual(['Pump', 'Valve', 'Fault']);
		expect(r.registry.skid.bounds).toEqual({ size: [1, 1, 1], center: [0, 0, 0] });
		expect(r.registry.skid.component).toBe(modules['/src/lib/Skid.svelte'].default);
		expect(Object.keys(r.registry).sort()).toEqual(['pump', 'skid', 'tank', 'valve']);
	});

	it('reports a member the export reads that the document does not list', () => {
		const doc: SceneDoc = { kinds: { skid: { type: 'Skid', members: ['Pump'], component: 'hmi/src/lib/Skid.svelte' } }, nodes: [] };
		const r = componentRegistry(doc, modules, registry);
		expect(r.errors.length).toBe(1);
		expect(r.errors[0].path).toBe('/kinds/skid/members');
		expect(r.errors[0].message.includes('Valve')).toBe(true);
	});

	it('reports a path no module matches, and a module with no component', () => {
		const doc: SceneDoc = { kinds: { a: { component: 'hmi/src/lib/Nope.svelte' }, b: { component: 'hmi/src/lib/Data.svelte' } }, nodes: [] };
		const r = componentRegistry(doc, { ...modules, '/src/lib/Data.svelte': { kind: meta } }, registry);
		expect(r.errors.map((e) => e.path)).toEqual(['/kinds/a/component', '/kinds/b/component']);
		expect('a' in r.registry).toBe(false);
	});

	it('takes the export’s status when the document has no template, and the template otherwise', () => {
		const withStatus = { '/src/lib/Skid.svelte': { default: fake(), kind: { ...meta, status: () => 'from export' } } };
		let r = componentRegistry({ kinds: { skid: { component: 'hmi/src/lib/Skid.svelte', members: meta.members } }, nodes: [] }, withStatus, registry);
		expect(r.registry.skid.status?.({}, true)).toBe('from export');
		r = componentRegistry({ kinds: { skid: { component: 'hmi/src/lib/Skid.svelte', members: meta.members, status: '{Fault?FAULT:ok}' } }, nodes: [] }, withStatus, registry);
		expect(r.registry.skid.status?.({ Fault: true }, true)).toBe('FAULT');
	});

	it('kindOf falls back to a small box', () => {
		expect(kindOf(fake()).bounds).toEqual({ size: [0.3, 0.3, 0.3], center: [0, 0.15, 0] });
	});

	it('validates: a component kind is a kind the document brings; one way per kind', () => {
		expect(validateScene({ kinds: { skid: { component: 'hmi/src/lib/Skid.svelte' } }, nodes: [{ id: 'S', kind: 'skid', pos: [0, 0, 0] }] }, KINDS).ok).toBe(true);
		const r = validateScene({ kinds: { skid: { component: 'hmi/src/lib/Skid.svelte', model: 'models/skid.glb' }, x: { component: '../Skid.svelte' } }, nodes: [] }, KINDS);
		expect(r.errors.map((e) => e.path).sort()).toEqual(['/kinds/skid', '/kinds/x/component']);
	});
});

describe('assemblies', () => {
	it('read every part’s tag and every inner ref’s root', () => {
		expect(assemblyMembers(skid.assembly)).toEqual(['Pump', 'Valve']);
		expect(kindMembers(skid)).toEqual(['Fault', 'Pump', 'Valve']);
		expect(assemblyMembers({ nodes: [{ id: 'a', kind: 'pump', tag: 'Pump', pos: [0, 0, 0], bind: { speed: '!Vfd.Rpm' } }] })).toEqual(['Pump', 'Vfd']);
		expect(kindDefinedBy(skid)).toBe('assembly');
	});

	it('box is the union of the parts’ boxes', () => {
		const b = assemblyBounds(skid.assembly!, registry);
		// pump: x -0.21..0.21, y 0.02-0.03..0.02+0.21, z -0.1..0.1; valve at 0.42: x 0.34..0.5, y 0.03..0.25, z -0.08..0.14
		expect(b.size.map((v) => +v.toFixed(3))).toEqual([0.71, 0.26, 0.24]);
		expect(b.center.map((v) => +v.toFixed(3))).toEqual([0.145, 0.12, 0.02]);
		expect(assemblyBounds({ nodes: [] }, registry)).toEqual({ size: [0.3, 0.3, 0.3], center: [0, 0.15, 0] });
	});

	it('registryFor lays an assembly on as an entry with no component', () => {
		const doc: SceneDoc = { kinds: { skid, pump: { model: 'models/pump.glb' } }, nodes: [] };
		const r = registryFor(doc, registry, null);
		expect(r.skid.component).toBe(undefined);
		expect(r.skid.assembly).toBe(skid.assembly);
		expect(r.skid.type).toBe('Skid');
		expect(r.skid.members).toEqual(['Fault', 'Pump', 'Valve']);
		expect(r.skid.status?.({ Fault: true }, true)).toBe('FAULT');
		expect(r.skid.bounds).toEqual(assemblyBounds(skid.assembly!, registry));
		// with no glTF component (flat look), the model kind stays the Svelte kind
		expect(r.pump).toBe(registry.pump);
	});

	it('validates parts as nodes in the kind’s frame, tags as members, and stays flat', () => {
		const ok = validateScene({ kinds: { skid }, nodes: [{ id: 'SK101', kind: 'skid', tag: 'SK101', pos: [0, 0, 0] }] }, KINDS);
		expect(ok).toEqual({ ok: true, errors: [] });
		const bad = validateScene(
			{
				kinds: {
					skid,
					twin: {
						assembly: {
							nodes: [
								{ id: 'a', kind: 'skid', tag: 'Left', pos: [0, 0, 0] },
								{ id: 'a', kind: 'pump', tag: 'Pump.Motor', pos: [0, 0] },
								{ id: 'b', kind: 'nope', pos: [0, 0, 0] }
							],
							pipes: [{ points: [[0, 0, 0]] }]
						}
					},
					empty: { assembly: {} }
				},
				nodes: []
			},
			KINDS
		);
		expect(bad.errors.map((e) => e.path).sort()).toEqual(
			[
				'/kinds/empty/assembly',
				'/kinds/twin/assembly/nodes/0/kind',
				'/kinds/twin/assembly/nodes/1/id',
				'/kinds/twin/assembly/nodes/1/pos',
				'/kinds/twin/assembly/nodes/1/tag',
				'/kinds/twin/assembly/nodes/2/kind',
				'/kinds/twin/assembly/pipes/0/points'
			].sort()
		);
	});

	it('bounds and labelAt are allowed on a component or an assembly, drives need a model', () => {
		const r = validateScene({ kinds: { a: { assembly: { nodes: [] }, bounds: { size: [1, 1, 1], center: [0, 0, 0] }, labelAt: [0, 1, 0] }, b: { component: 'X.svelte', drive: [] } }, nodes: [] }, KINDS);
		expect(r.errors.map((e) => e.path)).toEqual(['/kinds/b/drive']);
	});
});

describe('how a part reads its struct', () => {
	const sk = { Pump: { Running: true, Speed: 80 }, Valve: { Pos: 50 }, Fault: false };
	it('a member, then down the path', () => {
		expect(readPart(sk, {}, 'Pump')).toEqual({ Running: true, Speed: 80 });
		expect(readPart(sk, {}, 'Pump.Speed')).toBe(80);
		expect(readPart(sk, {}, 'Nope.X')).toBe(undefined);
		expect(readPart(undefined, {}, 'Pump')).toBe(undefined);
	});
	it('the node’s bound prop supplies a member (the flat-tag escape hatch)', () => {
		const over = { pump: { Running: false, Speed: 0 } };
		expect(readPart(sk, over, 'Pump.Running')).toBe(false);
		expect(readPart(sk, over, 'Valve.Pos')).toBe(50);
	});
	it('resolveRefs uses the reader, negation and absence as everywhere', () => {
		const out = resolveRefs({ running: 'Pump.Running', off: '!Pump.Running', missing: 'Nope' }, (p) => readPart(sk, {}, p));
		expect(out).toEqual({ running: true, off: false });
	});
});
