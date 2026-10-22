// Ports on a kind and pipe ends anchored to them (docs/design/spatial-hmi.md
// §3g, slice 3): validation, the resolver through a node's pos / rot /
// scale, and the orthogonal route between two ports.
import { describe, expect, it } from './harness.js';
import { Euler, Vector3 } from 'three';
import { validateScene, validateKind, resolvePipePoints, portInFrame, rotateXYZ, docPorts, resolvePoint, resolvePort, anchorPoint, anchorNormal, isAnchorWord, type SceneDoc, type Vec3 } from '../src/lib/scene.js';
import { routePorts } from '../src/lib/route.js';

const KINDS = ['tank', 'pump', 'valve', 'subpump'];
const pumpPorts = { suction: { at: [0, 0.1, 0.2] as Vec3, dir: [0, 0, 1] as Vec3 }, discharge: { at: [0, 0.43, -0.5] as Vec3 } };
const doc = (): SceneDoc => ({
	kinds: { subpump: { model: 'models/subpump.glb', ports: pumpPorts }, tank: { ports: { inlet: { at: [0, 0.5, 0], dir: [0, 1, 0] } } } },
	nodes: [
		{ id: 'P101', kind: 'subpump', pos: [1, 0, 2], rot: [0, 90, 0] },
		{ id: 'T101', kind: 'tank', pos: [3, 0, 0], scale: 2 }
	],
	pipes: [{ from: { node: 'P101', port: 'discharge' }, to: { node: 'T101', port: 'inlet' } }]
});

describe('ports and anchors validate', () => {
	it('accepts ports on a kind and a pipe anchored at both ends with no points', () => {
		expect(validateScene(doc(), KINDS)).toEqual({ ok: true, errors: [] });
	});
	it('a port needs an [x, y, z] at, a non-zero dir, and a plain name', () => {
		const r = validateScene({ ...doc(), pipes: [], kinds: { subpump: { model: 'models/x.glb', ports: { 'a/b': { at: [0, 0, 0] }, ok: { at: [1], dir: [0, 0, 0] } } } } }, KINDS);
		expect(r.errors.map((e) => e.path)).toEqual(['/kinds/subpump/ports/a/b', '/kinds/subpump/ports/ok/at', '/kinds/subpump/ports/ok/dir']);
	});
	it('an anchor must name a node in the frame and a port its kind has', () => {
		const d = doc();
		d.pipes = [{ from: { node: 'P999', port: 'discharge' }, to: { node: 'P101', port: 'vent' } }, { from: { node: 'P101' } as never, points: [[0, 0, 0]] }];
		const r = validateScene(d, KINDS);
		expect(r.errors).toEqual([
			{ path: '/pipes/0/from/node', message: 'no node "P999" in this frame' },
			{ path: '/pipes/0/to/port', message: 'kind "subpump" has no port "vent" (it has suction, discharge)' },
			{ path: '/pipes/1/from/port', message: "must name one of the node's kind's ports" }
		]);
	});
	it('a port on a kind the app registers is taken on trust; the run still needs two points counting anchors', () => {
		const d = doc();
		d.pipes = [{ from: { node: 'T101', port: 'drain' }, points: [] }];
		d.kinds = { subpump: d.kinds!.subpump };
		const r = validateScene(d, KINDS);
		expect(r.errors).toEqual([{ path: '/pipes/0/points', message: 'needs at least two points counting the anchored ends' }]);
	});
	it('a kind file may carry ports', () => {
		expect(validateKind({ model: 'models/p.glb', ports: pumpPorts }).ok).toBe(true);
		expect(validateKind({ model: 'models/p.glb', ports: { x: { at: [0, 0, 0], dir: 'up' } } }).errors[0].path).toBe('/ports/x/dir');
	});
});

