// Mimic clipboard: copy / cut / paste / duplicate of selected equipment.
// Pure functions over the doc (no Svelte, no DOM) so node --test covers
// them; EditorCanvas wires them to keys and posts the ops.
//
// A copy carries the equipment verbatim (component, geometry, label,
// props, bindings, port overrides) plus the pipes anchored at BOTH ends to
// copied equipment. Paste re-creates them under fresh ids, offset, with
// those pipes re-anchored to the copies; a pipe reaching equipment that
// wasn't copied stays behind (pasting it would anchor to the original).
import type { MimicDoc, MimicEquipment, MimicPipe } from '@joyautomation/nautilus-hmi';
import type { MimicOp } from './mimicState.svelte';

export type MimicClip = { equipment: MimicEquipment[]; pipes: MimicPipe[] };

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

export function clipEquipment(doc: MimicDoc, ids: string[]): MimicClip | undefined {
	const set = new Set(ids);
	const equipment = (doc.equipment ?? []).filter((e) => set.has(e.id)).map(clone);
	if (!equipment.length) return undefined;
	const pipes = (doc.pipes ?? []).filter((p) => p.from && p.to && set.has(p.from.equip) && set.has(p.to.equip)).map(clone);
	return { equipment, pipes };
}

/** First free `<stem><n>` — the host reducer's genId convention, where the
 * stem is the id with any trailing number dropped (tank3 → tank4). */
function freshId(id: string, taken: Set<string>): string {
	const m = /^(.*?)(\d+)$/.exec(id);
	const stem = m && m[1] ? m[1] : id;
	for (let n = 1; ; n++) {
		const c = `${stem}${n}`;
		if (!taken.has(c)) return c;
	}
}

/** The ONE batch op that pastes clip into doc, shifted by (dx, dy), plus
 * the new equipment ids (for selecting the copies). */
export function pasteOps(doc: MimicDoc, clip: MimicClip, dx: number, dy: number): { op: MimicOp; ids: string[] } {
	const taken = new Set<string>([...(doc.equipment ?? []).map((e) => e.id), ...(doc.pipes ?? []).map((p) => p.id)]);
	const idMap = new Map<string, string>();
	const ops: MimicOp[] = [];
	for (const e of clip.equipment) {
		const id = freshId(e.id, taken);
		taken.add(id);
		idMap.set(e.id, id);
		ops.push({ type: 'addEquipment', component: e.component, x: e.x + dx, y: e.y + dy, id });
		const patch: Record<string, unknown> = {};
		if (e.width !== undefined) patch.width = e.width;
		if (e.label !== undefined) patch.label = e.label;
		if (e.props && Object.keys(e.props).length) patch.props = clone(e.props);
		if (e.bind && Object.keys(e.bind).length) patch.bind = clone(e.bind);
		if (Object.keys(patch).length) ops.push({ type: 'updateEquipment', id, patch });
		if (e.ports) ops.push({ type: 'setEquipmentPorts', id, ports: clone(e.ports) });
	}
	for (const p of clip.pipes) {
		const from = p.from && idMap.get(p.from.equip);
		const to = p.to && idMap.get(p.to.equip);
		if (!from || !to) continue;
		const id = freshId(p.id, taken);
		taken.add(id);
		ops.push({
			type: 'addPipe',
			id,
			points: p.points.map(([x, y]) => [x + dx, y + dy] as [number, number]),
			from: { equip: from, port: p.from!.port },
			to: { equip: to, port: p.to!.port },
			...(p.routing === 'orthogonal' ? { routing: 'orthogonal' as const } : {})
		});
		const patch: Record<string, unknown> = {};
		if (p.color !== undefined) patch.color = p.color;
		if (p.props && Object.keys(p.props).length) patch.props = clone(p.props);
		if (p.bind && Object.keys(p.bind).length) patch.bind = clone(p.bind);
		if (Object.keys(patch).length) ops.push({ type: 'updatePipe', id, patch });
	}
	return { op: { type: 'batch', ops }, ids: [...idMap.values()] };
}
