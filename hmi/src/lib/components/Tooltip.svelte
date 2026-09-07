<script lang="ts">
	// Small floating label for a trigger (icon button, truncated text, …).
	// Shows on hover/focus, hides on the opposite, Escape dismisses. No
	// floating-ui dependency — the trigger is the positioning context, the
	// bubble is absolutely placed off one of its edges.
	import type { Snippet } from 'svelte';

	let {
		text,
		position = 'top',
		delay = 300,
		children
	}: {
		text: string;
		position?: 'top' | 'bottom' | 'left' | 'right';
		/** ms hover delay before showing. */
		delay?: number;
		children: Snippet;
	} = $props();

	let visible = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;
	let wrapEl = $state<HTMLElement | null>(null);
	const id = `nh-tt-${Math.random().toString(36).slice(2, 9)}`;

	function show() {
		clearTimeout(timer);
		timer = setTimeout(() => (visible = true), delay);
	}
	function hide() {
		clearTimeout(timer);
		visible = false;
	}
	function onkeydown(ev: KeyboardEvent) {
		if (ev.key === 'Escape') hide();
	}

	// Touch/pen path: hover and focus don't exist under a coarse pointer, so
	// a tap on the trigger toggles the tip directly (no hover delay — it was
	// a deliberate tap) and a tap anywhere outside, on the same pointer
	// family, closes it. Mouse/focus behaviour above is untouched.
	function onpointerdown(ev: PointerEvent) {
		if (ev.pointerType !== 'touch' && ev.pointerType !== 'pen') return;
		clearTimeout(timer);
		visible = !visible;
	}
	function onDocumentPointerDown(ev: PointerEvent) {
		if (ev.pointerType !== 'touch' && ev.pointerType !== 'pen') return;
		if (wrapEl?.contains(ev.target as Node)) return;
		hide();
	}
	$effect(() => {
		document.addEventListener('pointerdown', onDocumentPointerDown);
		return () => document.removeEventListener('pointerdown', onDocumentPointerDown);
	});
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<span
	class="tt-wrap"
	bind:this={wrapEl}
	onmouseenter={show}
	onmouseleave={hide}
	onfocusin={show}
	onfocusout={hide}
	{onpointerdown}
	{onkeydown}
>
	<span aria-describedby={visible ? id : undefined}>
		{@render children()}
	</span>
	{#if visible}
		<span class="bubble {position}" role="tooltip" {id}>{text}</span>
	{/if}
</span>

<style>
	.tt-wrap {
		position: relative;
		display: inline-flex;
	}
	.bubble {
		position: absolute;
		z-index: 300;
		padding: 4px 8px;
		border-radius: 6px;
		background: var(--ink);
		color: var(--bg);
		font-size: var(--font-2xs);
		font-weight: 550;
		white-space: nowrap;
		pointer-events: none;
		box-shadow: 0 4px 14px rgba(0, 0, 0, 0.25);
		animation: fade-in 0.1s ease-out;
	}
	@media (prefers-reduced-motion: reduce) {
		.bubble {
			animation: none;
		}
	}
	@keyframes fade-in {
		from {
			opacity: 0;
		}
	}
	.bubble.top {
		bottom: calc(100% + 6px);
		left: 50%;
		transform: translateX(-50%);
	}
	.bubble.bottom {
		top: calc(100% + 6px);
		left: 50%;
		transform: translateX(-50%);
	}
	.bubble.left {
		right: calc(100% + 6px);
		top: 50%;
		transform: translateY(-50%);
	}
	.bubble.right {
		left: calc(100% + 6px);
		top: 50%;
		transform: translateY(-50%);
	}
</style>
