<script lang="ts">
	// An edge panel — the thing a navigation dock becomes when the screen is
	// too narrow to give it a column. Built on the native <dialog> like Modal,
	// for the same reasons: showModal() is a browser-native focus trap, the
	// top layer stacks it over everything (including other dialogs' hosts),
	// and Escape arrives as a `cancel` event we do not have to reinvent.
	//
	// The dialog element IS the panel: it is pinned to one edge and sized to
	// the viewport's height (dvh, so a phone's collapsing URL bar does not
	// leave a gap under it), and its ::backdrop covers the rest. A click that
	// lands on the backdrop is dispatched to the dialog itself, which is how
	// "backdrop click closes" is told apart from a click inside `.box`.
	//
	// Open state is bindable so a host can drive it (`bind:open`) and read it
	// back after the built-in closers (×, Escape, backdrop) have fired.
	// Focus goes INTO the panel on open — the close button when there is a
	// title bar, otherwise the panel itself so a screen reader announces the
	// label — and BACK to whatever had it before (the hamburger, normally) on
	// close. Body scroll is locked while it is open; a drawer that lets the
	// page behind it scroll is a drawer the thumb keeps missing.
	import { tick, type Snippet } from 'svelte';
	import Icon from './Icon.svelte';

	let {
		open = $bindable(false),
		side = 'left',
		label,
		title = '',
		width = 'min(320px, 85vw)',
		height = 'min(75vh, 640px)',
		closeOnBackdrop = true,
		closeOnEscape = true,
		onclose,
		header,
		children,
		footer
	}: {
		open?: boolean;
		/** Which edge the panel is attached to. */
		side?: 'left' | 'right' | 'bottom';
		/** Accessible name of the dialog (aria-label). Required: a drawer
		 *  without one is announced as "dialog" and nothing else. */
		label: string;
		/** Renders a title bar with a close button. Omit it (and `header`) for
		 *  a bare panel — Escape and the backdrop still close it. */
		title?: string;
		/** Panel width for `left` / `right`. Any CSS length. */
		width?: string;
		/** Panel height for `bottom`. Any CSS length. */
		height?: string;
		closeOnBackdrop?: boolean;
		closeOnEscape?: boolean;
		/** Fires on every close, whichever affordance caused it. */
		onclose?: () => void;
		/** Overrides the default title bar's title; the × stays. */
		header?: Snippet;
		/** The scrolling region. */
		children?: Snippet;
		/** Pinned under the scrolling region, above the safe-area inset. */
		footer?: Snippet;
	} = $props();

	let dialogEl = $state<HTMLDialogElement | null>(null);
	let boxEl = $state<HTMLElement | null>(null);
	let closeBtn = $state<HTMLButtonElement | null>(null);

	// The element to hand focus back to. Captured on open, not on close: by
	// the time the dialog closes, focus is inside it.
	let opener: HTMLElement | null = null;
	let lockedOverflow: string | null = null;

	$effect(() => {
		const el = dialogEl;
		if (!el) return;
		if (open && !el.open) {
			opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
			el.showModal();
			lockedOverflow = document.body.style.overflow;
			document.body.style.overflow = 'hidden';
			tick().then(() => {
				if (!el.open) return;
				const auto = boxEl?.querySelector<HTMLElement>('[autofocus]');
				(auto ?? closeBtn ?? boxEl)?.focus({ preventScroll: true });
			});
		} else if (!open && el.open) {
			el.close();
		}
	});

	// Runs on unmount too — a route that unmounts its drawer while open must
	// not leave the page unscrollable behind it.
	$effect(() => {
		return () => unlock();
	});

	function unlock() {
		if (lockedOverflow === null) return;
		document.body.style.overflow = lockedOverflow;
		lockedOverflow = null;
	}

	function requestClose() {
		open = false;
		onclose?.();
	}

	function onBackdropClick(ev: MouseEvent) {
		if (closeOnBackdrop && ev.target === dialogEl) requestClose();
	}

	function oncancel(ev: Event) {
		// `cancel` fires natively on Escape; always prevent the default (which
		// would call .close() directly) so `open` stays the single source of
		// truth and `onclose` still fires.
		ev.preventDefault();
		if (closeOnEscape) requestClose();
	}

	// The native `close` event is the ONE exit path everything funnels into —
	// our own close(), or a browser closing it for us — so the unlock and the
	// focus return live here and nowhere else.
	function onclosed() {
		unlock();
		if (open) requestClose();
		const to = opener;
		opener = null;
		if (to && to.isConnected) to.focus({ preventScroll: true });
	}

	const hasBar = $derived(!!title || !!header);
