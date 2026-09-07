<script lang="ts" module>
	import type { AlarmInstance as _AlarmInstance, Priority as _Priority } from '../alarms.svelte.js';
	import { PRIORITY_ORDER, PRIORITY_META } from '../alarms.svelte.js';

	/**
	 * Order alarms the way a confirm dialog must list them: **worst first**.
	 * Ties break on age, oldest first — the one that has been shouting longest
	 * is the one the operator most needs to see before they answer.
	 */
	export function worstFirst(alarms: _AlarmInstance[]): _AlarmInstance[] {
		return [...alarms].sort((a, b) => {
			const d = PRIORITY_ORDER.indexOf(b.priority) - PRIORITY_ORDER.indexOf(a.priority);
			if (d !== 0) return d;
			return (a.activeMs ?? 0) - (b.activeMs ?? 0);
		});
	}

	/** `HIGH · Clearwell 1 level high-high` — one enumerated line. */
	export function ackLine(a: _AlarmInstance): string {
		const p = PRIORITY_META[a.priority as _Priority];
		return `${p ? p.label.toUpperCase() : a.priority} · ${a.name}`;
	}
</script>

<script lang="ts">
	// Acknowledge, with the confirm step attached.
	//
	// Ack is irreversible, unrecoverable, and — because nautilus has one token
	// rather than user accounts — recorded against a name nobody authenticated.
	// There is no role gate to slow an accidental click, so the click gets a
	// question instead. **Both** paths confirm: Ack All always enumerates, and
	// a single row confirms too, because it is one extra click against a
	// permanent record.
	//
	// Wraps `confirm()`, so it needs a `<ConfirmDialog/>` mounted once in the
	// app. Drop it beside an `AlarmBanner`, on a faceplate, or anywhere a
	// single alarm can be answered; `AlarmTable` has the same wiring built in
	// behind its `confirmAck` prop.
	import type { AlarmInstance } from '../alarms.svelte.js';
	import { confirm } from '../confirm.svelte.js';
	import { getOperator } from '../confirm.js';
	import Button from './Button.svelte';

	let {
		alarms = [],
		ids,
		label,
		operator = '',
		size = 'sm',
		variant = 'secondary',
		icon = 'check',
		disabled = false,
		confirmAck = true,
		onack
	}: {
		/** The alarms being acknowledged — enumerated in the dialog. */
		alarms?: AlarmInstance[];
		/**
		 * What to hand `onack`. Defaults to the ids of `alarms`; pass `['*']`
		 * for "ack everything" (the `Engine.Ack` nil/`["*"]` convention).
		 */
		ids?: string[];
		/** Button text. Defaults to `Ack` / `Ack (n)` / `Ack All`. */
		label?: string;
		/** Prefills the dialog's operator field. Falls back to the remembered name. */
		operator?: string;
		size?: 'sm' | 'md';
		variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
		icon?: string;
		disabled?: boolean;
		/** Off only where the host has already asked the question. */
		confirmAck?: boolean;
		onack: (ids: string[], by: string) => void;
	} = $props();

	const target = $derived(ids ?? alarms.map((a) => a.id));
	const all = $derived(target.length === 1 && target[0] === '*');
	const count = $derived(all ? alarms.length : target.length);
	const text = $derived(label ?? (all ? 'Ack All' : count === 1 ? 'Ack' : `Ack (${count})`));

	async function press() {
		if (!target.length) return;
		if (confirmAck) {
			const listed = worstFirst(alarms).map(ackLine);
			const ok = await confirm({
				title: all
					? `Acknowledge all ${count || ''} alarms?`.replace('  ', ' ')
					: `Acknowledge ${count} alarm${count === 1 ? '' : 's'}?`,
				items: listed,
				confirmLabel: 'Acknowledge',
				operator: true,
				note: 'Acknowledgement is permanent and unauthenticated — the name above is the whole record.'
			});
			if (!ok) return;
			// Read the name AFTER the dialog: it is editable there, and the
			// edited value is the one that goes on the record.
			onack(target, getOperator() || operator);
			return;
		}
		onack(target, operator || getOperator());
	}
</script>

<Button {size} {variant} {icon} disabled={disabled || !target.length} onclick={press}>
	{text}
</Button>
