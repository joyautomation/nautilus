// `confirm()` — the promise-based confirmation, and the reactive state the
// `<ConfirmDialog/>` host renders. Same shape as `toast`: mount the component
// once (root layout), call the function from anywhere.
//
//     import { confirm } from '@joyautomation/nautilus-hmi';
//     if (await confirm({ title: 'Stop P-101?', danger: true })) stop();
//
// Everything decidable lives in ./confirm.ts; this file is the ~40 lines of
// runes that make `current` reactive.
import { createConfirmQueue, type ConfirmOptions, type ConfirmRequest } from './confirm.js';

let current = $state<ConfirmRequest | null>(null);
let waiting = $state(0);
let warned = false;

const queue = createConfirmQueue({
	onchange: (c) => {
		current = c;
		waiting = queue.waiting;
	},
	fallback: (options) => {
		// No <ConfirmDialog/> is mounted. A promise that never settles is a
		// command that vanishes without a trace, so fall through to the
		// browser's own dialog — ugly, but it asks the question — and say
		// once, loudly, what the app forgot to mount.
		if (!warned) {
			warned = true;
			console.warn(
				'[nautilus-hmi] confirm() was called with no <ConfirmDialog/> mounted. ' +
					'Mount it once in your root layout, next to <Toast/>. Falling back to window.confirm.'
			);
		}
		const native = (globalThis as { confirm?: (m?: string) => boolean }).confirm;
		if (typeof native !== 'function') return false;
		const lines = [options.title, options.body, ...(options.items ?? [])].filter(Boolean);
		return native(lines.join('\n'));
	}
});

/**
 * Ask the operator to confirm an irreversible or plant-affecting action.
 * Resolves `true` on confirm, `false` on cancel, Escape, or a backdrop click.
 *
 * Escape and the backdrop cancel, and the confirm button is never the focused
 * default — a confirmation that a stray Enter can answer is not one.
 */
export function confirm(options: ConfirmOptions): Promise<boolean> {
	const p = queue.request(options);
	waiting = queue.waiting;
	return p;
}

/** The queue as `<ConfirmDialog/>` drives it. Not usually needed directly. */
export const confirmState = {
	/** The question on screen, or `null`. Reactive. */
	get current(): ConfirmRequest | null {
		return current;
	},
	/** How many questions are waiting behind it. Reactive. */
	get waiting(): number {
		return waiting;
	},
	/** Settle the current question. */
	settle(value: boolean): void {
		queue.settle(value);
	},
	/** Settle by id — a click on a dialog that has been superseded is ignored. */
	settleById(id: number, value: boolean): void {
		queue.settleById(id, value);
	},
	/** Registers a mounted host; returns the de-registration function. */
	attach(): () => void {
		return queue.attach();
	}
};

export type { ConfirmOptions, ConfirmRequest } from './confirm.js';
