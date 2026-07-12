// Banded FBD layout — a TypeScript port of the geometry in media/fbd.js:
// networks (connected components) stack as horizontal bands; within a band,
// columns follow the Go model's layer indices and rows order by pin-aware
// barycenter sweeps. Positions feed Svelte Flow as computed (non-draggable)
// node coordinates; the model stays layout-free.

export type FbdNode = {
  id: string;
  kind: "input" | "block" | "fb" | "coil";
  label: string;
  type?: string;
  wire?: string;
  inputs?: string[];
  outputs?: string[];
  layer: number;
  src?: unknown;
  x?: number;
  y?: number;
  status?: 'added' | 'removed' | 'changed' | 'same';
};
export type FbdEdge = {
  from: string;
  fromPin?: string;
  to: string;
  toPin?: string;
  wire?: string;
  negated?: boolean;
  feedback?: boolean;
  status?: 'added' | 'removed' | 'changed' | 'same';
};
export type FbdModel = { name: string; nodes: FbdNode[]; edges: FbdEdge[] };

export type Placed = FbdNode & {
  x: number;
  y: number;
  w: number;
  h: number;
  titleH: number;
  ins: string[];
  outs: string[];
};

export const EXTENSIBLE = new Set(['AND', 'OR', 'XOR', 'ADD', 'MUL', 'MIN', 'MAX', 'MUX']);

const PIN_PITCH = 18;
const TITLE_H = 20;
const FB_TITLE_H = 32;
const CHIP_H = 24;
const COL_GAP = 72;
const ROW_GAP = 16;
const PAD = 24;
const BAND_GAP = 44;

let meter: CanvasRenderingContext2D | null = null;
function textW(s: string, px: number): number {
  if (!meter) meter = document.createElement("canvas").getContext("2d");
  if (!meter) return s.length * px * 0.6;
  meter.font = `${px}px ${getComputedStyle(document.body).getPropertyValue("--vscode-editor-font-family") || "monospace"}`;
  return meter.measureText(s).width;
}

export function pinOffset(n: Placed, pin: string, side: "in" | "out"): number {
  if (n.kind === "input" || n.kind === "coil") return n.h / 2;
  const list = side === "in" ? n.ins : n.outs;
  const i = Math.max(0, list.indexOf(pin));
  return n.titleH + (i + 0.5) * PIN_PITCH;
}

