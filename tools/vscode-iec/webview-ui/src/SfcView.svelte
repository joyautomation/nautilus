<script lang="ts">
	// The SFC chart: steps as boxes (double border = initial), their action
	// associations as a table hugging the box's right side, transitions as
	// bars across the flow line (single = normal/alternative, double =
	// simultaneous) with their condition beside the bar. Structure is
	// canonical in the text (LD's posture, per docs/design/sfc.md §4) — the
	// pure layout pass (sfc.ts) derives positions from topology; only a
	// dragged step's position is stored, as a `(* @layout *)` pin.
	//
	// Every gesture posts a structural op through `onOp`, exactly like the
	// ladder/FBD editors: no span/text knowledge lives here.
	import {
		attachedTransitions,
		cascadeDeleteOps,
		commentId,
		connectHandlePos,
		layoutSfc,
		stepAtPoint,
		stepId,
		type OrphanChip,
		type PlacedNote,
		type PlacedStep,
		type SfcModel,
		type SfcStep,
		type SfcTransition,
		type TransRoute
	} from './sfc';
	import { live, liveValue } from './liveState.svelte';

	type Diag = { line: number; message: string; severity: string };

	let {
		model,
		editable = false,
		diags = [],
		showLive = true,
		onOp,
		requestInput
	}: {
		model: SfcModel;
		editable?: boolean;
		diags?: Diag[];
		showLive?: boolean;
		onOp?: (op: Record<string, unknown>) => void;
		requestInput?: (
			init: string,
			at: { x: number; y: number; w: number },
			commit: (v: string) => void,
			opts?: { multiline?: boolean; suggest?: 'tags' | 'types' | 'functions' }
		) => void;
	} = $props();

	function post(op: Record<string, unknown>) {
		try {
			onOp?.(JSON.parse(JSON.stringify(op)));
		} catch {
			/* structured-clone guard, mirrors the ladder/FBD editors */
		}
	}

	const layout = $derived(layoutSfc(model));
	const diagsByLine = $derived.by(() => {
		const m = new Map<number, Diag[]>();
		for (const d of diags) {
			const arr = m.get(d.line) ?? [];
			arr.push(d);
			m.set(d.line, arr);
		}
		return m;
	});
	function problemsFor(line: number, endLine: number): Diag[] {
		const out: Diag[] = [];
		for (let l = line; l <= endLine; l++) out.push(...(diagsByLine.get(l) ?? []));
		return out;
	}

	// ── live step activity ───────────────────────────────────────────────────
	// The transpiler retains one BOOL slot per step, `_S_<Step>_X` (see
	// docs/design/sfc.md §2.6/§3.1); runtime.AllLocals streams every
	// program's retained locals, and liveValues.ts fans that in as `locals`
	// on the SAME frame text pills use — so a step's activity resolves
	// exactly like any other live label, no controller API of its own.
	const showVal = $derived(showLive && live.enabled && live.fresh);
	function stepActive(name: string): boolean | undefined {
		if (!showVal) return undefined;
		const v = liveValue(`_S_${name}_X`);
		return typeof v === 'boolean' ? v : undefined;
	}

	// ── selection ────────────────────────────────────────────────────────────
	type Sel =
		| { kind: 'step'; id: string }
		| { kind: 'trans'; id: string }
		| { kind: 'assoc'; step: string; index: number }
		| { kind: 'comment'; index: number }
		| null;
	let selected = $state<Sel>(null);
	const isSelStep = (id: string) => selected?.kind === 'step' && selected.id === id;
	const isSelTrans = (id: string) => selected?.kind === 'trans' && selected.id === id;
	const isSelAssoc = (step: string, i: number) => selected?.kind === 'assoc' && selected.step === step && selected.index === i;
	const isSelComment = (i: number) => selected?.kind === 'comment' && selected.index === i;

	function selectStep(ev: Event, id: string) {
		ev.stopPropagation();
		selected = { kind: 'step', id };
	}
	function selectTrans(ev: Event, id: string) {
		ev.stopPropagation();
		selected = { kind: 'trans', id };
	}
	function selectAssoc(ev: Event, step: string, index: number) {
		ev.stopPropagation();
		selected = { kind: 'assoc', step, index };
	}
	function selectComment(ev: Event, index: number) {
		ev.stopPropagation();
		selected = { kind: 'comment', index };
	}
	function editComment(ev: Event, note: PlacedNote) {
		ev.stopPropagation();
		if (!editable || !requestInput) return;
		const rect = (ev.currentTarget as Element).getBoundingClientRect();
		requestInput(note.c.text, { x: rect.left, y: rect.top, w: Math.max(rect.width, 200) }, (v) => post({ type: 'setComment', comment: note.index, text: v }), { multiline: true });
	}

	// ── editing gestures ─────────────────────────────────────────────────────

	function renameStep(ev: Event, p: PlacedStep) {
		ev.stopPropagation();
		if (!editable || !requestInput) return;
		const rect = (ev.currentTarget as Element).getBoundingClientRect();
		requestInput(p.step.name, { x: rect.left, y: rect.top, w: Math.max(rect.width, 90) }, (v) => {
			if (v && v !== p.step.name) post({ type: 'renameStep', step: p.id, newName: v });
		});
	}

	function editCondition(ev: Event, t: SfcTransition, at: { x: number; y: number; w: number }) {
		ev.stopPropagation();
		if (!editable || !requestInput) return;
		requestInput(t.cond, at, (v) => post({ type: 'setCondition', transition: t.id, cond: v }));
	}

	function assocText(a: { qualifier: string; target: string; time?: string }): string {
		return a.time ? `${a.qualifier} ${a.target}(${a.time})` : `${a.qualifier} ${a.target}`;
	}
	function parseAssoc(text: string): { qualifier: string; target: string; time?: string } | undefined {
		const m = /^\s*([A-Za-z0-9]+)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(([^)]*)\))?\s*$/.exec(text);
		if (!m) return undefined;
		return { qualifier: m[1].toUpperCase(), target: m[2], time: m[3]?.trim() || undefined };
	}

	function editAssoc(ev: Event, step: SfcStep, index: number) {
		ev.stopPropagation();
		if (!editable || !requestInput) return;
		const a = step.actions![index];
		const rect = (ev.currentTarget as Element).getBoundingClientRect();
		requestInput(assocText(a), { x: rect.left, y: rect.top, w: Math.max(rect.width, 140) }, (v) => {
			const parsed = parseAssoc(v);
			if (parsed) post({ type: 'setAssoc', step: step.id, index, ...parsed });
		});
	}

	function addAssoc(ev: Event, step: SfcStep) {
		ev.stopPropagation();
		if (!editable || !requestInput) return;
		const rect = (ev.currentTarget as Element).getBoundingClientRect();
		requestInput('N Output', { x: rect.left, y: rect.bottom, w: 140 }, (v) => {
			const parsed = parseAssoc(v);
			if (parsed) post({ type: 'addAssoc', step: step.id, ...parsed });
		});
	}

	function deleteAssoc(ev: Event, step: SfcStep, index: number) {
		ev.stopPropagation();
		post({ type: 'deleteAssoc', step: step.id, index });
		selected = null;
	}

	// A body-action association (its target names an ACTION block, not a
	// plain BOOL variable) opens the multiline ST-body editor instead of the
	// "qualifier target" field.
	function actionFor(name: string) {
		return model.actions?.find((a) => a.name.toLowerCase() === name.toLowerCase());
	}
	function editActionBody(ev: Event, name: string) {
		ev.stopPropagation();
		if (!editable || !requestInput) return;
		const rect = (ev.currentTarget as Element).getBoundingClientRect();
		const body = actionFor(name)?.body ?? '';
		requestInput(body, { x: rect.left, y: rect.bottom + 4, w: Math.max(rect.width, 220) }, (v) => post({ type: 'setActionBody', action: name, body: v }), { multiline: true });
	}

	// ── step-name picker (dropdown of existing steps + a typed "other…"
	// escape hatch, since the text format allows naming a not-yet-existing
	// step and Check — not the editor — is what should flag it) ───────────
	const OTHER = '§other';
	function stepNames(): string[] {
		return (model.steps ?? []).map((s) => s.name);
	}
	/** The value a <select> should show for a (possibly not-yet-existing)
	 * step name: the name itself if it's a real step, else the OTHER
	 * sentinel (which renders the free-text fallback input). */
	function pickerSelectValue(name: string): string {
		return stepNames().some((n) => n.toLowerCase() === name.toLowerCase()) ? name : OTHER;
	}
	/** "FROM {…}"/"TO {…}" text for an orphan chip: the raw name list, or a
	 * literal "?" when a transition somehow has an empty side (shouldn't
	 * happen for text that parsed, but never crash the diagram over it). */
	function fmtEnds(names: string[]): string {
		return names.length ? names.join(', ') : '?';
	}

	// ── add step / transition / branches (a small popover form) ────────────
	let addOpen = $state(false);
	type AddKind = 'step' | 'transition' | 'alt' | 'sim';
	let addKind = $state<AddKind>('step');
	let fName = $state('');
	let fTo = $state('');
	let fCond = $state('TRUE');

	function openAdd(kind: AddKind) {
		addKind = kind;
		fName = nextStepName();
		fTo = '';
		fCond = 'TRUE';
		addOpen = true;
	}
	// Placeholder-text note, landing just above END_SFC — dblclick edits,
	// the same "post with a stub, edit in place" gesture LadderView's
	// addNote/FBD's palette comment template both use.
	function addNote() {
		post({ type: 'addComment', text: 'note' });
	}
	function nextStepName(): string {
		const steps = model.steps ?? [];
		const taken = new Set(steps.map((s) => s.name.toLowerCase()));
		let i = steps.length + 1;
		while (taken.has('step' + i)) i++;
		return 'Step' + i;
	}
	function selectedStepName(): string | undefined {
		if (selected?.kind !== 'step') return undefined;
		const id = selected.id;
		return (model.steps ?? []).find((s) => s.id === id)?.name;
	}
	function selectedTransId(): string | undefined {
		return selected?.kind === 'trans' ? selected.id : undefined;
	}
	function commitAdd() {
		if (addKind === 'step') {
			// A chart's first step is its INITIAL_STEP — an empty chart
			// (or a blank file being seeded) starts runnable.
			const first = (model.steps ?? []).length === 0;
			post({ type: 'addStep', name: fName, initial: first || undefined, after: selectedStepName() ? stepId(selectedStepName()!) : undefined });
		} else if (addKind === 'transition') {
			const from = selectedStepName();
			if (!from || !fTo) return;
			post({ type: 'addTransition', from: [from], to: [fTo], cond: fCond });
		} else if (addKind === 'alt') {
			const after = selectedTransId();
			if (!after || !fTo) return;
			post({ type: 'insertAlternativeBranch', after, to: [fTo], cond: fCond });
		} else if (addKind === 'sim') {
			const transition = selectedTransId();
			if (!transition || !fName) return;
			post({ type: 'insertSimultaneousBranch', transition, newStep: fName });
		}
		addOpen = false;
	}

	// ── drag a step: body-drag PINS its layout (setLayout on release); the
	// small connect handle on its bottom edge instead DRAGS OUT A WIRE that
	// creates a transition on drop — the same split FBD uses (a Handle wires,
	// the node body moves) and the mimic editor uses (ports drag differently
	// than the equipment box), adapted to this hand-rolled SVG (no xyflow
	// here, so the "handle" is a small always-present hit target rather than
	// a library Handle component; it only becomes visually prominent on
	// hover/while dragging, via CSS, so it never competes with the box for
	// attention at rest). ──────────────────────────────────────────────────
	type Drag =
		| { kind: 'move'; id: string; startX: number; startY: number; sx: number; sy: number; started: boolean }
		| { kind: 'connect'; from: string; x: number; y: number };
	let drag = $state<Drag | null>(null);
	let svgEl = $state<SVGSVGElement | undefined>();
	// Replaced wholesale on every change (never mutated in place) — plain
	// $state doesn't deep-track a Map, matching diagState.svelte.ts's
	// convention.
	let overrideAt = $state<Map<string, { x: number; y: number }>>(new Map());
	// Generic by-id live-drag override — steps use it via displayPos below;
	// comment notes (drag/pin parity with FBD's comment nodes) use it
	// directly through posOverride, since a PlacedNote isn't a PlacedStep.
	function posOverride(id: string, x: number, y: number): { x: number; y: number } {
		return overrideAt.get(id) ?? { x, y };
	}
	function displayPos(p: PlacedStep): { x: number; y: number } {
		return posOverride(p.id, p.x, p.y);
	}
	function beginDrag(ev: PointerEvent, p: PlacedStep) {
		if (!editable) return;
		ev.stopPropagation();
		selected = { kind: 'step', id: p.id };
		drag = { kind: 'move', id: p.id, startX: p.x, startY: p.y, sx: ev.clientX, sy: ev.clientY, started: false };
	}
	function beginNoteDrag(ev: PointerEvent, n: PlacedNote) {
		if (!editable) return;
		ev.stopPropagation();
		selected = { kind: 'comment', index: n.index };
		drag = { kind: 'move', id: commentId(n.index), startX: n.x, startY: n.y, sx: ev.clientX, sy: ev.clientY, started: false };
	}
	function svgPoint(ev: PointerEvent): { x: number; y: number } {
		const rect = svgEl?.getBoundingClientRect();
		return rect ? { x: ev.clientX - rect.left, y: ev.clientY - rect.top } : { x: 0, y: 0 };
	}
	function beginConnect(ev: PointerEvent, p: PlacedStep) {
		if (!editable) return;
		ev.stopPropagation();
		ev.preventDefault();
		selected = { kind: 'step', id: p.id };
		drag = { kind: 'connect', from: p.id, ...svgPoint(ev) };
	}
	/** The step under the connect drag's current point — the live drop
	 * target — excluding the source step itself (no self-loop via this
	 * gesture; use the transition popover's free-text form for that). */
	const connectTarget = $derived.by((): PlacedStep | undefined => {
		if (drag?.kind !== 'connect') return undefined;
		const t = stepAtPoint(layout, drag.x, drag.y);
		return t && t.id !== drag.from ? t : undefined;
	});
	function onPointerMove(ev: PointerEvent) {
		if (!drag) return;
		if (drag.kind === 'connect') {
			drag = { ...drag, ...svgPoint(ev) };
			return;
		}
		const dx = ev.clientX - drag.sx;
		const dy = ev.clientY - drag.sy;
		if (!drag.started && Math.hypot(dx, dy) < 4) return;
		drag.started = true;
		const next = new Map(overrideAt);
		next.set(drag.id, { x: Math.round(drag.startX + dx), y: Math.round(drag.startY + dy) });
		overrideAt = next;
	}
	// A newly connect-dragged transition is selected as soon as the model
	// round-trips (App.svelte's onOp -> host -> naut sfc edit -> new
	// sfcModel) — matched by its (from, to) pair rather than a guessed id,
	// since an unnamed transition's id (tr:<line>) isn't known client-side
	// until the model comes back.
	let pendingSelectTrans = $state<{ from: string; to: string } | null>(null);
	$effect(() => {
		if (!pendingSelectTrans) return;
		const want = pendingSelectTrans;
		const match = (model.trans ?? []).find(
			(t) =>
				t.from.length === 1 &&
				t.to.length === 1 &&
				t.from[0].toLowerCase() === want.from.toLowerCase() &&
				t.to[0].toLowerCase() === want.to.toLowerCase()
		);
		if (match) {
			selected = { kind: 'trans', id: match.id };
			pendingSelectTrans = null;
		}
	});
	function onPointerUp() {
		if (!drag) return;
		if (drag.kind === 'connect') {
			const from = drag.from;
			const target = connectTarget;
			drag = null;
			if (!target) return;
			const fromName = (model.steps ?? []).find((s) => s.id === from)?.name;
			const toName = target.step.name;
			if (!fromName) return;
			pendingSelectTrans = { from: fromName, to: toName };
			post({ type: 'addTransition', from: [fromName], to: [toName], cond: 'TRUE' });
			return;
		}
		const d = drag;
		drag = null;
		if (!d.started) return;
		const at = overrideAt.get(d.id);
		if (at) post({ type: 'setLayout', node: d.id, x: at.x, y: at.y });
	}
	// Once the extension round-trips the setLayout op, the model's own
	// `layout` map carries the pin and the override is redundant — drop it
	// so a later auto-reflow (e.g. clearLayout) isn't masked by a stale
	// local override.
	$effect(() => {
		void model;
		overrideAt = new Map();
	});

	// ── delete-step cascade offer (in-canvas popover, never a native
	// confirm()) ─────────────────────────────────────────────────────────
	// Default action stays "step only" — the breadcrumb philosophy (Check
	// flags the dangling transitions, nothing is silently cascaded) — the
	// popover only OFFERS the cascade as an explicit second button.
	let deleteStepConfirm = $state<{ stepId: string; stepName: string; attached: SfcTransition[] } | null>(null);

	function requestDeleteSelected() {
		if (!selected) return;
		if (selected.kind === 'step') {
			const id = selected.id;
			const step = (model.steps ?? []).find((s) => s.id === id);
			if (!step) return;
			const attached = attachedTransitions(model, step.name);
			if (attached.length > 0) {
				deleteStepConfirm = { stepId: step.id, stepName: step.name, attached };
				return;
			}
			post({ type: 'deleteStep', step: step.id });
		} else if (selected.kind === 'trans') {
			post({ type: 'deleteTransition', transition: selected.id });
		} else if (selected.kind === 'assoc') {
			post({ type: 'deleteAssoc', step: selected.step, index: selected.index });
		} else if (selected.kind === 'comment') {
			post({ type: 'deleteComment', comment: selected.index });
		}
		selected = null;
	}
	function confirmDeleteStepOnly() {
		if (!deleteStepConfirm) return;
		post({ type: 'deleteStep', step: deleteStepConfirm.stepId });
		deleteStepConfirm = null;
		selected = null;
	}
	function confirmDeleteStepCascade() {
		if (!deleteStepConfirm) return;
		const step = (model.steps ?? []).find((s) => s.id === deleteStepConfirm!.stepId);
		if (step) {
			// Ordering matters: see cascadeDeleteOps's doc — an unnamed
			// transition's id is its line number, which shifts once an
			// earlier delete in this same batch removes lines above it, so
			// the ops are pre-sorted bottom-of-file-first.
			for (const op of cascadeDeleteOps(step, deleteStepConfirm.attached)) post(op);
		}
		deleteStepConfirm = null;
		selected = null;
	}

	// ── orphaned-transition retarget popover (setTransitionEnds) ───────────
	type RetargetState = { transId: string; from: string[]; to: string[] };
	let retargetOpen = $state<RetargetState | null>(null);
	function openRetarget(ev: Event, o: OrphanChip) {
		ev.stopPropagation();
		retargetOpen = { transId: o.t.id, from: [...o.t.from], to: [...o.t.to] };
	}
	function setRetargetEnd(side: 'from' | 'to', i: number, value: string) {
		if (!retargetOpen) return;
		const arr = [...retargetOpen[side]];
		arr[i] = value;
		retargetOpen = { ...retargetOpen, [side]: arr };
	}
	function addRetargetEnd(side: 'from' | 'to') {
		if (!retargetOpen) return;
		retargetOpen = { ...retargetOpen, [side]: [...retargetOpen[side], stepNames()[0] ?? ''] };
	}
	function removeRetargetEnd(side: 'from' | 'to', i: number) {
		if (!retargetOpen || retargetOpen[side].length <= 1) return;
		retargetOpen = { ...retargetOpen, [side]: retargetOpen[side].filter((_, j) => j !== i) };
	}
	function commitRetarget() {
		if (!retargetOpen) return;
		const from = retargetOpen.from.map((n) => n.trim()).filter(Boolean);
		const to = retargetOpen.to.map((n) => n.trim()).filter(Boolean);
		if (from.length && to.length) post({ type: 'setTransitionEnds', transition: retargetOpen.transId, from, to });
		retargetOpen = null;
	}

	// ── keyboard: Esc cancels a connect drag / closes a popover; Del deletes
	// the selection (offering the cascade popover for an attached step) ────
	function handleKey(ev: KeyboardEvent) {
		if (ev.key === 'Escape') {
			if (drag?.kind === 'connect') {
				drag = null;
				ev.preventDefault();
				return;
			}
			if (deleteStepConfirm) {
				deleteStepConfirm = null;
				ev.preventDefault();
				return;
			}
			if (retargetOpen) {
				retargetOpen = null;
				ev.preventDefault();
				return;
			}
			if (addOpen) {
				closeAdd();
				ev.preventDefault();
				return;
			}
		}
		if (!editable) return;
		const ae = document.activeElement;
		if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA')) return;
		if (ev.key === 'Delete' || ev.key === 'Backspace') {
			if (!selected) return;
			requestDeleteSelected();
			ev.preventDefault();
		}
	}

	/** Escape cancels an in-flight connect drag even if focus somehow isn't
	 * on `.wrap` (pointer capture during a drag doesn't guarantee it) — a
	 * thin, idempotent safety net alongside handleKey's own Escape handling,
	 * deliberately NOT reusing handleKey wholesale here (that would double
	 * -fire Delete for a keydown that bubbles through both this window
	 * listener and .wrap's). */
	function onWindowEscape(ev: KeyboardEvent) {
		if (ev.key === 'Escape' && drag?.kind === 'connect') {
			drag = null;
			ev.preventDefault();
		}
	}

	// The add form: Enter submits, Esc closes, the first field takes focus.
	let wrapEl = $state<HTMLDivElement | undefined>();
	function closeAdd() {
		addOpen = false;
		wrapEl?.focus({ preventScroll: true });
	}
	function addFormKey(ev: KeyboardEvent) {
		ev.stopPropagation();
		if (ev.key === 'Enter') {
			ev.preventDefault();
			commitAdd();
			if (!addOpen) wrapEl?.focus({ preventScroll: true });
		} else if (ev.key === 'Escape') {
			ev.preventDefault();
			closeAdd();
		}
	}
	function autofocus(el: HTMLElement) {
		queueMicrotask(() => {
			el.focus();
			if (el instanceof HTMLInputElement) el.select();
		});
	}
	// Keep keyboard focus on the chart after any pointer gesture on it —
	// Del/Esc listen on .wrap, and the float editor (dblclick edits) hands
	// focus back to whatever held it when it opened.
	function focusWrap(ev: PointerEvent) {
		const t = ev.target as HTMLElement | null;
		if (t?.closest?.('input, select, textarea, button, .addform')) return;
		wrapEl?.focus({ preventScroll: true });
	}

	const hasPins = $derived(
		Object.keys(model.layout ?? {}).some((id) => id.startsWith('st:') || id.startsWith('cm:'))
	);
