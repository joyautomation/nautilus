<script lang="ts">
	// The variables panel: every header declaration, referenced or not — the
	// diagram itself only draws what the logic wires up, so this is where a
	// freshly declared (still unused) variable is visible. Live values ride
	// the same store as the node pills. The footer row declares in place —
	// no round-trip through the "+ add" palette.
	import type { VarDecl } from './layout';
	import Popover from './Popover.svelte';
	import Suggest from './Suggest.svelte';
	import { TYPES } from './suggest';
	import { live, liveValue, liveMissing, liveForced, formatLive } from './liveState.svelte';
	import { postOp } from './vscodeApi';

	// The panel is editor-agnostic: FBD and LD share it, differing only in
	// how a declare/delete lands (fbd edit vs ld edit ops) — so those are
	// injectable, defaulting to the FBD shapes.
	//
	// A ladder file may define FUNCTION_BLOCKs next to its PROGRAM: each is
	// its own scope (`scopes`), listed under its own heading, and a block's
	// scope declares its pins (VAR_INPUT / VAR_OUTPUT / VAR_IN_OUT, the
	// AOI's Parameters tab) as well as locals. Block instances a rung
	// declares by its call (`m1:MotorStarter(…)`) list read-only (`insts`).
	type Scope = { pou: string; label: string; sections: string[] };
	type Inst = { name: string; type: string; rung: string; pou?: string };
	let {
		open = $bindable(false),
		vars,
		used,
		insts = [],
		scopes = [{ pou: '', label: '', sections: ['VAR_EXTERNAL', 'VAR'] }],
		onDeclare = (name: string, type: string, section: string) =>
			postOp({ type: 'declareVar', newName: name, value: type, text: section }),
		onDelete = (name: string) => postOp({ type: 'deleteVar', newName: name }),
		onRename,
		readonly = false
	}: {
		open?: boolean;
		/** List only — no declare row, no delete buttons (an L5X export). */
		readonly?: boolean;
		vars: VarDecl[];
		used: Set<string>;
		insts?: Inst[];
		scopes?: Scope[];
		onDeclare?: (name: string, type: string, section: string, pou?: string) => void;
		onDelete?: (name: string, pou?: string) => void;
		/** Double-click a name to rename it (and its references); absent: no rename. */
		onRename?: (name: string, newName: string, pou?: string) => void;
	} = $props();

	const SECTION_BADGE: Record<string, string> = {
		VAR_EXTERNAL: 'ext',
		VAR: 'local',
		VAR_INPUT: 'in',
		VAR_OUTPUT: 'out',
		VAR_IN_OUT: 'in/out'
	};
	const SECTION_TITLE: Record<string, string> = {
		VAR_EXTERNAL: 'external tag (VAR_EXTERNAL)',
		VAR: 'retained local (VAR)',
		VAR_INPUT: 'input pin (VAR_INPUT)',
		VAR_OUTPUT: 'output pin (VAR_OUTPUT)',
		VAR_IN_OUT: 'in-out pin (VAR_IN_OUT)'
	};

	// Headings only when there is more than the one plain program scope.
	const grouped = $derived(scopes.length > 1 || scopes.some((s) => s.pou !== ''));
	const total = $derived(vars.length + insts.length);
	const varsOf = (pou: string) => vars.filter((v) => (v.pou ?? '') === pou);
	const instsOf = (pou: string) => insts.filter((i) => (i.pou ?? '') === pou);

	let newName = $state('');
	let newType = $state('REAL');
	let scopeIdx = $state(0);
	const scope = $derived(scopes[Math.min(scopeIdx, scopes.length - 1)] ?? scopes[0]);
	let newSection = $state('VAR_EXTERNAL');
	// A scope change resets the section to the scope's first.
	$effect(() => {
		if (scope && !scope.sections.includes(newSection)) newSection = scope.sections[0];
	});
	function nextSection() {
		const secs = scope.sections;
		newSection = secs[(secs.indexOf(newSection) + 1) % secs.length];
	}
	const nameOk = $derived(/^[A-Za-z_][A-Za-z0-9_]*$/.test(newName.trim()));
	function addVar() {
		if (!nameOk) return;
		onDeclare(newName.trim(), newType.trim() || 'REAL', newSection, scope.pou || undefined);
		newName = '';
	}

	// In-place rename (double-click a name).
	let renaming = $state<{ name: string; pou: string } | null>(null);
	let renameText = $state('');
	function startRename(v: VarDecl) {
		if (readonly || !onRename) return;
		renaming = { name: v.name, pou: v.pou ?? '' };
		renameText = v.name;
	}
	function commitRename() {
		const r = renaming;
		renaming = null;
		const to = renameText.trim();
		if (!r || !onRename || to === r.name || !/^[A-Za-z_][A-Za-z0-9_]*$/.test(to)) return;
		onRename(r.name, to, r.pou || undefined);
	}
	function focusSelect(el: HTMLInputElement) {
		el.focus();
		el.select();
	}

	// Escape closes the panel — but not from a field of the add row, where
	// it dismisses the type suggestions (and a script's Escape after typing a
	// type must leave the row to click +), nor from a rename, which it
	// cancels.
	function onWindowKey(ev: KeyboardEvent) {
		if (open && ev.key === 'Escape' && !ev.defaultPrevented) {
			open = false;
			renaming = null;
		}
	}