export function layout(model: FbdModel): {
  placed: Placed[];
  edges: FbdEdge[];
  laneIdx: Map<FbdEdge, number>;
} {
  // Pinned coordinates from the @layout block, captured before Placed
  // initializes x/y to zero for the auto pass.
  const pinned = new Map<string, { x: number; y: number }>();
  for (const n of model.nodes) {
    if (n.x !== undefined && n.y !== undefined) pinned.set(n.id, { x: n.x, y: n.y });
  }
  const placed: Placed[] = model.nodes.map((n) => {
    const ins = n.inputs ?? [];
    const outs = n.outputs ?? [];
    if (n.kind === "input" || n.kind === "coil") {
      return { ...n, ins, outs, x: 0, y: 0, w: Math.ceil(textW(n.label, 12)) + 22, h: CHIP_H, titleH: 0 };
    }
    const titleH = n.kind === "fb" ? FB_TITLE_H : TITLE_H;
    const rows = Math.max(ins.length, outs.length, 1);
    const inW = Math.max(0, ...ins.map((p) => textW(p, 9)));
    const outW = Math.max(0, ...outs.map((p) => textW(p, 9)));
    const titleW = Math.max(textW(n.label, 12), n.kind === "fb" ? textW(n.type ?? "", 10) : 0);
    return {
      ...n, ins, outs, x: 0, y: 0, titleH,
      w: Math.ceil(Math.max(titleW + 22, inW + outW + 34, 56)),
      h: titleH + rows * PIN_PITCH + 6,
    };
  });
  const byId = new Map(placed.map((n) => [n.id, n]));
  const edges = model.edges.filter((e) => byId.has(e.from) && byId.has(e.to));

  // Bands via union-find (the Go model splits networks; connectivity is enough).
  const parent = new Map<string, string>();
  const find = (x: string): string => {
    let r = x;
    while (parent.get(r) !== r) r = parent.get(r)!;
    while (parent.get(x) !== r) {
      const nx = parent.get(x)!;
      parent.set(x, r);
      x = nx;
    }
    return r;
  };
  for (const n of placed) parent.set(n.id, n.id);
  for (const e of edges) parent.set(find(e.from), find(e.to));
  const bandIdx = new Map<string, number>();
  const bands: Placed[][] = [];
  for (const n of placed) {
    const r = find(n.id);
    if (!bandIdx.has(r)) {
      bandIdx.set(r, bands.length);
      bands.push([]);
    }
    bands[bandIdx.get(r)!].push(n);
  }

  // Pin-aware adjacency for barycenter ordering.
  type Nb = { id: string; dy: number };
  const srcOf = new Map<string, Nb[]>();
  const dstOf = new Map<string, Nb[]>();
  const pinDy = (n: Placed, pin: string, side: "in" | "out") => pinOffset(n, pin, side) - n.h / 2;
  for (const e of edges) {
    if (e.feedback) continue;
    const from = byId.get(e.from)!;
    const to = byId.get(e.to)!;
    (srcOf.get(e.to) ?? srcOf.set(e.to, []).get(e.to)!).push({ id: e.from, dy: pinDy(from, e.fromPin ?? "", "out") });
    (dstOf.get(e.from) ?? dstOf.set(e.from, []).get(e.from)!).push({ id: e.to, dy: pinDy(to, e.toPin ?? "", "in") });
  }

  const fbCount = new Map<number, number>();
  for (const e of edges) {
    if (!e.feedback) continue;
    const b = bandIdx.get(find(e.from))!;
    fbCount.set(b, (fbCount.get(b) ?? 0) + 1);
  }

  let bandTop = PAD;
  bands.forEach((band, bi) => {
    const h = layoutBand(band, srcOf, dstOf, bandTop);
    const lanes = fbCount.get(bi) ?? 0;
    bandTop += h + (lanes ? 14 + lanes * 10 : 0) + BAND_GAP;
  });

  // Pinned positions (the @layout block) override auto placement — dragged
  // nodes sit exactly where the user left them; everything else stays auto.
  for (const n of placed) {
    const p = pinned.get(n.id);
    if (p) {
      n.x = p.x;
      n.y = p.y;
    }
  }

  // Backward wires (seal-in feedback, FB cycles) route in lanes: assign each
  // a stagger index so parallel lanes don't overlap. The PATH is computed
  // live in the edge component from the endpoints xyflow supplies, so lanes
  // follow node drags exactly like forward wires do.
  const laneIdx = new Map<FbdEdge, number>();
  const laneOf = new Map<number, number>();
  for (const e of edges) {
    const from = byId.get(e.from)!;
    const to = byId.get(e.to)!;
    const x1 = from.x + from.w;
    const x2 = to.x;
    if (x1 < x2 - 4) continue; // forward at rest: bezier, no lane
    const bi = bandIdx.get(find(e.from))!;
    const lane = laneOf.get(bi) ?? 0;
    laneOf.set(bi, lane + 1);
    laneIdx.set(e, lane);
  }
  return { placed, edges, laneIdx };
}

function layoutBand(
  band: Placed[],
  srcOf: Map<string, { id: string; dy: number }[]>,
  dstOf: Map<string, { id: string; dy: number }[]>,
  top: number
): number {
  const inBand = new Set(band.map((n) => n.id));
  const layers: Placed[][] = [];
  for (const n of band) (layers[n.layer] = layers[n.layer] ?? []).push(n);
  const cols = layers.filter((c) => c && c.length);

  const center = new Map<string, number>();
  const stack = (col: Placed[]) => {
    let y = 0;
    for (const n of col) {
      center.set(n.id, y + n.h / 2);
      y += n.h + ROW_GAP;
    }
  };
  cols.forEach(stack);
  const orderBy = (col: Placed[], nbs: Map<string, { id: string; dy: number }[]>) => {
    const bary = new Map<string, number>();
    for (const n of col) {
      const ns = (nbs.get(n.id) ?? []).filter((m) => inBand.has(m.id));
      bary.set(n.id, ns.length ? ns.reduce((a, m) => a + center.get(m.id)! + m.dy, 0) / ns.length : center.get(n.id)!);
    }
    col.sort((a, b) => bary.get(a.id)! - bary.get(b.id)!);
    stack(col);
  };
  for (let round = 0; round < 3; round++) {
    for (let i = 1; i < cols.length; i++) orderBy(cols[i], srcOf);
    for (let i = cols.length - 2; i >= 0; i--) orderBy(cols[i], dstOf);
  }
  for (let i = 1; i < cols.length; i++) orderBy(cols[i], srcOf);

  const colW = cols.map((col) => Math.max(0, ...col.map((n) => n.w)));
  const colH = cols.map((col) => col.reduce((a, n) => a + n.h, 0) + Math.max(0, col.length - 1) * ROW_GAP);
  const bandH = Math.max(0, ...colH);
  let x = PAD;
  cols.forEach((col, ci) => {
    let y = top + (bandH - colH[ci]) / 2;
    for (const n of col) {
      n.x = x + (colW[ci] - n.w) / 2;
      n.y = y;
      y += n.h + ROW_GAP;
    }
    x += colW[ci] + COL_GAP;
  });
  return bandH;
}
