// Alarm presentation: the alarm client's instance list folded to one entry
// per asset, so a node can carry a halo for its worst active alarm.

/** Mirrors the kit's PRIORITY_ORDER (hmi/src/lib/alarms.svelte.ts), low -> high.
 * Repeated here so this module has no Svelte entry to import. */
const PRIORITY_RANK: Record<string, number> = { diagnostic: 0, low: 1, medium: 2, high: 3, critical: 4 };

export interface AssetAlarm {
	/** The worst active priority on the asset. */
	priority: string;
	/** True while any of the asset's active alarms is unacknowledged. */
	unacked: boolean;
	/** How many alarms are active on the asset. */
	count: number;
}

/** The subset of the kit's AlarmInstance this fold reads. */
export interface AlarmLike {
	tag: string;
	priority: string;
	state: string;
}

/** An alarm's asset: the alarm tag's root (`P101.Fault` -> `P101`). */
export function alarmAsset(tag: string): string {
	const dot = tag.indexOf('.');
	return dot < 0 ? tag : tag.slice(0, dot);
}

/**
 * Worst active alarm per asset. Normal, shelved and suppressed instances
 * are not active and do not count; `unack-rtn` (returned to normal but not
 * yet acknowledged) does — ISA-18.2 keeps it on the operator's list until
 * it is acked, and so does the halo.
 */
export function worstAlarmByAsset(instances: Iterable<AlarmLike>): Map<string, AssetAlarm> {
	const out = new Map<string, AssetAlarm>();
	for (const a of instances) {
		if (a.state === 'normal' || a.state === 'shelved' || a.state === 'suppressed') continue;
		const asset = alarmAsset(a.tag);
		const cur = out.get(asset);
		const unacked = a.state.startsWith('unack');
		if (!cur) {
			out.set(asset, { priority: a.priority, unacked, count: 1 });
			continue;
		}
		const worse = (PRIORITY_RANK[a.priority] ?? -1) > (PRIORITY_RANK[cur.priority] ?? -1);
		out.set(asset, {
			priority: worse ? a.priority : cur.priority,
			unacked: cur.unacked || unacked,
			count: cur.count + 1
		});
	}
	return out;
}
