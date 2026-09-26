<script lang="ts" module>
	// One in-place edit on the canvas: where it floats, what it starts with,
	// what to do with the committed text, and which completion vocabulary
	// (if any) it offers. 'assoc' is an SFC action association ("N
	// PumpRun"): tags, completing the word after the qualifier. `multiline` selects the comment textarea.
	export type EditRequest = {
		init: string;
		at: { x: number; y: number; w: number };
		commit: (v: string) => void;
		multiline?: boolean;
		suggest?: 'tags' | 'types' | 'functions' | 'assoc';
	};
</script>

<script lang="ts">
	// The floating editor for double-click edits: a single-line input with
	// suggestions (constants, renames, declare-in-place) or a multiline
	// textarea with real newlines (comments). Opened imperatively via
	// open(); closes itself on commit/cancel.
	import Suggest from './Suggest.svelte';
	import { FUNCTIONS, TYPES, type SuggestItem } from './suggest';
	import { NOTE_LINE_H } from './layout';

	let { tagItems = [] }: { tagItems?: SuggestItem[] } = $props();

	let edit = $state<(EditRequest & { value: string }) | null>(null);
	let ta = $state<HTMLTextAreaElement | null>(null);
	let sg = $state<{ focus: () => void } | null>(null);

	// Whatever held keyboard focus when the editor opened (the ladder/SFC
	// wrapper, the flow pane) gets it back on Enter/Esc — otherwise focus
	// falls to <body> with the removed input and Del/N/M need another click.
	let returnFocus: HTMLElement | null = null;
	export function open(req: EditRequest) {
		const ae = document.activeElement;
		returnFocus = ae instanceof HTMLElement && ae !== document.body ? ae : null;
		edit = { ...req, value: req.init };
	}
	function restoreFocus() {
		const el = returnFocus;
		returnFocus = null;
		if (el?.isConnected) el.focus({ preventScroll: true });
	}

	$effect(() => {
		if (!edit) return;
		if (edit.multiline && ta) {
			ta.focus();
			ta.select();
		} else if (!edit.multiline && sg) {
			sg.focus();
		}
	});

	const items = $derived(
		edit?.suggest === 'tags' || edit?.suggest === 'assoc'
			? tagItems
			: edit?.suggest === 'types'
				? TYPES
				: edit?.suggest === 'functions'
					? FUNCTIONS
					: []
	);

	function keydown(ev: KeyboardEvent) {
		ev.stopPropagation();
		// Single-line: Enter saves. Multiline (comments): Enter is a plain
		// newline — Ctrl/Cmd+Enter saves.
		if (ev.key === 'Enter' && edit && (!edit.multiline || ev.ctrlKey || ev.metaKey)) {
			ev.preventDefault();
			const v = edit.value.trim();
			const commit = edit.commit;
			// A comment committed empty is a delete (the op contract); other
			// edits need a value, so an empty Enter just closes the editor.
			const allowEmpty = edit.multiline;
			edit = null;
			restoreFocus();
			if (v || allowEmpty) commit(v);
		} else if (ev.key === 'Escape') {
			edit = null;
			restoreFocus();
		}
	}
	// Clicking away from a multi-line comment edit saves it — losing typed
	// text to a stray click is worse than an unwanted save (Escape is the
	// explicit cancel). Emptying the text is a DELETE, so that stays behind
	// an explicit Ctrl+Enter; blur with empty text just cancels.
	function blur() {
		if (edit?.multiline) {
			const v = edit.value.trim();
			const commit = edit.commit;
			edit = null;
			if (v) commit(v);
		} else {
			edit = null;
		}
	}
</script>

{#if edit}
	{#if edit.multiline}
		<textarea
			bind:this={ta}
			class="nx-input note"
			spellcheck="false"
			style="left: {edit.at.x}px; top: {edit.at.y - 2}px; width: {Math.max(edit.at.w, 160)}px; height: {(edit.value.split('\n').length + 1) * NOTE_LINE_H + 10}px; line-height: {NOTE_LINE_H}px"
			bind:value={edit.value}
			onkeydown={keydown}
			onblur={blur}
		></textarea>
	{:else}
		<Suggest
			bind:this={sg}
			cls="line"
			style="left: {edit.at.x}px; top: {edit.at.y - 2}px; width: {Math.max(edit.at.w, 72)}px"
			bind:value={edit.value}
			{items}
			call={edit.suggest === 'functions'}
			tail={edit.suggest === 'assoc'}
			onkeydown={keydown}
			onblur={blur}
		/>
	{/if}
{/if}

<style>
	.note {
		position: fixed;
		z-index: 30;
		border-color: var(--nx-accent);
		border-radius: 4px;
		font-size: 11px;
		resize: none;
		white-space: pre;
	}
	:global(.suggest.line) {
		position: fixed;
		z-index: 30;
	}
	:global(.suggest.line input) {
		border-color: var(--nx-accent);
		border-radius: 4px;
	}
</style>
