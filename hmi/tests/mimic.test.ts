// Unit tests for the pipe-end attach pass (src/lib/mimic.ts attachPipeEnds).
//
//	npm test
//
// Same harness as the rest: ./harness.ts, a strict subset of vitest's API.
import { describe, expect, it } from './harness.js';
import {
	attachPipeEnds,
	brokenPipes,
	expandUnits,
	pipeCrossings,
	pipeTees,
	connectPorts,
	routedPoints,
	makeGetPort,
	resolveRuntimePorts,
	PORT_STUB,
	labelState,
	labelRefs,
	equipmentRefs,
	resolveBindings,
	unresolvedBindings,
	unresolvedMessage,
	type MimicDoc,
	type MimicEquipment
} from '../src/lib/mimic.js';
import unitsIn from '../../testdata/mimic-units/in.json';
import unitsOut from '../../testdata/mimic-units/out.json';

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
// A built-in Tank (no instance ports) — BUILTIN_PORTS supplies the cardinals,
// anchored to the vessel part (drawings.ts tankGeometry): at 80 × 60, left
// (402.77, 30), bottom (431.31, 57.69).
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
		// suction (100, 125) ← … tank left port (402.77, 30)
		const doc = docOf([{ id: 'a', points: [[97, 127], [404, 31]], routing: 'orthogonal' }]);
		const { doc: out, report } = attachPipeEnds(doc, { tolerance: 10 });
		expect(report.attached).toBe(2);
		expect(out.pipes![0].from).toEqual({ equip: 'P1', port: 'suction' });
		expect(out.pipes![0].to).toEqual({ equip: 'T1', port: 'left' });
		expect(out.pipes![0].points).toEqual([]);
		expect(report.free).toEqual([]);
	});

	it('reads a pipe with both ends anchored and no points list as no bends (bw01 haiku-1)', () => {
		// a document may leave points out, as a scene's pipe may; one such
		// pipe used to throw and blank the whole screen
		const doc = docOf([{ id: 'a', from: { equip: 'P1', port: 'suction' }, to: { equip: 'T1', port: 'left' }, routing: 'orthogonal' } as never]);
		const withEmpty = docOf([{ id: 'a', points: [], from: { equip: 'P1', port: 'suction' }, to: { equip: 'T1', port: 'left' }, routing: 'orthogonal' }]);
		expect(routedPoints(doc.pipes![0], getPortFor(doc))).toEqual(routedPoints(withEmpty.pipes![0], getPortFor(withEmpty)));
		expect(routedPoints(doc.pipes![0], getPortFor(doc)).length > 1).toBe(true);
		const { report } = attachPipeEnds(doc, { tolerance: 10 });
		expect(report.attached).toBe(0);
		// and a free pipe with no points draws nothing rather than throwing
		expect(routedPoints({ id: 'b' } as never)).toEqual([]);
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
		// Without measure: T1 is 80 × 60 (props.height) → bottom port at (431.31, 57.69).
		let r = attachPipeEnds(docOf([{ id: 'a', points: [[431, 58], [431, 196]] }]), { tolerance: 5 });
		expect(r.doc.pipes![0].from).toEqual({ equip: 'T1', port: 'bottom' });
		// With measure saying 80 × 200: bottom port is at (430.91, 196.36) and
		// the other end of the same pipe is what attaches.
		r = attachPipeEnds(docOf([{ id: 'a', points: [[431, 58], [431, 196]] }]), {
			tolerance: 5,
			measure: (eq) => ({ width: eq.width!, height: eq.id === 'T1' ? 200 : 50 })
		});
		expect(r.doc.pipes![0].from).toBe(undefined);
		expect(r.doc.pipes![0].to).toEqual({ equip: 'T1', port: 'bottom' });
		// No props.height and no measure: a square of `width`.
		const sq: MimicDoc = {
			canvas: { width: 10, height: 10 },
			equipment: [{ id: 'S', component: 'Tank', x: 0, y: 0, width: 40 }],
			pipes: [{ id: 'a', points: [[12, 38], [12, 90]] }]
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

describe('brokenPipes', () => {
	const getPort = makeGetPort(
		(id) => (id === 'P1' ? { x: pump.x, y: pump.y, w: 100, h: 50 } : undefined),
		(id) => (id === 'P1' ? resolveRuntimePorts(pump) : undefined)
	);
	it('names each end that is not there when the screen runs (bw01 fable-2)', () => {
		const got = brokenPipes(
			[
				{ id: 'ok', from: { equip: 'P1', port: 'discharge' }, points: [[130, 20]] },
				{ id: 'noport', from: { equip: 'P1', port: 'inf' }, points: [[10, 10]] },
				{ id: 'noequip', from: { equip: 'P1', port: 'suction' }, to: { equip: 'F1', port: 'a' }, points: [] }
			],
			getPort
		);
		expect(got).toEqual([
			{ id: 'noport', ends: ['P1.inf'] },
			{ id: 'noequip', ends: ['F1.a'] }
		]);
	});
	it('a free pipe whose points are all one spot draws nothing', () => {
		expect(brokenPipes([{ id: 'dot', points: [[5, 5], [5, 5]] }, { id: 'run', points: [[0, 0], [9, 0]] }], getPort)).toEqual([
			{ id: 'dot', ends: [] }
		]);
	});
});

describe('expandUnits', () => {
	it('draws a unit once per value, as naut does (testdata/mimic-units, shared with Go)', () => {
		expect(expandUnits(unitsIn as unknown as MimicDoc)).toEqual(unitsOut);
	});
	it('a document with no units comes back as it is', () => {
		const d: MimicDoc = { canvas: { width: 1, height: 1 }, equipment: [] };
		expect(expandUnits(d) === d).toBe(true);
	});
	it('marks each copy for the editor', () => {
		const d = expandUnits(unitsIn as unknown as MimicDoc, { mark: true });
		expect((d.equipment ?? []).map((e) => e.fromUnit ?? '')).toEqual(['', 'filter#1', 'filter#1', 'filter#2', 'filter#2', 'unit2#A', 'unit2#B']);
	});
});

describe('pipeCrossings', () => {
	// A header along y=100 and three drops: one crossing it to a header
	// below, one ending on it (a tee), one turning a corner on it.
	const header: [number, number][] = [[0, 100], [400, 100]];
	const lower: [number, number][] = [[0, 200], [400, 200]];
	const across: [number, number][] = [[100, 50], [100, 200]];
	const tee: [number, number][] = [[200, 50], [200, 100]];
	const corner: [number, number][] = [[300, 50], [300, 100], [380, 100]];
	it('finds where a drop crosses a header it does not join, header over', () => {
		expect(pipeCrossings([header, lower, across, tee, corner])).toEqual([{ x: 100, y: 100, over: 0, under: 2 }]);
	});
	it('leaves a tee, a corner and a run drawn on another alone', () => {
		expect(pipeCrossings([header, tee, corner, [[50, 100], [150, 100]]])).toEqual([]);
	});
	it('gives a tie in slant to the later pipe', () => {
		const a: [number, number][] = [[0, 0], [100, 100]];
		const b: [number, number][] = [[0, 100], [100, 0]];
		expect(pipeCrossings([a, b])).toEqual([{ x: 50, y: 50, over: 1, under: 0 }]);
	});
	it('knows a crossing of a header near (but clear of) an end', () => {
		expect(pipeCrossings([header, [[13, 50], [13, 150]]]).length).toBe(1);
		expect(pipeCrossings([header, [[11, 50], [11, 150]]]).length).toBe(0);
	});
});

describe('pipeTees', () => {
	const header: [number, number][] = [[0, 100], [400, 100]];
	it('dots a drop ending on a header, on the header', () => {
		expect(pipeTees([header, [[200, 50], [200, 103]]])).toEqual([{ x: 200, y: 100 }]);
	});
	it('dots a drop that turns onto a header and runs along it', () => {
		expect(pipeTees([header, [[300, 50], [300, 100], [380, 100]]]).some((t) => t.x === 300 && t.y === 100)).toBe(true);
	});
	it('leaves a crossing, an elbow and a run in two pieces alone', () => {
		expect(pipeTees([header, [[100, 50], [100, 200]]])).toEqual([]);
		expect(pipeTees([[[0, 0], [100, 0]], [[100, 0], [100, 100]]])).toEqual([]);
		expect(pipeTees([[[0, 0], [100, 0]], [[100, 0], [200, 0]]])).toEqual([]);
	});
	it('dots three pipes ending at one point', () => {
		expect(pipeTees([[[0, 0], [100, 0]], [[100, 0], [200, 0]], [[100, 0], [100, 80]]])).toEqual([{ x: 100, y: 0 }]);
	});
	it('leaves a drop that stops short of a header alone', () => {
		expect(pipeTees([header, [[200, 50], [200, 90]]])).toEqual([]);
	});
	it('does not dot a drop ending at a header\'s own end', () => {
		expect(pipeTees([header, [[400, 50], [400, 100]]])).toEqual([]);
	});
});

describe('labelState', () => {
	const l = {
		text: 'P-101',
		x: 0,
		y: 0,
		state: [
			{ when: 'Fault', text: 'Fault', kind: 'critical' as const },
			{ when: 'Run', text: 'Running', kind: 'good' as const },
			{ text: 'Stopped' }
		]
	};
	it('shows the first state whose tag is true', () => {
		expect(labelState(l, { Fault: true, Run: true })?.text).toBe('Fault');
		expect(labelState(l, { Fault: false, Run: true })?.text).toBe('Running');
	});
	it('falls back to the state with no tag', () => {
		expect(labelState(l, { Fault: false, Run: false })?.text).toBe('Stopped');
		expect(labelState(l, {})?.text).toBe('Stopped');
	});
	it('is null with no states', () => {
		expect(labelState({ text: 'x', x: 0, y: 0 }, {})).toBe(null);
	});
});

describe('resolveBindings on a struct member', () => {
	const tags = { CONV: { Run: true, Speed: 42, Inner: { Deep: 7 } }, 'Flat.Name': 3, Plain: 1 };
	it('walks a dotted path through the frame\'s nested struct', () => {
		expect(resolveBindings({ on: 'CONV.Run', hz: 'CONV.Speed', d: 'CONV.Inner.Deep' }, tags)).toEqual({ on: true, hz: 42, d: 7 });
	});
	it('still takes a flat key, even one with a dot, and negates with !', () => {
		expect(resolveBindings({ a: 'Plain', b: 'Flat.Name', c: '!CONV.Run' }, tags)).toEqual({ a: 1, b: 3, c: false });
	});
	it('leaves a missing member out rather than inventing one', () => {
		expect(resolveBindings({ x: 'CONV.Nope', y: 'NOPE.Run' }, tags)).toEqual({});
	});
});

describe('unresolvedBindings', () => {
	const tags = { CONV: { Run: true } };
	const doc = {
		canvas: { width: 10, height: 10 },
		equipment: [{ id: 'a', component: 'Pump', x: 0, y: 0, bind: { running: 'CONV.Run', speed: 'CONV.Speed' } }],
		pipes: [{ id: 'p', points: [], bind: { flowing: '!GONE' } }],
		labels: [{ text: 't', x: 0, y: 0, bind: 'CONV.Speed' }]
	} as unknown as MimicDoc;
	it('lists each reference that matches no tag, once, without the ! ', () => {
		expect(unresolvedBindings(doc, tags)).toEqual(['CONV.Speed', 'GONE']);
	});
	it('is empty before any frame has arrived', () => {
		expect(unresolvedBindings(doc, {})).toEqual([]);
	});
});

describe('unresolvedMessage', () => {
	it('does not call an absent tag a wrong name: a subscription or a read scope can leave it out', () => {
		const m = unresolvedMessage(['A.B']);
		expect(m.includes('not in the live frame')).toBe(true);
		expect(m.includes('not subscribed or readable')).toBe(true);
		expect(m.startsWith('1 binding ')).toBe(true);
		expect(unresolvedMessage(['A', 'B']).startsWith('2 bindings')).toBe(true);
	});
});

describe('labelRefs / equipmentRefs — what a chip reads (#1056)', () => {
	it('a label reads its bound tag and the when of each state', () => {
		expect(labelRefs({ x: 0, y: 0, bind: 'Level' })).toEqual(['Level']);
		expect(
			labelRefs({ x: 0, y: 0, state: [{ when: 'P_Fault', text: 'Fault' }, { text: 'OK' }] })
		).toEqual(['P_Fault']);
		expect(labelRefs({ x: 0, y: 0, text: 'plain' })).toEqual([]);
		// a bind wins over state (the label draws the value), so only it counts
		expect(labelRefs({ x: 0, y: 0, bind: 'Level', state: [{ when: 'P_Fault', text: 'F' }] })).toEqual(['Level']);
	});
	it('equipment reads what its bind names, a negated one included', () => {
		expect(
			equipmentRefs({ id: 'P1', component: 'Pump', x: 0, y: 0, bind: { running: 'P1_Run', fault: '!P1_Ok' } })
		).toEqual(['P1_Run', '!P1_Ok']);
		expect(equipmentRefs({ id: 'T', component: 'Tank', x: 0, y: 0 })).toEqual([]);
	});
});
