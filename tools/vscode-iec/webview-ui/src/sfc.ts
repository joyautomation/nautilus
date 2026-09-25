// The SFC render model (mirror of lang/sfc.Model, see lang/sfc/graph.go) and
// a pure, deterministic layout pass: steps flow top-to-bottom by rank
// (distance from the initial step along transitions); parallel/alternative
// legs fan into columns. No physics, no iteration to convergence — a single
// BFS assigns ranks (first-discovery wins, exactly like a shortest-path
// tree), then a post-order pass assigns each step a column as the average of
// its tree children's columns (leaves get the next integer column in visit
// order). A transition whose target(s) don't lie strictly BELOW its
// source(s) — a loop back to an earlier/same step, which every real chart
// has (aborts, "go back to Idle") — is a structurally back edge: instead of
// stretching a line back up the canvas, it renders as a compact "jump"
// glyph near its source (see TransRoute.jump).

export type SfcAssoc = { qualifier: string; target: string; time?: string; line: number };

// DiffStatus marks an element's relationship to a diff's base version — set
// only by diffSfc (never present in a model straight from `naut sfc
// graph`), the same overlay convention lang/fbd's diff.ts (mergeDiff) and
// ladder.ts (diffLd) use, rendered with the shared --nx-added/removed/
// changed palette (App.svelte's diff legend).
export type DiffStatus = 'added' | 'removed' | 'changed';

export type SfcStep = {
	id: string;
	name: string;
	initial: boolean;
	actions?: SfcAssoc[];
	line: number;
	endLine: number;
	status?: DiffStatus;
};

export type SfcTransKind = 'normal' | 'alt' | 'simDiverge' | 'simConverge';

export type SfcTransition = {
	id: string;
	name?: string;
	from: string[];
	to: string[];
	cond: string;
	kind: SfcTransKind;
	line: number;
	endLine: number;
	status?: DiffStatus;
};

export type SfcAction = { id: string; name: string; body: string; line: number; endLine: number; status?: DiffStatus };
export type SfcVar = { name: string; type: string; init?: string; section: string; line: number };
export type SfcComment = { line: number; endLine: number; text: string; status?: DiffStatus };

export type SfcModel = {
	name: string;
	vars?: SfcVar[];
	steps: SfcStep[];
	trans: SfcTransition[];
	actions?: SfcAction[];
	comments?: SfcComment[];
	// User-pinned positions, keyed by the same stable ids (st:/tr:/ac:) —
	// only step (`st:`) entries affect layout in v1 (§4.2: "drag a STEP").
	layout?: Record<string, { x: number; y: number }>;
	/** A whitespace-only source: no POU yet; the first op seeds one. */
	blank?: boolean;
};

/** Arrays always arrays: an older CLI (or saved webview state) can carry
 * `null` for an empty chart's steps/trans. */
export function normalizeSfc(m: SfcModel): SfcModel {
	return {
		...m,
		steps: m.steps ?? [],
		trans: (m.trans ?? []).map((t) => ({ ...t, from: t.from ?? [], to: t.to ?? [] }))
	};
}

export function stepId(name: string): string {
	return 'st:' + name;
}

// A comment's stable id (its ordinal into Model.Comments) — the same "cm:N"
// scheme lang/fbd uses for its own diagram notes, mirrored in
// lang/sfc/edit.go's commentID, so setLayout/setComment/deleteComment all
// address a note the same way.
export function commentId(index: number): string {
	return 'cm:' + index;
}

// ── layout geometry constants ───────────────────────────────────────────────

export const G = {
	STEP_W: 130,
	STEP_H: 44,
	ASSOC_ROW_H: 16,
	ASSOC_W: 168,
	COL_W: 300,
	RANK_H: 190,
	MARGIN: 40,
	NOTE_W: 220,
	NOTE_H: 30,
	CHIP_W: 190,
	CHIP_H: 40,
} as const;

