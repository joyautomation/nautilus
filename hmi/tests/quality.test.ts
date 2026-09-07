// Quality → what the operator is shown. The precedence order is the whole
// spec: get it wrong and a screen shows a confident number for a dead point,
// or cries "stale" about a value the simulator is refreshing every scan.
import { describe, it, expect } from './harness.js';
import {
	STATUS_META,
	formatAge,
	formatValue,
	isWritable,
	valueStatus
} from '../src/lib/quality.js';

describe('valueStatus — precedence', () => {
	it('good is the default: nothing said otherwise', () => {
		expect(valueStatus({})).toBe('good');
		expect(valueStatus({ present: true, quality: 'good', simulated: false })).toBe('good');
	});

	it('an unpublished point outranks everything', () => {
		expect(valueStatus({ present: false })).toBe('notPublished');
		expect(valueStatus({ present: false, quality: 'good' })).toBe('notPublished');
		expect(valueStatus({ present: false, quality: 'bad', simulated: true })).toBe('notPublished');
	});

	it('bad and notConnected both read as bad — same response from the operator', () => {
		expect(valueStatus({ quality: 'bad' })).toBe('bad');
		expect(valueStatus({ quality: 'notConnected' })).toBe('bad');
	});

	it('bad outranks simulated: a simulated value flagged bad is bad first', () => {
		expect(valueStatus({ quality: 'bad', simulated: true })).toBe('bad');
	});

	it('simulated outranks stale — the simulator is refreshing it', () => {
		expect(valueStatus({ quality: 'stale', simulated: true })).toBe('simulated');
	});

	it('stale survives on its own', () => {
		expect(valueStatus({ quality: 'stale' })).toBe('stale');
		expect(valueStatus({ quality: 'stale', simulated: false })).toBe('stale');
	});
});

describe('valueStatus — the write gate', () => {
	it('good and simulated may be commanded; nothing else may', () => {
		expect(isWritable({})).toBe(true);
		// Commands stay ENABLED while simulated — the footer says so instead.
		expect(isWritable({ simulated: true })).toBe(true);
		expect(isWritable({ quality: 'stale' })).toBe(false);
		expect(isWritable({ quality: 'bad' })).toBe(false);
		expect(isWritable({ quality: 'notConnected' })).toBe(false);
		expect(isWritable({ present: false })).toBe(false);
	});

	it('only good is undegraded', () => {
		expect(STATUS_META.good.degraded).toBe(false);
		expect(STATUS_META.stale.degraded).toBe(true);
		expect(STATUS_META.bad.degraded).toBe(true);
		expect(STATUS_META.notPublished.degraded).toBe(true);
		expect(STATUS_META.simulated.degraded).toBe(true);
	});

	it('every status carries a word, so colour is never the only cue', () => {
		for (const k of Object.keys(STATUS_META)) {
			const m = STATUS_META[k as keyof typeof STATUS_META];
			expect(m.label.length > 0).toBe(true);
			expect(m.token.startsWith('var(--')).toBe(true);
		}
	});
});

describe('formatValue — never a confident zero', () => {
	it('absent values render the placeholder, not 0 and not NaN', () => {
		expect(formatValue(undefined)).toEqual({ text: '—', present: false, numeric: false });
		expect(formatValue(null)).toEqual({ text: '—', present: false, numeric: false });
		expect(formatValue(NaN)).toEqual({ text: '—', present: false, numeric: false });
		expect(formatValue(Infinity)).toEqual({ text: '—', present: false, numeric: false });
		expect(formatValue(-Infinity).present).toBe(false);
	});

	it('a real zero is a real zero', () => {
		expect(formatValue(0)).toEqual({ text: '0.0', present: true, numeric: true });
	});

	it('honours precision', () => {
		expect(formatValue(12.3456, { precision: 2 }).text).toBe('12.35');
		expect(formatValue(12.3456, { precision: 0 }).text).toBe('12');
	});

	it('booleans render as words', () => {
		expect(formatValue(true).text).toBe('On');
		expect(formatValue(false).text).toBe('Off');
		expect(formatValue(false, { falseText: 'Closed' }).text).toBe('Closed');
		expect(formatValue(true).numeric).toBe(false);
	});

	it('strings pass through; an empty one is absent', () => {
		expect(formatValue('TRANSITION')).toEqual({ text: 'TRANSITION', present: true, numeric: false });
		expect(formatValue('').present).toBe(false);
	});

	it('the placeholder is overridable', () => {
		expect(formatValue(undefined, { placeholder: 'no data' }).text).toBe('no data');
	});
});

describe('formatAge', () => {
	it('steps s → m → h → d', () => {
		expect(formatAge(0)).toBe('0s');
		expect(formatAge(45_000)).toBe('45s');
		expect(formatAge(90_000)).toBe('1m');
		expect(formatAge(3_600_000)).toBe('1h');
		expect(formatAge(90_000_000)).toBe('1d');
	});

	it('returns nothing rather than NaN for an unknown age', () => {
		expect(formatAge(undefined)).toBe('');
		expect(formatAge(NaN)).toBe('');
		expect(formatAge(-1)).toBe('');
	});
});
