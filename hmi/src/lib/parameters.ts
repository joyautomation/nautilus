// Setpoint parameters kept in step with Git (#718): GET /api/parameters read,
// POST /api/parameters/import to take the repo's value. No runes, no DOM;
// tested in tests/parameters.test.ts. `ParameterList` renders it.
//
// The controller decides every request. A 401 asking for the password again
// is the kit fetch's business (createAuth); what this adds is saying each
// refusal in the controller's own words, never as "nothing to show".

import { kitFetch, type Fetch } from './kitfetch.js';
import { refusalMessage } from './auth.js';

/** Where the plant's value and the manifest's `init:` stand. */
export type DriftState = 'in-step' | 'plant-ahead' | 'repo-ahead' | 'conflict';

/** One parameter, as GET /api/parameters reports it. */
export interface Parameter {
	name: string;
	value: unknown;
	init: unknown;
	base?: unknown;
	unit?: string;
	desc?: string;
	min?: number;
	max?: number;
	state: DriftState;
	outOfRange?: boolean;
	editedBy?: string;
	editedAtMs?: number;
	unwritable?: string;
	committed?: boolean;
}

/** The manifest's `parameters.writeBack` mode. */
export type WriteBack = 'off' | 'patch' | 'commit';

export type ParametersAnswer =
	| { ok: true; parameters: Parameter[]; writeBack: WriteBack }
	| { ok: false; status: number; message: string };

/** One name's outcome of an import. */
export interface ImportResult {
	name: string;
	from?: unknown;
	to?: unknown;
	status: 'change' | 'applied' | 'unchanged' | 'refused';
	reason?: string;
}

export type ImportAnswer =
	| { ok: true; applied: boolean; results: ImportResult[] }
	/** `results` is set when the controller refused the set (422): nothing was written. */
	| { ok: false; status: number; message: string; results?: ImportResult[] };

export interface ParametersClientOptions {
	/** Default `/api/parameters`. */
	url?: string;
	/** `X-Nautilus-Token`, for a cross-origin reader. */
	token?: string;
	/** Default: the kit's fetch (sign-in rules in front, once `createAuth()` started). */
	fetch?: Fetch;
}

const UNREACHABLE = 'controller unreachable';

export class ParametersClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: Fetch;

	constructor(opts: ParametersClientOptions = {}) {
		this.#url = opts.url ?? '/api/parameters';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	#headers(json = false): Record<string, string> {
		const h: Record<string, string> = {};
		if (this.#token) h['X-Nautilus-Token'] = this.#token;
		if (json) h['Content-Type'] = 'application/json';
		return h;
	}

	/** The parameters and the write-back mode. */
	async list(): Promise<ParametersAnswer> {
		let res: Response;
		try {
			res = await this.#fetch(this.#url, { headers: this.#headers() });
		} catch {
			return { ok: false, status: 0, message: UNREACHABLE };
		}
		const text = await res.text();
		if (!res.ok) {
			if (res.status === 404) return { ok: false, status: 404, message: 'this controller has no parameters route (too old)' };
			return { ok: false, status: res.status, message: refusalMessage(res.status, text, 'reading the parameters failed') };
		}
		try {
			const j = JSON.parse(text) as { parameters?: Parameter[]; writeBack?: string };
			const wb = j.writeBack === 'patch' || j.writeBack === 'commit' ? j.writeBack : 'off';
			return { ok: true, parameters: j.parameters ?? [], writeBack: wb };
		} catch {
			return { ok: false, status: res.status, message: 'the controller answered something that is not JSON' };
		}
	}

	/**
	 * Apply `values` (name -> value) all or nothing, or with `dryRun` only say
	 * what would change. A refused set (422) comes back with its results and
	 * nothing written.
	 */
	async importSet(values: Record<string, unknown>, dryRun = false): Promise<ImportAnswer> {
		let res: Response;
		try {
			res = await this.#fetch(`${this.#url}/import`, {
				method: 'POST',
				headers: this.#headers(true),
				body: JSON.stringify({ version: 1, parameters: values, dryRun })
			});
		} catch {
			return { ok: false, status: 0, message: UNREACHABLE };
		}
		const text = await res.text();
		let j: { applied?: boolean; results?: ImportResult[] } | undefined;
		try {
			j = JSON.parse(text);
		} catch {
			/* not JSON: said below */
		}
		if (res.status === 422 && j?.results) {
			return { ok: false, status: 422, message: refusedText(j.results), results: j.results };
		}
		if (!res.ok) {
			if (res.status === 501) return { ok: false, status: 501, message: 'this server mirrors a controller it does not run, so it cannot import' };
			return { ok: false, status: res.status, message: refusalMessage(res.status, text, 'the import failed') };
		}
		if (!j) return { ok: false, status: res.status, message: 'the controller answered something that is not JSON' };
		return { ok: true, applied: !!j.applied, results: j.results ?? [] };
	}
}

