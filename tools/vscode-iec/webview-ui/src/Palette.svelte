<script lang="ts">
	// Instruction palette: pick a template, fill its fields here, Insert posts
	// an insertStatement op — Go validates the fragment before it touches the
	// file, and focus never leaves the diagram. Name-like fields complete
	// against declared tags (and function/type vocabularies) — free text with
	// suggestions, never a gate.
	//
	// "function block" opens the block picker (the ladder's, embedded): the
	// catalog `naut fbd graph` sends — the standard blocks, PID included,
	// then every user FUNCTION_BLOCK in scope — inserted as
	// `inst : TYPE(pin := _, …)`, every input an open pin to wire.
	import { postOp, type FbdEditOp } from './vscodeApi';
	import type { VarDecl } from './layout';
	import type { LdFbType } from './ladder';
	import Popover from './Popover.svelte';
	import Suggest from './Suggest.svelte';
	import LdBlockPicker from './LdBlockPicker.svelte';
	import { TYPES, fbCatalog, functionItems, fbOutputRefs, openArgs, type FbCatalogType, type FbInst, type SuggestItem } from './suggest';

	let {
		open = $bindable(false),
		vars = [],
		fbTypes = [],
		funcs = [],
		insts = [],
		taken = new Set<string>(),
		net = undefined,
		tags = []
	}: {
		open?: boolean;
		vars?: VarDecl[];
		/** The project manifest's tags: in scope without a declaration
		 * (#177/#210), so the name fields offer them too. */
		tags?: { name: string; type?: string }[];
		/** The block catalog from the model (empty from an older CLI). */
		fbTypes?: LdFbType[];
		/** The project's FUNCTIONs (the model's `funcs`): the function field
		 * offers them before the standard functions. */
		funcs?: FbCatalogType[];
		/** The FB instances on the diagram: their outputs are sources. */
		insts?: FbInst[];
		/** Every name in use (lowercased): a fresh instance avoids them. */
		taken?: Set<string>;
		/** The network a band-header click picked (n:K): inserts land at its
		 * end, a new network goes after it. Unset: the end of the body. */
		net?: string;
	} = $props();

	// What a field completes against: declared tags (optionally comma-lists),
	// a source (tags plus FB instance outputs, lic.CV), callable functions,
	// FB types, or declarable types.
	type Kind = 'tags' | 'tags-multi' | 'src' | 'fn' | 'fbtype' | 'type';
	type Field = { key: string; def: string; kind?: Kind };
	type Template = {
		label: string;
		preview: string;
		fields: Field[];
		// Either a netlist statement (insertStatement) or a custom op.
		build?: (f: Record<string, string>) => string;
		op?: (f: Record<string, string>) => FbdEditOp;
		/** Opens the block picker instead of a field form. */
		picker?: boolean;
	};
	const TEMPLATES: Template[] = [
		{ label: 'function block', preview: 'inst : PID(…)', fields: [], picker: true },
		{
			// A numbered network (#207): the statements after it, up to the
			// next one, run as one network, in network order.
			label: 'network',
			preview: "NETWORK 'title'",
			fields: [{ key: 'title', def: '' }],
			op: (f) => ({ type: 'addNetwork', text: f.title.trim() })
		},
		{
			label: 'block → wire',
			preview: 'w = AND(a, b)',
			fields: [
				{ key: 'name', def: 'w1' },
				{ key: 'function', def: 'AND', kind: 'fn' },
				{ key: 'inputs', def: 'in1, in2', kind: 'tags-multi' }
			],
			build: (f) => `${f.name} = ${f.function}(${f.inputs})`
		},
		{
			label: 'coil (assign output)',
			preview: 'Out := src',
			fields: [
				{ key: 'output', def: 'Output', kind: 'tags' },
				{ key: 'source', def: 'source', kind: 'src' }
			],
			build: (f) => `${f.output} := ${f.source}`
		},
		{
			label: 'timer',
			preview: 't : TON(…)',
			fields: [
				{ key: 'name', def: 't1' },
				{ key: 'type', def: 'TON', kind: 'fbtype' },
				{ key: 'IN', def: 'condition', kind: 'tags' },
				{ key: 'PT', def: 'T#1S' }
			],
			build: (f) => `${f.name} : ${f.type}(IN := ${f.IN}, PT := ${f.PT})`
		},
		{
			label: 'counter',
			preview: 'c : CTU(…)',
			fields: [
				{ key: 'name', def: 'c1' },
				{ key: 'CU', def: 'count', kind: 'tags' },
				{ key: 'R', def: 'reset', kind: 'tags' },
				{ key: 'PV', def: '10' }
			],
			build: (f) => `${f.name} : CTU(CU := ${f.CU}, R := ${f.R}, PV := ${f.PV})`
		},
		{
			label: 'comment',
			preview: '// note',
			fields: [{ key: 'text', def: 'note' }],
			build: (f) => '// ' + f.text
		},
		{
			label: 'input reference (bare)',
			preview: 'chip: name →',
			fields: [{ key: 'name', def: 'Tag1', kind: 'tags' }],
			// A ghost layout entry: the chip exists on the canvas only until a
			// wire makes it real netlist text.
			op: (f) => ({ type: 'setLayout', entries: [{ node: 'g:in.' + f.name, x: 40, y: 40 }] })
		},
		{
			// With a source (an FB output: lic.CV) it is the coil, written in
			// one gesture; left empty it is a bare chip to drop a wire on.
			label: 'output reference',
			preview: '→ coil: name [:= lic.CV]',
			fields: [
				{ key: 'name', def: 'Out1', kind: 'tags' },
				{ key: 'source', def: '', kind: 'src' }
			],
			op: (f) =>
				f.source.trim()
					? { type: 'insertStatement', text: `${f.name} := ${f.source.trim()}` }
					: { type: 'setLayout', entries: [{ node: 'g:out.' + f.name, x: 240, y: 40 }] }
		},
		{
			label: 'variable (external tag)',
			preview: 'name : REAL',
			fields: [
				{ key: 'name', def: 'Tag1' },
				{ key: 'type', def: 'REAL', kind: 'type' }
			],
			op: (f) => ({ type: 'declareVar', newName: f.name, value: f.type, text: 'VAR_EXTERNAL' })
		},
		{
			label: 'local variable (retained)',
			preview: 'VAR name : REAL',
			fields: [
				{ key: 'name', def: 'local1' },
				{ key: 'type', def: 'REAL', kind: 'type' }
			],
			op: (f) => ({ type: 'declareVar', newName: f.name, value: f.type, text: 'VAR' })
		}
	];

	const tagItems = $derived.by<SuggestItem[]>(() => {
		const items: SuggestItem[] = vars.map((v) => ({ name: v.name, detail: v.type }));
		const have = new Set(vars.map((v) => v.name.toLowerCase()));
		for (const t of tags) {
			if (!have.has(t.name.toLowerCase())) items.push({ name: t.name, detail: `${t.type ?? ''} · manifest`.trim() });
		}
		return items;
	});
	const catalog = $derived(fbCatalog(fbTypes));
	const fnItems = $derived(functionItems(funcs));
	// The picker's args field shows exactly what goes in: an older CLI's
	// catalog (no args) gets the same open-pin rule here.
	const pickerTypes = $derived(catalog.map((t) => ({ ...t, args: openArgs(t) })));
	const srcItems = $derived<SuggestItem[]>([...fbOutputRefs(insts), ...tagItems]);
	function itemsFor(kind: Kind): SuggestItem[] {
		switch (kind) {
			case 'tags':
			case 'tags-multi':
				return tagItems;
			case 'src':
				return srcItems;
			case 'fn':
				return fnItems;
			case 'fbtype':
				return catalog.map((t) => ({ name: t.name, detail: t.detail }));
			case 'type':
				return TYPES;
		}
	}

	let active = $state<Template | null>(null);
	let values = $state<Record<string, string>>({});

	function pick(t: Template) {
		active = t;
		values = Object.fromEntries(t.fields.map((f) => [f.key, f.def]));
	}

	// With a network picked, statements land at its end and a new network
	// follows it.
	function into(op: FbdEditOp): FbdEditOp {
		if (!net) return op;
		if (op.type === 'insertStatement' || op.type === 'addNetwork') return { ...op, node: net };
		return op;
	}

	// ── the function-block picker ──────────────────────────────────────────
	function freeInst(prefix: string): string {
		let n = 1;
		while (taken.has((prefix + n).toLowerCase())) n++;
		return prefix + n;
	}
	function insertBlock(v: { type: string; inst: string; args: string }) {
		const known = catalog.find((t) => t.name.toLowerCase() === v.type.toLowerCase());
		// An emptied args field still gets the open pins — a call needs an
		// argument list to be a block at all.
		const args = v.args || (known ? openArgs(known) : '');
		postOp(into({ type: 'insertStatement', text: `${v.inst} : ${v.type}(${args})` }));
		open = false;
		active = null;
	}
	// The picker's hint: what to wire, and how the instance's outputs read
	// once it lands — the output reference template's source.
	function blockHint(t: LdFbType, inst: string): string {
		const name = inst || '<inst>';
		const outs = (t.pins ?? []).filter((p) => p.dir === 'out').map((p) => `${name}.${p.name}`);
		const ins = (t.pins ?? []).filter((p) => p.dir !== 'out').length;
		const parts: string[] = [];
		if (ins) parts.push(`${ins} input pin${ins === 1 ? '' : 's'} open (_) — drag a tag onto each`);
		if (outs.length) parts.push(`outputs: ${outs.join(', ')}`);
		return parts.join(' · ');
	}

	function commit() {
		if (!active || active.picker) return;
		postOp(into(active.op ? active.op(values) : { type: 'insertStatement', text: active.build!(values) }));
		open = false;
		active = null;
	}
	function keydown(ev: KeyboardEvent) {
		ev.stopPropagation();
		if (ev.key === 'Enter') commit();
		// Reaching here at all means no suggestion list swallowed this Escape
		// first (Suggest.svelte stops propagation when its own dropdown is
		// open) — so on every field, plain or suggesting, an Escape that gets
		// this far closes the form outright, same as Escape does one level up
		// while still browsing templates. Stepping back to the template list
		// without losing the popover is still available via the explicit
		// "back" button.
		if (ev.key === 'Escape') {
			open = false;
			active = null;
		}
	}