describe('the resolver', () => {
	it('rotateXYZ matches three\'s XYZ Euler', () => {
		for (const rot of [[0, 90, 0], [30, 0, 0], [10, 20, 30], [-45, 120, 7]] as Vec3[]) {
			const v = new Vector3(0.3, -0.2, 0.7).applyEuler(new Euler(...(rot.map((d) => (d * Math.PI) / 180) as Vec3), 'XYZ'));
			const r = rotateXYZ([0.3, -0.2, 0.7], rot);
			expect(r.map((c) => Math.round(c * 1e6) / 1e6)).toEqual([v.x, v.y, v.z].map((c) => Math.round(c * 1e6) / 1e6));
		}
	});
	it('a port goes through pos, rot and scale; its direction through rot alone, unit', () => {
		const p = portInFrame({ pos: [1, 0, 2], rot: [0, 90, 0], scale: 2 }, { at: [0, 0.43, -0.5], dir: [0, 0, 3] })!;
		expect(p.at.map((c) => Math.round(c * 1000) / 1000)).toEqual([0, 0.86, 2]);
		expect(p.dir.map((c) => Math.round(c * 1000) / 1000)).toEqual([1, 0, 0]);
		expect(portInFrame({ pos: [0, 0, 0] }, { at: [0, 1, 0] })!.dir).toEqual([0, 1, 0]);
	});
	it('resolves a pipe port to port, keeps the points between, and refuses a bad anchor', () => {
		const d = doc();
		const portsOf = (k: string) => docPorts(d, k);
		expect(resolvePipePoints(d.pipes![0], d.nodes, portsOf)!.map((p) => p.map((c) => Math.round(c * 1000) / 1000))).toEqual([[0.5, 0.43, 2], [3, 1, 0]]);
		expect(resolvePipePoints({ from: { node: 'P101', port: 'suction' }, points: [[0, 1, 0]] }, d.nodes, portsOf)!.length).toBe(2);
		expect(resolvePipePoints({ from: { node: 'P101', port: 'nope' }, to: { node: 'T101', port: 'inlet' } }, d.nodes, portsOf)).toBe(null);
		expect(resolvePipePoints({ points: [[0, 0, 0], [1, 0, 0]] }, d.nodes, portsOf)).toEqual([[0, 0, 0], [1, 0, 0]]);
	});
});

describe('routePorts', () => {
	it('two ports facing up: straight up to a header above the higher lead, across, and down — no bend on a straight leg', () => {
		const pts = routePorts({ at: [0, 0.4, 0], dir: [0, 1, 0] }, { at: [2, 0.6, 1], dir: [0, 1, 0] });
		expect(pts).toEqual([[0, 0.85, 0], [2, 0.85, 0], [2, 0.85, 1]]);
	});
	it('two facing ports on one line: a straight pipe, no interior points', () => {
		expect(routePorts({ at: [0, 0.3, 0], dir: [1, 0, 0] }, { at: [2, 0.3, 0], dir: [-1, 0, 0] })).toEqual([]);
	});
	it('a horizontal port keeps its lead, then turns up', () => {
		const pts = routePorts({ at: [0, 0.2, 0], dir: [0, 0, 1] }, { at: [1, 1, 0], dir: [0, 1, 0] });
		expect(pts).toEqual([[0, 0.2, 0.1], [0, 1.25, 0.1], [1, 1.25, 0.1], [1, 1.25, 0]]);
	});
});

