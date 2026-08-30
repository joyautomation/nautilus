// Unit tests for the pipe-end attach pass (src/lib/mimic.ts attachPipeEnds).
//
//	npm test
//
// Same harness as the rest: ./harness.ts, a strict subset of vitest's API.
import { describe, expect, it } from './harness.js';
import {
	attachPipeEnds,
	connectPorts,
	routedPoints,
	makeGetPort,
	resolveRuntimePorts,
	PORT_STUB,
	type MimicDoc,
	type MimicEquipment
} from '../src/lib/mimic.js';

// A 100×100 pump at (100, 100) with a measured height, a discharge nozzle
// on its top face at x 0.3 exiting 'up' and a suction on its left face.
const pump: MimicEquipment = {
	id: 'P1',
	component: 'PidPumpImg',
	x: 100,
	y: 100,
	width: 100,
	props: { height: 50 },
	ports: [
		{ name: 'discharge', x: 0.3, y: 0, dir: 'up' },
		{ name: 'suction', x: 0, y: 0.5, dir: 'left' }
	]
};
// A built-in Tank (no instance ports) — BUILTIN_PORTS supplies the cardinals.
const tank: MimicEquipment = { id: 'T1', component: 'Tank', x: 400, y: 0, width: 80, props: { height: 60 } };

const docOf = (pipes: MimicDoc['pipes']): MimicDoc => ({
	canvas: { width: 600, height: 300 },
	equipment: [pump, tank],
	pipes
});

/** The runtime's own resolver over the test doc, so a written anchor can
 * be checked to render where the port is. */
const getPortFor = (doc: MimicDoc) =>
	makeGetPort(
		(id) => {
			const eq = doc.equipment!.find((e) => e.id === id);
			if (!eq) return undefined;
			return { x: eq.x, y: eq.y, w: eq.width!, h: (eq.props!.height as number) };
		},
		(id) => {
			const eq = doc.equipment!.find((e) => e.id === id);
			return eq && resolveRuntimePorts(eq);
		}
	);

