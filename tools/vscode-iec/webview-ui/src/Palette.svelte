<script lang="ts">
	// Instruction palette: pick a template, fill its fields here, Insert posts
	// an insertStatement op — Go validates the fragment before it touches the
	// file, and focus never leaves the diagram.
	import { postOp, type FbdEditOp } from './vscodeApi';

	let { open = $bindable(false) }: { open?: boolean } = $props();

	type Template = {
		label: string;
		preview: string;
		fields: [string, string][];
		// Either a netlist statement (insertStatement) or a custom op.
		build?: (f: Record<string, string>) => string;
		op?: (f: Record<string, string>) => FbdEditOp;
	};
	const TEMPLATES: Template[] = [
		{
			label: 'block → wire',
			preview: 'w = AND(a, b)',
			fields: [
				['name', 'w1'],
				['function', 'AND'],
				['inputs', 'in1, in2']
			],
			build: (f) => `${f.name} = ${f.function}(${f.inputs})`
		},
		{
			label: 'coil (assign output)',
			preview: 'Out := src',
			fields: [
				['output', 'Output'],
				['source', 'source']
			],
			build: (f) => `${f.output} := ${f.source}`
		},
		{
			label: 'timer',
			preview: 't : TON(…)',
			fields: [
				['name', 't1'],
				['type', 'TON'],
				['IN', 'condition'],
				['PT', 'T#1S']
			],
			build: (f) => `${f.name} : ${f.type}(IN := ${f.IN}, PT := ${f.PT})`
		},
		{
			label: 'counter',
			preview: 'c : CTU(…)',
			fields: [
				['name', 'c1'],
				['CU', 'count'],
				['R', 'reset'],
				['PV', '10']
			],
			build: (f) => `${f.name} : CTU(CU := ${f.CU}, R := ${f.R}, PV := ${f.PV})`
		},
		{
			label: 'comment',
			preview: '// note',
			fields: [['text', 'note']],
			build: (f) => '// ' + f.text
		},
		{
			label: 'input reference (bare)',
			preview: 'chip: name →',
			fields: [['name', 'Tag1']],
			// A ghost layout entry: the chip exists on the canvas only until a
			// wire makes it real netlist text.
			op: (f) => ({ type: 'setLayout', entries: [{ node: 'g:in.' + f.name, x: 40, y: 40 }] })
		},
		{
			label: 'output reference (bare)',
			preview: '→ coil: name',
			fields: [['name', 'Out1']],
			op: (f) => ({ type: 'setLayout', entries: [{ node: 'g:out.' + f.name, x: 240, y: 40 }] })
		},
		{
			label: 'variable (external tag)',
			preview: 'name : REAL',
			fields: [
				['name', 'Tag1'],
				['type', 'REAL']
			],
			op: (f) => ({ type: 'declareVar', newName: f.name, value: f.type, text: 'VAR_EXTERNAL' })
		},
		{
			label: 'local variable (retained)',
			preview: 'VAR name : REAL',
			fields: [
				['name', 'local1'],
				['type', 'REAL']
			],
			op: (f) => ({ type: 'declareVar', newName: f.name, value: f.type, text: 'VAR' })
		}
	];

	let active = $state<Template | null>(null);
	let values = $state<Record<string, string>>({});

	function pick(t: Template) {
		active = t;
		values = Object.fromEntries(t.fields);
	}
	function commit() {
		if (!active) return;
		postOp(active.op ? active.op(values) : { type: 'insertStatement', text: active.build!(values) });
		open = false;
		active = null;
	}
	function keydown(ev: KeyboardEvent) {
		ev.stopPropagation();
		if (ev.key === 'Enter') commit();
		if (ev.key === 'Escape') {
			if (active) active = null;
			else open = false;
		}
	}
</script>

{#if open}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="menu" onclick={(e) => e.stopPropagation()} onkeydown={keydown}>
		{#if !active}
			{#each TEMPLATES as t (t.label)}
				<button class="item" onclick={() => pick(t)}>
					<span>{t.label}</span>
					<code>{t.preview}</code>
				</button>
			{/each}
		{:else}
			{#each active.fields as [key] (key)}
				<label class="field">
					<span>{key}</span>
					<input spellcheck="false" bind:value={values[key]} />
				</label>
			{/each}
			<div class="actions">
				<button onclick={() => (active = null)}>back</button>
				<button class="primary" onclick={commit}>insert</button>
			</div>
		{/if}
	</div>
{/if}

<style>
	.menu {
		position: absolute;
		right: 8px;
		top: 30px;
		z-index: 20;
		display: flex;
		flex-direction: column;
		min-width: 250px;
		background: var(--vscode-editorWidget-background, #252526);
		border: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.4));
		border-radius: 5px;
		padding: 4px;
		box-shadow: 0 4px 14px rgba(0, 0, 0, 0.35);
	}
	.item {
		display: flex;
		justify-content: space-between;
		gap: 12px;
		background: transparent;
		border: none;
		color: var(--vscode-foreground, #ccc);
		padding: 5px 8px;
		border-radius: 3px;
		cursor: pointer;
		font-size: 12px;
		text-align: left;
	}
	.item:hover {
		background: var(--vscode-list-hoverBackground, rgba(128, 128, 128, 0.15));
	}
	.item code {
		font-family: var(--vscode-editor-font-family, monospace);
		font-size: 10px;
		color: var(--vscode-descriptionForeground, #888);
	}
	.field {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 3px 8px;
		font-size: 11px;
		color: var(--vscode-descriptionForeground, #888);
	}
	.field span {
		min-width: 64px;
	}
	.field input {
		flex: 1;
		font-family: var(--vscode-editor-font-family, monospace);
		font-size: 12px;
		padding: 2px 6px;
		border-radius: 3px;
		background: var(--vscode-input-background, #3c3c3c);
		color: var(--vscode-input-foreground, #ccc);
		border: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.4));
		outline: none;
	}
	.field input:focus {
		border-color: var(--vscode-focusBorder, #58a6ff);
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
		border: 1px solid var(--vscode-editorWidget-border, rgba(128, 128, 128, 0.4));
		background: transparent;
		color: var(--vscode-foreground, #ccc);
		cursor: pointer;
		font-size: 12px;
	}
	.actions button.primary {
		background: var(--vscode-button-background, #2ea043);
		color: var(--vscode-button-foreground, #fff);
		border-color: transparent;
	}
</style>