describe('box points: a port or a label at top / left / front of a part', () => {
	const box = { size: [0.4, 1, 0.2] as Vec3, center: [0, 0.5, 0] as Vec3 };
	it('anchor words: one face, two faces on different axes, center', () => {
		expect(['top', 'top-front', 'front-top', 'left-bottom', 'center'].every(isAnchorWord)).toBe(true);
		expect(['top-bottom', 'center-top', 'up', '', 'top-front-left'].some(isAnchorWord)).toBe(false);
	});
	it('a point on the box and the way out of it', () => {
		expect(anchorPoint(box, 'top')).toEqual([0, 1, 0]);
		expect(anchorPoint(box, 'left')).toEqual([-0.2, 0.5, 0]);
		expect(anchorPoint(box, 'top-front')).toEqual([0, 1, 0.1]);
		expect(anchorPoint(box, 'center')).toEqual([0, 0.5, 0]);
		expect(anchorNormal('bottom')).toEqual([0, -1, 0]);
		expect(anchorNormal('top-front').map((c) => Math.round(c * 1000) / 1000)).toEqual([0, 0.707, 0.707]);
		expect(anchorNormal('center')).toEqual([0, 1, 0]);
	});
	it('resolves against the bounds, a part, with an offset; null until the box is known', () => {
		const boxes = { bounds: box, parts: { Flange: { size: [0.1, 0.1, 0.1] as Vec3, center: [0.3, 0.9, 0] as Vec3 } } };
		expect(resolvePoint('top', boxes)).toEqual([0, 1, 0]);
		expect(resolvePoint({ anchor: 'right', offset: [0.05, 0, 0] }, boxes)).toEqual([0.25, 0.5, 0]);
		expect(resolvePoint({ part: 'Flange', anchor: 'top' }, boxes)!.map((c) => Math.round(c * 1000) / 1000)).toEqual([0.3, 0.95, 0]);
		expect(resolvePoint({ part: 'Missing', anchor: 'top' }, boxes)).toBe(null);
		expect(resolvePoint({ part: 'Flange', anchor: 'top' }, { bounds: box })).toBe(null);
		expect(resolvePoint([1, 2, 3], undefined)).toEqual([1, 2, 3]);
		const r = resolvePort({ at: { part: 'Flange', anchor: 'top' } }, boxes)!;
		expect({ at: r.at.map((c) => Math.round(c * 1000) / 1000), dir: r.dir }).toEqual({ at: [0.3, 0.95, 0], dir: [0, 1, 0] });
		expect(resolvePort({ at: { anchor: 'left' } }, boxes)!.dir).toEqual([-1, 0, 0]);
		expect(resolvePort({ at: { anchor: 'left' }, dir: [0, 0, 2] }, boxes)!.dir).toEqual([0, 0, 1]);
	});
	it('validates the forms on a port and on labelAt', () => {
		const ok = validateKind({ model: 'models/p.glb', labelAt: 'top', ports: { a: { at: { part: 'Flange', anchor: 'top-front', offset: [0, 0.01, 0] } }, b: { at: 'nope' as never } } });
		expect(ok.errors.map((e) => e.path)).toEqual(['/ports/b/at']);
		expect(validateKind({ model: 'models/p.glb', labelAt: { anchor: 'top-bottom' } }).errors.map((e) => e.path)).toEqual(['/labelAt']);
		expect(validateKind({ model: 'models/p.glb', labelAt: { part: 'Motor', anchor: 'top' } }).ok).toBe(true);
	});
	it('a pipe anchored at a box-point port resolves once the node\'s boxes are known', () => {
		const d: SceneDoc = {
			kinds: { pump: { ports: { discharge: { at: { part: 'Flange', anchor: 'top' } } } } },
			nodes: [{ id: 'P1', kind: 'pump', pos: [1, 0, 0] }],
			pipes: [{ from: { node: 'P1', port: 'discharge' }, points: [[1, 2, 0]] }]
		};
		expect(validateScene(d, ['pump']).ok).toBe(true);
		const portsOf = (k: string) => docPorts(d, k);
		expect(resolvePipePoints(d.pipes![0], d.nodes, portsOf)).toBe(null);
		const boxes = { parts: { Flange: { size: [0.1, 0.1, 0.1] as Vec3, center: [0, 0.4, 0] as Vec3 } } };
		expect(resolvePipePoints(d.pipes![0], d.nodes, portsOf, () => boxes)!.map((p) => p.map((c) => Math.round(c * 1000) / 1000))).toEqual([[1, 0.45, 0], [1, 2, 0]]);
	});
});
