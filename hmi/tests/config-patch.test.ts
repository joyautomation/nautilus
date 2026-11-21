// Editing a manifest where it changed and nowhere else (#711): the text
// patcher behind the config screens. The bar is the smallest diff, and a
// value the Go loader reads as the type the person meant.
import { describe, it, expect } from './harness.js';
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { parse } from 'yaml';
import { patch, renderScalar } from '../src/lib/config/yamlpatch.js';

const SAMPLE = `name: demo
# the field buses
drivers:
  - { type: modbus, manifest: modbus.yaml, scan-rate: 100ms }   # the PLC's meter
  - type: opcua-client
    endpoint: "opc.tcp://10.0.0.5:4840"
    manifest: ua.yaml
  - type: memory
devices:
  - { name: Meter,  profile: acme-meter, endpoint: 10.0.0.9 }
tags:
  - { name: A, role: input, type: DINT }
`;

// Lines in one text and not the other: an added or removed line counts 1, a
// changed line 2 (the old and the new).
function changedLines(a: string, b: string): number {
	const count = (t: string) => {
		const m = new Map<string, number>();
		for (const l of t.split('\n')) m.set(l, (m.get(l) ?? 0) + 1);
		return m;
	};
	const x = count(a), y = count(b);
	let n = 0;
	for (const k of new Set([...x.keys(), ...y.keys()])) n += Math.abs((x.get(k) ?? 0) - (y.get(k) ?? 0));
	return n;
}

describe('config patch: nothing changes unless something does', () => {
	it('setting a value to what it already is leaves every byte alone', () => {
		const out = patch(SAMPLE, [
			{ op: 'set', path: ['drivers', 0, 'scan-rate'], value: '100ms' },
			{ op: 'set', path: ['drivers', 1, 'manifest'], value: 'ua.yaml' },
			// written quoted; the patcher would write it plain: same value, so untouched
			{ op: 'set', path: ['drivers', 1, 'endpoint'], value: 'opc.tcp://10.0.0.5:4840' },
			{ op: 'set', path: ['name'], value: 'demo' }
		]);
		expect(out === SAMPLE).toBe(true);
	});
	it('no ops, no change', () => {
		expect(patch(SAMPLE, []) === SAMPLE).toBe(true);
	});
	it('a same-value set leaves each example manifest byte-identical (aligned columns, comments and all)', () => {
		let checked = 0;
		for (const d of readdirSync('../examples')) {
			const f = `../examples/${d}/nautilus.yaml`;
			if (!existsSync(f)) continue;
			const text = readFileSync(f, 'utf8');
			const doc = parse(text) as { name?: unknown; drivers?: unknown[]; driver?: Record<string, unknown> };
			const ops = [];
			if (typeof doc.name === 'string') ops.push({ op: 'set' as const, path: ['name'], value: doc.name });
			if (doc.driver && typeof doc.driver.type === 'string') ops.push({ op: 'set' as const, path: ['driver', 'type'], value: doc.driver.type });
			(doc.drivers ?? []).forEach((r, i) => {
				const t = (r as { type?: unknown }).type;
				if (typeof t === 'string') ops.push({ op: 'set' as const, path: ['drivers', i, 'type'], value: t });
			});
			expect(patch(text, ops) === text).toBe(true);
			checked++;
		}
		expect(checked > 5).toBe(true);
	});
});

