// A kind in a file of its own (docs/design/spatial-hmi.md §3g, slice 2):
// a `kinds` entry that is a path, inlined from the app's modules the way a
// component kind is found, validated on its own for the kind editor.
import { describe, expect, it } from './harness.js';
import { validateScene, validateKind, isKindPath, kindDefinedBy, kindMembers, type SceneDoc } from '../src/lib/scene.js';
import { inlineKindFiles, registryFor } from '../src/lib/defs.js';

const pump = {
	type: 'Motor',
	model: 'models/pump.glb',
	status: '{Running?run:stopped} {SpeedHz:0} Hz',
	drive: [{ mesh: 'Impeller', spin: { axis: 'y', revPerS: { bind: 'SpeedHz', scale: 0.0333 } } }]
};
const doc: SceneDoc = {
	kinds: { pump: 'hmi/src/lib/kinds/pump.kind.json', tank: { type: 'Tank' } },
	nodes: [{ id: 'P1', kind: 'pump', tag: 'P1', pos: [0, 0, 0] }]
};
// What `import.meta.glob('$lib/**/*.kind.json', { eager: true })` gives: a
// JSON module per file, keyed from the app root.
const modules = { '/src/lib/kinds/pump.kind.json': { default: pump }, '/src/lib/Skid.svelte': { default: () => {} } };

describe('isKindPath', () => {
	it('a {Name}.kind.json relative to the scene file', () => {
		expect(isKindPath('hmi/src/lib/kinds/pump.kind.json')).toBe(true);
		expect(isKindPath('pump.kind.json')).toBe(true);
	});
	it('refuses a scheme, an absolute path, a climb, and another file', () => {
		for (const p of ['/abs/pump.kind.json', '../pump.kind.json', 'http://x/pump.kind.json', 'pump.json', 'pump.kind.jsonx', '', 3])
			expect(isKindPath(p)).toBe(false);
	});
});

describe('inlineKindFiles', () => {
	it('replaces the path with the module default export and says where it came from', () => {
		const r = inlineKindFiles(doc, modules);
		expect(r.errors).toEqual([]);
		expect(r.doc.kinds?.pump).toEqual(pump);
		expect(r.doc.kinds?.tank).toEqual({ type: 'Tank' });
		expect(r.sources).toEqual({ pump: '/src/lib/kinds/pump.kind.json' });
		// The document itself is untouched.
		expect(typeof doc.kinds?.pump).toBe('string');
	});
	it('takes a bare object module too', () => {
		const r = inlineKindFiles(doc, { '/src/lib/kinds/pump.kind.json': pump });
		expect(r.doc.kinds?.pump).toEqual(pump);
	});
	it('a path no module matches is an error at the entry, and the entry stays', () => {
		const r = inlineKindFiles(doc, { '/src/lib/Other.svelte': {} });
		expect(r.errors.map((e) => e.path)).toEqual(['/kinds/pump']);
		expect(r.errors[0].message).toContain('no module matches');
		expect(r.doc.kinds?.pump).toBe('hmi/src/lib/kinds/pump.kind.json');
	});
	it('a module that is not an object is an error', () => {
		const r = inlineKindFiles(doc, { '/src/lib/kinds/pump.kind.json': { default: [1] } });
		expect(r.errors[0].message).toContain('is not a kind');
	});
	it('a document with no path entries comes back as it was', () => {
		const d: SceneDoc = { kinds: { tank: { type: 'Tank' } }, nodes: [] };
		expect(inlineKindFiles(d, modules).doc).toBe(d);
	});
	it('the inlined document validates and its model kind is in the registry', () => {
		const r = inlineKindFiles(doc, modules);
		expect(validateScene(r.doc, ['tank', 'pump']).errors).toEqual([]);
		const gltf = (() => {}) as unknown as import('svelte').Component<import('../src/lib/defs.js').NodeProps>;
		const reg = registryFor(r.doc, {}, gltf);
		expect(reg.pump?.data).toEqual(pump);
		expect(reg.pump?.members).toBe(undefined);
	});
});

describe('validateScene with a path entry', () => {
	it('accepts a kind path as a kind the document brings, and refuses a bad one', () => {
		expect(validateScene(doc, ['tank']).errors).toEqual([]);
		const bad: SceneDoc = { kinds: { pump: '/abs/pump.kind.json' }, nodes: [] };
		const e = validateScene(bad).errors;
		expect(e.length).toBe(1);
		expect(e[0].path).toBe('/kinds/pump');
		expect(e[0].message).toContain('kind file');
	});
	it('a path entry defines nothing until it is inlined', () => {
		expect(kindDefinedBy('x.kind.json')).toBe('file');
		expect(kindMembers('x.kind.json')).toEqual([]);
		expect(registryFor(doc, {}, null).pump).toBe(undefined);
	});
});

describe('validateKind (a kind file on its own)', () => {
	it('a data kind passes', () => {
		expect(validateKind(pump)).toEqual({ ok: true, errors: [] });
	});
	it('paths are from the file root', () => {
		const e = validateKind({ model: 'x.png', status: '{Level', drive: [{ mesh: 'M', spin: { axis: 'q', revPerS: { bind: 'S' } } }] }).errors;
		expect(e.map((x) => x.path)).toEqual(['/model', '/status', '/drive/0/spin/axis']);
	});
	it('a component or an assembly stays inline in the scene', () => {
		const e = validateKind({ component: 'hmi/src/lib/Skid.svelte' }).errors;
		expect(e.some((x) => x.path === '/component' && x.message.includes('stays inline'))).toBe(true);
		expect(validateKind([]).ok).toBe(false);
	});
});
