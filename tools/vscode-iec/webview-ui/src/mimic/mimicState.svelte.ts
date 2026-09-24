// Shared mimic-editor state (Svelte 5 runes module) + the op seam to the
// extension host. Every gesture ends in postOp(); the host applies it to
// the *.mimic.json text and the updated doc flows back as a mimicDoc
// message — the webview never owns the document, only in-flight gestures.
import { vscode } from '../vscodeApi';
import type { MimicDoc, MimicPipeAnchor } from '@joyautomation/nautilus-hmi';
import type { ComponentsManifest, Port } from './ports';

/** Mirror of src/mimicOps.ts (extension host). Patch semantics: a key set
 * to null DELETES the optional field; a present value sets it. */
export type MimicOp =
	| { type: 'batch'; ops: MimicOp[] }
	| { type: 'setName'; name?: string | null }
	| { type: 'setCanvas'; width: number; height: number }
	| { type: 'addEquipment'; component: string; x: number; y: number; id?: string }
	| { type: 'moveEquipment'; id: string; x: number; y: number }
	| { type: 'updateEquipment'; id: string; patch: Record<string, unknown> }
	| { type: 'deleteEquipment'; id: string }
	| { type: 'setEquipmentPorts'; id: string; ports: Port[] | null }
	| {
			type: 'addPipe';
			points: [number, number][];
			id?: string;
			from?: MimicPipeAnchor;
			to?: MimicPipeAnchor;
			/** Route-suggestion gesture (autoroute.ts) — see EditorCanvas's
			 * finishPipe(): a port-to-port draw with zero interior clicks
			 * defaults its suggested route to 'orthogonal'. Absent = 'direct'. */
			routing?: 'direct' | 'orthogonal';
	  }
	| { type: 'setPipePoints'; id: string; points: [number, number][] }
	| { type: 'updatePipe'; id: string; patch: Record<string, unknown> }
	| { type: 'deletePipe'; id: string }
	| { type: 'addLabel'; text: string; x: number; y: number }
	| { type: 'updateLabel'; index: number; patch: Record<string, unknown> }
	| { type: 'deleteLabel'; index: number };

export type Selection =
	| { kind: 'equipment'; id: string }
	| { kind: 'pipe'; id: string }
	/** Shift-click on vertex handles builds this up, one pipe at a time —
	 * starting it (or a plain click on a pipe/stroke) clears the other mode. */
	| { kind: 'nodes'; pipeId: string; indices: number[] }
	/** A pipe's terminal (start/end) handle, clicked directly — distinct from
	 * plain `'pipe'` selection so a keyboard nudge (EditorCanvas's Arrow-key
	 * handling) knows WHICH end to move without guessing. Applies to a
	 * terminal handle whether or not it's currently anchored (an anchored
	 * end's nudge detaches-and-nudges; a plain terminal point's nudge just
	 * moves it, same as any vertex). PropsPanel treats this the same as a
	 * `'pipe'` selection (resolving `pipeId`), so the properties panel and
	 * the pipe-selected highlight/handles keep working from it unchanged. */
	| { kind: 'end'; pipeId: string; end: 'from' | 'to' }
	| { kind: 'label'; index: number };

export type Tool = 'select' | 'pipe' | 'label' | 'place';

/** Ports editing targets either the SHARED manifest entry for the
 * instance's component (every instance of that component inherits it) or
 * just this one instance (writes eq.ports). Manifest is the default —
 * that's the point of the manifest tier. */
export type PortsEditTarget = 'manifest' | 'instance';
export type PortsEdit = { id: string; target: PortsEditTarget };

/** Mirror of src/mimicComponents.ts's ManifestOp. */
export type ManifestOp = { type: 'setComponentPorts'; component: string; ports: Port[] | null };

export const ed = $state({
	doc: null as MimicDoc | null,
	title: 'mimic',
	/** Parse error from the host — the JSON is being hand-edited mid-keystroke. */
	error: '',
	/** Live tag snapshot from the controller; null = offline (defaults render). */
	tags: null as Record<string, unknown> | null,
	/** Resolved component ports, aggregated from the project's
	 * *.component.json sidecars and keyed by component name; null until the
	 * host's first mimicManifest message. */
	manifest: null as ComponentsManifest | null,
	/** Palette "custom components" section: names with a project sidecar
	 * and/or referenced by this doc's equipment, built-ins already excluded
	 * host-side (mimicEditor.ts's paletteCustomComponents). Empty until the
	 * first mimicManifest message. */
	customComponents: [] as string[],
	selection: null as Selection | null,
	tool: 'select' as Tool,
	placeComponent: '',
	/** Non-null while the ports editor is active for one equipment instance
	 * (see EditorCanvas's port-drag handles + PropsPanel's ports section). */
	portsEdit: null as PortsEdit | null,
	/** Whether dragging equipment/labels/pipe vertices snaps to GRID —
	 * mirrors the `nautilus.mimic.snapToGrid` setting (see the toolbar
	 * toggle in MimicApp.svelte and setSnapToGrid() below). Defaults on
	 * (existing behavior) until the host's first mimicConfig message.
	 * Shift-arrow nudging ignores this — it always steps by GRID. */
	snapToGrid: true
});

/** While the document doesn't parse (ed.error), the canvas is a stale
 * read-only picture — MimicApp locks it, and ops are dropped here as a
 * backstop (a gesture already in flight, a keyboard nudge) so the host
 * isn't asked to edit text it would refuse with a warning toast apiece. */
export function postOp(op: MimicOp): void {
	if (ed.error) return;
	vscode.postMessage({ type: 'mimicOp', op });
}

/** Commit a component-level ports edit to the project manifest. */
export function postManifestOp(op: ManifestOp): void {
	if (ed.error) return;
	vscode.postMessage({ type: 'manifestOp', op });
}

/** Leave the graphical editor for VS Code's text editor on this file —
 * the way out when the JSON doesn't parse. */
export function reopenAsText(): void {
	vscode.postMessage({ type: 'reopenAsText' });
}

export function trace(msg: string): void {
	vscode.postMessage({ type: 'mimicTrace', msg });
}

/** Tell the host the message listener is up — it answers with the current
 * doc + tags. Without this handshake the one-shot mimicDoc post can land
 * before the app's listener exists and the canvas waits forever. */
export function announceReady(): void {
	vscode.postMessage({ type: 'mimicReady' });
}

/** Flip the snap-to-grid toggle: update local state immediately (the
 * toolbar button reflects it right away) and ask the host to persist it as
 * `nautilus.mimic.snapToGrid` (workspace-scoped when a folder is open, same
 * convention as the live-values toggle) so it survives editor reloads. */
export function setSnapToGrid(value: boolean): void {
	ed.snapToGrid = value;
	vscode.postMessage({ type: 'setSnapToGrid', value });
}

/** Placement grid, canvas units. */
export const GRID = 10;
/** Round to the grid when snap-to-grid is on; otherwise round to the
 * nearest whole pixel (free dragging, but still integer coordinates in the
 * saved doc). Shift-arrow nudging (EditorCanvas's onkeydown) bypasses this
 * entirely and always steps by GRID, regardless of the toggle. */
export const snap = (v: number): number => (ed.snapToGrid ? Math.round(v / GRID) * GRID : Math.round(v));
