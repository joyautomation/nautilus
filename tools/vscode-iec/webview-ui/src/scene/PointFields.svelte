<script lang="ts">
	// A point in the kind's frame, three ways (design §3g): typed metres,
	// a place on the kind's bounds (top, left, …), or a place on a named
	// part's box (top of the Flange) — the last two survive the model being
	// re-exported and read better than numbers. Used for the label and for
	// each port's `at`. Every change hands the whole point back.
	import { ANCHOR_WORDS, isBoxPoint, type BoxPoint, type ScenePoint, type Vec3 } from '@joyautomation/nautilus-hmi-3d';

	let {
		value,
		parts,
		picked = null,
		name,
		words = false,
		onchange
	}: {
		value: ScenePoint | string | undefined;
		/** The model's named parts. */
		parts: string[];
		/** The picked part, offered first in the part menu. */
		picked?: string | null;
		/** The aria-label stem: `port at`, `label`. */
		name: string;
		/** Whether a bare anchor word is a valid form (labelAt: `"top"`). */
		words?: boolean;
		onchange: (p: ScenePoint | string) => void;
	} = $props();

	type Mode = 'numbers' | 'bounds' | 'part';
	const mode = $derived<Mode>(value === undefined || Array.isArray(value) ? 'numbers' : typeof value === 'string' || !value.part ? 'bounds' : 'part');
	const vec = $derived<Vec3>(Array.isArray(value) ? value : [0, 0, 0]);
	const box = $derived<BoxPoint>(typeof value === 'string' ? { anchor: value } : isBoxPoint(value) ? value : { anchor: 'top' });
	const partList = $derived([...(picked && parts.includes(picked) ? [picked] : []), ...parts.filter((p) => p !== picked)]);
	const AXES = ['x', 'y', 'z'] as const;
	const fmt = (v: number | undefined) => (v === undefined ? '' : String(Math.round(v * 1000) / 1000));
	const PAIRS = ['top-front', 'top-back', 'top-left', 'top-right', 'bottom-front', 'bottom-back', 'bottom-left', 'bottom-right', 'front-left', 'front-right', 'back-left', 'back-right'];

	/** A box point as the file wants it: the word alone when it may be. */
	function emitBox(b: BoxPoint) {
		const clean: BoxPoint = { anchor: b.anchor };
		if (b.part) clean.part = b.part;
		if (b.offset && b.offset.some((c) => c !== 0)) clean.offset = b.offset;
		onchange(words && !clean.part && !clean.offset ? clean.anchor : clean);
	}
	function setMode(e: Event) {
		const m = (e.currentTarget as HTMLSelectElement).value as Mode;
		if (m === 'numbers') onchange(vec);
		else if (m === 'bounds') emitBox({ anchor: box.anchor, offset: box.offset });
		else emitBox({ part: box.part ?? picked ?? partList[0] ?? '', anchor: box.anchor, offset: box.offset });
	}
	function setNum(e: Event, i: number) {
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		const v = raw === '' ? 0 : Number(raw);
		if (!isFinite(v)) return;
		const next: Vec3 = [...vec] as Vec3;
		next[i] = v;
		onchange(next);
	}
	function setAnchor(e: Event) {
		emitBox({ ...box, anchor: (e.currentTarget as HTMLSelectElement).value });
	}
	function setPart(e: Event) {
		emitBox({ ...box, part: (e.currentTarget as HTMLSelectElement).value });
	}
	function setOffset(e: Event, i: number) {
		const raw = (e.currentTarget as HTMLInputElement).value.trim();
		const v = raw === '' ? 0 : Number(raw);
		if (!isFinite(v)) return;
		const next: Vec3 = [...(box.offset ?? [0, 0, 0])] as Vec3;
		next[i] = v;
		emitBox({ ...box, offset: next });
	}
</script>

<div class="point" data-kind="point" data-mode={mode}>
	<select class="nx-input mode" value={mode} onchange={setMode} aria-label="{name} mode" title="Metres, or a place on the bounds or on a part's box (survives a re-export)">
		<option value="numbers">metres</option>
		<option value="bounds">on the bounds</option>
		<option value="part" disabled={!parts.length}>on a part</option>
	</select>
	{#if mode === 'numbers'}
		{#each AXES as a, i (a)}
			<input class="nx-input num" type="number" step="0.01" value={fmt(vec[i])} onchange={(e) => setNum(e, i)} aria-label="{name} {a}" title="{a} m" />
		{/each}
	{:else}
		<select class="nx-input anchor" value={box.anchor} onchange={setAnchor} aria-label="{name} anchor" title="Where on the box">
			{#each ANCHOR_WORDS as w (w)}<option value={w}>{w}</option>{/each}
			{#each PAIRS as w (w)}<option value={w}>{w}</option>{/each}
		</select>
		{#if mode === 'part'}
			<select class="nx-input part" value={box.part ?? ''} onchange={setPart} aria-label="{name} part" title="The part whose box">
				{#if box.part && !parts.includes(box.part)}<option value={box.part}>{box.part} (not in model)</option>{/if}
				{#each partList as p (p)}<option value={p}>{p}</option>{/each}
			</select>
		{/if}
		<span class="off" title="Offset, metres">
			{#each AXES as a, i (a)}
				<input class="nx-input num" type="number" step="0.01" value={fmt(box.offset?.[i])} onchange={(e) => setOffset(e, i)} aria-label="{name} offset {a}" placeholder="0" title="+{a} m" />
			{/each}
		</span>
	{/if}
</div>

<style>
	.point {
		display: flex;
		flex-wrap: wrap;
		gap: 3px;
		align-items: center;
	}
	.nx-input {
		box-sizing: border-box;
		font-size: 12px;
		padding: 2px 4px;
	}
	.mode {
		width: 108px;
	}
	.anchor {
		width: 96px;
	}
	.part {
		flex: 1;
		min-width: 80px;
		font-family: var(--nx-mono);
	}
	.num {
		width: 52px;
		font-family: var(--nx-mono);
	}
	.off {
		display: inline-flex;
		gap: 3px;
	}
</style>
