// The config screens' pure kit (#711): the schema walker over the real
// manifest schema, the validator, the client's refusals, the form's
// edit-to-overlay path, and that only the /config entry carries the YAML
// parser.
import { describe, it, expect } from './harness.js';
import { readFileSync } from 'node:fs';
import { HANDLED, sectionField, rawFields, validate, type Field, type Json, type Schema } from '../src/lib/config/schema.js';
import { ConfigClient, classify } from '../src/lib/config/client.js';
import { patch } from '../src/lib/config/yamlpatch.js';
import { driverRows, deviceRows, setField, addDriver, addDevice, removeRow, overlay } from '../src/lib/config/form.js';

const full = JSON.parse(readFileSync('../tools/vscode-iec/schemas/nautilus.schema.json', 'utf8')) as {
	properties: Record<string, Schema>;
	definitions: Record<string, Schema>;
};
// What GET /api/config/schema?section=… serves, minus the pruning.
const slice = (section: string): Schema => ({ properties: { [section]: full.properties[section] }, definitions: full.definitions });

describe('config schema: the keywords the drivers and devices sections use', () => {
	// Every keyword under definitions.driver / device / what they reach.
	const used = new Set<string>();
	const seen = new Set<string>();
	const walk = (n: unknown, inProps = false) => {
		if (Array.isArray(n)) return n.forEach((x) => walk(x));
		if (n === null || typeof n !== 'object') return;
		for (const [k, v] of Object.entries(n as Record<string, unknown>)) {
			if (inProps) {
				walk(v); // a property name, not a keyword
				continue;
			}
			used.add(k);
			if (k === '$ref') {
				const name = String(v).replace('#/definitions/', '');
				if (!seen.has(name)) {
					seen.add(name);
					walk(full.definitions[name]);
				}
			} else walk(v, k === 'properties');
		}
	};
	for (const s of ['driver', 'device']) {
		seen.add(s);
		walk(full.definitions[s]);
	}
	const IGNORED = ['description', 'title', '$comment', 'default', 'examples', '$schema', 'definitions', 'anyOf', 'not'];
	it('are all handled, or ignored on purpose (then.anyOf / then.not are the server\'s to judge)', () => {
		const unhandled = [...used].filter((k) => !HANDLED.has(k) && !IGNORED.includes(k));
		expect(unhandled.join(',')).toBe('');
	});
	it('anyOf and not occur only inside an if/then, never on a field', () => {
		const onField: string[] = [];
		const props = (n: Schema) => Object.entries((n.properties ?? {}) as Record<string, Schema>);
		for (const s of ['driver', 'device']) {
			for (const [k, p] of props(full.definitions[s])) if ('anyOf' in p || 'not' in p) onField.push(`${s}.${k}`);
		}
		expect(onField.join(',')).toBe('');
	});
});

describe('config schema: the field tree', () => {
	it('drivers is a list of driver objects with a type enum and typed fields', () => {
		const f = sectionField(slice('drivers'), 'drivers');
		expect(f.kind).toBe('list');
		const d = f.item as Field;
		expect(d.kind).toBe('object');
		const by = (k: string) => (d.fields as Field[]).find((x) => x.key === k) as Field;
		expect(by('type').kind).toBe('enum');
		expect((by('type').enum as string[]).includes('modbus')).toBe(true);
		expect(by('slot').kind).toBe('integer');
		expect(by('primary').kind).toBe('boolean');
		expect(by('scan-rate').kind).toBe('string');
		expect(typeof by('scan-rate').pattern).toBe('string'); // a duration
		expect(by('scan-classes').kind).toBe('map');
		expect(by('group-ids').kind).toBe('list');
		expect(d.open).toBe(false);
	});
	it('the if/then rules become conditional requirements', () => {
		const d = sectionField(slice('driver'), 'driver');
		const eip = (d.conditional ?? []).find((c) => c.equals === 'eip');
		expect((eip?.required ?? []).join()).toBe('host,manifest');
	});
	it('devices rows have their required fields and a ports map with a key rule', () => {
		const f = sectionField(slice('devices'), 'devices');
		const row = f.item as Field;
		expect(((row.fields as Field[]).filter((x) => x.required).map((x) => x.key)).join()).toBe('name,profile,endpoint');
		const ports = (row.fields as Field[]).find((x) => x.key === 'ports') as Field;
		expect(['map', 'raw'].includes(ports.kind)).toBe(true);
	});
	it('a node with a keyword the form does not understand is raw, with its reason, never dropped', () => {
		const f = sectionField({ properties: { x: { type: 'object', properties: { a: { type: 'string', format: 'uri' }, b: { oneOf: [{ type: 'string' }] } } } } }, 'x');
		const raws = rawFields(f);
		expect(raws.map((r) => r.path.join('.')).join()).toBe('x.a,x.b');
		expect(raws[0].why).toBe('uses format');
	});
	it('the drivers, driver and devices sections have no raw field at all', () => {
		for (const s of ['drivers', 'driver', 'devices']) {
			expect(JSON.stringify(rawFields(sectionField(slice(s), s)))).toBe('[]');
		}
	});
});

