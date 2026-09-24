// Pure accessor-label resolution against a streamed value map — shared by
// the live store and testable headless. Labels are whatever a diagram chip
// says: "TempC", "a1.Q", "Motor.Speed", "TempHist[2]", "m[1][2]",
// "tbl[i].val" — including variable indexes, which resolve through the
// same value map ("TempHist[i]" follows the live value of i).

/** One case-insensitive member step (an FB output pin off its instance
 * struct, a UDT field). */
export function member(v: unknown, key: string): unknown {
	if (v === null || typeof v !== 'object' || Array.isArray(v)) return undefined;
	const obj = v as Record<string, unknown>;
	if (key in obj) return obj[key];
	const lk = key.toLowerCase();
	const hit = Object.keys(obj).find((k) => k.toLowerCase() === lk);
	return hit === undefined ? undefined : obj[hit];
}

/** Parse per-dimension lower bounds from a declared type string:
 * "ARRAY[1..4] OF REAL" → [1], "ARRAY[1..2, 0..3] OF T" → [1, 0].
 * undefined for non-array types. */
export function arrayLowerBounds(typeText: string): number[] | undefined {
	const m = /^\s*ARRAY\s*\[([^\]]+)\]/i.exec(typeText);
	if (!m) return undefined;
	const dims: number[] = [];
	for (const dim of m[1].split(',')) {
		const d = /^\s*(-?\d+)\s*\.\./.exec(dim);
		if (!d) return undefined;
		dims.push(parseInt(d[1], 10));
	}
	return dims;
}

/**
 * Resolve an accessor label. `bounds` maps lowercased variable names to
 * their arrays' per-dimension lower bounds (an IEC `ARRAY[1..4]` stores
 * element [1] at position 0). Unknown bounds — an index below a struct
 * member, an undeclared name — resolve to undefined rather than risk
 * showing the WRONG element's value, unless `defaultLo` says what every
 * array's lower bound is: 0 on Logix, where no other bound exists.
 */
export function resolveLabel(
	values: Record<string, unknown>,
	bounds: Record<string, number[]>,
	label: string,
	defaultLo?: number
): unknown {
	const head = /^([A-Za-z_][A-Za-z0-9_]*)/.exec(label);
	if (!head) return undefined;
	const base = head[1].toLowerCase();
	let v: unknown = values[base];
	let rest = label.slice(head[1].length);
	// Which array dimension the next [index] addresses; -1 once we've
	// stepped somewhere bounds no longer describe (inside a member).
	let dim = 0;
	const dims = bounds[base];
	while (rest !== '' && v !== undefined) {
		if (rest.startsWith('.')) {
			const m = /^\.([A-Za-z_][A-Za-z0-9_]*)/.exec(rest);
			if (!m) return undefined;
			v = member(v, m[1]);
			rest = rest.slice(m[0].length);
			dim = -1;
		} else if (rest.startsWith('[')) {
			const m = /^\[([^\][]+)\]/.exec(rest);
			if (!m || !Array.isArray(v)) return undefined;
			const t = m[1].trim();
			let idx: number;
			if (/^-?\d+$/.test(t)) {
				idx = parseInt(t, 10);
			} else {
				const iv = resolveLabel(values, bounds, t, defaultLo);
				if (typeof iv !== 'number' || !Number.isInteger(iv)) return undefined;
				idx = iv;
			}
			const lo = dim >= 0 && dims && dim < dims.length ? dims[dim] : defaultLo;
			if (lo === undefined) return undefined;
			const at = idx - lo;
			if (at < 0 || at >= v.length) return undefined;
			v = v[at];
			if (dim >= 0) dim++;
			rest = rest.slice(m[0].length);
		} else {
			return undefined;
		}
	}
	return v;
}

/**
 * Resolve a label named inside a Logix routine, whose rung says `Counts`
 * for the program tag a served tag store holds as `MainProgram_Counts`
 * (`naut logix serve`, and the naming of `naut logix import`).
 *
 * `scope` is the rung's, in Logix notation. A program tag shadows a
 * controller tag of the same name, as it does on the controller, and the
 * choice is made on the tag's EXISTENCE, not on whether the rest of the
 * path resolves: a bad member path on the program tag must not fall
 * through to an unrelated controller tag. An AOI routine resolves nothing,
 * because its operands are the parameters of whichever instance is
 * running, and a controller tag that happens to share a name is not one.
 */
export function resolveScoped(
	values: Record<string, unknown>,
	scope: string,
	label: string
): unknown {
	if (/^AOI:/i.test(scope)) return undefined;
	const prog = /^Program:(.+)$/i.exec(scope);
	const head = /^([A-Za-z_][A-Za-z0-9_]*)/.exec(label);
	if (prog && head) {
		const scoped = prog[1] + '_' + label;
		if ((prog[1] + '_' + head[1]).toLowerCase() in values) return resolveLabel(values, {}, scoped, 0);
	}
	return resolveLabel(values, {}, label, 0);
}
