// Unit tests for VLANs (src/lib/hardware/vlan.ts): a switch's Q-BRIDGE
// table read into each port's native and tagged VLANs; each declared link
// checked against what its switch ends carry, for the right reasons; each
// VLAN as a domain, split into islands when a trunk is pruned; and the
// overlay painting ports from it. The frame is the office cluster as the
// SNMP driver delivers it for the site as planned.
import { describe, expect, it } from './harness.js';
import sys112b from '../profiles/supermicro-sys-112b-wr.json';
import s3900 from '../profiles/fs-s3900-24t4s-r.json';
import hq from '../../examples/node-3d/hmi/src/lib/hq.topology.json';
import { resolveParts, type ChassisProfile } from '../src/lib/hardware/profile.js';
import { checkAll, type LinkCheck, type Plant, type Topology } from '../src/lib/hardware/topology.js';
import { vlanOverlay, type OverlayColors } from '../src/lib/hardware/overlay.js';
import {
	parseList,
	portPosition,
	readPortVlans,
	vlanText,
	checkVlans,
	checkAllVlans,
	vlanFacts,
	siteVlans,
	vlanColors,
	vlanDomains,
	vlanFloors,
	islandsOf,
	DEFAULT_VLAN_COLOR
} from '../src/lib/hardware/vlan.js';

const server = sys112b as unknown as ChassisProfile;
const sw = s3900 as unknown as ChassisProfile;
const topology = hq as unknown as Topology;
const plant: Plant = { topology, profileOf: (d) => (d.profile === server.profile ? server : d.kind === 'switch' ? sw : undefined) };
const link = (a: string, b: string) => topology.links.find((l) => (l.a === a && l.b === b) || (l.a === b && l.b === a))!;
const colors: OverlayColors = { ramp: ['r0', 'r1', 'r2', 'r3', 'r4'], good: 'good', warning: 'warning', critical: 'critical', neutral: 'neutral', accent: 'accent' };

const range = (a: number, b: number) => Array.from({ length: b - a + 1 }, (_, i) => a + i);
const csv = (xs: number[]) => xs.join(',');
/** One switch's tags as planned: VLAN 1 untagged on every port but the BMC
 * port, the node uplinks and ring trunked, the BMC port access 22; SW1's
 * g0/1 the site trunk. */
function switchTags(tag: string): Record<string, unknown> {
	const site = tag === 'SW1' ? [1] : [];
	const trunk = [...site, 25, 26, 27, 28];
	const out: Record<string, unknown> = {
		[tag]: { Online: true },
		[`${tag}_Vlan1`]: { Id: 1, Name: 'Default', Ports: csv([...range(1, 23), ...range(25, 28)]), Untagged: csv([...range(1, 23), ...range(25, 28)]) },
		[`${tag}_Vlan20`]: { Id: 20, Name: 'host-mgmt', Ports: csv(trunk), Untagged: '' },
		[`${tag}_Vlan21`]: { Id: 21, Name: 'vm-data', Ports: csv(trunk), Untagged: '' },
		[`${tag}_Vlan22`]: { Id: 22, Name: 'oob-mgmt', Ports: csv([...site, 24, 25, 26, 27, 28]), Untagged: '24' },
		[`${tag}_Vlan251`]: { Id: 251, Name: 'erps-raps', Ports: '27,28', Untagged: '' }
	};
	for (const p of range(1, 28)) out[`${tag}_Port${String(p).padStart(2, '0')}`] = { OperUp: true, Pvid: p === 24 ? 22 : 1 };
	return out;
}
const planned = () => ({ ...switchTags('SW1'), ...switchTags('SW2'), ...switchTags('SW3') });
/** Drop `port` from a VLAN's member lists. */
function prune(tags: Record<string, unknown>, vlanTag: string, port: number) {
	const v = tags[vlanTag] as { Ports: string; Untagged: string };
	const drop = (s: string) => csv(parseList(s).filter((p) => p !== port));
	tags[vlanTag] = { ...v, Ports: drop(v.Ports), Untagged: drop(v.Untagged) };
}
const cablesUp = (): { check: LinkCheck }[] => topology.links.map(() => ({ check: { verdict: 'consistent' } as LinkCheck }));

