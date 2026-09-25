// One table of gestures and keys per editor. The toolbar hint line and the
// "?" popover (ShortcutHelp.svelte) are both generated from these, so the
// two can't drift apart — add a gesture here and it shows up in both.

export type Shortcut = {
	/** The keys or gesture, as shown (Ctrl reads ⌘ on macOS). */
	keys: string;
	/** What it does — the popover's full description. */
	does: string;
	/** A compact form for the one-line toolbar hint; rows without one are
	 * popover-only. */
	hint?: string;
};

export type ShortcutGroup = { title: string; rows: Shortcut[] };

export const FBD_SHORTCUTS: ShortcutGroup[] = [
	{
		title: 'Select',
		rows: [
			{ keys: 'Click', does: 'Select a block, chip, coil, note or wire' },
			{ keys: 'Shift + drag', does: 'Box-select several' },
			{ keys: 'Ctrl + click', does: 'Add to or remove from the selection' },
			{ keys: 'Ctrl + A', does: 'Select everything' }
		]
	},
	{
		title: 'Edit',
		rows: [
			{ keys: 'Double-click', does: 'Edit a constant, rename a wire or instance, edit a note', hint: 'double-click: edit & rename' },
			{ keys: 'Enter', does: 'Commit an in-place edit (Ctrl + Enter in a multi-line note)' },
			{ keys: 'Esc', does: 'Cancel an in-place edit' },
			{ keys: 'Drag pin → pin', does: 'Wire an output to an input; drop on + to add an input', hint: 'drag pin→pin: wire (+ adds an input)' },
			{ keys: 'Del / Backspace', does: 'Delete the selection; a selected wire disconnects', hint: 'Del: delete / disconnect' },
			{ keys: 'Ctrl + C / X / V', does: 'Copy, cut, paste — pastes into another .fbd too', hint: 'Ctrl+C/X/V: copy cut paste' }
		]
	},
	{
		title: 'Layout & view',
		rows: [
			{ keys: 'Drag a node', does: 'Move it — the position is pinned in the file', hint: 'drag node / arrow keys: pin layout' },
			{ keys: 'Arrow keys', does: 'Move the selection (pinned like a drag)' },
			{ keys: 'Drag empty canvas', does: 'Pan' },
			{ keys: 'Scroll / pinch', does: 'Zoom (the corner buttons zoom and fit too)' }
		]
	}
];

export const LD_SHORTCUTS: ShortcutGroup[] = [
	{
		title: 'Select',
		rows: [
			{ keys: 'Click', does: 'Select an element', hint: 'click: select' },
			{ keys: "Click a rung's name", does: 'Select the whole rung', hint: "click a rung's name: select the rung" }
		]
	},
	{
		title: 'Edit',
		rows: [
			{ keys: 'Double-click', does: 'Retag a contact or coil, edit arguments, rename a rung', hint: 'dblclick: retag / edit args / rename rung' },
			{ keys: 'Enter / Esc', does: 'Commit / cancel an in-place edit' },
			{ keys: '⊕', does: 'Insert an element at that spot', hint: '⊕: insert' },
			{ keys: 'Drag a palette item', does: 'Drop it onto a rung spot' },
			{ keys: 'Drag an element', does: 'Move it to another spot or rung' },
			{ keys: 'Del / Backspace', does: 'Delete the element — or the rung, when its name is selected', hint: 'Del: delete element or rung' },
			{ keys: 'N', does: 'Toggle a contact between NO and NC', hint: 'N: NO/NC' },
			{ keys: 'M', does: 'Cycle a coil: normal → set → reset', hint: 'M: coil mode' },
			{ keys: 'B', does: 'Wrap the selection in a parallel branch', hint: 'B: branch around' },
			{ keys: 'Ctrl + C / X / V', does: 'Copy, cut, paste an element — pastes after the selection, into another ladder too', hint: 'Ctrl+C/X/V: copy cut paste' },
			{ keys: 'Esc', does: 'Cancel a drag', hint: 'Esc: cancel drag' }
		]
	}
];

