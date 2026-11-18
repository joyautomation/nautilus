// A task's thread placement as the diagnostics show it. Mirrors
// runtime.SchedStats; server/index.html (the built-in dashboard) carries
// the same rules in plain JS.
import type { SchedStats } from './types.js';

/** "cpu 3 · fifo 50", or "" when nothing was asked for. */
export function schedPlacement(s: SchedStats | undefined): string {
	if (!s || (!s.cpus?.length && !s.priority)) return '';
	return [s.cpus?.length ? 'cpu ' + s.cpus.join(',') : '', s.priority ? 'fifo ' + s.priority : '']
		.filter(Boolean)
		.join(' · ');
}

/**
 * What became of the request: "" when granted (or nothing asked), REFUSED
 * when the OS said no at start, DEMOTED when it was granted and later
 * withdrawn by the priority guard or the kernel's CPU limit, with the cause.
 */
export function schedVerdict(s: SchedStats | undefined): string {
	if (!s) return '';
	if (s.demoted) {
		const e = s.error ?? '';
		const cause = /overran|has run for more than/.test(e)
			? 'overrun'
			: /SIGXCPU|RLIMIT_RTTIME/.test(e)
				? 'CPU limit'
				: '';
		return cause ? `DEMOTED (${cause})` : 'DEMOTED';
	}
	return s.error ? 'REFUSED' : '';
}
