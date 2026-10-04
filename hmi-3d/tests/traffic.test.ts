// Unit tests for the traffic overlay (src/lib/hardware/overlay.ts): a port's
// load in bands of its line rate, drops as saturation's evidence, a server
// port read off its switch peer turned round.
import { describe, expect, it } from './harness.js';
import sys112b from '../profiles/supermicro-sys-112b-wr.json';
import s3900 from '../profiles/fs-s3900-24t4s-r.json';
import hq from '../../examples/node-3d/hmi/src/lib/hq.topology.json';
import { resolveParts, type ChassisProfile } from '../src/lib/hardware/profile.js';
import type { Plant, Topology } from '../src/lib/hardware/topology.js';
import { trafficOverlay, mute, interfacesOverlay, cablesOverlay, heatOverlay, portTraffic, trafficColor, trafficText, bitsText, peerTraffic, type OverlayColors } from '../src/lib/hardware/overlay.js';

const server = sys112b as unknown as ChassisProfile;
const sw = s3900 as unknown as ChassisProfile;
const topology = hq as unknown as Topology;
const plant: Plant = { topology, profileOf: (d) => (d.profile === server.profile ? server : d.kind === 'switch' ? sw : undefined) };
const colors: OverlayColors = { ramp: ['r0', 'r1', 'r2', 'r3', 'r4'], good: 'good', warning: 'warning', critical: 'critical', neutral: 'neutral', accent: 'accent' };
const port = (inBps: number, outBps: number, mbps: number, drops = 0) => ({ OperUp: true, InBps: inBps, OutBps: outBps, SpeedMbps: mbps, DiscardRate: drops });
const part = (node: string, profile: ChassisProfile, id: string) => resolveParts(profile, node).find((p) => p.partId === id)!;
const ctx = (node: string, profile: ChassisProfile, tags: Record<string, unknown>) => ({ node, profile, tags, colors });

describe('a port’s traffic', () => {
	it('reads the load as the busier direction over the line rate', () => {
		const t = portTraffic(port(3.2e9, 4.1e8, 10000))!;
		expect(Math.abs(t.load - 32) < 1e-9).toBe(true);
		expect(trafficText(t)).toBe('↓3.2G ↑410M · 32%');
		expect(bitsText(26124)).toBe('26k');
		expect(bitsText(999.99e6)).toBe('1G');
		expect(bitsText(999e3)).toBe('1M');
		expect(bitsText(950e3)).toBe('950k');
		expect(bitsText(512)).toBe('512');
		expect(portTraffic(undefined)).toBe(undefined);
	});
	it('colours by band, so idle and working differ even far below full', () => {
		expect(trafficColor(portTraffic(port(26e3, 11e3, 10000))!, colors)).toBe('r0');
		expect(trafficColor(portTraffic(port(2e8, 0, 10000))!, colors)).toBe('r1');
		expect(trafficColor(portTraffic(port(5e9, 0, 10000))!, colors)).toBe('r3');
		expect(trafficColor(portTraffic(port(8e9, 0, 10000))!, colors)).toBe('r4');
		expect(trafficColor(portTraffic(port(9.5e8, 0, 1000))!, colors)).toBe('warning');
	});
	it('calls a dropping port saturated, whatever its average says', () => {
		const t = portTraffic(port(4e8, 1e9, 1000, 120))!;
		expect(trafficColor(t, colors)).toBe('critical');
		expect(trafficText(t)).toBe('↓400M ↑1G · 100% · drops 120/s');
	});
});

describe('the traffic overlay', () => {
	it('labels a switch port by name, and leaves a port with no link faint', () => {
		const tags = { SW1_Port25: port(3.64e4, 1.1e4, 10000), SW1_Port05: { OperUp: false } };
		const o = trafficOverlay(plant);
		const te25 = part('SW1', sw, 'te25');
		expect(o.paint(te25, tags.SW1_Port25, ctx('SW1', sw, tags))).toEqual({ color: 'r0', text: 'te0/25: ↓36k ↑11k · <1%' });
		expect(o.paint(part('SW1', sw, 'g5'), tags.SW1_Port05, ctx('SW1', sw, tags)).dim).toBe(true);
		expect(o.fanOut).toBe(true);
	});
	it('reads a server port off its switch peer, turned round', () => {
		const tags = { SW1_Port25: port(1e9, 3e9, 10000) };
		const p = peerTraffic(plant, 'NODE1', 'nicSlot2p2', server, tags)!;
		expect(p.peer).toBe('sw1 te0/25');
		expect([p.traffic.inBps, p.traffic.outBps]).toEqual([3e9, 1e9]);
		const paint = trafficOverlay(plant).paint(part('NODE1', server, 'nicSlot2p2'), undefined, ctx('NODE1', server, tags));
		expect(paint.text).toBe('slot2 p2: ↓3G ↑1G · 30% (sw1 te0/25)');
		// Its switch offline: nothing to say about now.
		expect(peerTraffic(plant, 'NODE1', 'nicSlot2p2', server, { ...tags, SW1__Online: false })).toBe(undefined);
	});
	it('mutes to colours alone for a whole rack', () => {
		const tags = { SW1_Port24: port(4e8, 1e9, 1000, 120) };
		const g24 = part('SW1', sw, 'g24');
		const p = mute(trafficOverlay(plant)).paint(g24, tags.SW1_Port24, ctx('SW1', sw, tags));
		expect(p).toEqual({ color: 'critical', text: undefined });
	});
	it('says nothing live about an offline device', () => {
		// The switch went dark: its tags still hold the last poll.
		const tags = { SW1__Online: false, SW1_Port25: port(9.5e9, 9.5e9, 10000, 50) };
		const te25 = part('SW1', sw, 'te25');
		expect(trafficOverlay(plant).paint(te25, tags.SW1_Port25, ctx('SW1', sw, tags)).dim).toBe(true);
		expect(interfacesOverlay.paint(te25, tags.SW1_Port25, ctx('SW1', sw, tags)).dim).toBe(true);
		const nic = part('NODE1', server, 'nicSlot2p2');
		expect(cablesOverlay(plant).paint(part('SW1', sw, 'g5'), { OperUp: true }, ctx('SW1', sw, tags)).dim).toBe(true);
		const drive = resolveParts(server, 'NODE1').find((p) => p.kind === 'drive')!;
		expect(heatOverlay.paint(drive, { TempC: 70 }, ctx('NODE1', server, { NODE1__Online: false })).dim).toBe(true);
		// Online again: the same readings paint.
		expect(trafficOverlay(plant).paint(te25, tags.SW1_Port25, ctx('SW1', sw, { ...tags, SW1__Online: true })).color).toBe('critical');
		expect(nic.kind).toBe('port');
	});
});