export type PlacedStep = {
	step: SfcStep;
	id: string;
	x: number;
	y: number;
	w: number;
	h: number;
	rank: number;
	col: number;
	pinned: boolean;
};

/** Rank (BFS distance from the initial step, first-discovery-wins) and the
 * column (post-order tree layout over the first-discovery parent edges).
 * Exported separately from layoutSfc so ops (e.g. "insert after") can also
 * reason about structure without recomputing pixel geometry. */
export function computeRanksAndColumns(model: SfcModel): {
	rankOf: Map<string, number>;
	colOf: Map<string, number>;
} {
	const steps = model.steps ?? [];
	const rankOf = new Map<string, number>();
	const colOf = new Map<string, number>();
	if (steps.length === 0) return { rankOf, colOf };

	const initial = steps.find((s) => s.initial) ?? steps[0];

	// children: step id -> ordered, deduped list of target ids reachable via
	// any transition where this step is a FROM member (declaration order).
	const childrenOf = new Map<string, string[]>();
	for (const t of model.trans ?? []) {
		for (const fromName of t.from) {
			const fid = stepId(fromName);
			let arr = childrenOf.get(fid);
			if (!arr) {
				arr = [];
				childrenOf.set(fid, arr);
			}
			for (const toName of t.to) {
				const tid = stepId(toName);
				if (!arr.includes(tid)) arr.push(tid);
			}
		}
	}

	const treeChildren = new Map<string, string[]>();
	const queue: string[] = [initial.id];
	rankOf.set(initial.id, 0);
	let qi = 0;
	while (qi < queue.length) {
		const cur = queue[qi++];
		for (const kid of childrenOf.get(cur) ?? []) {
			if (rankOf.has(kid)) continue; // already discovered: a jump/converge edge, not a tree edge
			rankOf.set(kid, rankOf.get(cur)! + 1);
			let arr = treeChildren.get(cur);
			if (!arr) {
				arr = [];
				treeChildren.set(cur, arr);
			}
			arr.push(kid);
			queue.push(kid);
		}
	}

	// Steps never reached from the initial step (a malformed/disconnected
	// chart `naut sfc check` would flag) still get a visible slot: append
	// them as further roots below the deepest reached rank, in declaration
	// order, rather than dropping them from the diagram.
	let maxRank = 0;
	for (const r of rankOf.values()) maxRank = Math.max(maxRank, r);
	const roots = [initial.id];
	for (const s of steps) {
		if (!rankOf.has(s.id)) {
			maxRank += 1;
			rankOf.set(s.id, maxRank);
			roots.push(s.id);
		}
	}

	let counter = 0;
	function assignCol(id: string): number {
		const have = colOf.get(id);
		if (have !== undefined) return have;
		const kids = treeChildren.get(id) ?? [];
		let col: number;
		if (kids.length === 0) {
			col = counter++;
		} else {
			const cols = kids.map(assignCol);
			col = (Math.min(...cols) + Math.max(...cols)) / 2;
		}
		colOf.set(id, col);
		return col;
	}
	for (const r of roots) assignCol(r);

	return { rankOf, colOf };
}

export function stepBoxHeight(s: SfcStep): number {
	return G.STEP_H;
}

export function assocTableHeight(s: SfcStep): number {
	const n = s.actions?.length ?? 0;
	return n === 0 ? 0 : n * G.ASSOC_ROW_H + 6;
}

// Vertical spacing between the staggered bars of an alternative divergence.
const ALT_BAR_GAP = 26;

export type Leg = { x: number; y1: number; y2: number };

export type TransRoute = {
	t: SfcTransition;
	// Forward geometry: a horizontal bar (single line for normal/alt, a
	// double line — two close parallel strokes — for simDiverge/simConverge)
	// with vertical stems in from every source and out to every target.
	barY: number;
	barX1: number;
	barX2: number;
	double: boolean;
	legsIn: Leg[];
	legsOut: Leg[];
	condX: number;
	condY: number;
	// Present INSTEAD of the forward fields when the transition's target(s)
	// don't lie strictly below its source(s) (a loop back to an earlier
	// step): a compact glyph anchored under the (first) source, labelled
	// with the destination step name(s), rather than a line stretching back
	// up the canvas.
	jump?: { x: number; y: number; label: string };
};

