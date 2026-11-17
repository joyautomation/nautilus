// Unit tests for the pipe router (src/lib/route.ts routePipes).
//
//	npm test
import { describe, expect, it } from './harness.js';
import { routePipes, ROUTE_CLEAR, type Pt, type Rect } from '../src/lib/route.js';
import { autoRoutes, expandUnits, pipeCrossings, pipeTees, routedPoints, type GetPort, type MimicDoc } from '../src/lib/mimic.js';

// every run square, and none through a box (grown by ROUTE_CLEAR)
const square = (pts: Pt[]) => pts.every((p, i) => i === 0 || p[0] === pts[i - 1][0] || p[1] === pts[i - 1][1]);
const through = (pts: Pt[], r: Rect, skipEnds = 0) => {
	const x0 = r.x - ROUTE_CLEAR + 0.5, x1 = r.x + r.w + ROUTE_CLEAR - 0.5, y0 = r.y - ROUTE_CLEAR + 0.5, y1 = r.y + r.h + ROUTE_CLEAR - 0.5;
	for (let i = 1 + skipEnds; i < pts.length - skipEnds; i++) {
		const [a, b] = [pts[i - 1], pts[i]];
		for (let k = 0; k <= 20; k++) {
			const x = a[0] + ((b[0] - a[0]) * k) / 20, y = a[1] + ((b[1] - a[1]) * k) / 20;
			if (x > x0 && x < x1 && y > y0 && y < y1) return true;
		}
	}
	return false;
};

describe('routePipes', () => {
	it('goes round equipment between two ports, square', () => {
		const tank: Rect = { x: 300, y: 100, w: 100, h: 200 };
		const r = routePipes({
			width: 800, height: 500, obstacles: [{ x: 50, y: 180, w: 40, h: 40 }, tank, { x: 600, y: 180, w: 40, h: 40 }], fixed: [],
			jobs: [{ id: 'p', from: { x: 90, y: 200, dir: 'right', own: 0 }, to: { x: 600, y: 200, dir: 'left', own: 2 } }]
		});
		const pts = r.paths.get('p')!;
		expect(r.failed.length).toBe(0);
		expect(square(pts)).toBe(true);
		expect(through(pts, tank)).toBe(false);
		expect(pts[0]).toEqual([90, 200]);
		expect(pts[pts.length - 1]).toEqual([600, 200]);
	});
	it('drops straight down into the header it tees into', () => {
		const r = routePipes({
			width: 800, height: 500, obstacles: [{ x: 200, y: 100, w: 30, h: 40 }],
			fixed: [{ id: 'HDR', pts: [[50, 300], [700, 300]] }],
			jobs: [{ id: 'drop', from: { x: 215, y: 140, dir: 'down', own: 0 }, to: { pipe: 'HDR' } }]
		});
		expect(r.paths.get('drop')).toEqual([[215, 140], [215, 300]]);
	});
	it('a routed header can be teed into by a pipe routed after it, whatever their order', () => {
		const r = routePipes({
			width: 800, height: 500, obstacles: [{ x: 20, y: 280, w: 20, h: 40 }, { x: 740, y: 280, w: 20, h: 40 }, { x: 300, y: 100, w: 30, h: 40 }], fixed: [],
			jobs: [
				{ id: 'drop', from: { x: 315, y: 140, dir: 'down', own: 2 }, to: { pipe: 'HDR' } },
				{ id: 'HDR', from: { x: 40, y: 300, dir: 'right', own: 0 }, to: { x: 740, y: 300, dir: 'left', own: 1 } }
			]
		});
		expect(r.failed.length).toBe(0);
		expect(r.paths.get('HDR')).toEqual([[40, 300], [740, 300]]);
		expect(r.paths.get('drop')).toEqual([[315, 140], [315, 300]]);
	});
	it('never runs along another pipe', () => {
		const r = routePipes({
			width: 600, height: 300, obstacles: [], fixed: [{ id: 'a', pts: [[50, 100], [550, 100]] }],
			jobs: [{ id: 'b', from: { x: 50, y: 100, dir: 'right' }, to: { x: 550, y: 100, dir: 'left' } }]
		});
		const pts = r.paths.get('b')!;
		// it leaves the line the other pipe holds, and comes back to its port
		expect(pts.some(([, y]) => Math.abs(y - 100) >= 15)).toBe(true);
	});
	it('keeps off a label', () => {
		const label: Rect = { x: 280, y: 190, w: 60, h: 20 };
		const r = routePipes({
			width: 600, height: 400, obstacles: [label], fixed: [],
			jobs: [{ id: 'p', from: { x: 50, y: 200, dir: 'right' }, to: { x: 550, y: 200, dir: 'left' } }]
		});
		expect(through(r.paths.get('p')!, label)).toBe(false);
	});
	it('crosses a line it cannot avoid once, straight across', () => {
		const r = routePipes({
			width: 600, height: 400, obstacles: [], fixed: [{ id: 'long', pts: [[300, 20], [300, 380]] }],
			jobs: [{ id: 'p', from: { x: 50, y: 200, dir: 'right' }, to: { x: 550, y: 200, dir: 'left' } }]
		});
		expect(r.paths.get('p')).toEqual([[50, 200], [550, 200]]);
	});
	it('routes a filter gallery (4 cells, 6 drops each, 5 headers) in under 3 s', () => {
		const obstacles: Rect[] = [];
		const jobs: Parameters<typeof routePipes>[0]['jobs'] = [];
		for (let f = 0; f < 4; f++) {
			const x = 200 + f * 420;
			obstacles.push({ x, y: 300, w: 260, h: 260 });
			for (let v = 0; v < 6; v++) {
				const vx = x + 20 + v * 40, top = v < 3;
				obstacles.push({ x: vx, y: top ? 230 : 590, w: 28, h: 40 });
				jobs.push({ id: `F${f}V${v}`, from: { x: vx + 14, y: top ? 230 : 630, dir: top ? 'up' : 'down', own: obstacles.length - 1 }, to: { pipe: ['INF', 'BWS', 'AIR', 'EFF', 'WWD', 'FTW'][v] } });
			}
		}
		const fixed = ['INF', 'BWS', 'AIR'].map((id, k) => ({ id, pts: [[40, 80 + k * 40], [1880, 80 + k * 40]] as Pt[] }))
			.concat(['EFF', 'WWD', 'FTW'].map((id, k) => ({ id, pts: [[40, 720 + k * 40], [1880, 720 + k * 40]] as Pt[] })));
		const t = Date.now();
		const r = routePipes({ width: 1920, height: 1000, obstacles, fixed, jobs });
		const ms = Date.now() - t;
		console.log(`       ${jobs.length} pipes in ${ms} ms, ${r.failed.length} failed`);
		expect(r.failed.length).toBe(0);
		expect(ms < 3000).toBe(true);
	});
});

