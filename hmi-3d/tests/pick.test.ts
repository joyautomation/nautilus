// Unit tests for the pick resolver: which node a click lands on when
// several are stacked under the pointer.
//
//	npm test
import { describe, expect, it } from './harness.js';
import { resolvePick } from '../src/lib/pick.js';

const hit = (node: string | undefined, opaque = true) => ({ node, opaque });

describe('resolvePick', () => {
	it('picks the frontmost opaque node over the glass in front of it', () => {
		expect(resolvePick([hit('tank', false), hit('pump'), hit('tank', false)], null)).toBe('pump');
		expect(resolvePick([hit('tank', false), hit('pump')], 'valve')).toBe('pump');
	});
	it('all glass: the frontmost node', () => {
		expect(resolvePick([hit('tank', false), hit('box', false)], null)).toBe('tank');
	});
	it('skips hits that belong to no node (a floor, a gizmo)', () => {
		expect(resolvePick([hit(undefined), hit('pump')], null)).toBe('pump');
		expect(resolvePick([hit(undefined)], null)).toBe(null);
		expect(resolvePick([], 'pump')).toBe(null);
	});
	it('goes one deeper from the selected node, front to back, and round again', () => {
		// Front to back, distinct: tank, pump, floorbox.
		const stack = [hit('tank', false), hit('pump'), hit('tank', false), hit('floorbox')];
		expect(resolvePick(stack, 'pump')).toBe('floorbox');
		expect(resolvePick(stack, 'floorbox')).toBe('tank');
		expect(resolvePick(stack, 'tank')).toBe('pump');
		expect(resolvePick([hit('tank', false)], 'tank')).toBe('tank');
	});
});
