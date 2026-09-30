// The alarm list's logic (AlarmBox): which alarms to show, in what order,
// in what words, and where each one is in the 3D. No imports at runtime:
// the tests run this file on node as it is.

export type Priority = 'diagnostic' | 'low' | 'medium' | 'high' | 'critical';
export interface Alarm {
	id: string;
	tag: string;
	name: string;
	priority: Priority;
	state: string;
	site?: string;
	activeMs?: number;
	rtnMs?: number;
}
const RANK: Priority[] = ['critical', 'high', 'medium', 'low', 'diagnostic'];
// Needs a human first: new, then standing, then back to normal unacked.
const STATE_RANK: Record<string, number> = { 'unack-active': 0, 'ack-active': 1, 'unack-rtn': 2, shelved: 3 };

/** What to show: everything not normal or suppressed; still-active first,
 * then by priority, then newest. */
export function listAlarms<A extends Alarm>(all: A[]): A[] {
	return all
		.filter((a) => a.state in STATE_RANK)
		.sort(
			(a, b) =>
				STATE_RANK[a.state] - STATE_RANK[b.state] ||
				RANK.indexOf(a.priority) - RANK.indexOf(b.priority) ||
				(b.activeMs ?? 0) - (a.activeMs ?? 0)
		);
}

/** The state in an operator's words. */
export function stateText(state: string): string {
	return (
		{
			'unack-active': 'new',
			'ack-active': 'active, acknowledged',
			'unack-rtn': 'cleared, not acknowledged',
			shelved: 'shelved'
		}[state] ?? state
	);
}

/** `12 s`, `4 min`, `3 h`, `2 d` ago. */
export function ago(ms: number | undefined, now: number): string {
	if (!ms) return '';
	const s = Math.max(0, Math.round((now - ms) / 1000));
	if (s < 60) return `${s} s`;
	if (s < 3600) return `${Math.round(s / 60)} min`;
	if (s < 86400) return `${Math.round(s / 3600)} h`;
	return `${Math.round(s / 86400)} d`;
}

/** The worst priority still active (or, with none active, of the list). */
export function worst(list: Alarm[]): Priority | undefined {
	const live = list.filter((a) => a.state.endsWith('-active'));
	const from = live.length ? live : list;
	return RANK.find((p) => from.some((a) => a.priority === p));
}

/**
 * Where an alarm is: its device (the `site`, or the tag up to the first
 * `_` or `.`), and the asset it is on — the tag before the member
 * (`NODE1_PSU2.Fault` → `NODE1_PSU2`). A device-level alarm (`SW1_Flap`,
 * whose asset is not a part) is on the device.
 */
export function whereIs(a: Alarm, devices: string[]): { device?: string; asset: string } {
	const asset = a.tag.split('.')[0];
	const device = devices.find((d) => d === a.site) ?? devices.find((d) => asset === d || asset.startsWith(`${d}_`));
	return { device, asset };
}

/** A faceplate's alarm lines: the alarms on this asset — or, for a whole
 * device, on it and every part under it — as label / value facts. */
export function alarmFacts(all: Alarm[], asset: string, device = false): { label: string; value: string }[] {
	return listAlarms(all)
		.filter((a) => {
			const on = a.tag.split('.')[0];
			return on === asset || (device && on.startsWith(`${asset}_`));
		})
		.map((a) => ({ label: `Alarm · ${a.priority}`, value: `${a.name} (${stateText(a.state)})` }));
}
