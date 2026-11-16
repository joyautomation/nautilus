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
	// stepped somewhere bounds no longer describe. `path` is the dotted
	// path so far: a member with bounds of its own ("tank.hist", an array
	// local of a monitored FUNCTION_BLOCK instance) restarts the count.
	let dim = 0;
	let path = base;
	let dims = bounds[base];
	while (rest !== '' && v !== undefined) {
		if (rest.startsWith('.')) {
			const m = /^\.([A-Za-z_][A-Za-z0-9_]*)/.exec(rest);
			if (!m) return undefined;
			v = member(v, m[1]);
			rest = rest.slice(m[0].length);
			path += '.' + m[1].toLowerCase();
			dims = bounds[path];
			dim = dims ? 0 : -1;
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
			path += '[]';
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

/** Is a diagram label covered by the force table? `forced` is keyed by
 * lowercased address. A label is forced when its own address is, when a
 * whole-tag force covers it (`P101.Speed` under `P101`, `Tbl[2]` under
 * `Tbl`), or when it names a struct with a forced member. Mirrors
 * forcedAddress in the extension's src/forces.ts. */
export function forcedLabel(forced: Record<string, unknown>, label: string): boolean {
	const want = label.toLowerCase();
	for (const k in forced) {
		if (k === want || want.startsWith(k + '.') || want.startsWith(k + '[') || k.startsWith(want + '.')) return true;
	}
	return false;
}

/**
 * The controller address of a name written inside a FUNCTION_BLOCK body:
 * its own pins and locals are members of the MONITORED instance ("Running"
 * in TankCycle's rungs is `tank.Running`), so the body's label is
 * re-rooted at the instance, and so is every name an index expression
 * uses ("hist[i]" → "tank.hist[tank.i]"). A VAR_EXTERNAL name is the
 * controller's own tag and stays as written. Member steps (".Q") are never
 * re-rooted.
 *
 * undefined when a name needs an instance and none is monitored: a bare
 * pin has no value of its own, and resolving it as a top-level tag would
 * show whatever tag happens to share its name.
 */
export function fbAddress(label: string, inst: string | undefined, isExternal: (name: string) => boolean): string | undefined {
	let out = '';
	let i = 0;
	while (i < label.length) {
		const m = /^[A-Za-z_][A-Za-z0-9_]*/.exec(label.slice(i));
		if (!m) {
			out += label[i++];
			continue;
		}
		const name = m[0];
		const prev = label.slice(0, i).trimEnd().slice(-1);
		const rooted = prev === '' || prev === '[' || prev === ',';
		if (rooted && !isExternal(name)) {
			if (!inst) return undefined;
			out += inst + '.' + name;
		} else {
			out += name;
		}
		i += name.length;
	}
	return out;
}
