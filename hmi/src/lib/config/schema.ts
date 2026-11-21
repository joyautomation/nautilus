// The config screens' view of a manifest JSON Schema slice (#711): the
// pure half. It turns a slice from GET /api/config/schema into a field
// tree a form renders, and checks a value against it.
//
// It supports exactly the keywords the drivers and devices sections use
// (HANDLED below; tests/config.test.ts reads the real schema and fails when
// those sections start using another). A node that carries any other
// keyword is a `raw` field: the form shows its text and never drops or
// rewrites it. The validator here is a convenience for the person typing;
// stage + validate on the controller (#706) stays the authority.

export type Json = null | boolean | number | string | Json[] | { [k: string]: Json };

export interface Schema {
	[k: string]: unknown;
}

export type Kind = 'string' | 'integer' | 'number' | 'boolean' | 'enum' | 'list' | 'map' | 'object' | 'raw';

/** A condition from the schema's `if: {type: {const: X}} then: {required}`. */
export interface Conditional {
	key: string;
	equals: string;
	required: string[];
}

export interface Field {
	key: string;
	kind: Kind;
	description?: string;
	required: boolean;
	enum?: string[];
	minimum?: number;
	maximum?: number;
	minLength?: number;
	minItems?: number;
	pattern?: string;
	/** map: the pattern its keys must match. */
	keyPattern?: string;
	/** list: what one entry is. map: what one value is. */
	item?: Field;
	/** object: its properties, in schema order. */
	fields?: Field[];
	/** object: required keys that depend on another key's value. */
	conditional?: Conditional[];
	/** object: whether keys the schema does not name are allowed. */
	open?: boolean;
	/** raw: why the form leaves it as text. */
	rawWhy?: string;
}

/** Keywords the walker understands. Anything else makes a node raw. */
export const HANDLED = new Set([
	'type',
	'enum',
	'const',
	'required',
	'properties',
	'additionalProperties',
	'items',
	'minimum',
	'maximum',
	'minLength',
	'minItems',
	'pattern',
	'allOf',
	'if',
	'then',
	'$ref',
	'propertyNames'
]);
/** Keywords that carry no rule. */
const IGNORED = new Set(['description', 'title', '$comment', 'default', 'examples', '$schema', 'definitions']);

interface Root {
	definitions?: Record<string, Schema>;
	properties?: Record<string, Schema>;
}

function resolve(root: Root, node: Schema): Schema {
	let n = node;
	for (let i = 0; i < 16; i++) {
		const ref = n['$ref'];
		if (typeof ref !== 'string') return n;
		const name = ref.startsWith('#/definitions/') ? ref.slice('#/definitions/'.length) : '';
		const target = root.definitions?.[name];
		if (!target) return { ...n, $unresolved: ref };
		// A $ref's siblings (a description) win over the target's.
		const { $ref: _drop, ...rest } = n;
		n = { ...target, ...rest };
	}
	return n;
}

function rawOf(key: string, required: boolean, why: string, description?: string): Field {
	return { key, kind: 'raw', required, description, rawWhy: why };
}

function field(root: Root, key: string, schema: Schema, required: boolean): Field {
	const n = resolve(root, schema);
	const description = typeof n.description === 'string' ? n.description : undefined;
	if (n['$unresolved']) return rawOf(key, required, `unresolved ${n['$unresolved']}`, description);
	for (const k of Object.keys(n)) {
		if (!HANDLED.has(k) && !IGNORED.has(k)) return rawOf(key, required, `uses ${k}`, description);
	}
	const base: Field = { key, kind: 'raw', required, description };
	if (typeof n.pattern === 'string') base.pattern = n.pattern;
	if (typeof n.minLength === 'number') base.minLength = n.minLength;
	if (typeof n.minItems === 'number') base.minItems = n.minItems;
	if (typeof n.minimum === 'number') base.minimum = n.minimum;
	if (typeof n.maximum === 'number') base.maximum = n.maximum;
	if (Array.isArray(n.enum)) {
		if (!n.enum.every((e) => typeof e === 'string')) return rawOf(key, required, 'a non-string enum', description);
		return { ...base, kind: 'enum', enum: n.enum as string[] };
	}
	switch (n.type) {
		case 'string':
		case 'integer':
		case 'number':
		case 'boolean':
			return { ...base, kind: n.type };
		case 'array': {
			const it = n.items as Schema | undefined;
			if (!it) return rawOf(key, required, 'an array without items', description);
			return { ...base, kind: 'list', item: field(root, key, it, false) };
		}
		case 'object': {
			const props = n.properties as Record<string, Schema> | undefined;
			const ap = n.additionalProperties;
			if (!props && ap && typeof ap === 'object') {
				if (n.propertyNames && typeof n.propertyNames === 'object') {
					const pn = n.propertyNames as Schema;
					if (Object.keys(pn).some((k) => k !== 'pattern')) return rawOf(key, required, 'a propertyNames rule', description);
				}
				const kp = (n.propertyNames as Schema | undefined)?.pattern;
				return { ...base, kind: 'map', keyPattern: typeof kp === 'string' ? kp : undefined, item: field(root, key, ap as Schema, false) };
			}
			const req = new Set((n.required as string[] | undefined) ?? []);
			const fields = Object.entries(props ?? {}).map(([k, v]) => field(root, k, v, req.has(k)));
			const conditional: Conditional[] = [];
			for (const c of (n.allOf as Schema[] | undefined) ?? []) {
				const cond = conditionOf(c);
				if (!cond) return rawOf(key, required, 'an allOf entry the form does not read', description);
				conditional.push(cond);
			}
			return {
				...base,
				kind: 'object',
				fields,
				conditional: conditional.length ? conditional : undefined,
				open: ap !== false
			};
		}
	}
	return rawOf(key, required, 'no type', description);
}

