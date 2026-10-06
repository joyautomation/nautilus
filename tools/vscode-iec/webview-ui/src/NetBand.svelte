<script lang="ts">
	// One numbered FBD network (#207) as a band behind its logic: a header
	// strip with the number and title, and a frame around the region its
	// blocks occupy. Networks execute in order, top to bottom. The header
	// is the network's handle: click makes it the palette's target ("+ add"
	// inserts into it), double-click renames, the buttons move it up/down,
	// add a network after it, or remove its NETWORK line (its statements
	// join the network above — nothing else is deleted).
	import type { NetFrame } from './layout';
	import { NET_HEAD_H } from './layout';

	let {
		data
	}: {
		data: {
			f: NetFrame;
			count: number;
			editable: boolean;
			/** The palette's target network (n:K), read live. */
			activeOf: () => string | undefined;
			onActivate: (id: string) => void;
			onOp: (op: { type: 'renameNetwork' | 'moveNetwork' | 'removeNetwork' | 'addNetwork'; node: string; value?: string; text?: string }) => void;
			requestInput: (
				init: string,
				at: { x: number; y: number; w: number },
				commit: (v: string) => void
			) => void;
		};
	} = $props();
	const f = $derived(data.f);
	const active = $derived(data.activeOf() === f.id);

	function rename(ev: MouseEvent) {
		if (!data.editable) return;
		ev.stopPropagation();
		const rect = (ev.currentTarget as HTMLElement).getBoundingClientRect();
		data.requestInput(f.title ?? '', { x: rect.left, y: rect.top, w: Math.max(rect.width, 220) }, (v) =>
			data.onOp({ type: 'renameNetwork', node: f.id, text: v })
		);
	}
	function op(ev: MouseEvent, o: Parameters<typeof data.onOp>[0]) {
		ev.stopPropagation();
		data.onOp(o);
	}
</script>

<div class="band" class:active data-kind="network" data-id={f.id} style="width: {f.w}px; height: {f.h}px">
	<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
	<div
		class="head"
		style="height: {NET_HEAD_H}px"
		title={data.editable
			? `Network ${f.number} — runs ${f.number === 1 ? 'first' : `after network ${f.number - 1}`}. Click: "+ add" inserts here · double-click the title to rename`
			: `Network ${f.number}`}
		onclick={(e) => {
			e.stopPropagation();
			data.onActivate(f.id);
		}}
	>
		<span class="netno">Network {f.number}</span>
		<span class="ntitle" class:empty={!f.title} ondblclick={rename}>{f.title || (data.editable ? 'add a title' : '')}</span>
		{#if data.editable}
			<span class="tools">
				<button class="nbtn" data-act="up" title="Move this network up (it runs earlier)" disabled={f.number === 1} onclick={(e) => op(e, { type: 'moveNetwork', node: f.id, value: 'up' })}>▲</button>
				<button class="nbtn" data-act="down" title="Move this network down (it runs later)" disabled={f.number === data.count} onclick={(e) => op(e, { type: 'moveNetwork', node: f.id, value: 'down' })}>▼</button>
				<button class="nbtn" data-act="add" title="Add a network after this one" onclick={(e) => op(e, { type: 'addNetwork', node: f.id, text: '' })}>+</button>
				{#if !f.implicit}
					<button class="nbtn" data-act="remove" title="Remove this NETWORK line — its statements join the network above" onclick={(e) => op(e, { type: 'removeNetwork', node: f.id })}>✕</button>
				{/if}
			</span>
		{/if}
	</div>
</div>

<style>
	.band {
		box-sizing: border-box;
		border: 1px solid color-mix(in srgb, var(--nx-border) 85%, var(--nx-ink));
		border-radius: 4px;
		background: color-mix(in srgb, var(--nx-panel-bg) 35%, transparent);
		/* the frame never blocks the pane: only the header takes the mouse */
		pointer-events: none;
	}
	.band.active {
		border-color: var(--nx-accent);
		box-shadow: 0 0 0 2px color-mix(in srgb, var(--nx-accent) 22%, transparent);
	}
	.head {
		pointer-events: all;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 0 8px;
		font-size: 11px;
		color: var(--nx-ui-ink);
		background: color-mix(in srgb, var(--nx-panel-bg) 85%, var(--nx-ink) 6%);
		border-bottom: 1px solid var(--nx-border);
		border-radius: 4px 4px 0 0;
		cursor: pointer;
		user-select: none;
	}
	.netno {
		font-weight: 700;
		white-space: nowrap;
	}
	.ntitle {
		flex: 1;
		min-width: 0;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.ntitle.empty {
		color: var(--nx-muted);
		font-style: italic;
	}
	.tools {
		display: inline-flex;
		gap: 3px;
	}
	.nbtn {
		background: transparent;
		color: var(--nx-ui-ink);
		border: 1px solid var(--nx-border);
		border-radius: 3px;
		font-size: 9px;
		line-height: 1;
		padding: 2px 5px;
		cursor: pointer;
	}
	.nbtn:hover:not(:disabled) {
		background: var(--nx-hover);
	}
	.nbtn:disabled {
		opacity: 0.35;
		cursor: default;
	}
</style>