describe('config schema: the validator', () => {
	const drivers = sectionField(slice('drivers'), 'drivers');
	const issues = (v: Json) => validate(drivers, v).map((i) => i.message);
	it('accepts a plain row and an untouched empty section', () => {
		expect(issues([{ type: 'modbus', manifest: 'modbus.yaml', 'scan-rate': '100ms' }]).length).toBe(0);
		expect(validate({ ...drivers, required: false }, undefined).length).toBe(0);
	});
	it('names a bad enum, a bare-number duration, a wrong type and a stray key', () => {
		const m = issues([{ type: 'nope', manifest: 'm.yaml', 'scan-rate': 100, slot: 1.5, bogus: 1 }]).join(' | ');
		expect(m.includes('must be one of')).toBe(true);
		expect(m.includes('scan-rate must be text')).toBe(true);
		expect(m.includes('slot must be a whole number')).toBe(true);
		expect(m.includes('bogus is not a setting here')).toBe(true);
	});
	it('a duration needs its unit', () => {
		expect(issues([{ type: 'modbus', manifest: 'm.yaml', 'scan-rate': '100' }]).join().includes('scan-rate is not in the form')).toBe(true);
		expect(issues([{ type: 'modbus', manifest: 'm.yaml', 'scan-rate': '1h30m' }]).length).toBe(0);
	});
	it('requires what the type requires, and only then', () => {
		expect(issues([{ type: 'eip' }]).join().includes('host is required when type is eip')).toBe(true);
		expect(issues([{ type: 'memory' }]).length).toBe(0);
	});
	it('an empty manifest path is refused (minLength)', () => {
		expect(issues([{ type: 'modbus', manifest: '' }]).join().includes('manifest must not be empty')).toBe(true);
	});
});

describe('config client: every refusal says what to do', () => {
	const fake = (status: number, body: unknown) => (async () => new Response(typeof body === 'string' ? body : JSON.stringify(body), { status })) as unknown as typeof fetch;
	it('reads a slice and the files', async () => {
		const c = new ConfigClient({ fetch: fake(200, { leading: true, manifest: 'nautilus.yaml', files: { 'nautilus.yaml': 'x' } }) });
		const r = await c.files();
		expect(r.ok && r.value.manifest === 'nautilus.yaml').toBe(true);
	});
	it('a standby is a failure that says to open the leader', async () => {
		const r = await new ConfigClient({ fetch: fake(200, { leading: false, manifest: 'n', files: {} }) }).files();
		expect(!r.ok && r.kind === 'standby' && r.message.includes('Open the leader')).toBe(true);
	});
	it('401 says sign in, 403 names the permission and online-edits, 404 names the section, 503 says what is missing', async () => {
		const get = async (s: number) => {
			const r = await new ConfigClient({ fetch: fake(s, { error: 'no such manifest section "x" (sections: a, b)' }) }).schema('x');
			return r.ok ? '' : `${r.kind}: ${r.message}`;
		};
		expect((await get(401)).startsWith('signin: Sign in')).toBe(true);
		const f = await get(403);
		expect(f.includes('forbidden') && f.includes('online-edits')).toBe(true);
		expect((await get(404)).includes('sections: a, b')).toBe(true);
		expect((await get(503)).includes('live I/O configuration')).toBe(true);
	});
	it('a connection that fails is unreachable, not a crash', async () => {
		const c = new ConfigClient({ fetch: (async () => { throw new Error('boom'); }) as unknown as typeof fetch });
		const r = await c.files();
		expect(!r.ok && r.kind === 'unreachable' && r.message.includes('boom')).toBe(true);
	});
	it('sends the token and encodes the section', async () => {
		let url = '', tok = '';
		const f = (async (u: string, init?: RequestInit) => {
			url = u;
			tok = (init?.headers as Record<string, string>)['X-Nautilus-Token'];
			return new Response('{}', { status: 200 });
		}) as unknown as typeof fetch;
		await new ConfigClient({ fetch: f, token: 't' }).schema('tag-files');
		expect(url).toBe('/api/config/schema?section=tag-files');
		expect(tok).toBe('t');
	});
	it('classify keeps the controller\'s own text for anything else', () => {
		expect(classify(500, '{"error":"disk on fire"}').message.includes('disk on fire')).toBe(true);
	});
});

