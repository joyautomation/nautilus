// Unit tests for cabling (src/lib/hardware/topology.ts): the office
// cluster's declared links resolve to exact profile ports and tags, and
// the live check says confirmed / consistent / contradicted / down /
// unverified for the right reasons; the rack puts ports and cables where
// they are.
import { describe, expect, it } from './harness.js';
import sys112b from '../profiles/supermicro-sys-112b-wr.json';
import s3900 from '../profiles/fs-s3900-24t4s-r.json';
import hq from '../../examples/node-3d/hmi/src/lib/hq.topology.json';
import { validateProfile, resolveParts, type ChassisProfile } from '../src/lib/hardware/profile.js';
import { checkLink, neighbourhood, focusFade, linkAt, linkTags, portPart, resolveEnd, readEnd, linkFacts, type Plant, type Topology } from '../src/lib/hardware/topology.js';
import { cablesOverlay, interfacesOverlay, identifyOverlay, identify, type OverlayColors } from '../src/lib/hardware/overlay.js';
import { meshLayout } from '../src/lib/hardware/mesh.js';
import { placeDevice, endInRack, cablePath, RACK_U_MM, type RackLayout } from '../src/lib/hardware/rack.js';

const server = sys112b as unknown as ChassisProfile;
const sw = s3900 as unknown as ChassisProfile;
const topology = hq as unknown as Topology;
const plant: Plant = { topology, profileOf: (d) => (d.profile === server.profile ? server : d.kind === 'switch' ? sw : undefined) };
const link = (a: string) => topology.links.find((l) => l.a === a || l.b === a)!;
const up = (g: number) => ({ LinkUp: true, SpeedGbps: g });
const swUp = (mbps: number) => ({ OperUp: true, SpeedMbps: mbps });
const colors: OverlayColors = { ramp: ['r0', 'r1', 'r2', 'r3', 'r4'], good: 'good', warning: 'warning', critical: 'critical', neutral: 'neutral', accent: 'accent' };

describe('the S3900-24T4S-R profile', () => {
	it('validates: 24 RJ45 + 4 SFP+, each bound to its SwitchPort in ifIndex order', () => {
		expect(validateProfile(sw)).toEqual([]);
		expect(sw.parts.filter((p) => p.jack === 'rj45').length).toBe(24);
		expect(sw.parts.filter((p) => p.jack === 'sfp+').length).toBe(4);
		expect(sw.bindings.g24).toBe('{node}_Port24');
		expect(sw.bindings.te25).toBe('{node}_Port25');
	});
	it('finds a port by its cable-plan name', () => {
		expect(portPart(sw, 'te0/27')?.id).toBe('te27');
		expect(portPart(server, 'nicSlot2p2')?.id).toBe('nicSlot2p2');
		expect(portPart(server, 'slot3 p1')?.id).toBe('nicSlot3p1');
	});
});

describe('declared links', () => {
	it('resolve both ends to a tag and a label', () => {
		const e = resolveEnd(plant, 'sw1/te0/25');
		expect(e.tag).toBe('SW1_Port25');
		expect(e.label).toBe('sw1 te0/25');
		const n = resolveEnd(plant, 'node2/nicSlot3p1');
		expect(n.tag).toBe('NODE2_Nic_Slot3_P1');
		expect(n.label).toBe('node2 slot3 p1');
		expect(resolveEnd(plant, 'site').label).toBe('site');
	});
	it('every link end in the office topology is a real port', () => {
		for (const l of topology.links) for (const s of [l.a, l.b]) if (s.includes('/')) expect(resolveEnd(plant, s).tag !== undefined).toBe(true);
		expect(linkTags(plant).length).toBe(32);
	});
	it('find the link on a port from either side', () => {
		expect(linkAt(topology, 'node1', portPart(server, 'nicSlot2p2')!)?.near).toBe('a');
		expect(linkAt(topology, 'sw1', portPart(sw, 'te25')!)?.near).toBe('b');
		expect(linkAt(topology, 'node1', portPart(server, 'lan1')!)).toBe(undefined);
	});
});