describe('attachPipeEnds', () => {
	it('anchors an end within tolerance to the nearest port and drops the stored point', () => {
		// discharge is at (130, 100); the traced pipe ends at (133, 104).
		const doc = docOf([{ id: 'a', points: [[133, 104], [133, 20], [300, 20]], routing: 'orthogonal' }]);
		const { doc: out, report } = attachPipeEnds(doc, { tolerance: 10 });
		expect(report.attached).toBe(1);
		expect(out.pipes![0].from).toEqual({ equip: 'P1', port: 'discharge' });
		expect(out.pipes![0].to).toBe(undefined);
		// the stored end is gone; the next vertex was moved onto the port's x
		expect(out.pipes![0].points).toEqual([[130, 20], [300, 20]]);
		// and the run resolves straight up out of the nozzle
		const pts = routedPoints(out.pipes![0], getPortFor(out));
		expect(pts[0]).toEqual([130, 100]);
		expect(pts[1]).toEqual([130, 100 - PORT_STUB]);
		expect(pts[2]).toEqual([130, 20]);
	});

	it('leaves an end beyond tolerance free and reports its nearest candidate', () => {
		const doc = docOf([{ id: 'a', points: [[150, 130], [300, 130]] }]);
		const { doc: out, report } = attachPipeEnds(doc, { tolerance: 5 });
		expect(report.attached).toBe(0);
		expect(out.pipes![0].from).toBe(undefined);
		expect(out.pipes![0].points).toEqual([[150, 130], [300, 130]]);
		expect(report.free.length).toBe(2);
		expect(report.free[0].pipe).toBe('a');
		expect(report.free[0].end).toBe('from');
		expect(report.free[0].nearest?.equip).toBe('P1');
		expect(report.free[0].nearest?.port).toBe('discharge');
		expect(Math.round(report.free[0].nearest!.dist)).toBe(36);
	});

	it('attaches both ends of a two-point pipe, leaving no interior points', () => {
		// suction (100, 125) ← … tank left port (400, 30)
		const doc = docOf([{ id: 'a', points: [[97, 127], [402, 31]], routing: 'orthogonal' }]);
		const { doc: out, report } = attachPipeEnds(doc, { tolerance: 10 });
		expect(report.attached).toBe(2);
		expect(out.pipes![0].from).toEqual({ equip: 'P1', port: 'suction' });
		expect(out.pipes![0].to).toEqual({ equip: 'T1', port: 'left' });
		expect(out.pipes![0].points).toEqual([]);
		expect(report.free).toEqual([]);
	});

	it('does not move a vertex that is still a FREE end (it may be a shared tee)', () => {
		// discharge (130, 100); a two-point pipe up to a tee at (134, 40)
		const doc = docOf([{ id: 'a', points: [[131, 101], [134, 40]], routing: 'orthogonal' }]);
		const { doc: out } = attachPipeEnds(doc, { tolerance: 10 });
		expect(out.pipes![0].from).toEqual({ equip: 'P1', port: 'discharge' });
		expect(out.pipes![0].points).toEqual([[134, 40]]);
		// the router puts the jog at the free end, not at the nozzle
		const pts = routedPoints(out.pipes![0], getPortFor(out));
		expect(pts).toEqual([[130, 100], [130, 88], [130, 40], [134, 40]]);
	});

	it('drops the adjacent vertex when straightening lands it on the stub end', () => {
		// discharge (130, 100), stub end (130, 88); the trace put a corner at (132, 88)
		const doc = docOf([{ id: 'a', points: [[130, 100], [132, 88], [300, 88], [300, 200]] }]);
		const { doc: out } = attachPipeEnds(doc, { tolerance: 3 });
		expect(out.pipes![0].points).toEqual([[300, 88], [300, 200]]);
	});

	it('uses measure() for the box height and falls back to props.height then width', () => {
		// Without measure: T1 is 80 × 60 (props.height) → bottom port at (440, 60).
		let r = attachPipeEnds(docOf([{ id: 'a', points: [[440, 62], [440, 200]] }]), { tolerance: 5 });
		expect(r.doc.pipes![0].from).toEqual({ equip: 'T1', port: 'bottom' });
		// With measure saying 80 × 200: bottom port is at (440, 200) and the
		// other end of the same pipe is what attaches.
		r = attachPipeEnds(docOf([{ id: 'a', points: [[440, 62], [440, 200]] }]), {
			tolerance: 5,
			measure: (eq) => ({ width: eq.width!, height: eq.id === 'T1' ? 200 : 50 })
		});
		expect(r.doc.pipes![0].from).toBe(undefined);
		expect(r.doc.pipes![0].to).toEqual({ equip: 'T1', port: 'bottom' });
		// No props.height and no measure: a square of `width`.
		const sq: MimicDoc = {
			canvas: { width: 10, height: 10 },
			equipment: [{ id: 'S', component: 'Tank', x: 0, y: 0, width: 40 }],
			pipes: [{ id: 'a', points: [[20, 41], [20, 90]] }]
		};
		expect(attachPipeEnds(sq, { tolerance: 2 }).doc.pipes![0].from).toEqual({ equip: 'S', port: 'bottom' });
	});

	it('never anchors both ends of one pipe to the same port', () => {
		// a 6-px stub whose two ends both sit within 10 of the discharge (130, 100)
		const doc = docOf([{ id: 'a', points: [[128, 104], [134, 104]] }]);
		const { doc: out, report } = attachPipeEnds(doc, { tolerance: 10 });
		expect(report.attached).toBe(1);
		expect(out.pipes![0].from).toEqual({ equip: 'P1', port: 'discharge' });
		expect(out.pipes![0].to).toBe(undefined);
		expect(out.pipes![0].points).toEqual([[134, 104]]);
		expect(report.free.map((f) => f.end)).toEqual(['to']);
		expect(report.free[0].nearest?.port).toBe('discharge');
	});

	it('takes a per-end tolerance, so a caller can give shared junction vertices less reach', () => {
		// two pipes meet at (137, 108) — 10.6 from the discharge (130, 100)
		const doc = docOf([
			{ id: 'a', points: [[137, 108], [300, 108]] },
			{ id: 'b', points: [[137, 108], [137, 250]] },
			{ id: 'c', points: [[131, 100], [50, 100]] } // 1 px off the nozzle: a real hit
		]);
		const shared = new Set(['137,108']);
		const { doc: out, report } = attachPipeEnds(doc, {
			tolerance: (e) => (shared.has(`${e.x},${e.y}`) ? 5 : 15)
		});
		expect(out.pipes![0].from).toBe(undefined);
		expect(out.pipes![1].from).toBe(undefined);
		expect(out.pipes![2].from).toEqual({ equip: 'P1', port: 'discharge' });
		expect(report.attached).toBe(1);
	});

	it('is pure and leaves an already-anchored end alone', () => {
		const doc = docOf([{ id: 'a', from: { equip: 'P1', port: 'suction' }, points: [[97, 127], [300, 127]] }]);
		const before = JSON.stringify(doc);
		const { doc: out, report } = attachPipeEnds(doc, { tolerance: 10 });
		expect(JSON.stringify(doc)).toBe(before);
		// `from` is set, so [97,127] is an INTERIOR vertex — kept, not re-snapped
		expect(out.pipes![0].from).toEqual({ equip: 'P1', port: 'suction' });
		expect(out.pipes![0].points[0]).toEqual([97, 127]);
		expect(report.attached).toBe(0);
		expect(report.free.map((f) => f.end)).toEqual(['to']);
	});
});