/** A refused set in one line: every refused name with its reason. */
export function refusedText(results: ImportResult[]): string {
	const bad = results.filter((r) => r.status === 'refused');
	if (!bad.length) return 'the controller refused the set; nothing was written';
	return `refused, nothing written: ${bad.map((r) => `${r.name} (${r.reason ?? 'refused'})`).join('; ')}`;
}

/** What the badge says for a state, and how loudly. */
export function driftBadge(p: Pick<Parameter, 'state' | 'committed' | 'unwritable'>): {
	label: string;
	kind: 'good' | 'warning' | 'serious' | 'off';
	hint: string;
} {
	switch (p.state) {
		case 'in-step':
			return { label: 'in step', kind: 'good', hint: 'The plant runs the manifest init:.' };
		case 'plant-ahead':
			return {
				label: p.committed ? 'committed' : 'plant ahead',
				kind: p.committed ? 'off' : 'warning',
				hint: p.committed
					? 'An operator edit already committed to the repo; it waits for a redeploy.'
					: p.unwritable
						? `An operator edit the repo does not have. It cannot be written back: ${p.unwritable}`
						: 'An operator edit the repo does not have.'
			};
		case 'repo-ahead':
			return { label: 'repo ahead', kind: 'serious', hint: 'A redeploy brought a new init:; the plant still holds the old value.' };
		case 'conflict':
			return { label: 'conflict', kind: 'serious', hint: 'The plant and the repo both moved, to different values. A person decides.' };
		default:
			return { label: String(p.state), kind: 'off', hint: '' };
	}
}

/** A value for one cell: numbers and bools as they are, nothing as a dash. */
export function valueText(v: unknown): string {
	if (v === undefined || v === null) return '—';
	return typeof v === 'string' ? v : JSON.stringify(v);
}

/** `min – max` (either end may be open), or "". */
export function rangeText(p: Pick<Parameter, 'min' | 'max'>): string {
	if (p.min === undefined && p.max === undefined) return '';
	return `${p.min ?? '…'} – ${p.max ?? '…'}`;
}

/** The parameters whose plant value is not the manifest's init:. */
export function drifted(ps: Parameter[]): Parameter[] {
	return ps.filter((p) => p.state !== 'in-step');
}

/**
 * The set that takes the repo's value for these parameters (name -> init).
 * Only a repo-ahead or conflict row has a repo value worth taking; a
 * plant-ahead row is the operator's edit and is never reverted from here,
 * and a row with no init is skipped.
 */
export function takeRepoSet(ps: Parameter[]): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	for (const p of ps) {
		if ((p.state === 'repo-ahead' || p.state === 'conflict') && p.init !== undefined && p.init !== null) out[p.name] = p.init;
	}
	return out;
}

/** What `takeRepo` did. */
export type TakeOutcome =
	| { kind: 'nothing'; message: string }
	| { kind: 'declined' }
	| { kind: 'error'; message: string }
	| { kind: 'applied'; count: number; message: string };

/**
 * Take the repo's value for `which`: a dry run says what would change, `ask`
 * shows it (return false to decline, and nothing is sent), then the set is
 * applied all or nothing. A value that moved between the dry run and the
 * apply (someone edited it while the question was open) is overwritten by the
 * server, which has no expected-from guard; this says so, with the value that
 * was replaced, rather than reporting a clean apply.
 */
export async function takeRepo(
	client: Pick<ParametersClient, 'importSet'>,
	which: Parameter[],
	ask: (lines: string[]) => Promise<boolean>
): Promise<TakeOutcome> {
	const set = takeRepoSet(which);
	if (!Object.keys(set).length) return { kind: 'nothing', message: 'Nothing here has a repo value to take.' };
	const dry = await client.importSet(set, true);
	if (!dry.ok) return { kind: 'error', message: dry.message };
	const changes = dry.results.filter((x) => x.status === 'change');
	if (!changes.length) return { kind: 'nothing', message: 'The plant already runs the repo values.' };
	const ok = await ask(changes.map((x) => `${x.name}: ${valueText(x.from)} → ${valueText(x.to)}`));
	if (!ok) return { kind: 'declined' };
	const done = await client.importSet(set, false);
	if (!done.ok) return { kind: 'error', message: done.message };
	const applied = done.results.filter((x) => x.status === 'applied');
	const seen = new Map(changes.map((x) => [x.name, x.from]));
	const moved = applied.filter((x) => seen.has(x.name) && JSON.stringify(seen.get(x.name)) !== JSON.stringify(x.from));
	let message = `Applied ${applied.length} parameter(s).`;
	if (moved.length) {
		message += ` These changed while you were confirming, and their newer value was replaced: ${moved.map((x) => `${x.name} (was ${valueText(x.from)})`).join(', ')}.`;
	}
	return { kind: 'applied', count: applied.length, message };
}