// {if: {properties: {type: {const: X}}, required: [type]}, then: {required: [...], anyOf?: …}}
function conditionOf(c: Schema): Conditional | null {
	const i = c.if as Schema | undefined;
	const t = c.then as Schema | undefined;
	const props = i?.properties as Record<string, Schema> | undefined;
	if (!i || !t || !props) return null;
	const keys = Object.keys(props);
	if (keys.length !== 1) return null;
	const eq = props[keys[0]].const;
	if (typeof eq !== 'string') return null;
	// then.anyOf / then.not (alternatives) are the server's to judge.
	return { key: keys[0], equals: eq, required: ((t.required as string[] | undefined) ?? []).slice() };
}

/** The root field of a section slice from GET /api/config/schema. */
export function sectionField(slice: Schema, section: string): Field {
	const root = slice as unknown as Root;
	const prop = root.properties?.[section];
	if (!prop) throw new Error(`the schema slice has no section ${section}`);
	return field(root, section, prop, false);
}

/** Every raw node under f, with its path: what a form shows as text. */
export function rawFields(f: Field, path: string[] = []): { path: string[]; why: string }[] {
	const here = path.concat(f.key);
	if (f.kind === 'raw') return [{ path: here, why: f.rawWhy ?? '' }];
	const out: { path: string[]; why: string }[] = [];
	if (f.item) out.push(...rawFields(f.item, f.kind === 'list' ? here.concat('[]') : here));
	for (const c of f.fields ?? []) out.push(...rawFields(c, here));
	return out;
}

export interface Issue {
	path: string;
	message: string;
}

function show(path: string[]): string {
	return path.join('.') || '(root)';
}

/** What is wrong with value against f; empty when nothing the schema says is broken. */
export function validate(f: Field, value: Json | undefined, path: string[] = [f.key]): Issue[] {
	const at = show(path);
	if (value === undefined || value === null) {
		return f.required ? [{ path: at, message: `${at} is required` }] : [];
	}
	if (f.kind === 'raw') return [];
	const out: Issue[] = [];
	const bad = (message: string) => out.push({ path: at, message });
	switch (f.kind) {
		case 'string':
		case 'enum':
			if (typeof value !== 'string') {
				bad(`${at} must be text`);
				break;
			}
			if (f.minLength !== undefined && value.length < f.minLength) bad(`${at} must not be empty`);
			if (f.pattern && !new RegExp(f.pattern).test(value)) bad(`${at} is not in the form ${f.pattern}`);
			if (f.enum && !f.enum.includes(value)) bad(`${at} must be one of ${f.enum.join(', ')}`);
			break;
		case 'integer':
		case 'number':
			if (typeof value !== 'number' || !Number.isFinite(value) || (f.kind === 'integer' && !Number.isInteger(value))) {
				bad(`${at} must be ${f.kind === 'integer' ? 'a whole number' : 'a number'}`);
				break;
			}
			if (f.minimum !== undefined && value < f.minimum) bad(`${at} must be at least ${f.minimum}`);
			if (f.maximum !== undefined && value > f.maximum) bad(`${at} must be at most ${f.maximum}`);
			break;
		case 'boolean':
			if (typeof value !== 'boolean') bad(`${at} must be true or false`);
			break;
		case 'list':
			if (!Array.isArray(value)) {
				bad(`${at} must be a list`);
				break;
			}
			if (f.minItems !== undefined && value.length < f.minItems) bad(`${at} needs at least ${f.minItems} entries`);
			value.forEach((v, i) => out.push(...validate({ ...(f.item as Field), required: false }, v, path.concat(`[${i}]`))));
			break;
		case 'map':
			if (typeof value !== 'object' || Array.isArray(value)) {
				bad(`${at} must be a map`);
				break;
			}
			for (const [k, v] of Object.entries(value)) {
				if (f.keyPattern && !new RegExp(f.keyPattern).test(k)) out.push({ path: show(path.concat(k)), message: `${show(path.concat(k))}: the name ${k} is not in the form ${f.keyPattern}` });
				out.push(...validate(f.item as Field, v, path.concat(k)));
			}
			break;
		case 'object': {
			if (typeof value !== 'object' || Array.isArray(value)) {
				bad(`${at} must be a map`);
				break;
			}
			const obj = value as { [k: string]: Json };
			const known = new Set((f.fields ?? []).map((c) => c.key));
			for (const c of f.fields ?? []) out.push(...validate(c, obj[c.key], path.concat(c.key)));
			if (f.open === false) {
				for (const k of Object.keys(obj)) if (!known.has(k)) out.push({ path: show(path.concat(k)), message: `${show(path.concat(k))} is not a setting here` });
			}
			for (const c of f.conditional ?? []) {
				if (obj[c.key] !== c.equals) continue;
				for (const r of c.required) {
					if (obj[r] === undefined || obj[r] === null) out.push({ path: show(path.concat(r)), message: `${show(path.concat(r))} is required when ${c.key} is ${c.equals}` });
				}
			}
			break;
		}
	}
	return out;
}
