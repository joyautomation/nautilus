// The contract in three places (docs/design/spatial-hmi.md §3b, §3c): this
// package, internal/scene (Go) and the extension's JSON schema. These
// specs read the schema and the built-ins' kinds.json from the repo, so a
// channel or a kind added on one side without the others fails here
// rather than for a user. Skipped when run outside the monorepo.
import { readFileSync, existsSync } from 'node:fs';
import { describe, expect, it } from './harness.js';

// The package carries no @types/node (see harness.ts); `npm test` runs
// from the package root, which is what the two paths below assume.
declare const process: { cwd(): string };
import { DRIVE_CHANNELS, driveMembers, type Drive } from '../src/lib/drives.js';
import { validateScene, kindMembers, type SceneKind } from '../src/lib/scene.js';
import { BUILTIN_CONTRACT } from '../src/lib/kinds.js';

const SCHEMA = process.cwd() + '/../tools/vscode-iec/schemas/nautilus-scene.schema.json';
const KINDS = process.cwd() + '/models/kinds.json';

describe('the drive vocabulary matches the extension schema', () => {
	if (!existsSync(SCHEMA)) return;
	const schema = JSON.parse(readFileSync(SCHEMA, 'utf8'));
	const drive = schema.definitions.drive;
	it('one channel per oneOf branch, in the same order', () => {
		expect(drive.oneOf.map((o: { required: string[] }) => o.required[0])).toEqual([...DRIVE_CHANNELS]);
	});
	it('every channel is a property', () => {
		for (const c of DRIVE_CHANNELS) expect(Object.keys(drive.properties)).toContain(c);
	});
	it('the environment block has the fields scene.ts reads', () => {
		expect(Object.keys(schema.definitions.environment.properties).sort()).toEqual(['backdrop', 'background', 'floor', 'fog', 'hdri', 'intensity', 'shadows']);
	});
	it('the kind contract has the data-kind, component and assembly fields (§3c, §3d)', () => {
		expect(Object.keys(schema.definitions.kindContract.properties).sort()).toEqual(['assembly', 'bounds', 'component', 'drive', 'labelAt', 'members', 'model', 'status', 'type']);
		expect(Object.keys(schema.definitions.assembly.properties).sort()).toEqual(['nodes', 'pipes']);
	});
	it('the schema accepts what validateScene accepts for a component kind and an assembly', () => {
		// The schema's component pattern and validateScene's isComponentPath agree.
		const re = new RegExp(schema.definitions.kindContract.properties.component.pattern);
		for (const [p, ok] of [['hmi/src/lib/Skid.svelte', true], ['Skid.svelte', true], ['/abs/Skid.svelte', false], ['../Skid.svelte', false], ['http://x/S.svelte', false], ['a/b.ts', false]] as const)
			expect(re.test(p)).toBe(ok);
	});
});

describe('the built-ins as data (models/kinds.json)', () => {
	if (!existsSync(KINDS)) return;
	const kinds = JSON.parse(readFileSync(KINDS, 'utf8')) as Record<string, SceneKind>;
	it('declares exactly the built-in kinds, each with its own model', () => {
		expect(Object.keys(kinds).sort()).toEqual(Object.keys(BUILTIN_CONTRACT).sort());
		for (const [name, k] of Object.entries(kinds)) expect(k.model).toBe(`models/${name}.glb`);
	});
	it("reads only members the built-in's contract lists", () => {
		for (const [name, k] of Object.entries(kinds))
			for (const m of driveMembers(k.drive as Drive[])) expect(BUILTIN_CONTRACT[name].members ?? []).toContain(m);
	});
	it('validates as a scene', () => {
		const doc = { kinds, nodes: [] };
		expect(validateScene(doc, Object.keys(BUILTIN_CONTRACT))).toEqual({ ok: true, errors: [] });
	});
	it('kindMembers is the union with the drives', () => {
		expect(kindMembers({ members: ['Running'], drive: kinds.pump.drive, status: '{Fault?FAULT:ok}' })).toEqual(['Running', 'Speed', 'Fault']);
	});
});