</script>

{#if open}
	<Popover style="min-width: 300px; max-width: 420px; font-size: 12px">
		<div class="head">
			<span>variables</span>
			<span class="count">{total}</span>
		</div>
		<!-- Only the list scrolls: the add row stays put and its type
		     dropdown can overflow the card instead of being clipped. -->
		<div class="rows">
			{#if total === 0}
				<div class="empty">no declarations{readonly ? '' : ' — add one below'}</div>
			{/if}
			{#each scopes as sc (sc.pou)}
				{#if grouped}
					<div class="scopehead" data-kind="scope" data-id={sc.pou || 'program'}>{sc.label}</div>
				{/if}
				{#each varsOf(sc.pou) as v (v.section + ':' + v.name)}
					{@const val = sc.pou ? undefined : liveValue(v.name)}
					<div class="row" data-kind="chip" data-id={v.name} data-section={v.section} data-pou={sc.pou} title="line {v.line}{used.has(v.name.toLowerCase()) ? '' : ' — declared but not referenced by the logic; it appears in the diagram once something reads or writes it'}{onRename && !readonly ? ' · dblclick the name: rename it everywhere' : ''}">
						<span class="badge {v.section === 'VAR_EXTERNAL' ? 'ext' : ''}">{SECTION_BADGE[v.section] ?? v.section}</span>
						{#if renaming && renaming.name === v.name && renaming.pou === sc.pou}
							<!-- svelte-ignore a11y_autofocus -->
							<input
								class="nx-input rename"
								spellcheck="false"
								bind:value={renameText}
								use:focusSelect
								onkeydown={(e) => {
									e.stopPropagation();
									if (e.key === 'Enter') commitRename();
									else if (e.key === 'Escape') {
										e.preventDefault();
										renaming = null;
									}
								}}
								onblur={commitRename}
							/>
						{:else}
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<span class="name" class:renamable={!!onRename && !readonly} ondblclick={() => startRename(v)}>{v.name}</span>
						{/if}
						<span class="type">: {v.type}{v.init ? ` := ${v.init}` : ''}</span>
						<span class="spacer"></span>
						{#if val !== undefined}
							<span class="nx-pill val" class:off={!live.fresh} class:forced={liveForced(v.name)}>{formatLive(val)}</span>
						{/if}
						{#if v.section === 'VAR_EXTERNAL' && !sc.pou && liveMissing(v.name)}
							<span
								class="notag"
								title="No '{v.name}' tag on the controller — seed it in the runtime, drive it from a driver or the HMI, or write it from logic. A READ of a tag that was never written faults the scan."
							>no tag</span>
						{/if}
						{#if !used.has(v.name.toLowerCase())}
							<span class="unused">unused</span>
						{/if}
						{#if !readonly}
							<button
								class="del"
								data-kind="chip"
								data-id="del:{v.name}"
								title="Delete this declaration (references it still has become diagnostics)"
								onclick={() => onDelete(v.name, sc.pou || undefined)}
							>×</button>
						{/if}
					</div>
				{/each}
				{#each instsOf(sc.pou) as i (i.name)}
					{@const q = sc.pou ? undefined : liveValue(i.name)}
					<div class="row instrow" data-kind="chip" data-id={i.name} data-section="instance" data-pou={sc.pou} title="a {i.type} instance, declared by its call on rung {i.rung} — rename it by double-clicking the block's name on the rung">
						<span class="badge inst">inst</span>
						<span class="name">{i.name}</span>
						<span class="type">: {i.type}</span>
						<span class="spacer"></span>
						{#if q !== undefined && typeof q !== 'object'}
							<span class="nx-pill val" class:off={!live.fresh}>{formatLive(q)}</span>
						{/if}
						<span class="by">rung {i.rung}</span>
					</div>
				{/each}
			{/each}
		</div>
		{#if !readonly}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="addrow"
			onkeydown={(e) => {
				e.stopPropagation();
				if (e.key === 'Enter') addVar();
			}}
		>
			{#if scopes.length > 1}
				<select class="scopesel" title="declare in" bind:value={scopeIdx}>
					{#each scopes as sc, i (sc.pou)}
						<option value={i}>{sc.pou || 'program'}</option>
					{/each}
				</select>
			{/if}
			<button
				class="badge toggle"
				class:ext={newSection === 'VAR_EXTERNAL'}
				data-section={newSection}
				title="{SECTION_TITLE[newSection] ?? newSection} — click for {SECTION_TITLE[scope.sections[(scope.sections.indexOf(newSection) + 1) % scope.sections.length]] ?? ''}"
				onclick={nextSection}
			>{SECTION_BADGE[newSection] ?? newSection}</button>
			<input class="nx-input grow" placeholder="name" spellcheck="false" bind:value={newName} />
			<Suggest cls="typefield" bind:value={newType} items={TYPES} />
			<button class="add" disabled={!nameOk} title="Declare (Enter)" onclick={addVar}>+</button>
		</div>
		{/if}
	</Popover>
{/if}

<svelte:window onkeydown={onWindowKey} />

<style>
	.rows {
		max-height: 55vh;
		overflow-y: auto;
	}
	.addrow {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 6px 8px 4px;
		border-top: 1px solid var(--nx-border);
		margin-top: 3px;
	}
	.addrow .grow {
		flex: 1;
		min-width: 0;
	}
	.addrow :global(.typefield) {
		width: 84px;
	}
	.badge.toggle {
		background: transparent;
		cursor: pointer;
	}
	.add {
		background: transparent;
		border: 1px solid var(--nx-border);
		border-radius: 3px;
		color: var(--nx-ui-ink);
		font-size: 13px;
		line-height: 1;
		padding: 2px 7px;
		cursor: pointer;
	}
	.add:hover:enabled {
		background: var(--nx-hover);
	}
	.add:disabled {
		opacity: 0.4;
		cursor: default;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 4px 8px 6px;
		font-weight: 600;
		color: var(--nx-ui-ink);
		border-bottom: 1px solid var(--nx-border);
		margin-bottom: 3px;
	}
	.head .count {
		font-weight: 400;
		font-size: 11px;
		color: var(--nx-muted);
	}
	.empty {
		padding: 8px;
		color: var(--nx-muted);
		font-size: 11px;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 7px;
		padding: 3px 8px;
		border-radius: 3px;
		font-family: var(--nx-mono);
		cursor: default;
	}
	.row:hover {
		background: var(--nx-hover);
	}
	.badge {
		font-size: 9px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 3px;
		color: var(--nx-muted);
		border: 1px solid var(--nx-border);
		min-width: 30px;
		text-align: center;
	}
	.badge.ext {
		color: var(--nx-blue);
		border-color: color-mix(in srgb, var(--nx-blue) 55%, transparent);
	}
	.name {
		color: var(--nx-ui-ink);
	}
	.name.renamable {
		cursor: text;
	}
	.rename {
		width: 110px;
		font-family: var(--nx-mono);
	}
	.scopehead {
		padding: 6px 8px 2px;
		font-size: 10px;
		font-weight: 700;
		color: var(--nx-muted);
		letter-spacing: 0.02em;
	}
	.badge.inst {
		font-style: italic;
	}
	.instrow .name {
		color: var(--nx-muted);
	}
	.by {
		font-size: 9px;
		font-style: italic;
		color: var(--nx-muted);
	}
	.scopesel {
		max-width: 96px;
		font-size: 10px;
		background: var(--nx-panel-bg);
		color: var(--nx-ui-ink);
		border: 1px solid var(--nx-border);
		border-radius: 3px;
	}
	.type {
		color: var(--nx-muted);
		font-size: 11px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.spacer {
		flex: 1;
	}
	.val {
		font-size: 10px;
		padding: 1px 5px;
	}
	.unused {
		font-size: 9px;
		font-style: italic;
		color: var(--nx-warn);
	}
	.notag {
		font-size: 9px;
		font-weight: 700;
		padding: 1px 5px;
		border-radius: 3px;
		color: var(--nx-warn);
		border: 1px solid color-mix(in srgb, var(--nx-warn) 60%, transparent);
		cursor: help;
	}
	.del {
		background: transparent;
		border: none;
		color: var(--nx-muted);
		font-size: 13px;
		line-height: 1;
		padding: 0 3px;
		cursor: pointer;
		border-radius: 3px;
		visibility: hidden;
	}
	.row:hover .del {
		visibility: visible;
	}
	.del:hover {
		color: var(--nx-err);
		background: color-mix(in srgb, var(--nx-err) 15%, transparent);
	}
</style>
