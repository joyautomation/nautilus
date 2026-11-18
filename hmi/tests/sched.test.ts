// A task's placement verdict. A priority withdrawn mid-run (the runtime's
// priority guard) is not a refusal at start, and the operator must be able
// to tell them apart: one is a configuration the OS would not grant, the
// other a task that ran away.
import { describe, it, expect } from './harness.js';
import { schedPlacement, schedVerdict } from '../src/lib/sched.js';

describe('schedPlacement', () => {
	it('names what was asked for', () => {
		expect(schedPlacement({ cpus: [3], priority: 50, applied: true })).toBe('cpu 3 · fifo 50');
		expect(schedPlacement({ priority: 40, applied: true })).toBe('fifo 40');
		expect(schedPlacement({ applied: false })).toBe('');
	});
});

describe('schedVerdict', () => {
	it('granted says nothing', () => {
		expect(schedVerdict({ priority: 40, applied: true })).toBe('');
	});
	it('a refusal at start is REFUSED', () => {
		expect(schedVerdict({ priority: 40, applied: false, error: 'operation not permitted' })).toBe('REFUSED');
	});
	it('a priority withdrawn for overrunning is DEMOTED (overrun)', () => {
		expect(
			schedVerdict({ priority: 40, applied: false, demoted: true, error: '50 scans in a row overran the 1ms period (priority guard, overruns: 50)' })
		).toBe('DEMOTED (overrun)');
		expect(
			schedVerdict({ priority: 40, applied: false, demoted: true, error: 'one scan has run for more than 50ms, 50 × its 1ms period (priority guard, scan-multiple)' })
		).toBe('DEMOTED (overrun)');
	});
	it('a priority withdrawn by the kernel limit is DEMOTED (CPU limit)', () => {
		expect(
			schedVerdict({ priority: 40, applied: false, demoted: true, error: 'SIGXCPU: a CPU-time limit was passed (most likely RLIMIT_RTTIME …)' })
		).toBe('DEMOTED (CPU limit)');
	});
});