describe('config patch: a change touches only its own text', () => {
	it('replaces a scalar in a flow map and keeps its trailing comment', () => {
		const out = patch(SAMPLE, [{ op: 'set', path: ['drivers', 0, 'scan-rate'], value: '250ms' }]);
		expect(out).toBe(SAMPLE.replace('scan-rate: 100ms }', 'scan-rate: 250ms }'));
	});
	it('replaces a scalar in a block map', () => {
		const out = patch(SAMPLE, [{ op: 'set', path: ['drivers', 1, 'manifest'], value: 'other.yaml' }]);
		expect(changedLines(SAMPLE, out)).toBe(2);
		expect(parse(out).drivers[1].manifest).toBe('other.yaml');
	});
	it('adds a key to a flow map before its closing brace', () => {
		const out = patch(SAMPLE, [{ op: 'set', path: ['drivers', 0, 'validate'], value: 'static' }]);
		expect(out.includes('scan-rate: 100ms, validate: static }')).toBe(true);
		expect(changedLines(SAMPLE, out)).toBe(2);
	});
	it('adds a key to a block map at the map\'s own indent', () => {
		const out = patch(SAMPLE, [{ op: 'set', path: ['drivers', 1, 'scan-rate'], value: '1s' }]);
		expect(out.includes('    manifest: ua.yaml\n    scan-rate: 1s\n  - type: memory')).toBe(true);
		expect(changedLines(SAMPLE, out)).toBe(1);
	});
	it('removes a key from a flow map and from a block map', () => {
		const a = patch(SAMPLE, [{ op: 'delete', path: ['drivers', 0, 'scan-rate'] }]);
		expect(a.includes('{ type: modbus, manifest: modbus.yaml }')).toBe(true);
		const b = patch(SAMPLE, [{ op: 'delete', path: ['drivers', 1, 'manifest'] }]);
		expect(b.includes('manifest: ua.yaml')).toBe(false);
		expect(parse(b).drivers[1].endpoint).toBe('opc.tcp://10.0.0.5:4840');
		expect(changedLines(SAMPLE, b)).toBe(1);
	});
	it('refuses to delete the first key of a block row (it shares its line with the dash)', () => {
		let msg = '';
		try {
			patch(SAMPLE, [{ op: 'delete', path: ['drivers', 1, 'type'] }]);
		} catch (e) {
			msg = String(e);
		}
		expect(msg.includes('delete the row')).toBe(true);
	});
	it('removes a row: a flow row and a block row, each by its own lines', () => {
		const a = patch(SAMPLE, [{ op: 'delete', path: ['drivers', 0] }]);
		expect(a.includes('modbus.yaml')).toBe(false);
		expect(parse(a).drivers.length).toBe(2);
		const b = patch(SAMPLE, [{ op: 'delete', path: ['drivers', 1] }]);
		expect(b.includes('ua.yaml')).toBe(false);
		expect(parse(b).drivers.map((r: { type: string }) => r.type).join()).toBe('modbus,memory');
		// The only row of a list takes the list's key with it.
		const c = patch(SAMPLE, [{ op: 'delete', path: ['devices', 0] }]);
		expect(c.includes('Meter') || c.includes('devices:')).toBe(false);
		expect(c.includes('tags:')).toBe(true);
	});
	it('appends a row in the style of the last one', () => {
		const flow = patch(SAMPLE, [{ op: 'append', path: ['devices'], value: { name: 'Pump', profile: 'p', endpoint: '10.0.0.10' } }]);
		expect(flow.includes('  - { name: Pump, profile: p, endpoint: 10.0.0.10 }\ntags:')).toBe(true);
		const block = patch(SAMPLE, [{ op: 'append', path: ['drivers'], value: { type: 'memory' } }]);
		expect(block.includes('  - type: memory\n  - type: memory\ndevices:')).toBe(true);
		expect(parse(block).drivers.length).toBe(4);
	});
	it('creates a list that is not there yet', () => {
		const text = 'name: x\ntags:\n  - { name: A, role: input, type: DINT }\n';
		const out = patch(text, [{ op: 'append', path: ['devices'], value: { name: 'M', profile: 'p', endpoint: 'h' } }]);
		expect(out).toBe(text + 'devices:\n  - { name: M, profile: p, endpoint: h }\n');
	});
	it('every edit keeps the file parseable and everything else equal', () => {
		const ops = [
			{ op: 'set' as const, path: ['drivers', 0, 'scan-rate'], value: '5s' },
			{ op: 'set' as const, path: ['drivers', 2, 'host'], value: 'plc.local' },
			{ op: 'delete' as const, path: ['devices', 0] },
			{ op: 'append' as const, path: ['devices'], value: { name: 'N', profile: 'q', endpoint: 'e' } }
		];
		const out = patch(SAMPLE, ops);
		expect(out.includes('devices:\n  - { name: N, profile: q, endpoint: e }')).toBe(false); // appended at the file's end, after tags
		const want = parse(SAMPLE);
		want.drivers[0]['scan-rate'] = '5s';
		want.drivers[2].host = 'plc.local';
		delete want.devices;
		const got = parse(out);
		expect(JSON.stringify(got.devices)).toBe(JSON.stringify([{ name: 'N', profile: 'q', endpoint: 'e' }]));
		delete got.devices; // the list was emptied (its key went) and recreated at the end
		expect(JSON.stringify(got)).toBe(JSON.stringify(want));
		expect(out.includes('# the field buses')).toBe(true);
		expect(out.includes("# the PLC's meter")).toBe(true);
	});
});

describe('config patch: a value reads back as what was meant', () => {
	const cases: [unknown, string][] = [
		[true, 'true'],
		[false, 'false'],
		[12, '12'],
		[0.5, '0.5'],
		['100ms', '100ms'],
		['on', '"on"'],
		['yes', '"yes"'],
		['No', '"No"'],
		['0x10', '"0x10"'],
		['010', '"010"'],
		['1e3', '"1e3"'],
		['12', '"12"'],
		['null', '"null"'],
		['~', '"~"'],
		['', '""'],
		['a: b', '"a: b"'],
		['# not a comment', '"# not a comment"'],
		['opc.tcp://10.0.0.5:4840', 'opc.tcp://10.0.0.5:4840'],
		['10.0.0.5', '10.0.0.5'],
		['café', '"café"']
	];
	for (const [v, text] of cases) {
		it(`${JSON.stringify(v)} is written ${text} and reads back the same`, () => {
			expect(renderScalar(v as never)).toBe(text);
			expect(parse(`k: ${text}`).k).toBe(v);
		});
	}
});
