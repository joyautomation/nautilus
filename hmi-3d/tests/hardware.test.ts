// Unit tests for chassis profiles (src/lib/hardware/profile.ts): the
// committed SYS-112B-WR profile is valid and places node1's inventory, tags
// resolve from `{node}`, and a part's state ladder reads the way an
// operator needs it to (a pulled drive is ABSENT, never healthy).
import { describe, expect, it } from './harness.js';
import sys112b from '../profiles/supermicro-sys-112b-wr.json';
import {
	validateProfile,
	resolveParts,
	serverTags,
	tagFor,
	partState,
	partStatus,
	partFacts,
	capacity,
	isFitted,
	type ChassisProfile
} from '../src/lib/hardware/profile.js';

const profile = sys112b as unknown as ChassisProfile;
const clone = (): ChassisProfile => JSON.parse(JSON.stringify(profile));

describe('the SYS-112B-WR profile', () => {
	it('validates', () => {
		expect(validateProfile(profile)).toEqual([]);
	});
	it('has the chassis positions: 10 front bays, 2 boot M.2, 8 DIMM slots, 3 PCIe slots, 1 CPU, 6 fans, 2 PSUs', () => {
		const count = (k: string) => profile.parts.filter((p) => p.kind === k).length;
		expect(profile.parts.filter((p) => p.kind === 'drive' && p.form !== 'm2').length).toBe(10);
		expect(profile.parts.filter((p) => p.form === 'm2').length).toBe(2);
		expect(count('dimm')).toBe(8);
		expect(count('pcie-card')).toBe(3);
		expect(count('cpu')).toBe(1);
		expect(count('fan')).toBe(6);
		expect(count('psu')).toBe(2);
	});
	it('binds the tag names agreed with the drivers workstream', () => {
		const tags = serverTags(profile, 'NODE1');
		for (const t of ['NODE1', 'NODE1_Drive_NVMe0', 'NODE1_Drive_NVMe3', 'NODE1_Drive_Boot0', 'NODE1_Drive_Boot4', 'NODE1_Pcie_Slot2', 'NODE1_Pcie_Slot3', 'NODE1_Dimm_A1', 'NODE1_Dimm_G1', 'NODE1_Cpu1', 'NODE1_Fan6', 'NODE1_PSU2'])
			expect(tags).toContain(t);
	});
	it('carries a QR anchor on the chassis', () => {
		expect(profile.anchors?.qr?.size).toBe(100);
	});
});

describe('resolveParts', () => {
	const parts = resolveParts(profile, 'NODE1');
	it('names each part {node}/{id} and resolves its tag', () => {
		const bay0 = parts.find((p) => p.partId === 'bay0')!;
		expect(bay0.id).toBe('NODE1/bay0');
		expect(bay0.tag).toBe('NODE1_Drive_NVMe0');
		expect(parts.find((p) => p.partId === 'bay5')!.tag).toBe(undefined);
	});
	it('converts millimetres to metres and keeps extras as props', () => {
		const bay0 = parts.find((p) => p.partId === 'bay0')!;
		expect(bay0.size).toEqual([0.04, 0.038, 0.14]);
		expect(bay0.props.bus).toBe('nvme');
		expect(bay0.props.id).toBe(undefined);
	});
	it('gives every part an explode offset (zero when the profile has none)', () => {
		for (const p of parts) expect(p.explode.length).toBe(3);
	});
});

describe('validateProfile', () => {
	it('rejects a part outside the chassis', () => {
		const p = clone();
		p.parts[0].pos = [0, 20, 30];
		expect(validateProfile(p).map((e) => e.path)).toEqual(['/parts/0/pos']);
	});
	it('rejects a duplicate id and a binding to no part', () => {
		const p = clone();
		p.parts[1].id = p.parts[0].id;
		p.bindings.nope = '{node}_X';
		const paths = validateProfile(p).map((e) => e.path);
		expect(paths).toContain('/parts/1/id');
		expect(paths).toContain('/bindings/nope');
	});
	it('wants {node} in a binding', () => {
		const p = clone();
		p.bindings.bay0 = 'NODE1_Drive_NVMe0';
		expect(validateProfile(p).map((e) => e.path)).toEqual(['/bindings/bay0']);
	});
});

describe('part state', () => {
	const bound = { tag: 'NODE1_Drive_NVMe0' };
	const drive = { Bay: 0, Model: 'MZQL21T9HCJR-00A07', CapacityGB: 1920, Protocol: 'NVMe', Health: 0, Fault: false, PredictedFailure: false, TempC: 32, Present: true };
	it('reads the ladder: unbound, assumed, missing, absent, stale, critical, warning, ok', () => {
		expect(partState({}, undefined, true)).toBe('unbound');
		expect(partState({ static: { Model: 'x' } }, undefined, true)).toBe('assumed');
		expect(partState(bound, undefined, true)).toBe('missing');
		expect(partState(bound, { ...drive, Present: false }, true)).toBe('absent');
		expect(partState(bound, drive, false)).toBe('stale');
		expect(partState(bound, { ...drive, Fault: true }, true)).toBe('critical');
		expect(partState(bound, { ...drive, Health: 2 }, true)).toBe('critical');
		expect(partState(bound, { ...drive, PredictedFailure: true }, true)).toBe('warning');
		expect(partState(bound, { ...drive, Health: 3 }, true)).toBe('warning');
		expect(partState(bound, drive, true)).toBe('ok');
	});
	it('a pulled drive is not fitted, and says so', () => {
		expect(isFitted('absent')).toBe(false);
		expect(partStatus('drive', { ...drive, Present: false }, 'absent')).toBe('ABSENT');
	});
	it('labels a drive by capacity and temperature', () => {
		expect(partStatus('drive', drive, 'ok')).toBe('1.92 TB · 32 °C');
		expect(partStatus('drive', drive, 'warning')).toBe('1.92 TB · 32 °C · WARNING');
		expect(capacity(240)).toBe('240 GB');
		expect(capacity(65.536)).toBe('66 GB');
	});
	it('lists a drive faceplate identity first', () => {
		const rows = partFacts('drive', drive);
		expect(rows.slice(0, 3).map((r) => r.label)).toEqual(['Model', 'Capacity', 'Protocol']);
		expect(rows.find((r) => r.label === 'Temperature')?.value).toBe('32 °C');
		expect(rows.find((r) => r.label === 'Health')?.value).toBe('OK');
	});
	it('substitutes {node}', () => {
		expect(tagFor('{node}_Fan1', 'NODE2')).toBe('NODE2_Fan1');
	});
});
