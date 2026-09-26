<script lang="ts">
	// A live STRING readout placed on the mimic — the kit's built-in
	// MimicLabel.bind only formats numeric tags (see Mimic.svelte's
	// labelValue()), so SeqStep (a STRING tag: 'Idle'/'Lead'/'Lag'/...)
	// rides an ordinary equipment binding instead: `bind: { text: "SeqStep" }`
	// resolves through the same resolveBindings() path equipment props use,
	// which passes any tag value through untouched. App-supplied, not part
	// of the kit.
	let { text = '', width = 120 }: { text?: string; width?: number } = $props();
</script>

<div class="tag" style:width="{width}px">
	<span class="dot" aria-hidden="true"></span>
	<span class="v">{text || '—'}</span>
</div>

<style>
	.tag {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 3px 10px;
		border-radius: 999px;
		background: var(--surface-2, #232321);
		border: 1px solid var(--axis, #383835);
		font-size: 11px;
		font-weight: 650;
		letter-spacing: 0.02em;
		white-space: nowrap;
	}
	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--s1, #3987e5);
		flex-shrink: 0;
	}
	.v {
		color: var(--ink, #e7e6df);
		font-family: var(--mono, ui-monospace, monospace);
	}
</style>
