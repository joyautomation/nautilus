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
	anchorPayload,
	partState,
	partStatus,
	partFacts,
	capacity,
	isFitted,
	partForSensor,
	faceHoles,
	type ChassisProfile
} from '../src/lib/hardware/profile.js';
import s3900 from '../profiles/fs-s3900-24t4s-r.json';
import { heatPaint, sensorLimits, limitsFor, heatOverlay, interfacesOverlay, freeOverlay, nextDimms, placeLabels, HEAT_RAMP } from '../src/lib/hardware/overlay.js';

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
		for (const t of ['NODE1', 'NODE1_Drive_NVMe0', 'NODE1_Drive_NVMe3', 'NODE1_Drive_SATA0', 'NODE1_Drive_SATA4', 'NODE1_Pcie_Slot2', 'NODE1_Pcie_Slot3', 'NODE1_Dimm_A1', 'NODE1_Dimm_G1', 'NODE1_Cpu1', 'NODE1_Fan6', 'NODE1_PSU2'])
			expect(tags).toContain(t);
	});
	it('carries its AR anchors: the bench label and the racked pair 154 mm apart', () => {
		expect(profile.anchors?.lid?.size).toBe(100);
		const l = profile.anchors!['blanks-left'].pos, r = profile.anchors!['blanks-right'].pos;
		expect(r[0] - l[0]).toBe(154);
		expect(anchorPayload(profile.anchors!['blanks-left'], 'NODE1')).toBe('NAUT:NODE1/L');
	});
	it('wants an anchor\'s up perpendicular to its normal', () => {
		const p = clone();
		p.anchors!.lid.up = [0, 1, 0];
		expect(validateProfile(p).map((e) => e.path)).toEqual(['/anchors/lid/up']);
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
		expect(bay0.size).toEqual([0.074, 0.0185, 0.14]);
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
	it('wants bindings, and limits it can paint by', () => {
		const ok = { profile: 'x', units: 'mm', size: [100, 40, 100], parts: [], bindings: {} };
		expect(validateProfile(ok)).toEqual([]);
		const noBind = { ...ok, bindings: undefined };
		expect(validateProfile(noBind).map((x) => x.path)).toEqual(['/bindings']);
		const bad = (limits: unknown) => validateProfile({ ...ok, limits }).map((x) => x.path);
		expect(bad({ drive: [55, 70], 'drive:nvme': [70, 80] })).toEqual([]);
		expect(bad({ drive: [90] })).toEqual(['/limits/drive']);
		expect(bad({ drive: ['hot', 90] })).toEqual(['/limits/drive']);
		expect(bad({ drive: [90, 70] })).toEqual(['/limits/drive']);
		expect(bad({ toaster: [1, 2] })).toEqual(['/limits/toaster']);
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

describe('overlays', () => {
	const colors = { ramp: HEAT_RAMP, good: 'good', warning: 'warn', critical: 'crit', neutral: 'neutral', accent: 'accent' };
	const parts = resolveParts(profile, 'NODE1');
	const byId = (id: string) => parts.find((p) => p.partId === id)!;
	const ctx = (tags: Record<string, unknown>) => ({ node: 'NODE1', profile, tags: { NODE1: { InletTempC: 23 }, ...tags }, colors });

	it('heat: headroom from the inlet to the warning limit, then status past it', () => {
		expect(heatPaint(23, 70, 75, 23, colors)).toEqual({ color: HEAT_RAMP[0], text: '23°' });
		expect(heatPaint(69, 70, 75, 23, colors).color).toBe(HEAT_RAMP[4]);
		expect(heatPaint(71, 70, 75, 23, colors)).toEqual({ color: 'warn', text: '71° HIGH' });
		expect(heatPaint(80, 70, 75, 23, colors)).toEqual({ color: 'crit', text: '80° CRIT' });
	});
	it('heat: limits by kind:bus, and a part with no temperature goes faint', () => {
		expect(limitsFor(profile, byId('bay0'))).toEqual([70, 75]);
		expect(limitsFor(profile, byId('boot0'))).toEqual([60, 70]);
		expect(heatOverlay.paint(byId('fan1'), { RPM: 9000 }, ctx({})).dim).toBe(true);
		expect(heatOverlay.paint(byId('bay0'), { TempC: 32 }, ctx({})).text).toBe('32°');
		// an unbound member arrives as 0: not a reading
		expect(heatOverlay.paint(byId('bay0'), { TempC: 0 }, ctx({})).dim).toBe(true);
	});
	it('interfaces: rated speed green, slower amber, no link grey', () => {
		const p1 = byId('nicSlot2p1');
		expect(interfacesOverlay.paint(p1, { LinkUp: true, SpeedGbps: 25 }, ctx({}))).toEqual({ color: 'good', text: '25G' });
		expect(interfacesOverlay.paint(p1, { LinkUp: true, SpeedGbps: 10 }, ctx({}))).toEqual({ color: 'warn', text: '10/25G' });
		expect(interfacesOverlay.paint(byId('lan1'), { LinkUp: false, SpeedGbps: 0 }, ctx({}))).toEqual({ color: 'neutral', text: 'LAN1 no link' });
		expect(interfacesOverlay.paint(byId('bmc'), { LinkUp: true, SpeedGbps: 1 }, ctx({}))).toEqual({ color: 'good', text: 'BMC 1G' });
		expect(interfacesOverlay.paint(byId('psu1'), { InputOk: false }, ctx({})).color).toBe('crit');
		expect(interfacesOverlay.paint(byId('cpu1'), {}, ctx({})).dim).toBe(true);
	});
	it('labels: the same text nearby is said once, a different one steps down', () => {
		const m = placeLabels([
			{ id: 'a', at: [0, 0.05, 0], text: 'next' },
			{ id: 'b', at: [0.006, 0.05, 0], text: 'next' },
			{ id: 'c', at: [0.016, 0.05, 0], text: '10/25G' },
			{ id: 'd', at: [0.2, 0.05, 0], text: 'next' }
		]);
		expect([...m.keys()]).toEqual(['a', 'c', 'd']);
		expect(m.get('c')![1] < 0.05).toBe(true);
		expect(m.get('d')![1]).toBe(0.05);
	});
	it('free: the DIMMs to fill next follow the population order', () => {
		expect([...nextDimms(profile, new Set(['A1']))].sort()).toEqual(['C1', 'E1', 'G1']);
		expect([...nextDimms(profile, new Set(['A1', 'C1', 'E1', 'G1']))].sort()).toEqual(['B1', 'D1', 'F1', 'H1']);
		const fitted = { NODE1_Dimm_A1: {}, NODE1_Dimm_C1: {}, NODE1_Dimm_E1: {}, NODE1_Dimm_G1: {} };
		expect(freeOverlay.paint(byId('dimmB1'), undefined, ctx(fitted))).toEqual({ color: 'accent', text: 'B1 next' });
		expect(freeOverlay.paint(byId('bay5'), undefined, ctx(fitted))).toEqual({ color: 'accent', text: 'bay 5 SATA' });
		expect(freeOverlay.paint(byId('dimmA1'), {}, ctx(fitted)).dim).toBe(true);
		// Six empty front bays on node1, each with its own label.
		const free = parts.filter((p) => p.kind === 'drive' && p.props.form !== 'm2').map((p) => freeOverlay.paint(p, p.tag ? {} : undefined, ctx(fitted)).text).filter(Boolean);
		expect(free).toEqual(['bay 4 SATA', 'bay 5 SATA', 'bay 6 SATA', 'bay 7 SATA', 'bay 8 SATA', 'bay 9 SATA']);
	});
});

describe('sensor limits', () => {
	it('an unset warning (0) is the critical limit, never 0 °C', () => {
		expect(sensorLimits({ HighSP: 0, HighHighSP: 50 })).toEqual([50, 50]);
		expect(sensorLimits({ HighSP: 45, HighHighSP: 50 })).toEqual([45, 50]);
		expect(sensorLimits({ HighSP: 0, HighHighSP: 0 })).toBe(undefined);
		expect(sensorLimits({})).toBe(undefined);
	});
});

describe('partForSensor', () => {
	const parts = resolveParts(profile, 'NODE1');
	it('signs a sensor alarm on the part it measures', () => {
		expect(partForSensor(parts, 'NODE1_Temp_CPU')?.kind).toBe('cpu');
		expect(partForSensor(parts, 'NODE1_Temp_AOC_NIC3')?.tag).toBe('NODE1_Pcie_Slot3');
		expect(/^NODE1_Dimm_[E-H]1$/.test(partForSensor(parts, 'NODE1_Temp_DIMME_H')?.tag ?? '')).toBe(true);
		expect(partForSensor(parts, 'NODE1_Temp_NVMe_SSDA')?.kind).toBe('drive');
	});
	it('leaves a sensor that measures no one part to the server', () => {
		expect(partForSensor(parts, 'NODE1_Temp_Inlet')).toBe(undefined);
		expect(partForSensor(parts, 'NODE1_Fan3')).toBe(undefined);
	});
});

describe('faceplates', () => {
	const sw = s3900 as unknown as ChassisProfile;
	const overlap = (h: ReturnType<typeof faceHoles>) =>
		h.some((a, i) => h.some((b, j) => i !== j && Math.abs(a.x - b.x) * 2 < a.w + b.w && Math.abs(a.y - b.y) * 2 < a.h + b.h));
	it('a server is open at the front where its drive bays are', () => {
		const f = faceHoles(profile, 'front');
		expect(f.length).toBe(10);
		expect(f.every((o) => Math.abs(o.w - 0.0734) < 1e-6)).toBe(true);
	});
	it('its rear where the PSUs, ports and card brackets are; a port in its card’s bracket is one opening', () => {
		expect(faceHoles(profile, 'rear').length).toBe(7);
	});
	it('a switch at the front, one opening per jack, none at the rear', () => {
		expect(faceHoles(sw, 'front').length).toBe(28);
		expect(faceHoles(sw, 'rear')).toEqual([]);
	});
	it('no two openings overlap, and each stays inside the face', () => {
		for (const [p, face] of [[profile, 'front'], [profile, 'rear'], [sw, 'front']] as const) {
			const h = faceHoles(p, face);
			expect(overlap(h)).toBe(false);
			const [W, H] = [p.size[0] / 1000, p.size[1] / 1000];
			expect(h.every((o) => o.x - o.w / 2 > -W / 2 && o.x + o.w / 2 < W / 2 && o.y - o.h / 2 > 0 && o.y + o.h / 2 < H)).toBe(true);
		}
	});
	it('a part deep inside reaches neither face', () => {
		const p = clone();
		p.parts = [{ id: 'x', kind: 'fan', slot: 'x', pos: [0, 20, -300], size: [40, 40, 28] }] as ChassisProfile['parts'];
		expect([faceHoles(p, 'front'), faceHoles(p, 'rear')]).toEqual([[], []]);
	});
});
