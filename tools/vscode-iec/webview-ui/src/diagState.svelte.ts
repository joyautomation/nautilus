// Shared live geometry: node rectangles the edge components consult so
// feedback lanes can route BELOW everything they cross instead of slicing
// through blocks. App updates it on every render and continuously during
// node drags; replacing the map (not mutating) keeps Svelte reactivity
// simple and cheap at this scale.

export type Rect = { x: number; y: number; w: number; h: number };

export const diag = $state({
	rects: new Map<string, Rect>()
});

export function setRects(entries: Iterable<[string, Rect]>): void {
	diag.rects = new Map(entries);
}

export function updateRect(id: string, r: Rect): void {
	const next = new Map(diag.rects);
	next.set(id, r);
	diag.rects = next;
}
