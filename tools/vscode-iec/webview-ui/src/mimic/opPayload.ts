// What crosses postMessage must survive the structured clone — and a Svelte
// 5 `$state` value is a Proxy, which does not: a gesture that puts one into
// an op (a `$state` anchor, a live drag's points) makes postMessage throw
// `DataCloneError` and the op is simply lost. Every op is therefore copied
// to plain data here first — the same discipline LadderView.post() applies
// (a JSON round trip reads straight through a proxy). JSON is also exactly
// what the host writes: `undefined` fields drop out, `null` (the ops' "delete
// this field") survives.

/** A plain-data deep copy of `value`, safe to hand to postMessage. */
export function cloneSafe<T>(value: T): T {
	return JSON.parse(JSON.stringify(value)) as T;
}

/** The one-line message a failed post surfaces to the user. */
export function opFailureMessage(kind: string, err: unknown): string {
	const why = err instanceof Error ? err.message : String(err);
	return `the mimic editor couldn't send "${kind}" to the host: ${why}`;
}