describe('reading a switch’s VLANs', () => {
	it('parses member lists and port positions', () => {
		expect(parseList('24, 25,26')).toEqual([24, 25, 26]);
		expect(parseList('')).toEqual([]);
		expect(parseList(undefined)).toEqual([]);
		expect(portPosition('SW1_Port07')).toBe(7);
		expect(portPosition('NODE1_Nic_Slot2_P1')).toBe(undefined);
	});
	it('gives a trunk its native and tagged VLANs, an access port its one', () => {
		const tags = planned();
		expect(readPortVlans(tags, 'SW1', 'SW1_Port25')).toEqual({ reported: true, native: 1, untagged: [1], tagged: [20, 21, 22] });
		expect(readPortVlans(tags, 'SW2', 'SW2_Port27')).toEqual({ reported: true, native: 1, untagged: [1], tagged: [20, 21, 22, 251] });
		expect(readPortVlans(tags, 'SW3', 'SW3_Port24')).toEqual({ reported: true, native: 22, untagged: [22], tagged: [] });
		expect(vlanText(readPortVlans(tags, 'SW1', 'SW1_Port25'))).toBe('1 · T 20,21,22');
		expect(vlanText(readPortVlans(tags, 'SW1', 'SW1_Port24'))).toBe('22');
	});
	it('reports nothing for a switch that is offline or has no VLAN table', () => {
		const tags = { ...planned(), SW2__Online: false };
		expect(readPortVlans(tags, 'SW2', 'SW2_Port25').reported).toBe(false);
		expect(readPortVlans({ SW1_Port25: { Pvid: 1 } }, 'SW1', 'SW1_Port25').reported).toBe(false);
	});
	it('names the site’s VLANs: declared first, then any a switch adds', () => {
		const tags = planned();
		expect(siteVlans(topology, tags).map((v) => `${v.id} ${v.name}${v.declared ? '' : ' +'}`)).toEqual(['20 host-mgmt', '21 vm-data', '22 oob-mgmt', '251 erps-raps', '1 Default +']);
		const c = vlanColors([20, 21, 22, 251, 1]);
		expect(c.get(1)).toBe(DEFAULT_VLAN_COLOR);
		expect(new Set([20, 21, 22, 251].map((v) => c.get(v))).size).toBe(4);
	});
});