</script>

<!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events a11y_no_noninteractive_tabindex -->
<div class="wrap" tabindex={editable ? 0 : undefined} onkeydown={handleKey} onpointerdowncapture={focusWrap} bind:this={wrapEl}>
	{#if editable}
		<div class="palette" onclick={(e) => e.stopPropagation()}>
			<button title="Add a step (after the selected step, or at the end)" onclick={() => openAdd('step')}>+ step</button>
			<button title="Add a transition from the selected step to another (existing or new)" disabled={!selectedStepName()} onclick={() => openAdd('transition')}>+ transition</button>
			<button title="Add an alternative branch off the selected transition's source (priority = insertion order)" disabled={!selectedTransId()} onclick={() => openAdd('alt')}>+ alt branch</button>
			<button title="Widen the selected transition into a simultaneous divergence, adding a new parallel step" disabled={!selectedTransId()} onclick={() => openAdd('sim')}>+ parallel branch</button>
			<button title="Add a diagram note — dblclick to write it" onclick={addNote}>+ comment</button>
			<span class="sep"></span>
			<button title="Delete the selection (Del) — deleting a step with attached transitions offers a cascade choice" disabled={!selected} onclick={requestDeleteSelected}>✕ delete</button>
			{#if hasPins}
				<button title="Clear all pinned step positions (back to full auto-layout)" onclick={() => post({ type: 'clearLayout' })}>auto layout</button>
			{/if}
		</div>
	{/if}

	{#if addOpen}
		<div class="addform" onclick={(e) => e.stopPropagation()} onkeydown={addFormKey}>
			<div class="addtitle">
				{addKind === 'step' ? 'Add step' : addKind === 'transition' ? `Transition from ${selectedStepName()}` : addKind === 'alt' ? 'Alternative branch' : 'Simultaneous branch'}
			</div>
			{#if addKind === 'step' || addKind === 'sim'}
				<label><span>name</span><input class="nx-input" bind:value={fName} spellcheck="false" use:autofocus /></label>
			{/if}
			{#if addKind === 'transition' || addKind === 'alt'}
				<label>
					<span>to step</span>
					<span class="picker">
						<select
							class="nx-input"
							use:autofocus
							value={pickerSelectValue(fTo)}
							onchange={(e) => {
								const v = (e.currentTarget as HTMLSelectElement).value;
								fTo = v === OTHER ? '' : v;
							}}
						>
							{#each stepNames() as n (n)}<option value={n}>{n}</option>{/each}
							<option value={OTHER}>other… (new step)</option>
						</select>
						{#if pickerSelectValue(fTo) === OTHER}
							<input class="nx-input" bind:value={fTo} spellcheck="false" placeholder="new step name" />
						{/if}
					</span>
				</label>
				<label><span>condition</span><input class="nx-input" bind:value={fCond} spellcheck="false" /></label>
			{/if}
			<div class="addactions">
				<button onclick={closeAdd}>cancel</button>
				<button class="primary" title="Enter" onclick={commitAdd}>add</button>
			</div>
		</div>
	{/if}

	{#if deleteStepConfirm}
		<div class="addform confirm" onclick={(e) => e.stopPropagation()}>
			<div class="addtitle">Delete step "{deleteStepConfirm.stepName}"?</div>
			<div class="confirmbody">
				{deleteStepConfirm.attached.length} attached transition{deleteStepConfirm.attached.length === 1 ? '' : 's'} reference this step.
			</div>
			<div class="addactions">
				<button onclick={() => (deleteStepConfirm = null)}>cancel</button>
				<button onclick={confirmDeleteStepCascade}>step + {deleteStepConfirm.attached.length} transition{deleteStepConfirm.attached.length === 1 ? '' : 's'}</button>
				<button class="primary" onclick={confirmDeleteStepOnly}>step only (flag {deleteStepConfirm.attached.length})</button>
			</div>
		</div>
	{/if}

	{#if retargetOpen}
		<div class="addform retarget" onclick={(e) => e.stopPropagation()}>
			<div class="addtitle">Retarget transition</div>
			{#each (['from', 'to'] as const) as side (side)}
				<div class="retargetgroup">
					<span class="retargetlabel">{side.toUpperCase()}</span>
					{#each retargetOpen[side] as v, i (i)}
						<span class="picker retargetrow">
							<select
								class="nx-input"
								value={pickerSelectValue(v)}
								onchange={(e) => {
									const val = (e.currentTarget as HTMLSelectElement).value;
									setRetargetEnd(side, i, val === OTHER ? '' : val);
								}}
							>
								{#each stepNames() as n (n)}<option value={n}>{n}</option>{/each}
								<option value={OTHER}>other…</option>
							</select>
							{#if pickerSelectValue(v) === OTHER}
								<input class="nx-input" value={v} oninput={(e) => setRetargetEnd(side, i, (e.currentTarget as HTMLInputElement).value)} spellcheck="false" placeholder="step name" />
							{/if}
							{#if retargetOpen[side].length > 1}
								<button class="mini" title="remove" onclick={() => removeRetargetEnd(side, i)}>✕</button>
							{/if}
						</span>
					{/each}
					<button class="mini" title="add another {side.toUpperCase()} step (simultaneous convergence/divergence)" onclick={() => addRetargetEnd(side)}>+ {side}</button>
				</div>
			{/each}
			<div class="addactions">
				<button onclick={() => (retargetOpen = null)}>cancel</button>
				<button class="primary" onclick={commitRetarget}>apply</button>
			</div>
		</div>
	{/if}

	<svg class="chart" bind:this={svgEl} width={layout.width} height={layout.height} onclick={() => (selected = null)}>
		{#each layout.trans as r (r.t.id)}
			{@const problems = problemsFor(r.t.line, r.t.endLine)}
			<g class="trans {r.t.status ?? ''}" class:selected={isSelTrans(r.t.id)}>
				{#if r.jump}
					<g class="jump" transform="translate({r.jump.x}, {r.jump.y})" onclick={(e) => selectTrans(e, r.t.id)}>
						<title>{r.t.name ? r.t.name + ': ' : ''}{r.t.cond} — jumps to {r.t.to.join(', ')} (a loop back, drawn compact rather than as a long line){editable ? ' — click to select, dblclick condition to edit' : ''}</title>
						<rect x="-9" y="-9" width="18" height="18" rx="3" class="jumpbox" class:double={r.double} />
						<text y="4" text-anchor="middle" class="jumpmark">↩</text>
						<text
							x="16"
							y="4"
							class="cond jumplabel"
							ondblclick={(e) => editCondition(e, r.t, { x: (e.currentTarget as Element).getBoundingClientRect().left, y: (e.currentTarget as Element).getBoundingClientRect().top, w: 160 })}
						>{r.jump.label}: {r.t.cond}</text>
					</g>
				{:else}
					{#each r.legsIn as leg, i (i)}<line x1={leg.x} y1={leg.y1} x2={leg.x} y2={leg.y2} class="flowline" />{/each}
					{#each r.legsOut as leg, i (i)}<line x1={leg.x} y1={leg.y1} x2={leg.x} y2={leg.y2} class="flowline" />{/each}
					<g onclick={(e) => selectTrans(e, r.t.id)}>
						<title>{r.t.name ? r.t.name + ': ' : ''}{r.t.cond}{editable ? ' — click: select · dblclick: edit condition · Del: delete' : ''}</title>
						<line x1={r.barX1} y1={r.barY} x2={r.barX2} y2={r.barY} class="bar" />
						{#if r.double}
							<line x1={r.barX1} y1={r.barY + 4} x2={r.barX2} y2={r.barY + 4} class="bar" />
						{/if}
						<rect x={r.barX1 - 6} y={r.barY - 8} width={r.barX2 - r.barX1 + 12} height={(r.double ? 20 : 16)} class="barhit" />
						<text
							x={r.condX}
							y={r.condY + 4}
							class="cond"
							ondblclick={(e) => editCondition(e, r.t, { x: (e.currentTarget as Element).getBoundingClientRect().left, y: (e.currentTarget as Element).getBoundingClientRect().top, w: 160 })}
						>{r.t.cond || '…'}</text>
					</g>
				{/if}
				{#if problems.length}
					<g class="diag" transform="translate({(r.jump ? r.jump.x + 9 : r.barX1) - 3}, {(r.jump ? r.jump.y : r.barY) - 14})">
						<title>{problems.map((p) => p.message).join('\n')}</title>
						<circle r="6" class="diagdot" />
						<text y="3" text-anchor="middle" class="diagmark">!</text>
					</g>
				{/if}
			</g>
		{/each}

		{#each layout.steps as p (p.id)}
			{@const problems = problemsFor(p.step.line, p.step.endLine)}
			{@const pos = displayPos(p)}
			{@const active = stepActive(p.step.name)}
			<g
				class="step {p.step.status ?? ''}"
				transform="translate({pos.x}, {pos.y})"
				class:selected={isSelStep(p.id)}
				class:active
				class:editable
				class:connectSource={drag?.kind === 'connect' && drag.from === p.id}
				class:connectTarget={connectTarget?.id === p.id}
				onpointerdown={(e) => beginDrag(e, p)}
				onclick={(e) => selectStep(e, p.id)}
			>
				<title>{p.step.name}{p.step.initial ? ' (initial step)' : ''}{editable ? ' — click: select · dblclick: rename · drag body: pin position · drag ⊙ handle onto another step: connect · Del: delete' : ''}{active === true ? ' — ACTIVE' : ''}</title>
				<rect width={p.w} height={p.h} rx="4" class="box" />
				{#if p.step.initial}
					<rect x="4" y="4" width={p.w - 8} height={p.h - 8} rx="2" class="box inner" />
				{/if}
				<text x={p.w / 2} y={p.h / 2 + 4} text-anchor="middle" class="stepname" ondblclick={(e) => renameStep(e, p)}>{p.step.name}</text>
				{#if editable}
					<circle
						cx={p.w / 2}
						cy={p.h}
						r="6"
						class="connect-handle"
						onpointerdown={(e) => beginConnect(e, p)}
					><title>drag onto another step to add a transition</title></circle>
				{/if}
				{#if problems.length}
					<g class="diag" transform="translate({p.w + 4}, -4)">
						<title>{problems.map((d) => d.message).join('\n')}</title>
						<circle r="6" class="diagdot" />
						<text y="3" text-anchor="middle" class="diagmark">!</text>
					</g>
				{/if}
				{#if p.pinned}
					<circle cx="-6" cy="-6" r="3" class="pindot"><title>pinned position — "auto layout" clears all pins</title></circle>
				{/if}

				{#if (p.step.actions?.length ?? 0) > 0 || editable}
					<g class="assoctable" transform="translate({p.w + 12}, 0)">
						{#each p.step.actions ?? [] as a, i (i)}
							{@const isAction = !!actionFor(a.target)}
							<!-- An ACTION block has no box of its own on the chart, so a
							     diff of its BODY (T#3S -> T#5S inside Stir) shows on
							     every row that runs it; otherwise it marks nothing. -->
							<g
								class="assocrow {actionFor(a.target)?.status ?? ''}"
								class:selected={isSelAssoc(p.id, i)}
								transform="translate(0, {i * 16})"
								onclick={(e) => selectAssoc(e, p.id, i)}
								ondblclick={(e) => (isAction ? editActionBody(e, a.target) : editAssoc(e, p.step, i))}
							>
								<title>{isAction ? `ACTION ${a.target} — dblclick to edit its ST body` : `${a.qualifier} ${a.target}${a.time ? '(' + a.time + ')' : ''}`}</title>
								<rect x="0" y="1" width="164" height="15" class="assocbg" />
								<text x="4" y="11" class="assocq">{a.qualifier}</text>
								<text x="26" y="11" class="assoctarget" class:isaction={isAction}>{a.target}{a.time ? `(${a.time})` : ''}</text>
								{#if editable}
									<text x="150" y="11" class="assocdel" onclick={(e) => deleteAssoc(e, p.step, i)}>✕</text>
								{/if}
							</g>
						{/each}
						{#if editable}
							<g class="assocadd" transform="translate(0, {(p.step.actions?.length ?? 0) * 16})" onclick={(e) => addAssoc(e, p.step)}>
								<title>add an action association</title>
								<text x="4" y="11">+ action</text>
							</g>
						{/if}
					</g>
				{/if}
			</g>
		{/each}

		{#if drag?.kind === 'connect'}
			{@const from = drag.from}
			{@const src = layout.steps.find((p) => p.id === from)}
			{#if src}
				{@const start = connectHandlePos(src)}
				<line x1={start.x} y1={start.y} x2={drag.x} y2={drag.y} class="rubberband" />
				<circle cx={drag.x} cy={drag.y} r="4" class="rubberbandtip" />
			{/if}
		{/if}

		{#each layout.orphans as o (o.t.id)}
			{@const problems = problemsFor(o.t.line, o.t.endLine)}
			<g class="orphan" class:selected={isSelTrans(o.t.id)} transform="translate({o.x}, {o.y})">
				<title>{o.t.name ? o.t.name + ': ' : ''}orphaned transition — FROM {fmtEnds(o.t.from)} → TO {fmtEnds(o.t.to)}{editable ? ' — click: select · dblclick: edit condition · Del: delete · ⚙: retarget' : ''}</title>
				<rect width={o.w} height={o.h} rx="4" class="orphanbox" onclick={(e) => selectTrans(e, o.t.id)} />
				<text x="8" y="15" class="orphanlabel">FROM {fmtEnds(o.t.from)} → TO {fmtEnds(o.t.to)}</text>
				<text
					x="8"
					y="31"
					class="cond"
					ondblclick={(e) => editCondition(e, o.t, { x: (e.currentTarget as Element).getBoundingClientRect().left, y: (e.currentTarget as Element).getBoundingClientRect().top, w: 160 })}
				>{o.t.cond || '…'}</text>
				{#if editable}
					<text x={o.w - 14} y="15" class="orphanretarget" onclick={(e) => openRetarget(e, o)}><title>retarget FROM/TO</title>⚙</text>
				{/if}
				{#if problems.length}
					<g class="diag" transform="translate({o.w - 4}, -4)">
						<title>{problems.map((d) => d.message).join('\n')}</title>
						<circle r="6" class="diagdot" />
						<text y="3" text-anchor="middle" class="diagmark">!</text>
					</g>
				{/if}
			</g>
		{/each}

		{#each layout.notes as n (n.index)}
			{@const npos = posOverride(commentId(n.index), n.x, n.y)}
			<g class="note {n.c.status ?? ''}" class:editable class:selected={isSelComment(n.index)} transform="translate({npos.x}, {npos.y})" onpointerdown={(e) => beginNoteDrag(e, n)} onclick={(e) => selectComment(e, n.index)}>
				<title>comment{editable ? ' — click: select · dblclick: edit (empty text deletes) · drag: pin position · Del: delete' : ''}</title>
				<rect width={n.w} height={n.h} rx="3" class="notebox" ondblclick={(e) => editComment(e, n)} />
				<text x="8" y="19" class="notetext">{n.c.text.split('\n')[0]}</text>
				{#if n.pinned}
					<circle cx="-6" cy="-6" r="3" class="pindot"><title>pinned position — "auto layout" clears all pins</title></circle>
				{/if}
			</g>
		{/each}
	</svg>
</div>

<svelte:window onpointermove={onPointerMove} onpointerup={onPointerUp} onkeydown={onWindowEscape} />

<style>
	.wrap {
		position: relative;
		outline: none;
		width: max-content;
		min-width: 100%;
	}
	.palette {
		position: sticky;
		top: 0;
		left: 0;
		z-index: 10;
		display: flex;
		gap: 4px;
		align-items: center;
		padding: 5px 14px;
		background: color-mix(in srgb, var(--nx-bg) 88%, transparent);
		border-bottom: 1px solid var(--nx-border);
	}
	.palette button {
		padding: 2px 8px;
		border: 1px solid var(--nx-border);
		border-radius: 3px;
		background: transparent;
		color: var(--nx-ui-ink);
		font-size: 11px;
		cursor: pointer;
		white-space: nowrap;
	}
	.palette button:hover {
		border-color: var(--nx-accent);
		background: var(--nx-hover);
	}
	.palette button:disabled {
		opacity: 0.35;
		cursor: default;
	}
	.palette .sep {
		width: 1px;
		height: 16px;
		background: var(--nx-border);
		margin: 0 4px;
	}
	.addform {
		position: absolute;
		top: 34px;
		left: 14px;
		z-index: 20;
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 230px;
		background: var(--nx-panel-bg);
		border: 1px solid var(--nx-accent);
		border-radius: 5px;
		padding: 8px;
		box-shadow: var(--nx-shadow);
	}
	.addtitle {
		font-size: 11px;
		font-weight: 600;
		color: var(--nx-muted);
	}
	.addform label {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 11px;
		color: var(--nx-muted);
	}
	.addform label span {
		min-width: 56px;
	}
	.addform input {
		flex: 1;
	}
	.addactions {
		display: flex;
		justify-content: flex-end;
		gap: 6px;
	}
	.addactions button {
		padding: 2px 10px;
		border-radius: 3px;
		border: 1px solid var(--nx-border);
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font-size: 12px;
	}
	.addactions button.primary {
		background: var(--nx-btn-bg);
		color: var(--nx-btn-ink);
		border-color: transparent;
	}
	.chart {
		display: block;
	}
	.box {
		fill: var(--nx-panel-bg);
		stroke: var(--nx-ink);
		stroke-width: 1.4;
	}
	.box.inner {
		fill: none;
	}
	.step.editable {
		cursor: grab;
	}
	.step.selected .box {
		stroke: var(--nx-accent);
		stroke-width: 2;
	}
	.step.active .box {
		stroke: var(--nx-ok);
	}
	.step.active .stepname {
		fill: var(--nx-ok);
		font-weight: 700;
	}
	/* ── visual diff (base ↔ head overlay, diffSfc in sfc.ts) — the same
	   --nx-added/removed/changed palette and legend FBD/Ladder diffs use ── */
	.step.added .box {
		stroke: var(--nx-added);
	}
	.step.removed .box {
		stroke: var(--nx-removed);
		stroke-dasharray: 4 3;
		opacity: 0.75;
	}
	.step.changed .box {
		stroke: var(--nx-changed);
	}
	.assocrow.changed .assoctarget {
		fill: var(--nx-changed);
		font-weight: 700;
	}
	.assocrow.added .assoctarget {
		fill: var(--nx-added);
		font-weight: 700;
	}
	.stepname {
		font-family: var(--nx-mono);
		font-size: 12px;
		fill: var(--nx-ui-ink);
		cursor: text;
	}
	.pindot {
		fill: var(--nx-blue);
	}
	.assocbg {
		fill: transparent;
	}
	.assocrow:hover .assocbg,
	.assocrow.selected .assocbg {
		fill: var(--nx-hover);
	}
	.assocrow.selected .assocbg {
		fill: var(--nx-sel-bg);
	}
	.assocq {
		font-family: var(--nx-mono);
		font-size: 10px;
		font-weight: 700;
		fill: var(--nx-blue);
	}
	.assoctarget {
		font-family: var(--nx-mono);
		font-size: 10px;
		fill: var(--nx-ui-ink);
	}
	.assoctarget.isaction {
		fill: var(--nx-link);
		text-decoration: underline dotted;
	}
	.assocdel {
		font-size: 9px;
		fill: var(--nx-muted);
		opacity: 0;
		cursor: pointer;
	}
	.assocrow:hover .assocdel {
		opacity: 0.8;
	}
	.assocdel:hover {
		fill: var(--nx-err);
	}
	.assocadd {
		font-family: var(--nx-mono);
		font-size: 10px;
		fill: var(--nx-muted);
		cursor: pointer;
		opacity: 0.6;
	}
	.assocadd:hover {
		opacity: 1;
		fill: var(--nx-link);
	}
	.flowline {
		stroke: var(--nx-ink);
		stroke-width: 2;
	}
	.bar {
		stroke: var(--nx-ink);
		stroke-width: 2.6;
	}
	.barhit {
		fill: transparent;
		cursor: pointer;
	}
	.trans.selected .bar {
		stroke: var(--nx-accent);
	}
	.trans.selected .flowline {
		stroke: var(--nx-accent);
	}
	.trans.added .bar,
	.trans.added .flowline {
		stroke: var(--nx-added);
	}
	.trans.removed .bar,
	.trans.removed .flowline {
		stroke: var(--nx-removed);
		stroke-dasharray: 4 3;
		opacity: 0.75;
	}
	.trans.changed .bar,
	.trans.changed .flowline {
		stroke: var(--nx-changed);
	}
	.trans.added .cond {
		fill: var(--nx-added);
	}
	.trans.removed .cond {
		fill: var(--nx-removed);
	}
	.trans.changed .cond {
		fill: var(--nx-changed);
	}
	.cond {
		font-family: var(--nx-mono);
		font-size: 11px;
		fill: var(--nx-muted);
		cursor: text;
		dominant-baseline: middle;
	}
	.jumpbox {
		fill: var(--nx-panel-bg);
		stroke: var(--nx-warn);
		stroke-width: 1.4;
		stroke-dasharray: 3 2;
		cursor: pointer;
	}
	.jumpbox.double {
		stroke-width: 2.2;
	}
	.jumpmark {
		font-size: 11px;
		fill: var(--nx-warn);
		pointer-events: none;
	}
	.jumplabel {
		fill: var(--nx-warn);
	}
	.trans.selected .jumpbox {
		stroke: var(--nx-accent);
	}
	.diagdot {
		fill: var(--nx-err);
	}
	.diagmark {
		font-size: 9px;
		font-weight: 800;
		fill: var(--nx-bg);
		pointer-events: none;
	}

	/* ── drag-to-connect: the handle, the rubber-band, the drop target ──── */
	.connect-handle {
		fill: var(--nx-panel-bg);
		stroke: var(--nx-accent);
		stroke-width: 1.4;
		opacity: 0;
		cursor: crosshair;
		transition: opacity 0.1s;
	}
	.step.editable:hover .connect-handle,
	.step.connectSource .connect-handle {
		opacity: 1;
	}
	.step.connectTarget .box {
		stroke: var(--nx-ok);
		stroke-width: 2.6;
	}
	.rubberband {
		stroke: var(--nx-accent);
		stroke-width: 1.6;
		stroke-dasharray: 4 3;
		pointer-events: none;
	}
	.rubberbandtip {
		fill: var(--nx-accent);
		pointer-events: none;
	}

	/* ── orphaned-transition chips (a dangling FROM/TO — styled like a
	   diagnostic problem, not a normal route) ───────────────────────────── */
	.orphanbox {
		fill: var(--nx-err-bg);
		stroke: var(--nx-err);
		stroke-width: 1.4;
		stroke-dasharray: 4 2;
		cursor: pointer;
	}
	.orphan.selected .orphanbox {
		stroke: var(--nx-accent);
		stroke-width: 2;
	}
	.orphanlabel {
		font-family: var(--nx-mono);
		font-size: 10px;
		font-weight: 600;
		fill: var(--nx-err);
		pointer-events: none;
	}
	.orphanretarget {
		font-size: 11px;
		fill: var(--nx-muted);
		cursor: pointer;
	}
	.orphanretarget:hover {
		fill: var(--nx-accent);
	}

	/* ── comments (diagram notes) ─────────────────────────────────────── */
	.notebox {
		fill: var(--nx-panel-bg);
		stroke: var(--nx-comment);
		stroke-width: 1.2;
		stroke-dasharray: 3 2;
		cursor: pointer;
	}
	.note.selected .notebox {
		stroke: var(--nx-accent);
		stroke-width: 2;
	}
	.note.added .notebox {
		stroke: var(--nx-added);
	}
	.note.removed .notebox {
		stroke: var(--nx-removed);
		stroke-dasharray: 4 3;
		opacity: 0.75;
	}
	.note.changed .notebox {
		stroke: var(--nx-changed);
	}
	.note.editable {
		cursor: grab;
	}
	.notetext {
		font-family: var(--nx-mono);
		font-size: 10px;
		fill: var(--nx-comment);
		pointer-events: none;
	}

	/* ── delete-step cascade offer & orphan retarget popovers ───────────── */
	.confirm {
		left: 50%;
		top: 80px;
		transform: translateX(-50%);
		min-width: 280px;
	}
	.confirmbody {
		font-size: 11px;
		color: var(--nx-muted);
	}
	.retarget {
		min-width: 280px;
	}
	.retargetgroup {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.retargetlabel {
		font-size: 10px;
		font-weight: 700;
		color: var(--nx-muted);
	}
	.picker,
	.retargetrow {
		display: flex;
		align-items: center;
		gap: 4px;
	}
	.picker select,
	.picker input {
		flex: 1;
		min-width: 0;
	}
	.mini {
		padding: 1px 6px;
		border-radius: 3px;
		border: 1px solid var(--nx-border);
		background: transparent;
		color: var(--nx-ui-ink);
		cursor: pointer;
		font-size: 11px;
		align-self: flex-start;
	}
	.mini:hover {
		border-color: var(--nx-accent);
	}
</style>