// A rendered diagram note (from a `//` comment run) — a fixed-size box in a
// strip along the top edge, ahead of rank 0, so the step ranks below shift
// down to make room (§ layoutSfc). `index` is the Model.Comments index a
// setComment op addresses.
export type PlacedNote = { c: SfcComment; index: number; x: number; y: number; w: number; h: number; pinned: boolean };

// A transition whose FROM and/or TO references a step name that doesn't
// resolve — a dangling reference (typically left by deleteStep's breadcrumb
// philosophy, see docs/design/sfc.md §4.2). Rendered as a small "problem"
// chip rather than silently dropped (the bug this fixes: layoutSfc used to
// `continue` past these). `anchor`, when set, is the surviving endpoint's
// PlacedStep — the chip renders beside it; when both FROM and TO are
// unresolved, the chip has no anchor and lands in the bottom "unanchored"
// strip instead.
export type OrphanChip = {
	t: SfcTransition;
	anchor?: PlacedStep;
	x: number;
	y: number;
	w: number;
	h: number;
};

export type SfcLayout = {
	steps: PlacedStep[];
	trans: TransRoute[];
	notes: PlacedNote[];
	orphans: OrphanChip[];
	width: number;
	height: number;
};

/** True iff every target of `t` lies at a strictly greater rank than every
 * source — i.e. the transition flows forward through the chart. A partial
 * mix (rare, exotic topology) is treated as a whole-transition jump; see the
 * module doc. */
function isForward(t: SfcTransition, rankOf: Map<string, number>): boolean {
	const srcRanks = t.from.map((n) => rankOf.get(stepId(n)) ?? 0);
	const tgtRanks = t.to.map((n) => rankOf.get(stepId(n)) ?? 0);
	return Math.min(...tgtRanks) > Math.max(...srcRanks);
}

/** Lay out an SFC chart: a notes strip along the top edge (comments), step
 * boxes by rank/column below it (pinned steps override their auto position,
 * FBD/LD-style), a route for every transition whose FROM/TO both
 * resolve — forward transitions as bar+stems, back/converge-to-earlier
 * transitions as a compact jump glyph — and a "problem" chip for every
 * transition that DOESN'T fully resolve (a dangling FROM/TO, typically left
 * by deleteStep's breadcrumb philosophy): anchored beside its surviving
 * endpoint when it has one, else collected into a strip along the bottom
 * edge. Pure function of the model; deterministic. */
