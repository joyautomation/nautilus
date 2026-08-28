<script lang="ts">
	// The faceplate standard — one container, every family.
	//
	// A plant has four or five equipment families and no more; it does NOT have
	// one faceplate per device. (The port this came from collapsed six
	// hand-written valve popups into one.) So the layout is fixed here and only
	// the content varies:
	//
	//   header    label · tag path · quality chip · SIM chip · close
	//   status    the state strip — mode, fault, interlock, comms
	//   hero      the family's primary graphic beside its live trend
	//   tabs      the ONE scrolling region below them
	//   footer    the write controls, right-aligned, plus the sim banner
	//
	// TWO HOSTS, ONE COMPONENT. `as="modal"` is a native <dialog> (focus trap,
	// top layer and Escape for free); `as="page"` is the same thing laid out as
	// a route, for the small-screen rule where tapping a card navigates instead
	// of opening a floating popup. `as="auto"` picks by viewport width, so an
	// app gets the whole responsive behaviour from one prop.
	//
	// WRITE-BACKS ARE GATED ON QUALITY, NOT ON EXISTENCE. The shell renders the
	// quality it was given and exposes `writable` to its footer snippet; a
	// control bound to a value the runtime cannot vouch for must disable with
	// the quality as its reason. Simulation is the exception: commands stay
	// enabled and the footer carries a persistent banner saying the plant is
	// not being commanded.
	import type { Snippet } from 'svelte';
	import { tick } from 'svelte';
	import Icon from './Icon.svelte';
	import StateChip from './StateChip.svelte';
	import Tabs from './Tabs.svelte';
	import { STATUS_META, valueStatus } from '../quality.js';
	import type { Quality } from '../types.js';
	import type { ChipKind } from './StateChip.svelte';

	let {
		label,
		tag = '',
		typeName = '',
		quality = 'good',
		present = true,
		simulated = false,
		showQuality = false,
		as = 'auto',
		breakpoint = 900,
		size = 'lg',
		tabs = [],
		active = $bindable(0),
		simTab = 'Sim',
		chips = [],
		closeOnEscape = true,
		closeOnBackdrop = true,
		simNote = 'SIMULATED — not commanding the plant',
		onclose,
		hero,
		status,
		panel,
		sim,
		children,
		footer
	}: {
		/** The equipment's human name — the big line in the header. */
		label: string;
		/** The tag path, shown mono under the label. */
		tag?: string;
		/** Appended to the tag as `tag — typeName`, e.g. `Motor 1 Speed`. */
		typeName?: string;
		/** From `RealtimeClient.quality(tag)`. Drives the chip and `writable`. */
		quality?: Quality;
		/** False when the runtime does not publish this equipment at all. */
		present?: boolean;
		/** The equipment's own SIMULATE member. */
		simulated?: boolean;
		/**
		 * Show the quality chip even when quality is good. Off by default:
		 * `'good'` means "nothing said it was bad", and on a controller that
		 * cannot report quality at all a green chip is a lie. Check
		 * `ControllerMeta.quality` before turning this on.
		 */
		showQuality?: boolean;
		/** `auto` = modal at or above `breakpoint`, full page below. */
		as?: 'modal' | 'page' | 'auto';
		/** px. The plan's 900 — below it a faceplate is a route, not a popup. */
		breakpoint?: number;
		/** Modal width. Ignored in page mode. */
		size?: 'md' | 'lg' | 'xl';
		/** Tab labels, opening tab first. Verbatim from the source screen —
		 *  that fidelity is free and is what an operator reads. */
		tabs?: string[];
		/** Index into `tabs` + the appended Sim tab. Bindable. */
		active?: number;
		/** Label of the standard Sim tab appended when `sim` is supplied. */
		simTab?: string;
		/** The state strip, data-driven. Use the `status` snippet for more. */
		chips?: { label: string; kind?: ChipKind; title?: string }[];
		closeOnEscape?: boolean;
		closeOnBackdrop?: boolean;
		/** The footer banner shown while `simulated`. */
		simNote?: string;
		onclose: () => void;
		/** The family's primary graphic + trend, pinned above the tabs. */
		hero?: Snippet;
		/** Extra state strip content, after `chips`. */
		status?: Snippet;
		/** The active tab's content: `(label, index)`. */
		panel?: Snippet<[string, number]>;
		/**
		 * The standard Sim tab. Supplying it appends a `Sim` tab — every
		 * family gets one, on every build, because per-equipment
		 * SIMULATE/SIMVALUE is a production feature of the controller, not a
		 * demo affordance.
		 */
		sim?: Snippet;
		/** Body content when there are no tabs. */
		children?: Snippet;
		/** The action row. Receives `writable` so controls can gate on it. */
		footer?: Snippet<[boolean]>;
	} = $props();

	const st = $derived(valueStatus({ present, quality, simulated }));
	const meta = $derived(STATUS_META[st]);
	/** Whether a command may be sent — quality-gated, sim-permitted. */
	const writable = $derived(meta.writable);

	const allTabs = $derived(sim ? [...tabs, simTab] : tabs);
	const simIndex = $derived(sim ? allTabs.length - 1 : -1);
	const activeLabel = $derived(allTabs[active] ?? '');

	// ── which host ────────────────────────────────────────────────────────
	// `auto` follows the viewport. Before mount (and in SSR) it resolves to the
	// modal, the desktop case: a control room is the default, a phone is the
	// exception, and `as="page"` states the exception explicitly on the route
	// that needs it.
	let wide = $state(true);
	$effect(() => {
		if (as !== 'auto' || typeof globalThis.matchMedia !== 'function') return;
		const mq = globalThis.matchMedia(`(min-width: ${breakpoint}px)`);
		wide = mq.matches;
		const onchange = (e: MediaQueryListEvent) => (wide = e.matches);
		mq.addEventListener('change', onchange);
		return () => mq.removeEventListener('change', onchange);
	});
	const host = $derived(as === 'auto' ? (wide ? 'modal' : 'page') : as);

	let dialogEl = $state<HTMLDialogElement | null>(null);
	let pageEl = $state<HTMLElement | null>(null);
	let closeBtn = $state<HTMLButtonElement | null>(null);

	$effect(() => {
		if (host !== 'modal') return;
		const el = dialogEl;
		if (!el) return;
		if (!el.open) el.showModal();
		tick().then(() => closeBtn?.focus());
		return () => {
			if (el.open) el.close();
		};
	});

	// A route-hosted faceplate takes focus on its container, not on the close
	// button: the operator arrived here by navigating, and the first thing a
	// screen reader should read is the equipment, not "Close".
	$effect(() => {
		if (host === 'page') pageEl?.focus({ preventScroll: true });
	});

	function oncancel(ev: Event) {
		// Always prevent the native close so `onclose` is the only exit.
		ev.preventDefault();
		if (closeOnEscape) onclose();
	}

	function onPageKey(ev: KeyboardEvent) {
		if (host === 'page' && closeOnEscape && ev.key === 'Escape') {
			ev.stopPropagation();
			onclose();
		}
	}
