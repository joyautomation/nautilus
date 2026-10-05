// The plant's fault inputs, as the scenario panel and a part's right-click
// menu read them. The plant (examples/it-cluster in nautilus) has one
// struct of fault inputs per part, named `Sim_<the part's tag>`, plus
// `Sim_Cable_<one end's tag>`, `Sim_Room` and `Sim_Net`; `Scenario` runs a
// named preset and `ScenarioCatalog` lists them. Nothing here knows the
// plant's parts: what a part can suffer is whatever members its struct
// has, so a new fault in the plant shows up here without a change.
// No imports: the tests run this file on node as it is.

export interface Scenario {
	name: string;
	about: string;
}

/** A fault input a part offers: one member of its `Sim_*` struct. */
export interface Fault {
	/** The struct, `Sim_NODE1_Fan3`. */
	tag: string;
	member: string;
	/** What the menu says: `fail`, `lose input`, `CPU 100 %`. */
	label: string;
	/** Set now (differs from zero / false). */
	on: boolean;
	/** The value written to set it. */
	set: boolean | number;
	/** The value written to clear it. */
	clear: boolean | number;
}

/** A fault that is set, for the panel's list. */
export interface ActiveFault extends Fault {
	/** Whose: `NODE1 Fan3`, `cable SW3 Port25`, `room`. */
	who: string;
	value: boolean | number;
}

// What setting a member means, in the menu's words, and the value that
// sets it. BOOL members are set with true; a REAL member needs a value
// that makes the fault, which the plant's own presets use (scenarios.yaml).
const MEMBERS: Record<string, { label: string; set: boolean | number }> = {
	Fail: { label: 'fail', set: true },
	Pulled: { label: 'pull', set: true },
	Failing: { label: 'failing', set: true },
	InputLost: { label: 'lose input', set: true },
	Down: { label: 'down', set: true },
	AdminDown: { label: 'admin down', set: true },
	SpeedMbps: { label: '1G', set: 1000 },
	ErrorRate: { label: 'errors 50/s', set: 50 },
	Dark: { label: 'dark', set: true },
	Reboot: { label: 'reboot', set: true },
	PowerOff: { label: 'power off', set: true },
	CpuLoad: { label: 'CPU 100 %', set: 100 },
	InletDeltaC: { label: 'hot room +10 °C', set: 10 },
	BroadcastPps: { label: 'broadcast storm', set: 1_000_000 },
	MulticastPps: { label: 'multicast storm', set: 300_000 }
};
// The menu's order: the fault that takes the part out first.
const ORDER = Object.keys(MEMBERS);

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
/** A member that differs from zero / false is a fault that is set. */
export const isSet = (v: unknown): boolean => (typeof v === 'number' ? v !== 0 : v === true);

/** The catalog tag's JSON, `[{name, about}]`; anything else is none. */
export function parseCatalog(v: unknown): Scenario[] {
	if (typeof v !== 'string') return [];
	try {
		const a = JSON.parse(v);
		return Array.isArray(a) ? a.filter((s) => isObj(s) && typeof s.name === 'string').map((s) => ({ name: s.name, about: typeof s.about === 'string' ? s.about : '' })) : [];
	} catch {
		return [];
	}
}

/** `Sim_NODE1_Fan3` → `NODE1 Fan3`; `Sim_Cable_SW3_Port25` → `cable SW3 Port25`. */
export function who(tag: string): string {
	const t = tag.replace(/^Sim_/, '');
	if (t === 'Room') return 'room';
	if (t === 'Net') return 'network';
	return t.replace(/^Cable_/, 'cable ').replace(/_/g, ' ');
}

function faultsOf(tag: string, v: unknown): Fault[] {
	if (!isObj(v)) return [];
	// A server's struct has PowerOff; its Dark is the BMC going quiet, not the box.
	const server = 'PowerOff' in v;
	return Object.entries(v)
		.filter(([, x]) => typeof x === 'number' || typeof x === 'boolean')
		.map(([member, x]) => {
			const m = MEMBERS[member];
			const num = typeof x === 'number';
			return {
				tag,
				member,
				label: member === 'Dark' && server ? 'BMC dark' : (m?.label ?? member),
				on: isSet(x),
				set: m?.set ?? (num ? 1 : true),
				clear: num ? 0 : false
			};
		})
		.sort((a, b) => rank(a.member) - rank(b.member));
}
const rank = (m: string) => (ORDER.includes(m) ? ORDER.indexOf(m) : ORDER.length);

/**
 * What a part can suffer: the members of `Sim_<tag>` for each tag given
 * that the plant has, in the order given. `state` is the plant's tags
 * (`/plant/api/state?tags=Sim_*`).
 */
export function faultsFor(state: Record<string, unknown>, simTags: string[]): Fault[] {
	return simTags.flatMap((t) => faultsOf(t, state[t]));
}

/**
 * The fault structs a part's menu reads: its own (`Sim_<part tag>`), the
 * cable on it (`Sim_Cable_<end>` — the plant names a cable by one of its
 * ends, so both are tried and faultsFor keeps the one it has), then its
 * device's (`Sim_<device tag>`) when the part is not the device itself.
 */
export function simTagsFor(partTag: string | undefined, cableEnds: (string | undefined)[] = [], deviceTag?: string): string[] {
	const out: string[] = [];
	if (partTag) out.push(`Sim_${partTag}`);
	for (const e of cableEnds) if (e) out.push(`Sim_Cable_${e}`);
	if (deviceTag && deviceTag !== partTag) out.push(`Sim_${deviceTag}`);
	return [...new Set(out)];
}

/** Every fault that is set now, by whose, then in the menu's order. */
export function activeFaults(state: Record<string, unknown>): ActiveFault[] {
	return Object.keys(state)
		.filter((t) => t.startsWith('Sim_'))
		.sort()
		.flatMap((t) =>
			faultsOf(t, state[t])
				.filter((f) => f.on)
				.map((f) => ({ ...f, who: who(t), value: (state[t] as Record<string, boolean | number>)[f.member] }))
		);
}

/** `ScenarioS` as the panel shows it: `42 s`, `3 m 07 s`, `1 h 02 m`. */
export function since(s: unknown): string {
	if (typeof s !== 'number' || !Number.isFinite(s) || s < 0) return '';
	const t = Math.floor(s);
	if (t < 60) return `${t} s`;
	if (t < 3600) return `${Math.floor(t / 60)} m ${String(t % 60).padStart(2, '0')} s`;
	return `${Math.floor(t / 3600)} h ${String(Math.floor((t % 3600) / 60)).padStart(2, '0')} m`;
}
