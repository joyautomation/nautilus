// Kinds as data: the drive vocabulary. A data kind is a glTF plus a list of
// drives, each naming one mesh in the model and one channel — spin, turn,
// scale, tint, emissive, visible — fed by a member of the node's struct.
// This module is the pure half: the types, the validator `validateScene`
// calls, and the evaluator that turns a struct value into what each mesh
// should do this frame. GltfNode.svelte applies the result to three.js.
// Brief: docs/design/spatial-hmi.md §3c.
import { member, num } from './bindings.js';
import type { Vec3 } from './scene.js';

export type Axis = 'x' | 'y' | 'z';

/** A number read off the struct: member × scale + offset, clamped. */
export interface NumExpr {
	/** A member of the node's struct (`Speed`). */
	bind: string;
	scale?: number;
	offset?: number;
	min?: number;
	max?: number;
}

/** A boolean read off the struct: `true`, or a number above `threshold`
 * (default 0). A leading `!` on `bind` negates, as everywhere else. */
export interface BoolExpr {
	bind: string;
	threshold?: number;
}

export interface SpinDrive {
	mesh: string;
	spin: { axis: Axis; revPerS: NumExpr };
}
export interface TurnDrive {
	mesh: string;
	turn: { axis: Axis; deg: NumExpr };
}
export interface ScaleDrive {
	mesh: string;
	scale: { axis: Axis | 'xyz'; to: NumExpr };
}
export interface TintDrive {
	mesh: string;
	/** `on` / `off` are palette slots (`running`, `fluid`, `handle`, a priority
	 * name) or CSS colours. Absent `off` = the model's own material colour. */
	tint: BoolExpr & { on: string; off?: string };
}
export interface EmissiveDrive {
	mesh: string;
	emissive: BoolExpr & { on: string; intensity?: number };
}
export interface VisibleDrive {
	mesh: string;
	visible: BoolExpr;
}
export type Drive = SpinDrive | TurnDrive | ScaleDrive | TintDrive | EmissiveDrive | VisibleDrive;

/** The vocabulary. The extension's schema and internal/scene carry the same
 * list; a test on each side reads the schema so the three cannot drift. */
export const DRIVE_CHANNELS = ['spin', 'turn', 'scale', 'tint', 'emissive', 'visible'] as const;
export type DriveChannel = (typeof DRIVE_CHANNELS)[number];

/** What GltfNode does to one mesh this frame. `null` = leave the model's own. */
export interface MeshState {
	mesh: string;
	/** Radians per second about the axis; the component integrates it. */
	spin?: { axis: Axis; radPerS: number };
	/** Absolute rotation, radians. */
	turn?: { axis: Axis; rad: number };
	scale?: Vec3;
	tint?: string | null;
	emissive?: { color: string; intensity: number } | null;
	visible?: boolean;
}

const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);
const isStr = (v: unknown): v is string => typeof v === 'string' && v.length > 0;
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
const AXES = new Set(['x', 'y', 'z']);
const MEMBER_RE = /^!?[A-Za-z_][A-Za-z0-9_]*$/;

/** The prop name a member is bound under on a node (`Level` -> `level`),
 * the built-ins' convention, so a node's `bind` overrides a drive's member
 * the same way it overrides a component's. */
export function propFor(memberName: string): string {
	return memberName.charAt(0).toLowerCase() + memberName.slice(1);
}

/** The member a `bind` reads, without the negation. */
export function bindMember(bind: string): string {
	return bind.startsWith('!') ? bind.slice(1) : bind;
}

/** Read a member off the struct, letting a node-level bound prop override it. */
function read(bind: string, value: unknown, over: Record<string, unknown>): unknown {
	const m = bindMember(bind);
	const o = over[propFor(m)];
	return o !== undefined ? o : member(value, m);
}

export function evalNum(e: NumExpr, value: unknown, over: Record<string, unknown> = {}): number {
	let v = num(read(e.bind, value, over)) * (e.scale ?? 1) + (e.offset ?? 0);
	if (e.min !== undefined) v = Math.max(e.min, v);
	if (e.max !== undefined) v = Math.min(e.max, v);
	return v;
}

