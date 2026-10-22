// Port marks (design §3g, slice 3): what both editors draw where a kind's
// ports are — a dot at the port and a short stroke along the direction a
// pipe leaves it — built as plain three objects added under a node's own
// group, so they turn, scale and move with it (the turntable, the gizmo).
// Pure three; no Svelte.
import { Box3, BufferGeometry, Group, Line, LineBasicMaterial, Matrix4, Mesh, MeshBasicMaterial, SphereGeometry, Vector3, type Object3D } from 'three';
import { resolvePort, type KindBoxes, type SceneBox, type ScenePort, type Vec3 } from '@joyautomation/nautilus-hmi-3d';

export const PORT_COLOR = '#f2b640';
export const PORT_HOT = '#ffe08a';
/** The dot's radius, metres, before `scale`. */
export const PORT_R = 0.016;

export type PortMark = Group & { userData: { nxPort: string; nxHighlight: true; ink: false } };

/** A mark for one resolved port (its point and direction in the frame it
 * is declared in). `hot` is the hovered / armed look. */
export function makePortMark(name: string, port: { at: Vec3; dir: Vec3 }, { scale = 1, hot = false } = {}): PortMark {
	const g = new Group() as PortMark;
	g.userData = { nxPort: name, nxHighlight: true, ink: false };
	g.position.set(...port.at);
	const r = PORT_R * scale * (hot ? 1.5 : 1);
	const color = hot ? PORT_HOT : PORT_COLOR;
	const dot = new Mesh(new SphereGeometry(r, 12, 8), new MeshBasicMaterial({ color, depthTest: false, transparent: true, opacity: hot ? 1 : 0.9 }));
	dot.userData = { nxPortDot: name, nxHighlight: true, ink: false };
	dot.renderOrder = 998;
	g.add(dot);
	const d = new Vector3(...port.dir);
	if (d.lengthSq() > 0) {
		d.normalize().multiplyScalar(r * 4);
		const line = new Line(new BufferGeometry().setFromPoints([new Vector3(0, 0, 0), d]), new LineBasicMaterial({ color, depthTest: false, transparent: true, opacity: 0.9 }));
		line.userData = { nxHighlight: true, ink: false };
		line.raycast = () => {};
		line.renderOrder = 998;
		g.add(line);
	}
	return g;
}

export function dropMark(g: Object3D | null | undefined): void {
	if (!g) return;
	g.removeFromParent();
	g.traverse((o) => {
		const m = o as Mesh;
		m.geometry?.dispose?.();
		(m.material as { dispose?: () => void } | undefined)?.dispose?.();
	});
}

/** Lay one mark per port under `parent`, dropping what was there. A port
 * on a box not known yet (the model still loading) gets no mark; the
 * caller lays again once it is. Returns the marks by port name. */
export function layPortMarks(parent: Object3D, ports: Record<string, ScenePort> | undefined, boxes: KindBoxes | undefined, previous: Map<string, PortMark>, hot: string | null, scale = 1): Map<string, PortMark> {
	for (const m of previous.values()) dropMark(m);
	const out = new Map<string, PortMark>();
	for (const [name, port] of Object.entries(ports ?? {})) {
		const r = resolvePort(port, boxes);
		if (!r) continue;
		const m = makePortMark(name, r, { scale, hot: name === hot });
		parent.add(m);
		out.set(name, m);
	}
	return out;
}

/** Each named part's box in `group`'s own frame, from the meshes under it
 * (the renderer's clone of the model, once loaded) — skipping marks and
 * highlights. Empty until the model is on stage. */
export function partBoxes(group: Object3D): Record<string, SceneBox> {
	group.updateWorldMatrix(true, true);
	const inv = new Matrix4().copy(group.matrixWorld).invert();
	const out: Record<string, SceneBox> = {};
	const m = new Matrix4();
	group.traverse((o) => {
		if (!o.name || o === group || o.userData.nxHighlight) return;
		const box = new Box3();
		o.traverse((c) => {
			const mesh = c as Mesh;
			if (!mesh.isMesh || c.userData.nxHighlight || !mesh.geometry) return;
			if (!mesh.geometry.boundingBox) mesh.geometry.computeBoundingBox();
			box.union(mesh.geometry.boundingBox!.clone().applyMatrix4(m.multiplyMatrices(inv, mesh.matrixWorld)));
		});
		if (!box.isEmpty() && !(o.name in out)) out[o.name] = { size: box.getSize(new Vector3()).toArray() as Vec3, center: box.getCenter(new Vector3()).toArray() as Vec3 };
	});
	return out;
}

/** The port a raycast hit belongs to, or null. */
export function portOf(o: Object3D | null | undefined, stopAt?: Object3D | null): string | null {
	for (let p = o; p && p !== stopAt; p = p.parent) if (typeof p.userData?.nxPort === 'string') return p.userData.nxPort as string;
	return null;
}

/** A sanitised port name from a part's: `Discharge` → `discharge`. */
export function portNameFrom(part: string | null | undefined, taken: Iterable<string>): string {
	const have = new Set(taken);
	let base = (part ?? 'port').replace(/[^A-Za-z0-9_-]+/g, '-').replace(/^[^A-Za-z_]+/, '').toLowerCase() || 'port';
	if (!have.has(base)) return base;
	for (let i = 2; ; i++) if (!have.has(`${base}${i}`)) return `${base}${i}`;
}

export type Hit = { at: Vec3; dir: Vec3 };