describe('connectPorts', () => {
	// A vertical can pump, 40 × 80 at (100, 100), between two header rails:
	// discharge on its LEFT casing edge, suction at the bottom. The left rail
	// runs the full height at x = 80; the right rail only starts below the
	// pump, at x = 160 from y = 200 down.
	const can: MimicEquipment = {
		id: 'B1',
		component: 'PidPumpImg',
		x: 100,
		y: 100,
		width: 40,
		props: { height: 80 },
		ports: [
			{ name: 'discharge', x: 0, y: 0.6, dir: 'left' },
			{ name: 'suction', x: 0.5, y: 1, dir: 'down' },
			{ name: 'in', x: 0.5, y: 1, dir: 'down' } // alias of suction
		]
	};
	const rails: MimicDoc = {
		canvas: { width: 300, height: 300 },
		equipment: [can],
		pipes: [
			{ id: 'L', points: [[80, 0], [80, 300]] },
			{ id: 'R', points: [[160, 200], [160, 300]] }
		]
	};
	const getPortFor2 = (doc: MimicDoc) =>
		makeGetPort(
			(id) => {
				const eq = doc.equipment!.find((e) => e.id === id);
				return eq && { x: eq.x, y: eq.y, w: eq.width!, h: eq.props!.height as number };
			},
			(id) => {
				const eq = doc.equipment!.find((e) => e.id === id);
				return eq && resolveRuntimePorts(eq);
			}
		);

	it('runs the discharge straight to the rail its ray hits, and the suction to a DIFFERENT run with an elbow', () => {
		const { doc, report } = connectPorts(rails, { reach: 50 });
		expect(report.connected.length).toBe(2);
		expect(report.skipped).toEqual([]);
		const d = report.connected[0];
		expect([d.port, d.target, d.via, d.dist]).toEqual(['discharge', 'L', 'ray', 20]);
		const s = report.connected[1];
		expect([s.port, s.target, s.via]).toEqual(['suction', 'R', 'nearest']);
		expect(Math.round(s.dist)).toBe(45); // hypot(40, 20)
		// the alias got no pipe of its own
		expect(doc.pipes!.map((p) => p.id)).toEqual(['L', 'R', 'B1-discharge', 'B1-suction']);
		// discharge: nozzle (100,148) → stub → rail, one straight leg
		const dp = routedPoints(doc.pipes![2], getPortFor2(doc));
		expect(dp).toEqual([[100, 148], [88, 148], [80, 148]]);
		// suction: nozzle (120,180) → stub down → corner → (160, 200) on the right rail
		const sp = routedPoints(doc.pipes![3], getPortFor2(doc));
		expect(sp[0]).toEqual([120, 180]);
		expect(sp[1]).toEqual([120, 192]);
		expect(sp[sp.length - 1]).toEqual([160, 200]);
		expect(sp.length).toBe(4);
		// the connector's stored geometry is interior-free: anchor + one free vertex
		expect(doc.pipes![3].from).toEqual({ equip: 'B1', port: 'suction' });
		expect(doc.pipes![3].to).toBe(undefined);
		expect(doc.pipes![3].points).toEqual([[160, 200]]);
	});

	it('leaves a nozzle alone once any pipe anchors to it — under any alias', () => {
		const doc: MimicDoc = { ...rails, pipes: [...rails.pipes!, { id: 'x', from: { equip: 'B1', port: 'in' }, points: [[120, 260]] }] };
		const { report } = connectPorts(doc, { reach: 50 });
		expect(report.connected.map((c) => c.port)).toEqual(['discharge']);
		// …and that pipe is never a target for the equipment's other ports
		expect(report.connected[0].target).toBe('L');
	});

	it('settles for an already-used run only when nothing else is within reach', () => {
		const one: MimicDoc = { ...rails, pipes: [rails.pipes![0]] }; // left rail only
		const { report } = connectPorts(one, { reach: 60 });
		expect(report.connected.map((c) => [c.port, c.target])).toEqual([
			['discharge', 'L'],
			['suction', 'L']
		]);
	});

	it('skips a port whose stub would land in another box or off the canvas, and one with no run in reach', () => {
		const below: MimicEquipment = { id: 'B2', component: 'PidPumpImg', x: 100, y: 185, width: 40, props: { height: 80 }, ports: can.ports };
		// canvas ends at 270: B2's suction stub (y 277) would leave it
		const doc: MimicDoc = { canvas: { width: 300, height: 270 }, equipment: [can, below], pipes: [rails.pipes![0]] };
		const { report } = connectPorts(doc, { reach: 15 });
		expect(report.skipped.map((s) => [s.equip, s.port, s.reason])).toEqual([
			['B1', 'discharge', 'no run within reach'],
			['B1', 'suction', 'stub lands inside B2'],
			['B2', 'discharge', 'no run within reach'],
			['B2', 'suction', 'stub leaves the canvas']
		]);
		expect(report.skipped[0].nearest).toEqual({ pipe: 'L', dist: 20 });
	});

	it('honours the caller’s port list and order, and is pure', () => {
		const before = JSON.stringify(rails);
		const { doc, report } = connectPorts(rails, { reach: 50, ports: (eq) => (eq.id === 'B1' ? ['suction'] : []) });
		expect(JSON.stringify(rails)).toBe(before);
		expect(report.connected.map((c) => [c.port, c.target])).toEqual([['suction', 'L']]);
		expect(doc.pipes!.length).toBe(3);
	});
});