export function evalBool(e: BoolExpr, value: unknown, over: Record<string, unknown> = {}): boolean {
	const raw = read(e.bind, value, over);
	const on = typeof raw === 'number' ? raw > (e.threshold ?? 0) : raw === true;
	return e.bind.startsWith('!') ? !on : on;
}

const deg = Math.PI / 180;

/**
 * Evaluate a kind's drives against a node's struct value (and the node's
 * bound overrides). Pure: the same inputs give the same list, one entry
 * per drive in document order, so a mesh named twice gets both.
 */
export function evalDrives(drives: Drive[] | undefined, value: unknown, over: Record<string, unknown> = {}): MeshState[] {
	if (!drives) return [];
	return drives.map((d) => {
		const s: MeshState = { mesh: d.mesh };
		if ('spin' in d) s.spin = { axis: d.spin.axis, radPerS: evalNum(d.spin.revPerS, value, over) * 2 * Math.PI };
		else if ('turn' in d) s.turn = { axis: d.turn.axis, rad: evalNum(d.turn.deg, value, over) * deg };
		else if ('scale' in d) {
			const k = evalNum(d.scale.to, value, over);
			const a = d.scale.axis;
			s.scale = [a === 'x' || a === 'xyz' ? k : 1, a === 'y' || a === 'xyz' ? k : 1, a === 'z' || a === 'xyz' ? k : 1];
		} else if ('tint' in d) s.tint = evalBool(d.tint, value, over) ? d.tint.on : (d.tint.off ?? null);
		else if ('emissive' in d)
			s.emissive = evalBool(d.emissive, value, over) ? { color: d.emissive.on, intensity: d.emissive.intensity ?? 1 } : null;
		else if ('visible' in d) s.visible = evalBool(d.visible, value, over);
		return s;
	});
}

/** The members a list of drives reads, deduplicated, in order. */
export function driveMembers(drives: Drive[] | undefined): string[] {
	const out: string[] = [];
	for (const d of drives ?? []) {
		const ch = channelOf(d);
		if (!ch) continue;
		const body = (d as unknown as Record<string, unknown>)[ch] as Record<string, unknown>;
		const expr = ch === 'spin' ? body.revPerS : ch === 'turn' ? body.deg : ch === 'scale' ? body.to : body;
		const bind = isObj(expr) ? expr.bind : undefined;
		if (isStr(bind)) {
			const m = bindMember(bind);
			if (!out.includes(m)) out.push(m);
		}
	}
	return out;
}

/** Which channel a drive uses, or undefined when it has none or several. */
export function channelOf(d: unknown): DriveChannel | undefined {
	if (!isObj(d)) return undefined;
	const found = DRIVE_CHANNELS.filter((c) => d[c] !== undefined);
	return found.length === 1 ? found[0] : undefined;
}

/**
 * Structural validation of a `drive` list, with paths under `base`
 * (`/kinds/pump/drive`). The same rules internal/scene applies offline.
 */