export function layoutSfc(model: SfcModel): SfcLayout {
	const { rankOf, colOf } = computeRanksAndColumns(model);
	const byId = new Map<string, PlacedStep>();
	const steps: PlacedStep[] = [];

	const comments = model.comments ?? [];
	const notes: PlacedNote[] = comments.map((c, i) => {
		const autoX = G.MARGIN + i * (G.NOTE_W + 12);
		const autoY = G.MARGIN;
		const pin = model.layout?.[commentId(i)];
		return {
			c,
			index: i,
			x: pin ? pin.x : autoX,
			y: pin ? pin.y : autoY,
			w: G.NOTE_W,
			h: G.NOTE_H,
			pinned: !!pin,
		};
	});
	// Reserve a band for the notes strip: every step rank shifts down to make
	// room, so a comment never overlaps step 0's row.
	const notesBand = notes.length > 0 ? G.NOTE_H + 24 : 0;

	for (const s of model.steps ?? []) {
		const rank = rankOf.get(s.id) ?? 0;
		const col = colOf.get(s.id) ?? 0;
		const autoX = G.MARGIN + col * G.COL_W;
		const autoY = G.MARGIN + notesBand + rank * G.RANK_H;
		const pin = model.layout?.[s.id];
		const x = pin ? pin.x : autoX;
		const y = pin ? pin.y : autoY;
		const p: PlacedStep = {
			step: s,
			id: s.id,
			x,
			y,
			w: G.STEP_W,
			h: stepBoxHeight(s),
			rank,
			col,
			pinned: !!pin,
		};
		steps.push(p);
		byId.set(s.id, p);
	}

	const centerX = (p: PlacedStep) => p.x + p.w / 2;

	// Forward transitions grouped by their exact source set, in declaration
	// order — the alternative-divergence groups the bar stagger below needs.
	const sourceKey = (t: SfcTransition) => t.from.map((n) => n.toLowerCase()).sort().join(',');
	const forwardFrom = new Map<string, SfcTransition[]>();
	for (const t of model.trans ?? []) {
		const resolved = [...t.from, ...t.to].every((n) => byId.has(stepId(n)));
		if (!resolved || !isForward(t, rankOf)) continue;
		const k = sourceKey(t);
		forwardFrom.set(k, [...(forwardFrom.get(k) ?? []), t]);
	}

	const trans: TransRoute[] = [];
	// Transitions whose FROM and/or TO don't fully resolve — collected here,
	// positioned into OrphanChips once every step's final position is known.
	const dangling: { t: SfcTransition; anchor?: PlacedStep }[] = [];
	for (const t of model.trans ?? []) {
		const sources = t.from.map((n) => byId.get(stepId(n))).filter((p): p is PlacedStep => !!p);
		const targets = t.to.map((n) => byId.get(stepId(n))).filter((p): p is PlacedStep => !!p);
		if (sources.length === 0 || targets.length === 0) {
			dangling.push({ t, anchor: sources[0] ?? targets[0] });
			continue;
		}
		if (!isForward(t, rankOf)) {
			const anchor = sources[0];
			trans.push({
				t,
				barY: 0,
				barX1: 0,
				barX2: 0,
				double: t.kind === 'simDiverge' || t.kind === 'simConverge',
				legsIn: [],
				legsOut: [],
				condX: 0,
				condY: 0,
				jump: {
					x: centerX(anchor),
					// Below the box AND below its action table (plus the
					// "+ action" row the editor draws under it): a step with
					// three actions reaches further down than the box, and
					// the glyph's label runs right, into that column.
					y: anchor.y + Math.max(anchor.h, assocTableHeight(anchor.step) + G.ASSOC_ROW_H) + 14,
					label: '↩ ' + t.to.join(', '),
				},
			});
			continue;
		}
		const srcBottom = Math.max(...sources.map((p) => p.y + p.h));
		const tgtTop = Math.min(...targets.map((p) => p.y));
		// Forward transitions leaving the SAME source set (an alternative
		// divergence, e.g. Fill -> Aborted vs Fill -> (Heat, Mix)) would all
		// put their bar at the same midpoint — drawn on top of each other,
		// with one condition label sitting on the other's bar. Stagger them
		// in declaration (= priority) order, highest priority on top.
		const group = forwardFrom.get(sourceKey(t)) ?? [t];
		const slot = group.indexOf(t) - (group.length - 1) / 2;
		const barY = (srcBottom + tgtTop) / 2 + slot * ALT_BAR_GAP;
		const xs = [...sources, ...targets].map(centerX);
		const barX1 = Math.min(...xs);
		const barX2 = Math.max(...xs);
		// A bar that only runs LEFT of its source would label its right end —
		// which is the source's own stem, where a sibling's bar continues.
		// Label it above its own left run instead.
		const leftOnly = group.length > 1 && Math.max(...targets.map(centerX)) < Math.min(...sources.map(centerX));
		trans.push({
			t,
			barY,
			barX1,
			barX2,
			double: t.kind === 'simDiverge' || t.kind === 'simConverge',
			legsIn: sources.map((p) => ({ x: centerX(p), y1: p.y + p.h, y2: barY })),
			legsOut: targets.map((p) => ({ x: centerX(p), y1: barY, y2: p.y })),
			condX: leftOnly ? barX1 + 10 : barX2 + 10,
			condY: leftOnly ? barY - 12 : barY,
		});
	}

	let width = G.MARGIN * 2 + G.STEP_W;
	let height = G.MARGIN * 2 + G.STEP_H;
	for (const p of steps) {
		width = Math.max(width, p.x + p.w + G.ASSOC_W + G.MARGIN);
		height = Math.max(height, p.y + p.h + G.MARGIN);
	}
	for (const r of trans) {
		width = Math.max(width, r.condX + 220);
		if (r.jump) height = Math.max(height, r.jump.y + G.MARGIN);
	}
	for (const n of notes) {
		width = Math.max(width, n.x + n.w + G.MARGIN);
		height = Math.max(height, n.y + n.h + G.MARGIN);
	}

	// Anchored orphan chips stack beside their surviving endpoint (right of
	// the step box); unanchored ones (neither FROM nor TO resolves) go in a
	// left-to-right strip along the bottom edge, below everything else.
	const stackAt = new Map<string, number>();
	const orphans: OrphanChip[] = [];
	let unanchoredX = G.MARGIN;
	for (const d of dangling) {
		if (d.anchor) {
			const n = stackAt.get(d.anchor.id) ?? 0;
			stackAt.set(d.anchor.id, n + 1);
			const x = d.anchor.x + d.anchor.w + 16;
			const y = d.anchor.y + n * (G.CHIP_H + 8);
			orphans.push({ t: d.t, anchor: d.anchor, x, y, w: G.CHIP_W, h: G.CHIP_H });
			width = Math.max(width, x + G.CHIP_W + G.MARGIN);
			height = Math.max(height, y + G.CHIP_H + G.MARGIN);
		} else {
			orphans.push({ t: d.t, x: unanchoredX, y: 0, w: G.CHIP_W, h: G.CHIP_H }); // y fixed below, once `height` settles
			unanchoredX += G.CHIP_W + 12;
		}
	}
	const stripY = height + 16;
	let sawUnanchored = false;
	for (const o of orphans) {
		if (!o.anchor) {
			o.y = stripY;
			sawUnanchored = true;
		}
	}
	if (sawUnanchored) {
		height = stripY + G.CHIP_H + G.MARGIN;
		width = Math.max(width, unanchoredX + G.MARGIN);
	}

	return { steps, trans, notes, orphans, width, height };
}