</script>

{#if open}
	<Popover onkeydown={keydown}>
		{#if net && !active}
			<div class="target" data-kind="net-target">inserting into network {net.slice(2)}</div>
		{/if}
		{#if !active}
			{#each TEMPLATES as t (t.label)}
				<button class="item" data-kind="chip" data-id={t.label} onclick={() => pick(t)}>
					<span>{t.label}</span>
					<code>{t.preview}</code>
				</button>
			{/each}
		{:else if active.picker}
			<LdBlockPicker
				types={pickerTypes}
				{freeInst}
				embedded
				hint={blockHint}
				onInsert={insertBlock}
				onClose={() => (active = null)}
			/>
		{:else}
			{#each active.fields as f (f.key)}
				<label class="field">
					<span>{f.key}</span>
					{#if f.kind}
						<Suggest
							cls="grow"
							bind:value={values[f.key]}
							items={itemsFor(f.kind)}
							multi={f.kind === 'tags-multi'}
						/>
					{:else}
						<input class="nx-input grow" spellcheck="false" bind:value={values[f.key]} />
					{/if}
				</label>
			{/each}
			<div class="actions">
				<button onclick={() => (active = null)}>back</button>
				<button class="primary" onclick={commit}>insert</button>
			</div>
		{/if}
	</Popover>
{/if}

<style>
	.item {
		display: flex;
		justify-content: space-between;
		gap: 12px;
		background: transparent;
		border: none;
		color: var(--nx-ui-ink);
		padding: 5px 8px;
		border-radius: 3px;
		cursor: pointer;
		font-size: 12px;
		text-align: left;
	}
	.target {
		font-size: 10px;
		color: var(--nx-accent);
		padding: 2px 8px 4px;
	}
	.item:hover {
		background: var(--nx-hover);
	}
	.item code {
		font-family: var(--nx-mono);
		font-size: 10px;
		color: var(--nx-muted);
	}
	.field {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 3px 8px;
		font-size: 11px;
		color: var(--nx-muted);
	}
	.field span {
		min-width: 64px;
	}
	.field :global(.grow) {
		flex: 1;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 6px;
		padding: 6px 8px 4px;
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
</style>
