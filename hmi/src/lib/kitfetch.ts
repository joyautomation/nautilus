// The kit's one fetch. Every client in the kit (writeTag, alarms,
// substitutes, IO-Link parameters, history) calls `kitFetch` unless it was
// handed a fetch of its own, so `createAuth()` can put the sign-in rules
// (the elevation prompt and its one retry, following a mid-session sign-out
// and a standby's referral) in front of all of them by installing itself
// here, once, in the root layout. With nothing installed it is the
// browser's fetch, exactly as before.

export type Fetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let installed: Fetch | null = null;
let streamTrouble: (() => void) | null = null;
let streamFine: (() => void) | null = null;

/** fetch, through whatever `setKitFetch` installed. */
export function kitFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
	return installed ? installed(input, init) : globalThis.fetch(input, init);
}

/** Install (or, with null, remove) the fetch every kit client uses. */
export function setKitFetch(f: Fetch | null): void {
	installed = f;
}

/**
 * Install (or remove) the hooks a live stream reports to: `trouble` when it
 * errors (an EventSource cannot read the status that closed it, so the
 * listener asks the controller itself), `fine` when it opens again.
 */
export function setStreamHooks(trouble: (() => void) | null, fine: (() => void) | null = null): void {
	streamTrouble = trouble;
	streamFine = fine;
}

/** A stream errored. */
export function reportStreamTrouble(): void {
	streamTrouble?.();
}

/** A stream (re)opened. */
export function reportStreamFine(): void {
	streamFine?.();
}