// ── drag-to-connect ──────────────────────────────────────────────────────

/** The step (if any) whose box contains canvas point (x, y) — the
 * drag-to-connect gesture's live drop-target lookup. */
export function stepAtPoint(layout: SfcLayout, x: number, y: number): PlacedStep | undefined {
	return layout.steps.find((p) => x >= p.x && x <= p.x + p.w && y >= p.y && y <= p.y + p.h);
}

/** Where a step's connect handle sits: bottom-center of its box — steps
 * flow top-to-bottom (§ the module doc), so this is the natural "drag to
 * make a transition" affordance, mirroring FBD's output-pin-on-the-right. */
export function connectHandlePos(p: PlacedStep): { x: number; y: number } {
	return { x: p.x + p.w / 2, y: p.y + p.h };
}

// ── delete-step cascade ──────────────────────────────────────────────────

/** Transitions "attached" to a step: its name appears (case-insensitive) in
 * either the transition's FROM or TO set — what the delete-step popover
 * offers to cascade-delete alongside the step itself. */
export function attachedTransitions(model: SfcModel, stepName: string): SfcTransition[] {
	const lower = stepName.toLowerCase();
	return (model.trans ?? []).filter(
		(t) => t.from.some((n) => n.toLowerCase() === lower) || t.to.some((n) => n.toLowerCase() === lower)
	);
}

export type CascadeOp = { type: 'deleteTransition'; transition: string } | { type: 'deleteStep'; step: string };