describe('the live check', () => {
	const l = link('node1/nicSlot2p2');
	it('consistent: both ends up at the declared speed', () => {
		const c = checkLink(plant, l, { NODE1_Nic_Slot2_P2: up(10), SW1_Port25: swUp(10000) });
		expect(c.verdict).toBe('consistent');
		expect(c.reasons).toEqual(['both ends up at 10G']);
	});
	it('contradicted: one end up, the other down; speeds differ; not the declared rate', () => {
		expect(checkLink(plant, l, { NODE1_Nic_Slot2_P2: up(10), SW1_Port25: { OperUp: false, SpeedMbps: 10000 } }).verdict).toBe('contradicted');
		const c = checkLink(plant, l, { NODE1_Nic_Slot2_P2: up(10), SW1_Port25: swUp(1000) });
		expect(c.verdict).toBe('contradicted');
		expect(c.reasons).toEqual(['node1 slot2 p2 at 10G, sw1 te0/25 at 1G']);
		expect(checkLink(plant, l, { NODE1_Nic_Slot2_P2: up(1), SW1_Port25: swUp(1000) }).reasons).toEqual(['both up at 1G, declared 10G']);
	});
	it('down: both ends reported, neither linked', () => {
		expect(checkLink(plant, l, { NODE1_Nic_Slot2_P2: { LinkUp: false }, SW1_Port25: { OperUp: false } }).verdict).toBe('down');
	});
	it('unverified: an end is not reported (and a site uplink never is)', () => {
		const c = checkLink(plant, l, { SW1_Port25: swUp(10000) });
		expect(c.verdict).toBe('unverified');
		expect(c.reasons).toEqual(['not reported: node1 slot2 p2', 'sw1 te0/25 up at 10G']);
		expect(checkLink(plant, link('sw1/g0/1'), { SW1_Port01: swUp(1000) }).verdict).toBe('unverified');
	});
	it('down: the only end that reports has no link', () => {
		const c = checkLink(plant, link('sw1/g0/1'), { SW1_Port01: { OperUp: false, SpeedMbps: 1000 } });
		expect(c.verdict).toBe('down');
		expect(c.reasons).toEqual(['not reported: site (outside the model)', 'sw1 g0/1 down']);
	});
	it('confirmed: LLDP names the declared neighbour; contradicted when it names another', () => {
		const ring = link('sw1/te0/27');
		const ok = checkLink(plant, ring, { SW1_Port27: { ...swUp(10000), LldpSystem: 'hq-sw2', LldpPort: 'TGigaEthernet0/28' }, SW2_Port28: swUp(10000) });
		expect(ok.verdict).toBe('confirmed');
		const bad = checkLink(plant, ring, { SW1_Port27: { ...swUp(10000), LldpSystem: 'hq-sw3' }, SW2_Port28: swUp(10000) });
		expect(bad.verdict).toBe('contradicted');
		expect(bad.reasons).toEqual(['sw1 te0/27 sees hq-sw3 over LLDP, not hq-sw2']);
	});
	it('confirmed: the server port’s MAC is learned on the switch port', () => {
		const c = checkLink(plant, l, { NODE1_Nic_Slot2_P2: { ...up(10), MAC: '90:5a:08:00:00:01' }, SW1_Port25: { ...swUp(10000), Macs: ['90-5A-08-00-00-01'] } });
		expect(c.verdict).toBe('confirmed');
		// A redacted MAC proves nothing.
		expect(checkLink(plant, l, { NODE1_Nic_Slot2_P2: { ...up(10), MAC: '(redacted)' }, SW1_Port25: { ...swUp(10000), Macs: [] } }).verdict).toBe('consistent');
	});
	it('reads either port UDT', () => {
		expect(readEnd(swUp(25000))).toEqual({ reported: true, up: true, gbps: 25, mac: undefined, lldp: undefined, macs: undefined });
		expect(readEnd(undefined).reported).toBe(false);
	});
	it('the faceplate rows say where, what and why', () => {
		const c = checkLink(plant, l, { NODE1_Nic_Slot2_P2: up(10), SW1_Port25: swUp(10000) });
		expect(linkFacts(c, 'a', l).slice(0, 3)).toEqual([
			{ label: 'Cable to', value: 'sw1 te0/25' },
			{ label: 'Link', value: 'uplink · 10G declared' },
			{ label: 'Check', value: '= Consistent' }
		]);
	});
});

