// The one decision `Modal` makes about a native <dialog>'s `close` event,
// kept here so it is tested on plain node (tests/dialog.test.ts).
//
// `close` is dispatched as a task, after `.close()` returns. When a host
// closes the dialog and opens it again at once (the confirm dialog asking
// for the password again after a wrong one, or a second confirm right after
// the first), the first close's event arrives while the NEW question is on
// screen. Taken as the user closing it, it cancelled the new question.

/**
 * Whether a `close` event is the user (or the browser) closing the dialog:
 * the host still thinks it is open (`open`), and the element really is
 * closed (`stillOpen` false). A close event that finds the element open
 * again belongs to an earlier close, and is ignored.
 */
export function closeIsReal(open: boolean, stillOpen: boolean): boolean {
	return open && !stillOpen;
}