/** Ops for "delete step + its attached transitions", ordered so every op's
 * id stays resolvable against the CURRENT text as the host's edit queue
 * applies them one at a time (each op is resolved against a FRESH parse —
 * see sfcPreview.ts's sfcEditQueue — but an UNNAMED transition's id is its
 * TRANSITION keyword's LINE NUMBER, tr:<line>, which shifts when an earlier
 * deletion in this same cascade removes lines above it). Deleting
 * strictly bottom-of-file-first (highest line number first) means every
 * remaining target in the batch is still entirely above every completed
 * deletion, so its line — and hence its id — never goes stale; the step
 * itself (always the topmost of the group, since steps are declared before
 * transitions) is deleted last. */
export function cascadeDeleteOps(step: SfcStep, transitions: SfcTransition[]): CascadeOp[] {
	const items: { line: number; op: CascadeOp }[] = transitions.map((t) => ({
		line: t.line,
		op: { type: 'deleteTransition', transition: t.id }
	}));
	items.push({ line: step.line, op: { type: 'deleteStep', step: step.id } });
	items.sort((a, b) => b.line - a.line);
	return items.map((i) => i.op);
}

// ── visual diff (base ↔ head) ─────────────────────────────────────────────
// A TypeScript port of the same overlay shape lang/fbd's diff.ts (mergeDiff)
// and ladder.ts (diffLd) use: match base and head by stable id, stamp a
// DiffStatus on every element, splice removed elements back into the head
// list (ghosted, never editable — SfcView only enables edit gestures when
// !diffing) so a reviewer sees what left as well as what arrived. Steps/
// transitions/actions are id-addressed exactly like the live editor
// addresses them (st:/tr:/ac:) — a RENAME changes that id, so like FBD's
// wire/instance rename it shows as removed+added rather than "changed",
// the same accepted trade-off. Comments have no stable id (purely
// positional prose), so they match by exact text instead — good enough to
// show a note arriving or leaving; an EDITED comment shows as one of each,
// not "changed".
function stepShape(s: SfcStep): string {
	return JSON.stringify({ initial: s.initial, actions: s.actions ?? [] });
}
function transShape(t: SfcTransition): string {
	return JSON.stringify({ from: t.from, to: t.to, cond: t.cond, kind: t.kind, name: t.name ?? '' });
}

export function diffSfc(base: SfcModel, head: SfcModel): SfcModel {
	const bSteps = new Map(base.steps.map((s) => [s.id, s]));
	const hStepIds = new Set(head.steps.map((s) => s.id));
	const steps: SfcStep[] = head.steps.map((s) => {
		const b = bSteps.get(s.id);
		return b
			? { ...s, status: stepShape(b) !== stepShape(s) ? ('changed' as const) : undefined }
			: { ...s, status: 'added' as const };
	});
	for (const b of base.steps) if (!hStepIds.has(b.id)) steps.push({ ...b, status: 'removed' });

	const bTrans = new Map(base.trans.map((t) => [t.id, t]));
	const hTransIds = new Set(head.trans.map((t) => t.id));
	const trans: SfcTransition[] = head.trans.map((t) => {
		const b = bTrans.get(t.id);
		return b
			? { ...t, status: transShape(b) !== transShape(t) ? ('changed' as const) : undefined }
			: { ...t, status: 'added' as const };
	});
	for (const b of base.trans) if (!hTransIds.has(b.id)) trans.push({ ...b, status: 'removed' });

	const bActions = new Map((base.actions ?? []).map((a) => [a.id, a]));
	const hActionIds = new Set((head.actions ?? []).map((a) => a.id));
	const actions: SfcAction[] = (head.actions ?? []).map((a) => {
		const b = bActions.get(a.id);
		return b
			? { ...a, status: b.body !== a.body ? ('changed' as const) : undefined }
			: { ...a, status: 'added' as const };
	});
	for (const b of base.actions ?? []) if (!hActionIds.has(b.id)) actions.push({ ...b, status: 'removed' });

	// Greedy text match: each head comment consumes the first unmatched base
	// comment with identical text, so N identical notes diff as N unchanged
	// rather than all-removed-all-added.
	const baseRemaining = [...(base.comments ?? [])];
	const comments: SfcComment[] = (head.comments ?? []).map((c) => {
		const i = baseRemaining.findIndex((b) => b.text === c.text);
		if (i === -1) return { ...c, status: 'added' };
		baseRemaining.splice(i, 1);
		return { ...c };
	});
	comments.push(...baseRemaining.map((c) => ({ ...c, status: 'removed' as const })));

	return {
		name: head.name || base.name,
		vars: head.vars,
		steps,
		trans,
		actions,
		comments,
		layout: head.layout,
	};
}

