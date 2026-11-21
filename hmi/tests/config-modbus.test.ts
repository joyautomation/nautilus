// The Modbus points editor's pure half (#711 slice 5a).
import { describe, it, expect } from './harness.js';
import { readFileSync } from 'node:fs';
import { parse } from 'yaml';
import { parseModbus, modbusManifests, setSourceField, setTagField, problems } from '../src/lib/config/modbus.js';
import { patch } from '../src/lib/config/yamlpatch.js';
import { overlay } from '../src/lib/config/form.js';

const T = `# generated
sources:
  - id: P1
    host: 10.0.0.1
    unitid: 1
  - { id: P2, host: 10.0.0.2, port: 1502 }
tags:
  - {name: A, source: P1, table: holding, address: 2000, format: bit:0}   # status
  - {name: B, source: P1, table: holding, address: 2001, format: int16, scale: 0.1, scanclass: fast}
  - {name: C, source: P2, table: coil, address: 3, format: bool, writable: true}
  - {name: D, source: P2, table: input, address: 7, format: uint16}
`;
const doc = (t: string) => parse(t) as { sources: Record<string, unknown>[]; tags: Record<string, unknown>[] };

describe('modbus points', () => {
	it('reads sources and tags, with the columns the editor shows', () => {
		const m = parseModbus(T);
		expect(m.sources.length).toBe(2);
		expect(m.sources[1].port).toBe(1502);
		expect(m.tags.length).toBe(4);
		expect(m.tags[1].scanclass).toBe('fast');
		expect(m.tags[2].writable).toBe(true);
		expect(parseModbus('').tags.length).toBe(0);
		expect(parseModbus('tags: [1, {name: X}]').tags.length).toBe(1);
	});
	it('an edit changes one value and leaves comments and layout alone', () => {
		const out = patch(T, [setTagField(0, 'address', 2005)]);
		expect(doc(out).tags[0].address).toBe(2005);
		expect(out.replace('2005', '2000') === T).toBe(true);
		const s = patch(T, [setSourceField(0, 'host', '10.9.9.9')]);
		expect(doc(s).sources[0].host).toBe('10.9.9.9');
		expect(s.includes('# generated') && s.includes('# status')).toBe(true);
	});
	it('sets, adds and clears optional fields', () => {
		expect(doc(patch(T, [setTagField(0, 'scanclass', 'slow')])).tags[0].scanclass).toBe('slow');
		expect(doc(patch(T, [setTagField(1, 'scanclass', undefined)])).tags[1].scanclass === undefined).toBe(true);
		expect(doc(patch(T, [setSourceField(0, 'port', 503)])).sources[0].port).toBe(503);
		expect(doc(patch(T, [setSourceField(1, 'port', undefined)])).sources[1].port === undefined).toBe(true);
	});
	it('an edit that changes nothing is byte-identical', () => {
		expect(patch(T, [setTagField(0, 'table', 'holding'), setSourceField(0, 'unitid', 1)]) === T).toBe(true);
	});
	it('finds the manifests of modbus drivers the controller sent', () => {
		const nautilus = 'drivers:\n  - { type: modbus, name: a, manifest: m.yaml }\n  - { type: opcua-client, manifest: u.yaml }\n  - { type: modbus, manifest: gone.yaml }\n';
		const r = modbusManifests({ 'nautilus.yaml': nautilus, 'm.yaml': T, 'u.yaml': 'x: 1' }, 'nautilus.yaml');
		expect(JSON.stringify(r)).toBe(JSON.stringify([{ path: 'm.yaml', driver: 'a', classes: [], tagClasses: false }]));
		const c = modbusManifests({ 'nautilus.yaml': 'drivers:\n  - { type: modbus, manifest: m.yaml, scan-classes: { fast: 50ms, slow: 1s } }\n', 'm.yaml': T }, 'nautilus.yaml');
		expect(c[0].classes.join()).toBe('fast,slow');
		expect(modbusManifests({ 'nautilus.yaml': 'drivers: [' }, 'nautilus.yaml').length).toBe(0);
	});
	it('names what is wrong before staging', () => {
		expect(problems(parseModbus(T), ['fast']).length).toBe(0);
		const bad = parseModbus(T.replace('address: 3,', 'address: 70000,').replace('table: input', 'table: nope').replace('port: 1502', 'port: 99999').replace('host: 10.0.0.1', 'host: ""').replace('unitid: 1', 'unitid: 300'));
		const msg = problems(bad, ['fast']).map((p) => p.message).join(' | ');
		for (const s of ['needs a host', 'port must be', 'unit id must be', 'address must be', 'table must be one of']) expect(msg.includes(s)).toBe(true);
		const w = problems(parseModbus(T.replace('table: coil', 'table: discrete'))).map((p) => p.message).join();
		expect(w.includes('only the holding and coil tables can be written')).toBe(true);
		expect(problems(parseModbus(T.replace('source: P2, table: input', 'source: PX, table: input'))).map((p) => p.message).join().includes('names source PX')).toBe(true);
	});
	it('a bit table takes only bool', () => {
		const msg = (t: string) => problems(parseModbus(t), ['fast']).map((p) => p.message).join();
		expect(msg(T.replace('table: input, address: 7, format: uint16', 'table: discrete, address: 7, format: uint16')).includes('must be bool')).toBe(true);
		expect(msg(T.replace('table: coil, address: 3, format: bool', 'table: coil, address: 3, format: int16')).includes('must be bool')).toBe(true);
		expect(msg(T.replace('table: input, address: 7, format: uint16', 'table: discrete, address: 7, format: bool')).includes('must be bool')).toBe(false);
	});
	it('a non-numeric port or unit id is named, not shown as empty', () => {
		const msg = problems(parseModbus(T.replace('unitid: 1', 'unitid: abc').replace('port: 1502', 'port: "x"')), ['fast']).map((p) => p.message).join(' | ');
		expect(msg.includes('port must be')).toBe(true);
		expect(msg.includes('unit id must be')).toBe(true);
		expect(parseModbus(T.replace('unitid: 1', 'unitid: abc')).sources[0].unitid).toBe('abc');
	});
	it('a file that does not parse is one named problem, not a throw', () => {
		const m = parseModbus('sources:\n  - {id: A\n\ttags: [');
		expect(m.tags.length).toBe(0);
		expect(problems(m).map((p) => p.message).join().includes('not valid YAML')).toBe(true);
	});
	it('two drivers on one manifest: only classes both declare, both named, tag-classes noted', () => {
		const n = 'drivers:\n  - { type: modbus, name: a, manifest: m.yaml, scan-classes: { fast: 50ms, slow: 1s } }\n  - { type: modbus, name: b, manifest: m.yaml, scan-classes: { slow: 1s }, tag-classes: { "P*": slow } }\n';
		const r = modbusManifests({ 'nautilus.yaml': n, 'm.yaml': T }, 'nautilus.yaml');
		expect(r.length).toBe(1);
		expect(r[0].driver).toBe('a, b');
		expect(r[0].classes.join()).toBe('slow');
		expect(r[0].tagClasses).toBe(true);
		expect(modbusManifests({ 'nautilus.yaml': 'drivers:\n  - { type: modbus, manifest: m.yaml, tag-classes: {} }\n', 'm.yaml': T }, 'nautilus.yaml')[0].tagClasses).toBe(false);
	});
	it('a scan class the driver does not declare is named; declared ones and none pass', () => {
		const m = parseModbus(T); // tag B has scanclass: fast
		expect(problems(m, []).map((p) => p.message).join().includes('scan class fast is not declared')).toBe(true);
		expect(problems(m, ['fast']).length).toBe(0);
		expect(problems(parseModbus(T.replace('scanclass: fast', 'scanclass: none')), []).length).toBe(0);
	});
	it('the overlay carries a points edit as that one file', () => {
		const o = overlay({ 'nautilus.yaml': 'x: 1\n', 'm.yaml': T }, { 'm.yaml': [setTagField(3, 'address', 8)] });
		expect(o.changed.join()).toBe('m.yaml');
	});
	it('reads the real example manifests', () => {
		for (const f of ['lift-station/modbus_manifest.yaml', 'bin-fill/modbus_manifest.yaml']) {
			const m = parseModbus(readFileSync('../examples/' + f, 'utf8'));
			expect(m.tags.length > 0).toBe(true);
			expect(problems(m).length).toBe(0);
		}
	});
});
