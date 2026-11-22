// Equipment out of service, as the HMI kit shows and asks for it (#715 PR 5,
// #616): the badge on a suppressed alarm, the form that takes a piece of
// equipment out of service, and the list of windows soonest-ending first.
//
// Plain TS (no runes, no DOM) so the rules are tested on node. The controller
// decides every request; this only asks the same questions first so the
// operator is not sent round a refusal.

import { kitFetch, type Fetch } from './kitfetch.js';
import { refusalMessage } from './auth.js';

/** The longest a window may be: the controller refuses more than a week. */
export const OOS_MAX_SECONDS = 7 * 24 * 3600;
/** The controller's cap on a reason, in characters. */
export const OOS_REASON_MAX = 200;

/** Durations the form offers (seconds), all within the cap. */
export const OOS_DURATIONS_S: { seconds: number; label: string }[] = [
	{ seconds: 3600, label: '1 hour' },
	{ seconds: 4 * 3600, label: '4 hours' },
	{ seconds: 8 * 3600, label: '8 hours' },
	{ seconds: 24 * 3600, label: '1 day' },
	{ seconds: 3 * 24 * 3600, label: '3 days' },
	{ seconds: OOS_MAX_SECONDS, label: '7 days' }
];

/** One window (`alarm.OutOfService` in Go: GET /api/alarms/out-of-service). */
export interface OutOfService {
	equipment: string;
	by: string;
	reason: string;
	startMs: number;
	untilMs: number;
	/** The alarm ids it covers. */
	alarms: string[];
}

/** The fields of an alarm instance the badge reads. */
export interface SuppressedFields {
	state?: string;
	suppression?: string;
	reason?: string;
	suppressedBy?: string;
	suppressUntilMs?: number;
	equipment?: string;
}

/** What a suppressed row shows beside its state. */
export interface SuppressionBadge {
	/** `Out of service`, `Designed`, `Substituted`, `Unreadable`. */
	label: string;
	/** Why, from the controller. */
	why: string;
	/** Who took it out (out of service only). */
	who: string;
	/** Epoch ms it ends (out of service only), 0 when it does not. */
	untilMs: number;
}

const KIND_LABEL: Record<string, string> = {
	'out-of-service': 'Out of service',
	designed: 'Suppressed by design',
	substituted: 'Signal substituted',
	unreadable: 'Condition unreadable'
};

// The controller's reason for an out-of-service alarm reads
// `out of service until <time> by <who>: <the person's words>`; the badge
// shows who and until as fields of their own, so it keeps only the words.
function ownWords(reason: string): string {
	const m = /^out of service until \S+ by .*?: ([\s\S]*)$/.exec(reason);
	return m ? m[1] : reason;
}

/** The badge for an alarm, or null when it is not suppressed. */
export function suppressionBadge(a: SuppressedFields): SuppressionBadge | null {
	if (a.state !== 'suppressed') return null;
	const kind = a.suppression ?? '';
	return {
		label: KIND_LABEL[kind] ?? 'Suppressed',
		why: kind === 'out-of-service' ? ownWords(a.reason ?? '') : (a.reason ?? ''),
		who: kind === 'out-of-service' ? (a.suppressedBy ?? '') : '',
		untilMs: kind === 'out-of-service' ? (a.suppressUntilMs ?? 0) : 0
	};
}

/** `why, by alice, until 14:05`-style text for a badge's tooltip and screen readers. */
export function badgeText(b: SuppressionBadge, fmtTime: (ms: number) => string = defaultTime): string {
	const parts = [b.label];
	if (b.why) parts.push(b.why);
	if (b.who) parts.push(`by ${b.who}`);
	if (b.untilMs) parts.push(`until ${fmtTime(b.untilMs)}`);
	return parts.join(' · ');
}

function defaultTime(ms: number): string {
	return new Date(ms).toLocaleString();
}

