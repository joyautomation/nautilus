<script lang="ts">
	// The ladder palette's FB picker: every block type a rung in this file
	// can instantiate — the standard blocks, then the project's own (this
	// file's and the libraries' FUNCTION_BLOCKs, the same prelude `naut
	// check` composes; `naut ld graph` sends the catalog with the model) —
	// with the new instance's name and a starting argument list, both
	// editable before the insert. Free text still wins: a type the catalog
	// doesn't know inserts too, and the diagnostic says what's wrong.
	import type { LdFbType } from './ladder';

	let {
		types = [],
		freeInst,
		onInsert,
		onClose
	}: {
		types?: LdFbType[];
		freeInst: (prefix: string) => string;
		onInsert: (v: { type: string; inst: string; args: string }) => void;
		onClose: () => void;
	} = $props();

	let filter = $state('');
	let chosen = $state<string>('');
	let inst = $state('');
	let args = $state('');
	let instTouched = false;
	let active = $state(-1);
	let filterEl = $state<HTMLInputElement | null>(null);
	let instEl = $state<HTMLInputElement | null>(null);

	const shown = $derived.by(() => {
		const t = filter.trim().toLowerCase();
		if (!t || t === chosen.toLowerCase()) return types;
		const pre = types.filter((x) => x.name.toLowerCase().startsWith(t));
		const sub = types.filter((x) => !x.name.toLowerCase().startsWith(t) && x.name.toLowerCase().includes(t));
		return pre.concat(sub);
	});
	const std = $derived(shown.filter((t) => !t.user));
	const user = $derived(shown.filter((t) => t.user));
	const ordered = $derived(std.concat(user));
	const cur = $derived(types.find((t) => t.name.toLowerCase() === chosen.toLowerCase()));
	const ident = /^[A-Za-z_][A-Za-z0-9_]*$/;
	const typeName = $derived(chosen || filter.trim());
	const canInsert = $derived(ident.test(typeName) && ident.test(inst.trim()));

	let root = $state<HTMLDivElement | null>(null);
	$effect(() => {
		filterEl?.focus();
	});
	// A press anywhere else closes it, like any dropdown (capture phase: the
	// ladder's own handlers stop propagation).
	$effect(() => {
		const away = (ev: PointerEvent) => {
			if (root && !root.contains(ev.target as Node)) onClose();
		};
		window.addEventListener('pointerdown', away, true);
		return () => window.removeEventListener('pointerdown', away, true);
	});

	function prefixOf(name: string): string {
		const t = types.find((x) => x.name.toLowerCase() === name.toLowerCase());
		if (t?.prefix) return t.prefix;
		return (/[A-Za-z]/.exec(name)?.[0] ?? 'fb').toLowerCase();
	}

	function choose(t: LdFbType, focusInst = true) {
		chosen = t.name;
		filter = t.name;
		args = t.args ?? '';
		if (!instTouched) inst = freeInst(prefixOf(t.name));
		active = -1;
		if (focusInst) {
			queueMicrotask(() => {
				instEl?.focus();
				instEl?.select();
			});
		}
	}

	function commit() {
		// A typed name the list never got to pick still inserts (free text).
		if (!chosen && ident.test(filter.trim())) {
			const known = types.find((x) => x.name.toLowerCase() === filter.trim().toLowerCase());
			if (known) choose(known, false);
			else {
				chosen = filter.trim();
				if (!instTouched) inst = freeInst(prefixOf(chosen));
			}
		}
		if (!canInsert) return;
		onInsert({ type: typeName, inst: inst.trim(), args: args.trim() });
	}

	function onFilterKey(ev: KeyboardEvent) {
		if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
			const n = ordered.length;
			if (n) active = ev.key === 'ArrowDown' ? (active + 1) % n : active <= 0 ? n - 1 : active - 1;
			ev.preventDefault();
		} else if (ev.key === 'Enter') {
			ev.preventDefault();
			const pick =
				active >= 0
					? ordered[active]
					: types.find((x) => x.name.toLowerCase() === filter.trim().toLowerCase()) ??
						(ordered.length === 1 ? ordered[0] : undefined);
			if (pick && pick.name !== chosen) choose(pick);
			else commit();
		}
	}

	function keydown(ev: KeyboardEvent) {
		// Nothing typed here may reach the ladder's shortcuts (Del, N, M, B).
		ev.stopPropagation();
		if (ev.key === 'Escape') {
			ev.preventDefault();
			onClose();
		} else if (ev.key === 'Enter' && (ev.target as HTMLElement).tagName === 'INPUT' && ev.target !== filterEl) {
			ev.preventDefault();
			commit();
		}
	}

	const pinHint = $derived.by(() => {
		if (!cur) return '';
		const parts: string[] = [];
		parts.push(cur.powerIn ? `power → ${cur.powerIn}` : 'takes no power (first on the rung)');
		if (cur.powerOut) parts.push(`${cur.powerOut} → rung`);
		const outs = (cur.pins ?? []).filter((p) => p.dir === 'out' && p.name !== cur.powerOut).map((p) => p.name);
		if (outs.length) parts.push(`capture: ${outs.map((o) => o + ' => Tag').join(', ')}`);
		const ins = (cur.pins ?? [])
			.filter((p) => p.dir === 'in' && p.name !== cur.powerIn && p.type.toUpperCase() === 'BOOL')
			.map((p) => p.name);
		if (ins.length) parts.push(`optional: ${ins.map((i) => i + ' := …').join(', ')}`);
		return parts.join(' · ');
	});