describe('the cables overlay', () => {
	const ov = cablesOverlay(plant);
	const parts = resolveParts(server, 'NODE1');
	const byId = (id: string) => parts.find((p) => p.partId === id)!;
	const ctx = (tags: Record<string, unknown>) => ({ node: 'NODE1', profile: server, tags, colors });
	const tags = { NODE1_Nic_Slot2_P1: up(25), NODE2_Nic_Slot3_P1: up(25), NODE1_Nic_Slot2_P2: up(10), SW1_Port25: swUp(1000), NODE1_Nic_LAN1: up(1) };
	it('labels each port with its far end, coloured by the check', () => {
		expect(ov.paint(byId('nicSlot2p1'), tags.NODE1_Nic_Slot2_P1, ctx(tags))).toEqual({ color: 'r2', text: '= node2 slot3 p1' });
		expect(ov.paint(byId('nicSlot2p2'), tags.NODE1_Nic_Slot2_P2, ctx(tags))).toEqual({ color: 'critical', text: '✗ sw1 te0/25' });
		expect(ov.paint(byId('nicSlot3p2'), undefined, ctx(tags))).toEqual({ color: 'neutral', text: '? sw2 te0/26' });
		expect(ov.paint(byId('bmc'), undefined, ctx(tags))).toEqual({ color: 'neutral', text: '? sw1 g0/24' });
	});
	it('calls out a linked port the plan does not mention, and dims the rest', () => {
		expect(ov.paint(byId('lan1'), tags.NODE1_Nic_LAN1, ctx(tags))).toEqual({ color: 'warning', text: '! not in plan' });
		expect(ov.paint(byId('lan2'), undefined, ctx(tags)).dim).toBe(true);
		expect(ov.paint(byId('bay0'), {}, ctx(tags)).dim).toBe(true);
	});
	it('works on a switch: its ports name the servers', () => {
		const swParts = resolveParts(sw, 'SW1');
		const te25 = swParts.find((p) => p.partId === 'te25')!;
		const t = { NODE1_Nic_Slot2_P2: up(10), SW1_Port25: swUp(10000) };
		expect(ov.paint(te25, t.SW1_Port25, { node: 'SW1', profile: sw, tags: t, colors })).toEqual({ color: 'r2', text: '= node1 slot2 p2' });
	});
	it('the interfaces overlay reads a switch port too', () => {
		const swParts = resolveParts(sw, 'SW1');
		const te25 = swParts.find((p) => p.partId === 'te25')!;
		expect(interfacesOverlay.paint(te25, swUp(10000), { node: 'SW1', profile: sw, tags: {}, colors }).text).toBe('10G');
	});
});

describe('the rack', () => {
	const layout: RackLayout = { units: 24, depth: 700, base: 60, devices: [{ id: 'sw1', u: 22, face: 'rear' }, { id: 'node1', u: 14, face: 'front' }] };
	const profileOf = (id: string) => (id.startsWith('sw') ? sw : server);
	it('places a device by its unit and face', () => {
		expect(placeDevice(layout, layout.devices[1])).toEqual({ pos: [0, (60 + 13 * RACK_U_MM) / 1000, 0], rotY: 0 });
		expect(placeDevice(layout, layout.devices[0]).rotY).toBe(180);
	});
	it('a server port faces the rack rear; a rear-mounted switch port faces it too', () => {
		const n = endInRack(layout, profileOf, 'node1', 'nicSlot2p2')!;
		expect(n.out).toEqual([0, 0, -1]);
		expect(Math.abs(n.at[2] - -0.595) < 1e-9).toBe(true);
		const s = endInRack(layout, profileOf, 'sw1', 'te0/25')!;
		expect(s.out).toEqual([0, 0, -1]);
		// Turned round: the port face is on the rear posts, x mirrored.
		expect(Math.abs(s.at[2] - -0.7) < 1e-9).toBe(true);
		expect(Math.abs(s.at[0] - -0.12) < 1e-9).toBe(true);
	});
	it('a cable leaves each port straight, then runs down the nearer side', () => {
		const a = endInRack(layout, profileOf, 'node1', 'nicSlot2p2')!;
		const b = endInRack(layout, profileOf, 'sw1', 'te0/25')!;
		const p = cablePath(layout, a, b);
		expect(p.length).toBe(6);
		expect(p[2][0]).toBe(-0.226);
		expect(p[3][0]).toBe(-0.226);
		expect(cablePath(layout, a, undefined).length).toBe(4);
	});
});

