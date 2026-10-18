// The props strip's value grammar (PreviewApp.svelte). A prop's sample
// value is typed as text and read back as the JSON-ish thing it looks
// like, so `62` is a number, `true` a boolean, `[4, 6, 5]` an array,
// `{"a": 1}` an object, and anything else the string itself (quotes
// optional: `P-101` and `"P-101"` both read as the string P-101). Pure, so
// it's unit-tested with `node --experimental-strip-types --test`.

/** What the strip shows for a value: a bare string as-is, everything else
 * as JSON (so it round-trips through parsePropInput). */
export function formatPropValue(v: unknown): string {
	if (typeof v === 'string') return v;
	if (v === undefined) return '';
	try {
		return JSON.stringify(v);
	} catch {
		return String(v);
	}
}

/** Text -> value. JSON wins when the text parses as JSON (numbers, booleans,
 * null, arrays, objects, quoted strings); otherwise the trimmed text is a
 * string. Empty text is the empty string, not undefined — deleting a prop
 * is the row's own ✕, not a blank value. */
export function parsePropInput(text: string): unknown {
	const t = text.trim();
	if (t === '') return '';
	try {
		return JSON.parse(t);
	} catch {
		return t;
	}
}

/** The short type badge beside a row. */
export function kindOf(v: unknown): string {
	if (v === null) return 'null';
	if (Array.isArray(v)) return 'array';
	return typeof v;
}

/** A prop name the strip accepts: a JS identifier (what `$props()`
 * destructures), so a typo like `level pct` is refused at the input. */
export function isPropName(name: string): boolean {
	return /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(name);
}