// A routed document draws through the same pass as a hand-drawn one: the
// paths autoRoutes() gives go to pipeTees() and pipeCrossings() with the rest.
describe('autoRoutes', () => {
	it('routes a unit\'s drops into a routed header: a tee dot at each, a crossing where the header passes a drawn pipe', () => {
		const doc = expandUnits({
			canvas: { width: 800, height: 500 },
			equipment: [
				{ id: 'IN', component: 'OffPage', x: 20, y: 280 },
				{ id: 'END', component: 'OffPage', x: 740, y: 280 }
			],
			pipes: [
				{ id: 'HDR', routing: 'auto', from: { equip: 'IN', port: 'right' }, to: { equip: 'END', port: 'left' } },
				{ id: 'X', points: [[200, 10], [200, 490]] }
			],
			units: [{ each: [1, 2], step: [100, 0], equipment: [{ id: 'V{n}', component: 'Valve', x: 300, y: 100 }], pipes: [{ id: 'D{n}', routing: 'auto', from: { equip: 'V{n}', port: 'bottom' }, to: { pipe: 'HDR' } }] }]
		} as MimicDoc);
		const box = (id: string) => {
			const e = doc.equipment!.find((q) => q.id === id)!;
			return e.component === 'Valve' ? { x: e.x, y: e.y, w: 30, h: 40 } : { x: e.x, y: e.y, w: 20, h: 40 };
		};
		const getPort: GetPort = (equip, port) => {
			if (!doc.equipment!.some((q) => q.id === equip)) return undefined;
			const b = box(equip);
			if (port === 'right') return { x: b.x + b.w, y: b.y + b.h / 2, dir: 'right' };
			if (port === 'left') return { x: b.x, y: b.y + b.h / 2, dir: 'left' };
			if (port === 'bottom') return { x: b.x + b.w / 2, y: b.y + b.h, dir: 'down' };
			return undefined;
		};
		const r = autoRoutes(doc, getPort, box);
		expect(r.failed.length).toBe(0);
		expect(r.paths.get('HDR')).toEqual([[40, 300], [740, 300]]);
		expect(r.paths.get('D1')).toEqual([[315, 140], [315, 300]]);
		expect(r.paths.get('D2')).toEqual([[415, 140], [415, 300]]);
		const paths = doc.pipes!.map((p) => (p.routing === 'auto' ? r.paths.get(p.id)! : routedPoints(p, getPort)));
		expect(pipeTees(paths)).toEqual([{ x: 315, y: 300 }, { x: 415, y: 300 }]);
		const xs = pipeCrossings(paths);
		expect(xs.length).toBe(1);
		expect([xs[0].x, xs[0].y]).toEqual([200, 300]);
	});
});