describe('the VLAN check', () => {
	it('finds the site as planned consistent, and the mesh carrying none', () => {
		const tags = planned();
		const checks = checkAllVlans(plant, tags);
		const by = (v: string) => checks.filter((c) => c.verdict === v).length;
		expect(by('contradicted')).toBe(0);
		expect(by('none')).toBe(3);
		expect(by('consistent')).toBe(13);
		const ring = checkVlans(plant, link('sw1/te0/27', 'sw2/te0/28'), tags);
		expect(ring.carries).toEqual([20, 21, 22, 251]);
		const up = checkVlans(plant, link('node1/nicSlot2p2', 'sw1/te0/25'), tags);
		expect(up.reasons.some((r) => r.includes('node1 slot2 p2 tags in its OS'))).toBe(true);
		expect(checkVlans(plant, link('node1/bmc', 'sw1/g0/24'), tags).carries).toEqual([22]);
	});
	it('names a VLAN pruned from a trunk', () => {
		const tags = planned();
		prune(tags, 'SW2_Vlan21', 28);
		const c = checkVlans(plant, link('sw1/te0/27', 'sw2/te0/28'), tags);
		expect(c.verdict).toBe('contradicted');
		expect(c.reasons[0]).toBe('sw2 te0/28 lacks 21');
		expect(c.carries).toEqual([20, 22, 251]);
	});
	it('names a native VLAN mismatch between two switches', () => {
		const tags = planned();
		tags.SW1_Port27 = { OperUp: true, Pvid: 20 };
		const c = checkVlans(plant, link('sw1/te0/27', 'sw2/te0/28'), tags);
		expect(c.verdict).toBe('contradicted');
		expect(c.reasons).toContain('native VLAN mismatch: sw1 te0/27 20, sw2 te0/28 1');
	});
	it('names an access port in the wrong VLAN, and carries the wrong one', () => {
		const tags = planned();
		prune(tags, 'SW3_Vlan22', 24);
		tags.SW3_Vlan20 = { ...(tags.SW3_Vlan20 as object), Ports: '24,25,26,27,28', Untagged: '24' };
		tags.SW3_Port24 = { OperUp: true, Pvid: 20 };
		const c = checkVlans(plant, link('node3/bmc', 'sw3/g0/24'), tags);
		expect(c.verdict).toBe('contradicted');
		expect(c.reasons[0]).toBe('sw3 g0/24 native 20, declared 22');
		expect(c.carries).toEqual([20]);
	});
	it('names a VLAN carried that the plan does not', () => {
		const tags = planned();
		tags.SW1_Vlan251 = { Id: 251, Name: 'erps-raps', Ports: '25,27,28', Untagged: '' };
		const c = checkVlans(plant, link('node1/nicSlot2p2', 'sw1/te0/25'), tags);
		expect(c.reasons[0]).toBe('sw1 te0/25 also carries 251');
	});
	it('cannot say with the switch offline or silent: the plan is what crosses', () => {
		const tags = { ...planned(), SW1__Online: false };
		const c = checkVlans(plant, link('node1/nicSlot2p2', 'sw1/te0/25'), tags);
		expect(c.verdict).toBe('unverified');
		expect(c.observed).toBe(false);
		expect(c.carries).toEqual([20, 21, 22]);
		expect(c.reasons.some((r) => r.includes('SW1 offline'))).toBe(true);
	});
	it('gives a faceplate the switch’s VLANs, the plan and the verdict', () => {
		const tags = planned();
		prune(tags, 'SW2_Vlan21', 28);
		const rows = vlanFacts(checkVlans(plant, link('sw1/te0/27', 'sw2/te0/28'), tags), 'b');
		expect(rows[0]).toEqual({ label: 'VLANs', value: 'native 1 · tagged 20,22,251' });
		expect(rows[1].value).toBe('T 20,21,22,251');
		expect(rows[2].value).toBe('✗ contradicted');
	});
});

describe('VLAN domains', () => {
	it('puts every device on the host and VM VLANs in one island, as planned', () => {
		const tags = planned();
		const ds = vlanDomains(plant, checkAllVlans(plant, tags), tags, cablesUp());
		const v21 = ds.find((d) => d.id === 21)!;
		expect(v21.islands.length).toBe(1);
		expect(v21.devices).toEqual(['node1', 'node2', 'node3', 'sw1', 'sw2', 'sw3', 'site']);
		expect(v21.links.find((l) => topology.links[l.index].rpl)?.blocked).toBe(true);
		const v251 = ds.find((d) => d.id === 251)!;
		expect(v251.devices).toEqual(['sw1', 'sw2', 'sw3']);
		const v22 = ds.find((d) => d.id === 22)!;
		expect(v22.links.filter((l) => topology.links[l.index].kind === 'bmc').length).toBe(3);
		// VLAN 1 is the default: native on the trunks, never declared, never carried.
		expect(ds.find((d) => d.id === 1)!.devices).toEqual([]);
	});
	it('splits a VLAN pruned from a ring link while the protection link is blocked', () => {
		const tags = planned();
		prune(tags, 'SW2_Vlan21', 28); // sw1 te0/27 ↔ sw2 te0/28
		const ds = vlanDomains(plant, checkAllVlans(plant, tags), tags, cablesUp());
		const v21 = ds.find((d) => d.id === 21)!;
		expect(v21.islands).toEqual([
			['node1', 'node2', 'node3', 'sw2', 'sw3'],
			['node1', 'node3', 'sw1', 'site']
		]);
		// node1 and node3 team across the split: in both.
		expect(islandsOf(v21, 'node1')).toEqual([0, 1]);
		expect(islandsOf(v21, 'node2')).toEqual([0]);
		const missing = v21.links.filter((l) => l.declared && !l.carried);
		expect(missing.length).toBe(1);
		// The other VLANs are whole.
		expect(ds.find((d) => d.id === 20)!.islands.length).toBe(1);
	});
	it('heals round the ring when a ring link is down: the protection link opens', () => {
		const tags = planned();
		prune(tags, 'SW2_Vlan21', 28);
		const cables = cablesUp();
		const i = topology.links.indexOf(link('sw2/te0/27', 'sw3/te0/28'));
		cables[i] = { check: { verdict: 'down' } as LinkCheck };
		const v21 = vlanDomains(plant, checkAllVlans(plant, tags), tags, cables).find((d) => d.id === 21)!;
		// sw1 reaches sw3 over the protection link; sw2 is cut off from both.
		expect(v21.links.find((l) => topology.links[l.index].rpl)?.blocked).toBe(false);
		expect(v21.islands.length).toBe(2);
		expect(v21.islands.some((g) => g.includes('sw1') && g.includes('sw3'))).toBe(true);
	});
	it('lays the VLANs out as floors, each device where the mesh puts it', () => {
		const tags = planned();
		const ds = vlanDomains(plant, checkAllVlans(plant, tags), tags, cablesUp());
		const fl = vlanFloors(topology, ds);
		expect(fl.length).toBe(5);
		expect(fl[0].y > fl[1].y).toBe(true);
		const at = (f: number, id: string) => fl[f].nodes.find((n) => n.id === id)!.pos;
		expect([at(0, 'sw1')[0], at(0, 'sw1')[2]]).toEqual([at(1, 'sw1')[0], at(1, 'sw1')[2]]);
		expect(at(0, 'sw1')[1]).toBe(fl[0].y);
		expect(fl[3].nodes.map((n) => n.id)).toEqual(['sw1', 'sw2', 'sw3']);
	});
});