describe('the mesh', () => {
	const { nodes, edges } = meshLayout(topology);
	const deg = (id: string) => {
		const p = nodes.find((n) => n.id === id)!.pos;
		return Math.round(((Math.atan2(p[0], p[2]) * 180) / Math.PI + 360) % 360);
	};
	it('puts the switches on a ring and each server under the two switches it is cabled to', () => {
		expect([deg('sw1'), deg('sw2'), deg('sw3')]).toEqual([0, 120, 240]);
		expect([deg('node1'), deg('node2'), deg('node3')]).toEqual([60, 180, 300]);
	});
	it('puts far ends outside the model above the switch they hang off, apart', () => {
		const site = nodes.find((n) => n.id === 'site')!;
		expect(site.kind).toBe('outside');
		expect(deg('site')).toBe(0);
		expect(deg('mira1')).toBe(34);
	});
	it('draws every link, parallel links between one pair bowed apart', () => {
		expect(edges.length).toBe(17);
		const pair = edges.filter((e) => e.a === 'node1' && e.b === 'sw1');
		expect(pair.length).toBe(2);
		expect(pair[0].points[1][0] !== pair[1].points[1][0]).toBe(true);
	});
});

describe('focus', () => {
	it('a device’s neighbourhood: what its cables reach, and those links', () => {
		const n = neighbourhood(topology, 'node2');
		expect([...n.neighbours].sort()).toEqual(['node1', 'node3', 'sw2', 'sw3']);
		expect(n.links.size).toBe(5);
		expect([...neighbourhood(topology, 'sw1').neighbours].sort()).toEqual(['mira1', 'node1', 'node3', 'site', 'sw2', 'sw3']);
	});
	it('fades in three steps: the focus, its neighbours, the rest', () => {
		expect(focusFade(topology, undefined, 'sw1')).toBe(1);
		expect(focusFade(topology, 'node2', 'node2')).toBe(1);
		expect(focusFade(topology, 'node2', 'sw2')).toBe(0.4);
		expect(focusFade(topology, 'node2', 'sw1')).toBe(0.1);
	});
});

describe('the identify overlay', () => {
	const parts = resolveParts(server, 'NODE1');
	const byId = (id: string) => parts.find((p) => p.partId === id)!;
	const ctx = { node: 'NODE1', profile: server, tags: {}, colors };
	it('says what each part is and where it sits', () => {
		expect(identify(byId('bay0'), { CapacityGB: 1920, Protocol: 'NVMe' })).toBe('bay 0 · 1.92 TB NVMe');
		expect(identify(byId('boot0'), { CapacityGB: 240, Protocol: 'SATA' })).toBe('M.2 · 240 GB SATA');
		expect(identify(byId('dimmA1'), { CapacityGB: 64 })).toBe('A1 · 64 GB');
		expect(identify(byId('cpu1'), { Model: 'Xeon 6505P', Cores: 12 })).toBe('Xeon 6505P · 12c');
		expect(identify(byId('psu1'), { CapacityW: 860 })).toBe('PSU1 · 860 W');
		expect(identify(byId('nicSlot2p2'), {})).toBe('slot2 p2');
		expect(identify(byId('bmc'), {})).toBe('BMC');
	});
	it('a fitted part no driver reports is named from the profile; empty positions stay faint', () => {
		expect(identifyOverlay.paint(byId('slot1'), undefined, ctx)).toEqual({ color: '#2fa38f', text: 'AOC-SLG4-2H8M2' });
		expect(identifyOverlay.paint(byId('bay5'), undefined, ctx).dim).toBe(true);
	});
});
