// The inventory tags workstream A will publish, stubbed from the BMC
// capture (../../stub-inventory.mjs) and laid over the controller's frame.
// A stub never hides a real tag: once the driver publishes a name, the
// controller's value wins. `live` members follow bench tags that exist
// today (temperatures), so the stub moves where the recording does.
import type { RealtimeClient, NautilusFrame, Quality } from '@joyautomation/nautilus-hmi';

export interface Stub {
	source: string;
	tags: Record<string, Record<string, unknown>>;
	live: Record<string, string>;
}

function read(tags: Record<string, unknown>, path: string): unknown {
	let v: unknown = tags;
	for (const k of path.split('.')) v = v && typeof v === 'object' ? (v as Record<string, unknown>)[k] : undefined;
	return v;
}

/** The frame's tags with the stub's laid under them. */
export function withStub(stub: Stub, tags: Record<string, unknown>): Record<string, unknown> {
	const out: Record<string, unknown> = {};
	for (const [name, value] of Object.entries(stub.tags)) out[name] = { ...value };
	for (const [path, from] of Object.entries(stub.live)) {
		const v = read(tags, from);
		if (v === undefined) continue;
		const [name, m] = path.split('.');
		if (out[name]) (out[name] as Record<string, unknown>)[m] = v;
	}
	return { ...out, ...tags };
}

/** A read-only view of a RealtimeClient with the stub's tags in every frame
 * — what SceneView and the faceplate read. Stubbed tags report good. */
export function stubbed(rt: RealtimeClient<NautilusFrame>, stub: Stub): RealtimeClient<NautilusFrame> {
	const isStub = (tag: string) => tag in stub.tags && !(rt.frame?.tags && tag in rt.frame.tags);
	return new Proxy(rt, {
		get(target, key) {
			if (key === 'frame') {
				const f = target.frame;
				return f ? { ...f, tags: withStub(stub, (f.tags ?? {}) as Record<string, unknown>) } : f;
			}
			if (key === 'isGood') return (tag: string) => isStub(tag) || target.isGood(tag);
			if (key === 'quality') return (tag: string): Quality => (isStub(tag) ? 'good' : target.quality(tag));
			const v = Reflect.get(target, key);
			return typeof v === 'function' ? v.bind(target) : v;
		}
	});
}

export const isStubTag = (stub: Stub, tag: string | undefined) => !!tag && tag in stub.tags;