export const SFC_SHORTCUTS: ShortcutGroup[] = [
	{
		title: 'Select',
		rows: [
			{ keys: 'Click', does: 'Select a step, transition, action or note', hint: 'click: select' },
			{ keys: 'Ctrl / Shift + click', does: 'Add a step to or remove it from the selection', hint: 'Ctrl-click: multi-select' },
			{ keys: 'Ctrl + A', does: 'Select every step' }
		]
	},
	{
		title: 'Edit',
		rows: [
			{ keys: 'Double-click', does: 'Rename a step, edit a condition, an action or its ST body', hint: 'dblclick: rename / edit condition / edit action' },
			{ keys: 'Enter / Esc', does: 'Commit / cancel an in-place edit or the add form' },
			{ keys: 'Drag a step body', does: 'Move it — the position is pinned in the file', hint: 'drag a step: pin layout' },
			{ keys: 'Drag a step’s ⊙ handle', does: 'Drop on another step to connect them with a transition', hint: 'drag ⊙ onto a step: connect' },
			{ keys: 'Del / Backspace', does: 'Delete the selection (a step with transitions offers a cascade; several steps delete with the transitions between them)', hint: 'Del: delete' },
			{ keys: 'Ctrl + C / X / V', does: 'Copy, cut, paste steps with their actions and the transitions between them — pasted to the right of the chart under free names, into another .sfc too', hint: 'Ctrl+C/X/V: copy cut paste steps' },
			{ keys: 'Esc', does: 'Cancel a connect drag, close a popover', hint: 'Esc: cancel' }
		]
	}
];

export const MIMIC_SHORTCUTS: ShortcutGroup[] = [
	{
		title: 'Select',
		rows: [
			{ keys: 'Click', does: 'Select equipment, a pipe or a label', hint: 'click selects' },
			{ keys: 'Ctrl + click', does: 'Add equipment to or remove it from the selection', hint: 'Ctrl-click: multi-select' },
			{ keys: 'Ctrl + A', does: 'Select all equipment' },
			{ keys: 'Shift + click a vertex', does: 'Multi-select pipe points', hint: 'shift-click a vertex: select points' },
			{ keys: 'Esc', does: 'Cancel the current tool, leave the ports editor, or deselect', hint: 'Esc: cancel / deselect' }
		]
	},
	{
		title: 'Edit',
		rows: [
			{ keys: 'Drag', does: 'Move equipment, labels, pipe points', hint: 'drag moves' },
			{ keys: 'Drag a pipe end', does: 'Onto a port to attach it; off to detach', hint: 'drag a pipe end onto a port: attach' },
			{ keys: 'Arrow keys', does: 'Nudge the selection (Shift = one grid step)', hint: 'arrows nudge' },
			{ keys: 'Del / Backspace', does: 'Delete the selection (attached pipe ends are kept, detached)', hint: 'Del deletes' },
			{ keys: 'Ctrl + C / X / V', does: 'Copy, cut, paste equipment with props, bindings and the pipes between them — into another mimic too', hint: 'Ctrl+C/X/V/D: copy cut paste duplicate' },
			{ keys: 'Ctrl + D', does: 'Duplicate the selection in place (offset)' },
			{ keys: 'P', does: 'Open the ports editor for the selected equipment' }
		]
	},
	{
		title: '+ Pipe tool',
		rows: [
			{ keys: 'Click', does: 'Add a point; starting or ending on a port dot anchors that end' },
			{ keys: 'Enter / double-click', does: 'Finish the pipe' },
			{ keys: 'Shift', does: 'Free angle while placing a point' }
		]
	}
];

export const COMPONENT_SHORTCUTS: ShortcutGroup[] = [
	{
		title: 'Ports',
		rows: [
			{ keys: 'Drag a dot', does: 'Move the port', hint: 'drag a dot to move it' },
			{ keys: 'Double-click a dot', does: 'Remove the port', hint: 'double-click a dot to remove' },
			{ keys: 'Double-click the outline', does: 'Add a port there', hint: 'double-click the outline to add' },
			{ keys: 'Click a dot or panel row', does: 'Select the port — the panel renames it and sets its exit direction' },
			{ keys: 'Esc', does: 'Deselect', hint: 'Esc deselects' }
		]
	}
];

/** The one-line toolbar hint for a table. */
export function hintLine(groups: ShortcutGroup[]): string {
	return groups
		.flatMap((g) => g.rows)
		.filter((r) => r.hint)
		.map((r) => r.hint)
		.join(' · ');
}
