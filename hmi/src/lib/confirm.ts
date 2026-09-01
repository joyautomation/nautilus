// The confirm queue — the pure half of `ConfirmDialog`.
//
// Why a queue and not a boolean: an operator can trigger two confirmable
// actions before answering either (a mis-click on Ack All, then Ack). One
// dialog is on screen at a time; the rest wait their turn and each keeps its
// own promise. Dropping the second request would silently discard a command,
// which is exactly the failure the confirm step exists to prevent.
//
// Runes live in `confirm.svelte.ts`, which is a thin reactive wrapper over
// this. Everything decidable — ordering, resolution, the no-host fallback, the
// operator name — is here, where it can be tested on plain node.

/** What a confirmation asks. */
export interface ConfirmOptions {
	/** The question, as a question. "Acknowledge 3 alarms?" */
	title: string;
	/** One sentence of consequence, if the title does not carry it all. */
	body?: string;
	/**
	 * The things being acted on, enumerated. Ack All always lists; a single
	 * row lists itself. Order is the caller's — worst first, for alarms.
	 */
	items?: string[];
	/** Cap on rendered items; the rest collapse to "+ N more". Default 8. */
	maxItems?: number;
	/** Default `'Confirm'`. */
	confirmLabel?: string;
	/** Default `'Cancel'`. */
	cancelLabel?: string;
	/** Styles the confirm button as destructive. */
	danger?: boolean;
	/**
	 * Show the editable operator field. The value is committed to the shared
	 * operator name (`getOperator()`) when the dialog is confirmed — the last
	 * point before an unauthenticated record is written.
	 */
	operator?: boolean;
	/** A standing caveat under the body, e.g. "Recorded as rchon — unauthenticated". */
	note?: string;
}

/** One queued question, as the host component sees it. */
export interface ConfirmRequest {
	readonly id: number;
	readonly options: ConfirmOptions;
}

export interface ConfirmQueue {
	/** Ask. Resolves `true` on confirm, `false` on cancel/Escape/backdrop. */
	request(options: ConfirmOptions): Promise<boolean>;
	/** Settle the CURRENT request and promote the next one. No-op if empty. */
	settle(value: boolean): void;
	/** Settle a request by id — ignored if it is not the current one. */
	settleById(id: number, value: boolean): void;
	/** The question on screen, or `null`. */
	readonly current: ConfirmRequest | null;
	/** How many are waiting behind `current`. */
	readonly waiting: number;
	/**
	 * Register a mounted `<ConfirmDialog/>`. Returns the de-registration
	 * function. With no host registered, `request` takes the fallback path.
	 */
	attach(): () => void;
	/** Whether a host is mounted. */
	readonly attached: boolean;
}

export interface QueueOptions {
	/** Called whenever `current` changes, including to `null`. */
	onchange?: (current: ConfirmRequest | null) => void;
	/**
	 * Consulted when `request` is called with NO host mounted. Return a
	 * boolean to settle immediately, or `null` to queue anyway (the host may
	 * be about to mount). The default returns `null`.
	 *
	 * `confirm.svelte.ts` supplies a fallback that warns and falls through to
	 * the browser's own `window.confirm`, because a promise that never
	 * settles is a command that vanishes without a trace.
	 */
	fallback?: (options: ConfirmOptions) => boolean | null;
}

let nextId = 1;

export function createConfirmQueue(opts: QueueOptions = {}): ConfirmQueue {
	type Entry = { id: number; options: ConfirmOptions; resolve: (v: boolean) => void };
	const pending: Entry[] = [];
	let hosts = 0;

	const head = (): Entry | undefined => pending[0];

	function announce() {
		const e = head();
		opts.onchange?.(e ? { id: e.id, options: e.options } : null);
	}

	function settleById(id: number, value: boolean) {
		const i = pending.findIndex((e) => e.id === id);
		// Only the head is answerable — a stale click on a dialog that has
		// already been superseded must not settle somebody else's question.
		if (i !== 0) return;
		const [e] = pending.splice(0, 1);
		e.resolve(value);
		announce();
	}

	return {
		request(options: ConfirmOptions): Promise<boolean> {
			if (hosts === 0) {
				const answer = opts.fallback?.(options) ?? null;
				if (answer !== null) return Promise.resolve(answer);
			}
			const id = nextId++;
			return new Promise<boolean>((resolve) => {
				pending.push({ id, options, resolve });
				// Only the first entry changes what is on screen; anything
				// behind it is invisible until its turn.
				if (pending.length === 1) announce();
			});
		},
		settle(value: boolean) {
			const e = head();
			if (e) settleById(e.id, value);
		},
		settleById,
		get current() {
			const e = head();
			return e ? { id: e.id, options: e.options } : null;
		},
		get waiting() {
			return Math.max(0, pending.length - 1);
		},
		attach() {
			hosts++;
			return () => {
				hosts = Math.max(0, hosts - 1);
			};
		},
		get attached() {
			return hosts > 0;
		}
	};
}

/**
 * How many items a dialog renders before collapsing the rest. Exported so a
 * caller can pre-trim a very long list rather than handing over 2,000 alarms.
 */
export const DEFAULT_MAX_ITEMS = 8;

/** `['A','B','C']`, max 2 → `{ shown: ['A','B'], more: 1 }`. */
export function splitItems(
	items: string[] | undefined,
	max: number = DEFAULT_MAX_ITEMS
): { shown: string[]; more: number } {
	if (!items?.length) return { shown: [], more: 0 };
	if (max <= 0 || items.length <= max) return { shown: items, more: 0 };
	return { shown: items.slice(0, max), more: items.length - max };
}

// ── the operator name ──────────────────────────────────────────────────────
// nautilus has one token, not user accounts, so the HMI supplies the name that
// goes on the record. It is prefilled from localStorage and editable in the
// dialog — the last point before an unauthenticated, permanent record is
// written. Deliberately NOT reactive state: it changes once per shift, and a
// plain read at dialog-open time is enough.

const OPERATOR_KEY = 'nautilus.operator';

/** The remembered operator name, or `''`. Safe in SSR and in private mode. */
export function getOperator(): string {
	try {
		return globalThis.localStorage?.getItem(OPERATOR_KEY) ?? '';
	} catch {
		return '';
	}
}

/** Remember the operator name for the next dialog. `''` forgets it. */
export function setOperator(name: string): void {
	try {
		if (name) globalThis.localStorage?.setItem(OPERATOR_KEY, name);
		else globalThis.localStorage?.removeItem(OPERATOR_KEY);
	} catch {
		/* private mode — the name just does not persist */
	}
}