</script>

<!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events -->
<div class="fbpick" bind:this={root} onclick={(e) => e.stopPropagation()} onpointerdown={(e) => e.stopPropagation()} onkeydown={keydown}>
	<input
		class="nx-input fbfilter"
		spellcheck="false"
		placeholder="block type — TON, CTU, or a project block"
		bind:this={filterEl}
		bind:value={filter}
		oninput={() => {
			chosen = '';
			active = -1;
		}}
		onkeydown={onFilterKey}
	/>
	<div class="list">
		{#if std.length}<div class="grp">standard</div>{/if}
		{#each std as t, i (t.name)}
			<button
				class="fbitem"
				class:on={t.name === chosen}
				class:active={i === active}
				data-type={t.name}
				title="{t.name} — {t.detail ?? ''} (double-click inserts)"
				onclick={() => choose(t)}
				ondblclick={() => {
					choose(t, false);
					commit();
				}}
			><span class="nm">{t.name}</span><span class="dt">{t.detail ?? ''}</span></button>
		{/each}
		{#if user.length}<div class="grp">project</div>{/if}
		{#each user as t, i (t.name)}
			<button
				class="fbitem"
				class:on={t.name === chosen}
				class:active={std.length + i === active}
				data-type={t.name}
				title="{t.name}: power {t.detail ?? ''} (double-click inserts)"
				onclick={() => choose(t)}
				ondblclick={() => {
					choose(t, false);
					commit();
				}}
			><span class="nm">{t.name}</span><span class="dt">{t.detail ?? ''}</span></button>
		{/each}
		{#if !ordered.length}<div class="none">no block matches — insert inserts it anyway</div>{/if}
	</div>
	<label class="field"><span>instance</span>
		<input
			class="nx-input fbinst"
			spellcheck="false"
			bind:this={instEl}
			bind:value={inst}
			oninput={() => (instTouched = true)}
		/>
	</label>
	<label class="field"><span>args</span>
		<input class="nx-input fbargs" spellcheck="false" bind:value={args} placeholder="Pin := value, Out => Tag" />
	</label>
	{#if pinHint}<div class="hint">{pinHint}</div>{/if}
	<div class="actions">
		<button onclick={onClose}>cancel</button>
		<button class="primary fbinsert" disabled={!canInsert} onclick={commit}>insert</button>
	</div>
</div>

<style>
	.fbpick {
		position: absolute;
		top: calc(100% + 2px);
		left: 14px;
		z-index: 30;
		display: flex;
		flex-direction: column;
		gap: 4px;
		width: 330px;
		padding: 6px;
		background: var(--nx-panel-bg);
		border: 1px solid var(--nx-border);
		border-radius: 5px;
		box-shadow: var(--nx-shadow);
		font-family: var(--nx-mono);
		cursor: default;
	}
	.list {
		display: flex;
		flex-direction: column;
		max-height: 220px;
		overflow-y: auto;
		border: 1px solid var(--nx-border);
		border-radius: 3px;
	}
	.grp {
		font-size: 9px;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--nx-muted);
		padding: 4px 8px 1px;
	}
	.fbitem {
		display: flex;
		justify-content: space-between;
		gap: 12px;
		border: none;
		background: transparent;
		color: var(--nx-ui-ink);
		padding: 3px 8px;
		font-family: var(--nx-mono);
		font-size: 12px;
		text-align: left;
		cursor: pointer;
		white-space: nowrap;
	}
	.fbitem:hover,
	.fbitem.active {
		background: var(--nx-hover);
	}
	.fbitem.on {
		background: var(--nx-sel-bg);
		color: var(--nx-sel-ink);
	}
	.fbitem .dt {
		font-size: 10px;
		opacity: 0.65;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.none {
		font-size: 11px;
		color: var(--nx-muted);
		padding: 4px 8px;
	}
	.field {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 11px;
		color: var(--nx-muted);
	}
	.field span {
		min-width: 56px;
	}
	.field input {
		flex: 1;
	}
	.hint {
		font-size: 10px;
		color: var(--nx-muted);
		line-height: 1.35;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 6px;
	}
	.actions button {
		padding: 2px 10px;
		border-radius: 3px;
		border: 1px solid var(--nx-border);
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font-size: 12px;
	}
	.actions button.primary {
		background: var(--nx-btn-bg);
		color: var(--nx-btn-ink);
		border-color: transparent;
	}
	.actions button:disabled {
		opacity: 0.4;
		cursor: default;
	}
</style>