describe('config form: edits become an overlay of only the files that changed', () => {
	const m = 'name: x\ndrivers:\n  - { type: modbus, manifest: modbus.yaml, scan-rate: 100ms }\ndevices:\n  - { name: M, profile: p, endpoint: h }\n';
	const files = { 'nautilus.yaml': m, 'modbus.yaml': 'sources: []\n' };
	it('reads the rows', () => {
		expect(driverRows(m)[0].path.join()).toBe('drivers,0');
		expect(deviceRows(m)[0].value.name).toBe('M');
		expect(driverRows('driver:\n  type: memory\n')[0].path.join()).toBe('driver');
	});
	it('an untouched form is an empty overlay', () => {
		expect(overlay(files, { 'nautilus.yaml': [] }).changed.length).toBe(0);
		expect(overlay(files, { 'nautilus.yaml': [setField(['drivers', 0], 'scan-rate', '100ms')] }).changed.length).toBe(0);
	});
	it('a change is an overlay of just that file', () => {
		const o = overlay(files, { 'nautilus.yaml': [setField(['drivers', 0], 'scan-rate', '250ms'), setField(['drivers', 0], 'manifest', undefined)] });
		expect(o.changed.join()).toBe('nautilus.yaml');
		expect(o.files['nautilus.yaml'].includes('{ type: modbus, scan-rate: 250ms }')).toBe(true);
	});
	it('adds a device and a driver; refuses to add one to a single driver: manifest, and to drop the last driver', () => {
		const o = overlay(files, { 'nautilus.yaml': [addDevice({ name: 'Pump', profile: 'q', endpoint: 'e' }), addDriver(m, { type: 'memory' })] });
		expect(o.files['nautilus.yaml'].includes('  - { name: Pump, profile: q, endpoint: e }')).toBe(true);
		let msg = '';
		try { addDriver('driver:\n  type: memory\n', { type: 'x' }); } catch (e) { msg = String(e); }
		expect(msg.includes('drivers: list')).toBe(true);
		msg = '';
		try { removeRow(m, driverRows(m)[0]); } catch (e) { msg = String(e); }
		expect(msg.includes('last driver')).toBe(true);
	});
	it('both driver: and drivers: is refused with the fix, never edited', () => {
		let msg = '';
		try { driverRows('driver: { type: memory }\ndrivers:\n  - { type: memory }\n'); } catch (e) { msg = String(e); }
		expect(msg.includes('fix the manifest first')).toBe(true);
	});
	it('a file the controller did not send is refused', () => {
		let msg = '';
		try { overlay(files, { 'other.yaml': [] }); } catch (e) { msg = String(e); }
		expect(msg.includes('not one of the files')).toBe(true);
	});
});

describe('the root export does not carry the YAML parser', () => {
	// Follow every import/export from src/lib/index.ts, through .ts and
	// .svelte files, and fail if the graph reaches config/ or `yaml`.
	const seen = new Set<string>();
	const reach: string[] = [];
	const resolve = (from: string, spec: string): string => {
		const dir = from.slice(0, from.lastIndexOf('/'));
		const parts = (dir + '/' + spec).split('/');
		const out: string[] = [];
		for (const p of parts) {
			if (p === '..') out.pop();
			else if (p !== '.') out.push(p);
		}
		return out.join('/').replace(/\.js$/, '.ts');
	};
	const visit = (file: string) => {
		if (seen.has(file)) return;
		seen.add(file);
		let src = '';
		try { src = readFileSync(file, 'utf8'); } catch { return; }
		for (const m of src.matchAll(/(?:from|import)\s+['"]([^'"]+)['"]/g)) {
			const spec = m[1];
			if (spec === 'yaml') reach.push(`${file} imports yaml`);
			if (spec.startsWith('.')) {
				const next = resolve(file, spec);
				if (next.includes('/config/')) reach.push(`${file} imports ${next}`);
				visit(next);
			}
		}
	};
	it('src/lib/index.ts reaches neither config/ nor yaml', () => {
		visit('src/lib/index.ts');
		expect(seen.size > 20).toBe(true); // it did walk the kit
		expect(reach.join('; ')).toBe('');
	});
	it('the ./config entry is in the package exports', () => {
		const pkg = JSON.parse(readFileSync('package.json', 'utf8')) as { exports: Record<string, unknown>; dependencies: Record<string, string> };
		expect('./config' in pkg.exports).toBe(true);
		expect('yaml' in pkg.dependencies).toBe(true);
	});
});

describe('config patch fixtures (also read by the Go loader)', () => {
	const fx = JSON.parse(readFileSync('tests/config-fixtures.json', 'utf8')) as { cases: { name: string; before: string; ops: never[]; after: string }[] };
	it('has the cases', () => {
		expect(fx.cases.length > 10).toBe(true);
	});
	for (const c of fx.cases) {
		it(`${c.name}: patch(before, ops) is the committed after`, () => {
			expect(patch(c.before, c.ops)).toBe(c.after);
		});
	}
});
