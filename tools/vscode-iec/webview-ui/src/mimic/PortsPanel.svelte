<script lang="ts">
	// Ports side panel for the standalone *.component.json editor
	// (ComponentApp.svelte) AND the mimic editor's ports-mode (via
	// EditorCanvas.svelte -> its own instance of this panel) — styled like
	// the mimic editor's PropsPanel (same --nx-* tokens, same
	// .nx-input/.del idioms). One row per port: a name text input (rename),
	// the x/y fraction readout, a dir selector (auto/left/right/up/down —
	// "auto" shows what inferPortDir resolves the port's position to, or
	// "none" for a corner/interior port with no inferred direction), and a
	// delete button; an "Add port" button appends one at the first free
	// quarter-position slot on the outline (portsGestures.ts's
	// newPortAtFreeSlot). Clicking a row selects the matching dot on the
	// canvas and vice versa (the `selected` prop is shared state, not owned
	// here). Every edit commits through the parent's whole-list op path, so
	// it's undoable exactly like a canvas gesture.
	import { inferPortDir, type PortDir } from '@joyautomation/nautilus-hmi/mimic';
	import { fmtFraction, type Port } from './portsGestures';

	let {
		ports,
		selected,
		onSelect,
		onRename,
		onDelete,
		onAdd,
		onSetDir
	}: {
		ports: Port[];
		selected: number | null;
		onSelect: (i: number | null) => void;
		/** Returns false (and the input reverts) when the name is empty or
		 * already taken by another port. */
		onRename: (i: number, name: string) => boolean;
		onDelete: (i: number) => void;
		onAdd: () => void;
		/** `dir: null` clears the override back to "auto" (inferred). */
		onSetDir: (i: number, dir: PortDir | null) => void;
	} = $props();

	function rename(i: number, e: Event & { currentTarget: HTMLInputElement }) {
		if (!onRename(i, e.currentTarget.value)) e.currentTarget.value = ports[i].name;
	}

	const DIRS: PortDir[] = ['left', 'right', 'up', 'down'];
	const autoLabel = (port: Port): string => {
		const inferred = inferPortDir(port);
		return inferred ? `auto (${inferred})` : 'auto (none)';
	};
</script>

<aside aria-label="Ports">
	<h4>Ports <span class="dim">· {ports.length}</span></h4>
	{#if ports.length === 0}
		<p class="note">No connection points yet — double-click the outline, or add one below.</p>
	{/if}
	{#each ports as port, i (i)}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div class="row" class:sel={selected === i} data-id={port.name} data-kind="port" onpointerdown={() => onSelect(i)}>
			<input
				class="nx-input name"
				data-id={port.name}
				data-kind="port-name"
				value={port.name}
				onchange={(e) => rename(i, e)}
				onclick={(e) => e.stopPropagation()}
			/>
			<span class="coord">{fmtFraction(port.x, port.y)}</span>
			<button
				class="del"
				data-id={port.name}
				data-kind="port-delete"
				title="Delete {port.name}"
				onclick={(e) => {
					e.stopPropagation();
					onDelete(i);
				}}
			>
				✕
			</button>
			<select
				class="nx-input dir"
				title="Exit direction — the direction a pipe leaves this port"
				value={port.dir ?? ''}
				onclick={(e) => e.stopPropagation()}
				onchange={(e) => onSetDir(i, (e.currentTarget.value || null) as PortDir | null)}
			>
				<option value="">{autoLabel(port)}</option>
				{#each DIRS as d (d)}
					<option value={d}>{d}</option>
				{/each}
			</select>
		</div>
	{/each}
	<button class="addbtn" data-kind="port-add" onclick={onAdd}>+ Add port</button>
</aside>

<style>
	aside {
		flex: none;
		width: 200px;
		overflow-y: auto;
		border-left: 1px solid var(--nx-border);
		padding: 10px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	h4 {
		margin: 0 0 2px;
		font-size: 12px;
		color: var(--nx-ink);
	}
	.dim {
		color: var(--nx-muted);
		font-weight: 400;
		font-family: var(--nx-mono);
	}
	.note {
		margin: 0;
		font-size: 11px;
		color: var(--nx-muted);
	}
	.row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		grid-template-rows: auto auto auto;
		gap: 2px 6px;
		padding: 5px 6px;
		border: 1px solid var(--nx-border);
		border-radius: 5px;
		cursor: pointer;
	}
	.row:hover {
		background: var(--nx-hover);
	}
	.row.sel {
		border-color: var(--nx-sel-ink);
		background: var(--nx-sel-bg);
	}
	.row :global(.nx-input.name) {
		grid-column: 1;
		grid-row: 1;
		width: 100%;
		min-width: 0;
	}
	.row .del {
		grid-column: 2;
		grid-row: 1;
		border: none;
		background: transparent;
		color: var(--nx-err);
		cursor: pointer;
		font-size: 12px;
		line-height: 1;
		padding: 2px 4px;
		border-radius: 3px;
	}
	.row .del:hover {
		background: var(--nx-err-bg);
	}
	.row .coord {
		grid-column: 1 / -1;
		grid-row: 2;
		font-size: 10px;
		font-family: var(--nx-mono);
		color: var(--nx-muted);
	}
	.row :global(.nx-input.dir) {
		grid-column: 1 / -1;
		grid-row: 3;
		width: 100%;
		min-width: 0;
		font-size: 11px;
	}
	.addbtn {
		margin-top: 2px;
		padding: 4px 8px;
		border: 1px solid var(--nx-border);
		border-radius: 5px;
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font: inherit;
		font-size: 12px;
	}
	.addbtn:hover {
		background: var(--nx-hover);
	}
</style>
