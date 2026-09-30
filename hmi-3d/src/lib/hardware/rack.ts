// Racks (docs/design/spatial-hmi.md §3e): where each device of a topology
// sits — its rack unit and which way it faces — and where every port and
// cable ends up in the room. Pure, like profile.ts: the rack view draws
// what this computes, and the tests check it.
//
// The rack's frame: origin on the floor at the centre of the front posts'
// plane, +x to the right looking at the front, +y up, +z out of the front.
// A device facing `front` has its profile origin (bottom centre of its
// front face) on the front posts; one facing `rear` is turned round and
// mounted on the rear posts, `depth` behind.
import type { Vec3 } from '../scene.js';
import { mm, type ChassisProfile, type ProfilePart } from './profile.js';
import { portPart } from './topology.js';

/** One rack unit, mm (EIA-310). */
export const RACK_U_MM = 44.45;

export interface RackDevice {
	/** The topology device id. */
	id: string;
	/** The lowest unit it occupies, 1-based from the bottom. */
	u: number;
	/** Which posts it is mounted on, facing out. */
	face: 'front' | 'rear';
}

export interface RackLayout {
	name?: string;
	/** Height in units. */
	units: number;
	/** Front posts to rear posts, mm. */
	depth: number;
	/** Floor to the bottom of U1, mm. */
	base?: number;
	devices: RackDevice[];
	/** Where the layout came from, and what is not yet measured. */
	note?: string;
}

export interface Placement {
	/** Metres, in the rack's frame. */
	pos: Vec3;
	/** Degrees about y: 0 faces front, 180 faces rear. */
	rotY: 0 | 180;
}

export const uY = (r: RackLayout, u: number) => ((r.base ?? 60) + (u - 1) * RACK_U_MM) / 1000;

// The 19-inch mounting geometry (EIA-310), metres from the rack's centre.
/** A rack-mount device's ears reach this far out (482.6 mm across). */
export const EAR_X = 0.2413;
/** The mounting rails' inner edge: the opening a chassis slides through (450.8 mm). */
export const RAIL_X = 0.2254;
/** The rails' hole centres (465.1 mm apart). */
export const HOLE_X = 0.23255;
/** The ears are this thick, in front of the rails (z from -EAR_T to 0). */
export const EAR_T = 0.003;
/** A unit's three holes, mm above the unit's bottom. */
export const U_HOLES = [6.35, 22.225, 38.1];
/** Every mounting hole's height in the rack, metres: three per unit. */
export const holeYs = (r: RackLayout): number[] =>
	Array.from({ length: r.units }, (_, i) => U_HOLES.map((h) => uY(r, i + 1) + h / 1000)).flat();

export function placeDevice(r: RackLayout, d: RackDevice): Placement {
	return d.face === 'rear' ? { pos: [0, uY(r, d.u), -r.depth / 1000], rotY: 180 } : { pos: [0, uY(r, d.u), 0], rotY: 0 };
}

/** A point in a device's profile frame (metres) → the rack's frame. */
export function toRack(p: Placement, v: Vec3): Vec3 {
	return p.rotY === 180 ? [p.pos[0] - v[0], p.pos[1] + v[1], p.pos[2] - v[2]] : [p.pos[0] + v[0], p.pos[1] + v[1], p.pos[2] + v[2]];
}

/** A port's mouth and the way it faces, in the rack's frame. A port
 * turned 180° about y (a switch's front panel) faces +z in its profile;
 * one that is not (a server's rear I/O) faces -z. */
export function portMouth(p: Placement, profile: ChassisProfile, part: ProfilePart): { at: Vec3; out: Vec3 } {
	const pos = mm(part.pos);
	const size = mm(part.size);
	const dz = Math.abs((part.rot?.[1] ?? 0) % 360) === 180 ? 1 : -1;
	const at = toRack(p, [pos[0], pos[1], pos[2] + (dz * size[2]) / 2]);
	const out = p.rotY === 180 ? -dz : dz;
	return { at, out: [0, 0, out] };
}

/** The rack's end of a `device/port` link end, if both are in the rack. */
export function endInRack(r: RackLayout, profileOf: (id: string) => ChassisProfile | undefined, device: string, port: string) {
	const d = r.devices.find((x) => x.id === device);
	const profile = profileOf(device);
	const part = profile && portPart(profile, port);
	return d && profile && part ? portMouth(placeDevice(r, d), profile, part) : undefined;
}

const add = (a: Vec3, b: Vec3, k = 1): Vec3 => [a[0] + b[0] * k, a[1] + b[1] * k, a[2] + b[2] * k];

/**
 * A cable's path between two port mouths: straight out of each port, then
 * along a lane down the side of the rack (the side nearer both ends), so
 * cables run where a tidy rack runs them instead of through the chassis.
 * `lane` spreads cables in the same lane apart. `b` undefined leaves the
 * rack through the top (a site uplink).
 */
export function cablePath(r: RackLayout, a: { at: Vec3; out: Vec3 }, b: { at: Vec3; out: Vec3 } | undefined, lane = 0, halfWidth = 0.226): Vec3[] {
	const reach = 0.035 + lane * 0.004;
	const a1 = add(a.at, a.out, reach);
	if (!b) {
		const top = uY(r, r.units + 1) + 0.15;
		const side = (a.at[0] >= 0 ? 1 : -1) * (halfWidth + lane * 0.003);
		return [a.at, a1, [side, a1[1], a1[2]], [side, top, a1[2]]];
	}
	const b1 = add(b.at, b.out, reach);
	const side = (a.at[0] + b.at[0] >= 0 ? 1 : -1) * (halfWidth + lane * 0.003);
	return [a.at, a1, [side, a1[1], a1[2]], [side, b1[1], b1[2]], b1, b.at];
}