/** Windows soonest-ending first (ties by equipment name), as the controller lists them. */
export function soonestFirst(list: OutOfService[]): OutOfService[] {
	return [...list].sort((a, b) => a.untilMs - b.untilMs || a.equipment.localeCompare(b.equipment));
}

/** `2d 3h`, `3h 5m`, `4m`: how long is left, rounded down to the two largest units. */
export function timeLeft(untilMs: number, now: number): string {
	const s = Math.max(0, Math.floor((untilMs - now) / 1000));
	if (s < 60) return `${s}s`;
	if (s < 3600) return `${Math.floor(s / 60)}m`;
	if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
	return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

/** The distinct equipment names the alarms belong to (the picker's choices), skipping the controller's own. */
export function equipmentChoices(alarms: { equipment?: string }[]): string[] {
	const seen = new Map<string, string>();
	for (const a of alarms) {
		const e = (a.equipment ?? '').trim();
		if (!e || /^_*nautilus\./i.test(e) || e.toLowerCase().startsWith('__nautilus')) continue;
		const k = e.toLowerCase();
		if (!seen.has(k)) seen.set(k, e);
	}
	return [...seen.values()].sort((a, b) => a.localeCompare(b));
}

/** What is wrong with a request, or '' when the controller would take it. */
export function checkOutOfService(equipment: string, seconds: number, reason: string): string {
	if (!equipment.trim()) return 'choose the equipment';
	if (!(seconds > 0)) return 'choose how long';
	if (seconds > OOS_MAX_SECONDS) return 'at most 7 days: out of service is never permanent';
	const r = reason.trim();
	if (!r) return 'a reason is required';
	if (/[\r\n]/.test(r)) return 'the reason is one line';
	if ([...r].length > OOS_REASON_MAX) return `the reason is at most ${OOS_REASON_MAX} characters`;
	return '';
}

/** The result of a write: the controller's answer or its refusal in words. */
export type OosResult = { ok: true } | { ok: false; message: string };

async function post(f: Fetch, url: string, body: unknown, failed: string, headers: Record<string, string>): Promise<OosResult> {
	try {
		const res = await f(url, { method: 'POST', headers, body: JSON.stringify(body) });
		if (res.ok) return { ok: true };
		return { ok: false, message: refusalMessage(res.status, await res.text(), failed) };
	} catch {
		return { ok: false, message: `${failed} — controller unreachable` };
	}
}

/** `POST <base>/out-of-service`. Goes through the kit's fetch, so the elevation prompt comes with it. */
export function takeOutOfService(
	base: string,
	req: { equipment: string; seconds: number; reason: string; by: string },
	headers: Record<string, string> = { 'Content-Type': 'application/json' },
	f: Fetch = kitFetch
): Promise<OosResult> {
	const bad = checkOutOfService(req.equipment, req.seconds, req.reason);
	if (bad) return Promise.resolve({ ok: false, message: bad });
	return post(
		f,
		`${base}/out-of-service`,
		{ equipment: req.equipment.trim(), seconds: req.seconds, reason: req.reason.trim(), by: req.by },
		'out of service failed',
		headers
	);
}

/** `POST <base>/in-service`. */
export function returnToService(
	base: string,
	req: { equipment: string; by: string },
	headers: Record<string, string> = { 'Content-Type': 'application/json' },
	f: Fetch = kitFetch
): Promise<OosResult> {
	return post(f, `${base}/in-service`, { equipment: req.equipment, by: req.by }, 'return to service failed', headers);
}

/** `GET <base>/out-of-service`: the windows, soonest-ending first, or null when the read failed. */
export async function listOutOfService(
	base: string,
	headers: Record<string, string> = {},
	f: Fetch = kitFetch
): Promise<OutOfService[] | null> {
	try {
		const res = await f(`${base}/out-of-service`, { headers });
		if (!res.ok) return null;
		const body = (await res.json()) as OutOfService[] | null;
		return soonestFirst(Array.isArray(body) ? body : []);
	} catch {
		return null;
	}
}
