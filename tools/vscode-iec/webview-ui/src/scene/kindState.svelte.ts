// Shared kind-editor state (Svelte 5 runes module) + the op seam to the
// extension host — sceneState for a *.kind.json (design §3g, slice 2).
// The host owns the file; the webview holds the sample values that drive
// the preview, what the model told it (mesh names, bounds), and the look.
import { vscode } from '../vscodeApi';
import { kindMembers, type Drive, type SceneKind, type SceneLook, type ScenePort, type Vec3 } from '@joyautomation/nautilus-hmi-3d';
import { cloneSafe, opFailureMessage } from '../mimic/opPayload';

/** Mirror of src/kindOps.ts (extension host). `null` deletes a field. */
export type KindField = 'type' | 'members' | 'model' | 'bounds' | 'labelAt' | 'status';
export type KindOp =
	| { type: 'batch'; ops: KindOp[] }
	| { type: 'setField'; key: KindField; value: unknown }
	| { type: 'addDrive'; drive: Drive; index?: number }
	| { type: 'updateDrive'; index: number; drive: Drive }
	| { type: 'deleteDrive'; index: number }
	| { type: 'moveDrive'; index: number; to: number }
	| { type: 'setPort'; name: string; port: ScenePort }
	| { type: 'renamePort'; name: string; to: string }
	| { type: 'deletePort'; name: string };

/** Mirror of src/sceneEditorLogic.ts's AssetReport. */
export type AssetReport = { pointer: string; path: string; file?: string };

export type Box = { size: Vec3; center: Vec3 };

export const kd = $state({
	/** The kind as written — what the panel shows and edits. */
	kind: null as SceneKind | null,
	/** The same with `model` as a URI the webview can fetch. */
	rendered: null as SceneKind | null,
	/** Where the host found the model (file absent = nowhere; null = no model). */
	model: null as AssetReport | null,
	/** The kind's name: the file's, minus .kind.json. */
	name: 'kind',
	title: 'kind',
	error: '',
	/** What the model said once loaded: its named objects and its box. */
	meshes: [] as string[],
	/** Each named object's nearest named ancestor (none at the top), so
	 * the panel can say "Vane1 · in Impeller". */
	parentOf: {} as Record<string, string | null>,
	/** The part under the pointer (on the stage, a chip or a drive row)
	 * and the part a click picked — a drive starts on it. Webview state. */
	hover: null as string | null,
	picked: null as string | null,
	/** Where the last click landed on the picked part, in the kind's frame:
	 * the point and the surface's outward normal — where "Add port here"
	 * puts a port, and the direction it gives it. */
	hit: null as { at: Vec3; dir: Vec3 } | null,
	/** The port under the pointer in the panel (its mark grows on stage). */
	hoverPort: null as string | null,
	bounds: null as Box | null,
	modelState: 'none' as 'none' | 'loading' | 'ok' | 'failed',
	/** Sample member values for the preview (never written to the file). */
	samples: {} as Record<string, number | boolean>,
	look: 'lit' as SceneLook,
	turntable: true
});

/** The named ancestors of a part, nearest first. */
export function ancestorsOf(name: string): string[] {
	const out: string[] = [];
	let p = kd.parentOf[name] ?? null;
	while (p && !out.includes(p)) {
		out.push(p);
		p = kd.parentOf[p] ?? null;
	}
	return out;
}

/** How many drives name a part. */
export function drivesOn(name: string): number {
	return (kd.kind?.drive ?? []).filter((d) => d.mesh === name).length;
}

/** The members the kind reads, in order: `members`, then the drives' and
 * the status template's — what hmi-3d's kindMembers and naut check see. */
export function members(): string[] {
	return kindMembers(kd.kind ?? undefined);
}

/** What a member looks like, from how the kind reads it: a boolean where
 * a tint / emissive / visible or a `{X?a:b}` reads it, a number where a
 * spin / turn / scale or `{X:1}` does, else unknown (shown as a number). */
export function memberShape(m: string): 'bool' | 'num' {
	const k = kd.kind;
	if (!k) return 'num';
	const strip = (b: string) => b.replace(/^!/, '');
	for (const d of k.drive ?? []) {
		if ('tint' in d && strip(d.tint.bind) === m) return 'bool';
		if ('emissive' in d && strip(d.emissive.bind) === m) return 'bool';
		if ('visible' in d && strip(d.visible.bind) === m) return 'bool';
	}
	if (k.status) {
		for (const x of k.status.matchAll(/\{([A-Za-z_][A-Za-z0-9_]*)(\?[^}]*)?(:\d)?\}/g)) if (x[1] === m && x[2]) return 'bool';
	}
	return 'num';
}

/** The preview's struct: every member with its sample (a default where
 * none was typed: on, or 50 — enough to spin, fill and light). */
export function sampleStruct(): Record<string, number | boolean> {
	const out: Record<string, number | boolean> = {};
	for (const m of members()) {
		const v = kd.samples[m];
		out[m] = v !== undefined ? v : memberShape(m) === 'bool' ? true : 50;
	}
	return out;
}

function send(type: 'kindOp', op: { type: string }): boolean {
	try {
		vscode.postMessage({ type, op: cloneSafe(op) });
		return true;
	} catch (err) {
		const msg = opFailureMessage(op.type, err).replace('the mimic editor', 'the kind editor');
		try {
			vscode.postMessage({ type: 'kindError', msg });
		} catch {
			// nothing left to tell
		}
		console.error('nautilus kind: ' + msg);
		return false;
	}
}

/** While the file doesn't parse (kd.error), the stage is a stale picture
 * and ops are dropped here as a backstop. */
export function postOp(op: KindOp): boolean {
	if (kd.error) return false;
	return send('kindOp', op);
}

export function reopenAsText(): void {
	vscode.postMessage({ type: 'reopenAsText' });
}

export function trace(msg: string): void {
	vscode.postMessage({ type: 'kindTrace', msg });
}

/** Tell the host the message listener is up — it answers with the file. */
export function announceReady(): void {
	vscode.postMessage({ type: 'kindReady' });
}

/** A drive's channel name, when it has exactly one. */
export function channelOf(d: Drive): 'spin' | 'turn' | 'scale' | 'tint' | 'emissive' | 'visible' {
	if ('spin' in d) return 'spin';
	if ('turn' in d) return 'turn';
	if ('scale' in d) return 'scale';
	if ('tint' in d) return 'tint';
	if ('emissive' in d) return 'emissive';
	return 'visible';
}

/** A fresh drive of a channel on a mesh, with the first member it could
 * read — the row the "Add drive" button and a channel change make. */
export function blankDrive(mesh: string, channel: ReturnType<typeof channelOf>, bind = members()[0] ?? 'Running'): Drive {
	switch (channel) {
		case 'spin':
			return { mesh, spin: { axis: 'y', revPerS: { bind, scale: 0.02 } } };
		case 'turn':
			return { mesh, turn: { axis: 'z', deg: { bind } } };
		case 'scale':
			return { mesh, scale: { axis: 'y', to: { bind, scale: 0.01, min: 0.01 } } };
		case 'tint':
			return { mesh, tint: { bind, on: 'running' } };
		case 'emissive':
			return { mesh, emissive: { bind, on: 'running', intensity: 2 } };
		default:
			return { mesh, visible: { bind } };
	}
}