</script>

{#snippet shell()}
	<header>
		<div class="id">
			<h2 class="label">{label}</h2>
			{#if tag}
				<p class="path">{tag}{typeName ? ` — ${typeName}` : ''}</p>
			{/if}
		</div>

		<div class="badges">
			{#if simulated}
				<StateChip kind="simulated" label="SIM" title={STATUS_META.simulated.description} />
			{/if}
			{#if meta.degraded || showQuality}
				<StateChip kind={st} label={meta.label} title={meta.description} />
			{/if}
			<button bind:this={closeBtn} type="button" class="x" aria-label="Close" onclick={onclose}>
				<Icon name="x-mark" size={16} strokeWidth={1.75} />
			</button>
		</div>
	</header>

	{#if chips.length || status}
		<div class="strip">
			{#each chips as c, i (`${i}:${c.label}`)}
				<StateChip label={c.label} kind={c.kind ?? 'neutral'} title={c.title} />
			{/each}
			{#if status}{@render status()}{/if}
		</div>
	{/if}

	{#if hero}
		<div class="hero">{@render hero()}</div>
	{/if}

	{#if allTabs.length}
		<Tabs tabs={allTabs} bind:active />
	{/if}

	<div class="scroll">
		{#if sim && active === simIndex}
			{@render sim()}
		{:else if panel && allTabs.length}
			{@render panel(activeLabel, active)}
		{:else if children}
			{@render children()}
		{/if}
	</div>

	{#if footer || simulated}
		<footer>
			{#if simulated}
				<p class="simbanner">{simNote}</p>
			{/if}
			{#if footer}
				<div class="actions">{@render footer(writable)}</div>
			{/if}
		</footer>
	{/if}
{/snippet}

<svelte:window onkeydown={onPageKey} />

{#if host === 'modal'}
	<dialog
		bind:this={dialogEl}
		class="fp modal {size}"
		{oncancel}
		onclick={(e) => {
			if (closeOnBackdrop && e.target === dialogEl) onclose();
		}}
		aria-label={label}
	>
		<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
		<div class="box" onclick={(e) => e.stopPropagation()}>
			{@render shell()}
		</div>
	</dialog>
{:else}
	<section bind:this={pageEl} class="fp page" tabindex="-1" aria-label={label}>
		<div class="box">
			{@render shell()}
		</div>
	</section>
{/if}

<style>
	/* ── the modal host ──────────────────────────────────────────────────── */
	.modal {
		padding: 0;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
		color: var(--ink);
		/* The one elevation, for the one kind of thing that floats. */
		box-shadow: var(--elevation-float);
		width: min(560px, calc(100vw - var(--space-4)));
		height: min(88vh, 760px);
		max-height: calc(100vh - var(--space-4));
		max-height: calc(100dvh - var(--space-4));
	}
	.modal.md {
		width: min(460px, calc(100vw - var(--space-4)));
	}
	.modal.xl {
		width: min(880px, calc(100vw - var(--space-4)));
	}
	.modal::backdrop {
		background: var(--overlay, color-mix(in srgb, #000 55%, transparent));
		backdrop-filter: blur(2px);
	}
	.modal[open] {
		display: flex;
		animation: rise 0.14s ease-out;
	}
	@keyframes rise {
		from {
			opacity: 0;
			transform: translateY(8px) scale(0.985);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.modal[open] {
			animation: none;
		}
	}

	/* ── the page host ───────────────────────────────────────────────────── */
	/* Same regions, same order, no chrome: the header is the page's header and
	   the scroll region is the page's scroll region. */
	.page {
		display: block;
		min-height: 100%;
		background: var(--bg);
		color: var(--ink);
		outline: none;
	}
	.page > .box {
		max-width: 780px;
		margin: 0 auto;
		min-height: 100dvh;
		background: var(--surface);
		border-left: 1px solid var(--border);
		border-right: 1px solid var(--border);
	}

	.box {
		display: flex;
		flex-direction: column;
		min-height: 0;
		width: 100%;
	}

	/* ── regions ─────────────────────────────────────────────────────────── */
	header {
		flex: none;
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: var(--space-2);
		padding: var(--space-3) var(--space-3) var(--space-2);
		border-bottom: 1px solid var(--border);
	}
	.page header {
		position: sticky;
		top: 0;
		z-index: 1;
		background: var(--surface);
	}
	.id {
		min-width: 0;
	}
	.label {
		/* Overrides theme.css's global h2 (an eyebrow) — this is a title. */
		margin: 0;
		font-size: var(--font-md);
		font-weight: 680;
		text-transform: none;
		letter-spacing: normal;
		color: var(--ink);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.path {
		margin: 2px 0 0;
		font-family: var(--mono);
		font-size: var(--font-2xs);
		color: var(--muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.badges {
		flex: none;
		display: flex;
		align-items: center;
		gap: var(--space-1);
	}
	.x {
		flex: none;
		width: 28px;
		height: 28px;
		padding: 0;
		display: grid;
		place-items: center;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--muted);
		line-height: 1;
		cursor: pointer;
	}
	.x:hover {
		color: var(--ink);
		border-color: var(--muted);
	}
	.x:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}

	.strip {
		flex: none;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1);
		padding: var(--space-2) var(--space-3);
		border-bottom: 1px solid var(--border);
	}

	.hero {
		flex: none;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-3);
		padding: var(--space-3);
		border-bottom: 1px solid var(--border);
	}

	/* The ONE scrolling region. Everything above it stays pinned — a header,
	   a state strip or a tab bar that scrolls out of view is how an operator
	   loses track of which device they are commanding. */
	.scroll {
		flex: 1 1 auto;
		min-height: 0;
		overflow-y: auto;
		padding: var(--space-3);
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}
	.page .scroll {
		/* On a route there is no dialog height to divide up; the page scrolls. */
		overflow-y: visible;
		min-height: 0;
	}

	footer {
		flex: none;
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border-top: 1px solid var(--border);
		background: var(--surface);
	}
	.page footer {
		position: sticky;
		bottom: 0;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		align-items: center;
		gap: var(--space-2);
	}
	/* Persistent, not a toast: while the value is substituted, every command
	   in this footer is landing on a simulation and the operator must be able
	   to see that at the moment they press the button. */
	.simbanner {
		margin: 0;
		font-size: var(--font-2xs);
		font-weight: var(--weight-eyebrow);
		letter-spacing: 0.06em;
		color: var(--q-simulated);
		border: 1px dashed var(--q-simulated);
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--q-simulated) var(--tint-strength), transparent);
		padding: var(--space-1) var(--space-2);
		text-align: center;
	}
</style>