</script>

<dialog
	bind:this={dialogEl}
	class="drawer {side}"
	style:--drawer-w={width}
	style:--drawer-h={height}
	aria-label={label}
	aria-modal="true"
	{oncancel}
	onclick={onBackdropClick}
	onclose={onclosed}
>
	<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
	<div class="box" bind:this={boxEl} tabindex="-1" onclick={(e) => e.stopPropagation()}>
		{#if hasBar}
			<header>
				<div class="bar-title">
					{#if header}
						{@render header()}
					{:else}
						<h3>{title}</h3>
					{/if}
				</div>
				<button bind:this={closeBtn} type="button" class="x" aria-label="Close" onclick={requestClose}>
					<Icon name="x-mark" size={18} strokeWidth={1.75} />
				</button>
			</header>
		{/if}
		<div class="body">
			{#if children}{@render children()}{/if}
		</div>
		{#if footer}
			<footer>
				{@render footer()}
			</footer>
		{/if}
	</div>
</dialog>

<style>
	/* The dialog is the panel. Undo the UA's centred-box defaults (margin
	   auto, max-* calc(100% - 2em - 6px)) and pin it to an edge instead. */
	.drawer {
		position: fixed;
		margin: 0;
		padding: 0;
		max-width: none;
		max-height: none;
		border: 0;
		border-radius: 0;
		background: var(--surface);
		color: var(--ink);
		/* The one elevation, for the one kind of thing that floats. */
		box-shadow: var(--elevation-float);
		overflow: hidden;
	}
	.drawer.left,
	.drawer.right {
		inset: 0 auto 0 0;
		width: var(--drawer-w);
		height: 100vh;
		height: 100dvh;
	}
	.drawer.left {
		border-right: 1px solid var(--border);
	}
	.drawer.right {
		inset: 0 0 0 auto;
		border-left: 1px solid var(--border);
	}
	.drawer.bottom {
		inset: auto 0 0 0;
		width: 100vw;
		width: 100dvw;
		height: var(--drawer-h);
		max-height: 100dvh;
		border-top: 1px solid var(--border);
		border-radius: var(--radius) var(--radius) 0 0;
	}
	.drawer::backdrop {
		background: var(--overlay, color-mix(in srgb, #000 55%, transparent));
		backdrop-filter: blur(2px);
	}
	.drawer[open] {
		display: flex;
		animation: slide 0.18s ease-out;
	}
	.drawer[open]::backdrop {
		animation: fade 0.18s ease-out;
	}
	.drawer.left[open] {
		--from: translateX(-100%);
	}
	.drawer.right[open] {
		--from: translateX(100%);
	}
	.drawer.bottom[open] {
		--from: translateY(100%);
	}
	@keyframes slide {
		from {
			transform: var(--from);
		}
	}
	@keyframes fade {
		from {
			opacity: 0;
		}
	}
	/* [data-motion='reduced'] is handled globally by theme.css. */
	@media (prefers-reduced-motion: reduce) {
		.drawer[open],
		.drawer[open]::backdrop {
			animation: none;
		}
	}

	.box {
		display: flex;
		flex-direction: column;
		min-height: 0;
		width: 100%;
		height: 100%;
		outline: none;
	}
	header {
		flex: none;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
		min-height: var(--header-h, 56px);
		padding: var(--space-2) var(--space-2) var(--space-2) var(--space-3);
		border-bottom: 1px solid var(--border);
	}
	.bar-title {
		min-width: 0;
		flex: 1;
	}
	header h3 {
		margin: 0;
		font-size: var(--font-md);
		font-weight: 680;
		color: var(--ink);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	/* 44 px: a thumb target, since a drawer is a touch-first thing. */
	.x {
		flex: none;
		width: 44px;
		height: 44px;
		padding: 0;
		display: grid;
		place-items: center;
		border: 0;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--muted);
		line-height: 1;
		cursor: pointer;
	}
	.x:hover {
		color: var(--ink);
		background: var(--hover);
	}
	.x:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: -2px;
	}
	.body {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		overscroll-behavior: contain;
		-webkit-overflow-scrolling: touch;
	}
	footer {
		flex: none;
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		padding-bottom: calc(var(--space-2) + env(safe-area-inset-bottom, 0px));
		border-top: 1px solid var(--border);
		background: var(--surface-2);
	}
</style>