export function validateDrives(drives: unknown, base: string): { path: string; message: string }[] {
	const errors: { path: string; message: string }[] = [];
	const err = (path: string, message: string) => errors.push({ path, message });
	if (!Array.isArray(drives)) return [{ path: base, message: 'must be an array of drives' }];
	drives.forEach((d, i) => {
		const p = `${base}/${i}`;
		if (!isObj(d)) return err(p, 'must be an object');
		if (!isStr(d.mesh)) err(`${p}/mesh`, 'must name a mesh in the model');
		const present = DRIVE_CHANNELS.filter((c) => d[c] !== undefined);
		if (present.length !== 1) return err(p, `a drive has exactly one of ${DRIVE_CHANNELS.join(', ')}`);
		const ch = present[0];
		const body = d[ch];
		if (!isObj(body)) return err(`${p}/${ch}`, 'must be an object');
		const numExpr = (e: unknown, ep: string) => {
			if (!isObj(e)) return err(ep, 'must be { bind, scale?, offset?, min?, max? }');
			if (!isStr(e.bind) || !MEMBER_RE.test(e.bind) || e.bind.startsWith('!')) err(`${ep}/bind`, 'must name a member of the struct');
			for (const k of ['scale', 'offset', 'min', 'max']) if (e[k] !== undefined && !isNum(e[k])) err(`${ep}/${k}`, 'must be a number');
		};
		const boolExpr = (e: Record<string, unknown>, ep: string) => {
			if (!isStr(e.bind) || !MEMBER_RE.test(e.bind)) err(`${ep}/bind`, 'must name a member of the struct (a leading ! negates)');
			if (e.threshold !== undefined && !isNum(e.threshold)) err(`${ep}/threshold`, 'must be a number');
		};
		const axis = (a: unknown, ap: string, xyz = false) => {
			if (!(isStr(a) && (AXES.has(a) || (xyz && a === 'xyz')))) err(ap, xyz ? "must be 'x', 'y', 'z' or 'xyz'" : "must be 'x', 'y' or 'z'");
		};
		switch (ch) {
			case 'spin':
				axis(body.axis, `${p}/spin/axis`);
				numExpr(body.revPerS, `${p}/spin/revPerS`);
				break;
			case 'turn':
				axis(body.axis, `${p}/turn/axis`);
				numExpr(body.deg, `${p}/turn/deg`);
				break;
			case 'scale':
				axis(body.axis, `${p}/scale/axis`, true);
				numExpr(body.to, `${p}/scale/to`);
				break;
			case 'tint':
				boolExpr(body, `${p}/tint`);
				if (!isStr(body.on)) err(`${p}/tint/on`, 'must be a palette slot or a CSS colour');
				if (body.off !== undefined && !isStr(body.off)) err(`${p}/tint/off`, 'must be a palette slot or a CSS colour');
				break;
			case 'emissive':
				boolExpr(body, `${p}/emissive`);
				if (!isStr(body.on)) err(`${p}/emissive/on`, 'must be a palette slot or a CSS colour');
				if (body.intensity !== undefined && !(isNum(body.intensity) && body.intensity >= 0)) err(`${p}/emissive/intensity`, 'must be ≥ 0');
				break;
			case 'visible':
				boolExpr(body, `${p}/visible`);
				break;
		}
	});
	return errors;
}

// ── status templates ────────────────────────────────────────────────────

const TEMPLATE_RE = /\{([A-Za-z_][A-Za-z0-9_]*)(?::(\d))?(?:\?([^:}]*):([^}]*))?\}/g;

/** The members a status template reads, in order, deduplicated. */
export function statusMembers(template: string | undefined): string[] {
	const out: string[] = [];
	if (!template) return out;
	for (const m of template.matchAll(TEMPLATE_RE)) if (!out.includes(m[1])) out.push(m[1]);
	return out;
}

/**
 * Render a data kind's label text. `{Level}` is the member (numbers to one
 * decimal), `{Level:0}` sets the digits, `{Running?run:stopped}` picks by
 * truth. Bad quality reads `stale`, like every built-in.
 */
export function formatStatus(template: string, value: unknown, good: boolean, over: Record<string, unknown> = {}): string {
	if (!good) return 'stale';
	return template.replace(TEMPLATE_RE, (_, name: string, digits?: string, yes?: string, no?: string) => {
		const v = read(name, value, over);
		if (yes !== undefined) return (typeof v === 'number' ? v > 0 : v === true) ? yes : (no ?? '');
		if (typeof v === 'number') return v.toFixed(digits !== undefined ? Number(digits) : 1);
		if (v === undefined || v === null) return '—';
		return String(v);
	});
}

/** Is a status template well-formed? (Braces balanced and every field a member.) */
export function validStatusTemplate(t: unknown): t is string {
	if (typeof t !== 'string') return false;
	return t.replace(TEMPLATE_RE, '').indexOf('{') < 0 && t.replace(TEMPLATE_RE, '').indexOf('}') < 0;
}
