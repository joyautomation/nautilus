// Writing a tag from the mimic canvas: an operator control on the screen
// (the kit's CommandButton / WriteToggle, or a project component's own
// button) is handed `write(name, value)` and the canvas lets its clicks
// through. Pure, so it tests without a DOM or a host.

/** What a write needs from the editor's live state. */
export interface LiveState {
	liveEnabled: boolean;
	/** null = the controller is offline (or live values are off). */
	tags: Record<string, unknown> | null;
}

/** Why a write can't go to the controller right now, or null when it can.
 * The reason is the control's to show beside itself (the kit's controls
 * render it), so it says what to do about it. */
export function writeRefusal(s: LiveState): string | null {
	if (!s.liveEnabled) return 'Live values are off: turn them on to operate the screen';
	if (s.tags === null) return 'No controller: run the project (naut run) to operate the screen';
	return null;
}

/** The elements inside a piece of equipment that take a click of their
 * own: with a controller connected, a press on one of these reaches the
 * component instead of selecting or dragging the equipment. */
export const CONTROL_SELECTOR =
	'button, input, select, textarea, [role="button"], [role="switch"], [role="slider"], [data-mimic-control]';

/** Whether a pointer press on `target` belongs to an operator control. */
export function isControlTarget(target: EventTarget | null, s: LiveState): boolean {
	if (writeRefusal(s) !== null) return false;
	const el = target as { closest?: (sel: string) => unknown } | null;
	return !!el?.closest?.(CONTROL_SELECTOR);
}