describe('the VLAN overlay', () => {
	const ctx = (node: string, profile: ChassisProfile, tags: Record<string, unknown>) => ({ node, profile, tags, colors });
	const part = (node: string, profile: ChassisProfile, id: string) => resolveParts(profile, node).find((p) => p.partId === id)!;
	it('labels a switch port native · tagged, in its native VLAN’s colour', () => {
		const tags = planned();
		const o = vlanOverlay(plant);
		const vc = vlanColors(siteVlans(topology, tags).map((v) => v.id));
		const te25 = part('SW1', sw, 'te25');
		expect(o.paint(te25, tags[te25.tag!], ctx('SW1', sw, tags))).toEqual({ color: vc.get(1), text: '1 · T 20,21,22' });
		const g24 = part('SW1', sw, 'g24');
		expect(o.paint(g24, tags[g24.tag!], ctx('SW1', sw, tags))).toEqual({ color: vc.get(22), text: '22' });
		// An unconfigured copper port: nothing to say.
		const g5 = part('SW1', sw, 'g5');
		expect(o.paint(g5, tags[g5.tag!], ctx('SW1', sw, tags)).dim).toBe(true);
	});
	it('paints a port red where its link disagrees with the plan', () => {
		const tags = planned();
		prune(tags, 'SW2_Vlan21', 28);
		const te28 = part('SW2', sw, 'te28');
		expect(vlanOverlay(plant).paint(te28, tags[te28.tag!], ctx('SW2', sw, tags))).toEqual({ color: 'critical', text: '✗ 1 · T 20,22,251' });
	});
	it('gives a server port the plan, and a picked VLAN only the ports carrying it', () => {
		const tags = planned();
		const p = part('NODE1', server, 'nicSlot2p2');
		expect(vlanOverlay(plant).paint(p, undefined, ctx('NODE1', server, tags)).text).toBe('T 20,21,22 (plan)');
		const o = vlanOverlay(plant, 251);
		const te27 = part('SW1', sw, 'te27');
		const te25 = part('SW1', sw, 'te25');
		expect(o.paint(te27, tags[te27.tag!], ctx('SW1', sw, tags)).dim).toBe(undefined);
		expect(o.paint(te25, tags[te25.tag!], ctx('SW1', sw, tags)).dim).toBe(true);
	});
	it('agrees with the cable check on which links exist', () => {
		const tags = planned();
		expect(checkAll(plant, tags).length).toBe(checkAllVlans(plant, tags).length);
	});
});
