// Shared scene-editor state (Svelte 5 runes module) + the op seam to the
// extension host — the mimic editor's mimicState, for a *.scene.json.
// Every gesture ends in postOp(); the host applies it to the text and the
// updated doc flows back as a sceneDoc message. The webview never owns the
// document, only in-flight gestures (a drag's live position, the armed
// kind, the selection).
import { vscode } from '../vscodeApi';
import type { SceneDoc, Vec3 } from '@joyautomation/nautilus-hmi-3d';
import { cloneSafe, opFailureMessage } from '../mimic/opPayload';

/** Mirror of src/sceneOps.ts (extension host). Patch semantics: a key set
 * to null DELETES the optional field; a present value sets it. */
export type SceneOp =
	| { type: 'batch'; ops: SceneOp[] }
	| { type: 'addNode'; kind: string; pos: Vec3; id?: string; tag?: string; label?: string }
	| { type: 'moveNode'; id: string; pos: Vec3; rot?: Vec3 }
	| { type: 'updateNode'; id: string; patch: Record<string, unknown> }
	| { type: 'deleteNode'; id: string }
	| { type: 'addPipe'; points: Vec3[]; id?: string; bind?: { flowing?: string } }
	| { type: 'setPipePoints'; index: number; points: Vec3[] }
	| { type: 'deletePipe'; index: number }
	| { type: 'setCamera'; pos: Vec3; target: Vec3 };

/** Mirror of src/sceneKinds.ts's PaletteKind. */
export type PaletteKind = {
	name: string;
	definedBy: 'builtin' | 'model' | 'component' | 'assembly' | 'declared';
	type?: string;
	members: string[];
	source?: string;
	parts?: number;
};

/** Mirror of src/sceneEditorLogic.ts's AssetReport: where each asset path
 * the document names was found (`file` absent = nowhere). */
export type AssetReport = { pointer: string; path: string; file?: string };

export type Tool = 'select' | 'place';
export type Gizmo = 'translate' | 'rotate';

export const ed = $state({
	/** The document as written — what the panel shows and edits. */
	doc: null as SceneDoc | null,
	/** The same document with every asset path the host found rewritten
	 * to a URI the webview can fetch — what the stage renders. */
	rendered: null as SceneDoc | null,
	title: 'scene',
	/** Parse error from the host — the JSON is being hand-edited mid-keystroke. */
	error: '',
	/** Live tag snapshot from the controller; null = offline or live off. */
	tags: null as Record<string, unknown> | null,
	liveEnabled: true,
	/** The palette: built-ins plus the document's own kinds. */
	kinds: [] as PaletteKind[],
	assets: [] as AssetReport[],
	/** The selected node's id. */
	selected: null as string | null,
	tool: 'select' as Tool,
	/** The kind armed for placement (tool === 'place'). */
	placeKind: '',
	gizmo: 'translate' as Gizmo,
	/** Mirrors `nautilus.scene.snapToGrid`. */
	snapToGrid: true,
	/** The orbit camera as it stands, kept by the stage for "Save view". */
	view: null as { pos: Vec3; target: Vec3 } | null
});

/** The asset paths the host could not find, for the banner. */
export function missingAssets(): AssetReport[] {
	return ed.assets.filter((a) => !a.file);
}

function send(type: 'sceneOp', op: { type: string }): boolean {
	try {
		vscode.postMessage({ type, op: cloneSafe(op) });
		return true;
	} catch (err) {
		const msg = opFailureMessage(op.type, err).replace('the mimic editor', 'the scene editor');
		try {
			vscode.postMessage({ type: 'sceneError', msg });
		} catch {
			// nothing left to tell
		}
		console.error('nautilus scene: ' + msg);
		return false;
	}
}

/** While the document doesn't parse (ed.error), the stage is a stale
 * read-only picture and ops are dropped here as a backstop. */
export function postOp(op: SceneOp): boolean {
	if (ed.error) return false;
	return send('sceneOp', op);
}

export function toggleLive(): void {
	vscode.postMessage({ type: 'toggleLive' });
}

export function reopenAsText(): void {
	vscode.postMessage({ type: 'reopenAsText' });
}

export function trace(msg: string): void {
	vscode.postMessage({ type: 'sceneTrace', msg });
}

/** Tell the host the message listener is up — it answers with the
 * current doc, config and tags. */
export function announceReady(): void {
	vscode.postMessage({ type: 'sceneReady' });
}

export function setSnapToGrid(value: boolean): void {
	ed.snapToGrid = value;
	vscode.postMessage({ type: 'setSnapToGrid', value });
}

/** Arm a kind for placement, or disarm it when it is the armed one. */
export function armPlace(kind: string): void {
	if (ed.tool === 'place' && ed.placeKind === kind) {
		ed.tool = 'select';
		ed.placeKind = '';
		return;
	}
	ed.tool = 'place';
	ed.placeKind = kind;
}

export function disarm(): void {
	ed.tool = 'select';
	ed.placeKind = '';
}

/** The grid pitch a drag snaps to, metres: the document's grid cell. */
export function snapCell(): number {
	const cell = ed.doc?.grid?.cell;
	return typeof cell === 'number' && cell > 0 ? cell : 0.1;
}

/** Rotation snap, degrees. */
export const ROT_SNAP_DEG = 15;

/** Round a position to the grid when snap is on (and Shift is not held),
 * else to the millimetre. */
export function snapPos(p: Vec3, free = false): Vec3 {
	const cell = snapCell();
	const q = (v: number) => (ed.snapToGrid && !free ? Math.round(v / cell) * cell : Math.round(v * 1000) / 1000);
	return [q(p[0]), q(p[1]), q(p[2])];
}

/** The y a new node is dropped at: the grid's plane, else the
 * environment's floor, else the site origin. */
export function groundY(doc: SceneDoc | null): number {
	const gy = doc?.grid?.pos?.[1];
	if (typeof gy === 'number') return gy;
	const fy = doc?.environment?.floor;
	return typeof fy === 'number' ? fy : 0;
}