// ── clipboard (copy / cut / paste of steps) ─────────────────────────────────

/** What a copy of SFC steps carries: each step's name, associations and
 * position, plus the transitions WHOLLY inside the selection (both ends
 * copied) — a transition to a step left behind would paste dangling. */
export type SfcClip = {
	steps: { name: string; actions: { qualifier: string; target: string; time?: string }[]; x: number; y: number }[];
	trans: { name?: string; from: string[]; to: string[]; cond: string }[];
};

/** The transitions whose every FROM and TO step is in `names`. */
export function innerTransitions(model: SfcModel, names: Set<string>): SfcTransition[] {
	const inSet = (n: string) => names.has(n.toLowerCase());
	return (model.trans ?? []).filter((t) => t.from.length > 0 && t.to.length > 0 && t.from.every(inSet) && t.to.every(inSet));
}

export function clipSteps(model: SfcModel, layout: SfcLayout, ids: string[]): SfcClip | undefined {
	const placed = new Map(layout.steps.map((p) => [p.id, p]));
	const steps: SfcClip['steps'] = [];
	for (const id of ids) {
		const p = placed.get(id);
		if (!p) continue;
		steps.push({
			name: p.step.name,
			actions: (p.step.actions ?? []).map((a) => (a.time ? { qualifier: a.qualifier, target: a.target, time: a.time } : { qualifier: a.qualifier, target: a.target })),
			x: p.x,
			y: p.y
		});
	}
	if (!steps.length) return undefined;
	const names = new Set(steps.map((s) => s.name.toLowerCase()));
	const trans = innerTransitions(model, names).map((t) => ({
		...(t.name ? { name: t.name } : {}),
		from: [...t.from],
		to: [...t.to],
		cond: t.cond
	}));
	return { steps, trans };
}

/** The pasteSteps op for a clip: the copies keep their relative
 * arrangement and land in a clear column to the right of the current
 * chart, top-aligned with it — never on top of an existing step. */
export function pasteStepsOp(clip: SfcClip, layout: SfcLayout): Record<string, unknown> {
	const minX = Math.min(...clip.steps.map((s) => s.x));
	const minY = Math.min(...clip.steps.map((s) => s.y));
	const top = layout.steps.length ? Math.min(...layout.steps.map((p) => p.y)) : G.MARGIN;
	// layout.width spans the action tables and notes too, not just the boxes.
	const left = layout.steps.length ? layout.width + 20 : G.MARGIN;
	return {
		type: 'pasteSteps',
		steps: clip.steps.map((s) => ({
			name: s.name,
			actions: s.actions,
			x: Math.round(left + (s.x - minX)),
			y: Math.round(top + (s.y - minY))
		})),
		trans: clip.trans
	};
}

/** One deleteSelection op for a set of steps: the steps plus every
 * transition wholly between them (what a cut copied along). */
export function deleteStepsOp(model: SfcModel, ids: string[]): Record<string, unknown> {
	const steps = (model.steps ?? []).filter((s) => ids.includes(s.id));
	const names = new Set(steps.map((s) => s.name.toLowerCase()));
	return { type: 'deleteSelection', nodes: [...steps.map((s) => s.id), ...innerTransitions(model, names).map((t) => t.id)] };
}
