// FBD diagram renderer. Consumes the render model emitted by
// `nautilus fbd graph` (see lang/fbd/graph.go for the contract) and draws an
// SVG: layered left-to-right, blocks with pins, wires as bezier curves,
// negation circles, feedback routed below the diagram. In diff mode it
// overlays two models and colors nodes/edges added / removed / changed.
// All geometry lives here; the model carries only topology + layer indices.
/* eslint-env browser */
(function () {
  "use strict";

  const vscode = acquireVsCodeApi();

  // ── geometry constants ────────────────────────────────────────────────
  const PIN_PITCH = 18; // vertical distance between pins
  const TITLE_H = 20; // block title band
  const FB_TITLE_H = 32; // fb: instance name + type
  const CHIP_H = 24; // input/coil variable chips
  const COL_GAP = 72; // horizontal gap between layers
  const ROW_GAP = 16; // vertical gap between nodes in a layer
  const PAD = 24; // diagram padding

  // ── DOM scaffold ──────────────────────────────────────────────────────
  const app = document.getElementById("app");
  app.innerHTML = "";
  const style = document.createElement("style");
  style.textContent = `
    :root { color-scheme: light dark; }
    html, body, #app { height: 100%; margin: 0; padding: 0; overflow: hidden; }
    #app { display: flex; flex-direction: column;
           font-family: var(--vscode-font-family); color: var(--vscode-foreground);
           background: var(--vscode-editor-background); }
    #bar { display: flex; align-items: center; gap: 8px; padding: 4px 10px;
           font-size: 12px; border-bottom: 1px solid var(--vscode-editorWidget-border, rgba(128,128,128,.35));
           user-select: none; flex: none; }
    #bar .title { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    #bar .spacer { flex: 1; }
    #bar button { background: var(--vscode-button-secondaryBackground, transparent);
           color: var(--vscode-button-secondaryForeground, var(--vscode-foreground));
           border: 1px solid var(--vscode-editorWidget-border, rgba(128,128,128,.35));
           border-radius: 3px; width: 26px; height: 22px; cursor: pointer; font-size: 12px; }
    #bar button.fit { width: auto; padding: 0 8px; }
    #bar button:hover { background: var(--vscode-toolbar-hoverBackground, rgba(128,128,128,.2)); }
    #legend { display: none; gap: 10px; font-size: 11px; }
    #legend.on { display: inline-flex; }
    #legend i { display: inline-block; width: 10px; height: 10px; border-radius: 2px; margin-right: 3px; vertical-align: -1px; }
    #error { display: none; padding: 6px 10px; font-size: 12px; white-space: pre-wrap;
             font-family: var(--vscode-editor-font-family, monospace); flex: none;
             color: var(--vscode-errorForeground);
             background: var(--vscode-inputValidation-errorBackground, rgba(200,60,60,.12));
             border-bottom: 1px solid var(--vscode-errorForeground); }
    #error.on { display: block; }
    #canvas { flex: 1; min-height: 0; outline: none; }
    #canvas.stale svg { opacity: .45; }
    svg { display: block; width: 100%; height: 100%; cursor: grab; }
    svg.panning { cursor: grabbing; }
    text { font-family: var(--vscode-editor-font-family, monospace); fill: var(--vscode-editor-foreground); }
    .empty { padding: 24px; font-size: 13px; opacity: .75; }

    /* diagram element colors, overridden per diff status */
    g.node { --ink: var(--vscode-editor-foreground); }
    g.edge { --ink: var(--vscode-editor-foreground); }
    g.added { --ink: var(--vscode-gitDecoration-addedResourceForeground, #2ea043); }
    g.removed { --ink: var(--vscode-gitDecoration-deletedResourceForeground, #f85149); }
    g.changed { --ink: var(--vscode-gitDecoration-modifiedResourceForeground, #d7a021); }
    .diffing g.same { opacity: .55; }
    g.removed { opacity: .8; }
    g.node rect.body { fill: var(--vscode-editorWidget-background, var(--vscode-editor-background));
                       stroke: var(--ink); stroke-width: 1.2; }
    g.removed rect.body { stroke-dasharray: 4 3; }
    g.node text { fill: var(--ink); }
    g.node text.type { opacity: .75; font-size: 10px; }
    g.node text.pin { font-size: 9px; opacity: .8; }
    g.node line.divider { stroke: var(--ink); stroke-width: .6; opacity: .5; }
    g.chip rect.body { rx: 11; }
    g.edge path { fill: none; stroke: var(--ink); stroke-width: 1.4; opacity: .9; }
    g.edge.feedback path { stroke-dasharray: 6 3; }
    g.edge circle.neg { fill: var(--vscode-editor-background); stroke: var(--ink); stroke-width: 1.3; }
    g.edge text.wire { font-size: 9px; fill: var(--ink); opacity: .85; }
    /* edit affordances (preview mode only) */
    g.node.editable { cursor: pointer; }
    g.node.editable:hover rect.body { stroke-width: 2; }
    circle.not-hit { fill: transparent; pointer-events: all; cursor: pointer; }
    circle.not-hit:hover { fill: var(--vscode-editor-foreground); fill-opacity: .18; }
    #lit-edit {
      position: fixed; z-index: 10; font-family: var(--vscode-editor-font-family, monospace);
      font-size: 12px; padding: 2px 6px; border-radius: 4px;
      background: var(--vscode-input-background); color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-focusBorder, #58a6ff); outline: none;
    }
    #bar .hint { font-size: 11px; color: var(--vscode-descriptionForeground, #888); display: none; }
    #bar .hint.on { display: inline; }
    @media (prefers-reduced-motion: no-preference) {
      svg * { transition: opacity .12s ease; }
    }
  `;
  document.head.appendChild(style);

  const bar = el("div", { id: "bar" });
  const titleEl = el("span", { class: "title" }, "FBD Preview");
  const legendEl = el("span", { id: "legend" });
  legendEl.innerHTML =
    '<span><i style="background:var(--vscode-gitDecoration-addedResourceForeground,#2ea043)"></i>added</span>' +
    '<span><i style="background:var(--vscode-gitDecoration-deletedResourceForeground,#f85149)"></i>removed</span>' +
    '<span><i style="background:var(--vscode-gitDecoration-modifiedResourceForeground,#d7a021)"></i>changed</span>';
  const hintEl = el("span", { class: "hint" }, "double-click a constant to edit · click a pin to toggle NOT");
  const zoomOut = el("button", { title: "Zoom out (-)" }, "−");
  const zoomFit = el("button", { class: "fit", title: "Fit (0)" }, "fit");
  const zoomIn = el("button", { title: "Zoom in (+)" }, "+");
  bar.append(titleEl, legendEl, hintEl, el("span", { class: "spacer" }), zoomOut, zoomFit, zoomIn);
  const errorEl = el("div", { id: "error" });
  const canvas = el("div", { id: "canvas", tabindex: "0", role: "img", "aria-label": "FBD diagram" });
  app.append(bar, errorEl, canvas);

  function el(tag, attrs, text) {
    const e = document.createElement(tag);
    for (const k in attrs || {}) e.setAttribute(k, attrs[k]);
    if (text) e.textContent = text;
    return e;
  }
  function svgEl(tag, attrs) {
    const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (const k in attrs || {}) e.setAttribute(k, attrs[k]);
    return e;
  }

  // ── text measurement ──────────────────────────────────────────────────
  const meter = document.createElement("canvas").getContext("2d");
  function textW(s, px) {
    const family = getComputedStyle(document.body).getPropertyValue("--vscode-editor-font-family") || "monospace";
    meter.font = px + "px " + family;
    return meter.measureText(s).width;
  }

  // ── layout ────────────────────────────────────────────────────────────
  // Each network (connected component) lays out as its own horizontal band,
  // stacked vertically — the way FBD editors draw independent logic. The
  // model is pre-shaped for this: variable chips repeat per network, so
  // connectivity alone recovers the bands.
  const BAND_GAP = 44;

  function layout(model) {
    const nodes = model.nodes.map((n) => ({ ...n, inputs: n.inputs || [], outputs: n.outputs || [] }));
    const byId = new Map(nodes.map((n) => [n.id, n]));
    const edges = model.edges.filter((e) => byId.has(e.from) && byId.has(e.to));

    for (const n of nodes) {
      if (n.kind === "input" || n.kind === "coil") {
        n.w = Math.ceil(textW(n.label, 12)) + 22;
        n.h = CHIP_H;
      } else {
        n.titleH = n.kind === "fb" ? FB_TITLE_H : TITLE_H;
        const rows = Math.max(n.inputs.length, n.outputs.length, 1);
        const inW = Math.max(0, ...n.inputs.map((p) => textW(p, 9)));
        const outW = Math.max(0, ...n.outputs.map((p) => textW(p, 9)));
        const titleW = Math.max(textW(n.label, 12), n.kind === "fb" ? textW(n.type || "", 10) : 0);
        n.w = Math.ceil(Math.max(titleW + 22, inW + outW + 34, 56));
        n.h = n.titleH + rows * PIN_PITCH + 6;
      }
    }

    // Bands: union-find over all edges; band order by first node appearance.
    const parent = new Map();
    const find = (x) => {
      let r = x;
      while (parent.get(r) !== r) r = parent.get(r);
      while (parent.get(x) !== r) {
        const next = parent.get(x);
        parent.set(x, r);
        x = next;
      }
      return r;
    };
    for (const n of nodes) parent.set(n.id, n.id);
    for (const e of edges) parent.set(find(e.from), find(e.to));
    const bandIdx = new Map();
    const bands = [];
    for (const n of nodes) {
      const r = find(n.id);
      if (!bandIdx.has(r)) {
        bandIdx.set(r, bands.length);
        bands.push([]);
      }
      bands[bandIdx.get(r)].push(n);
    }

    // Adjacency for crossing reduction (forward edges only). Each entry
    // carries the neighbor plus the pin's y-offset from that neighbor's
    // center, so ordering aligns wires pin-to-pin — two chips feeding IN1
    // and IN2 of the same block stack in pin order instead of tying.
    const pinDy = (n, pin, list) => {
      if (n.kind === "input" || n.kind === "coil") return 0;
      const i = Math.max(0, list.indexOf(pin));
      return n.titleH + (i + 0.5) * PIN_PITCH - n.h / 2;
    };
    const srcOf = new Map(); // node id -> [{id, dy}] of its input-pin sources
    const dstOf = new Map(); // node id -> [{id, dy}] of its output-pin targets
    for (const e of edges) {
      if (e.feedback) continue;
      const from = byId.get(e.from);
      const to = byId.get(e.to);
      if (!srcOf.has(e.to)) srcOf.set(e.to, []);
      srcOf.get(e.to).push({ id: e.from, dy: pinDy(from, e.fromPin || "", from.outputs) });
      if (!dstOf.has(e.from)) dstOf.set(e.from, []);
      dstOf.get(e.from).push({ id: e.to, dy: pinDy(to, e.toPin || "", to.inputs) });
    }

    // Feedback wires route in lanes below their band — reserve gap for them.
    const fbCount = new Map();
    for (const e of edges) {
      if (!e.feedback) continue;
      const b = bandIdx.get(find(e.from));
      fbCount.set(b, (fbCount.get(b) || 0) + 1);
    }

    let bandTop = PAD;
    let width = 0;
    bands.forEach((band, bi) => {
      const h = layoutBand(band, byId, srcOf, dstOf, bandTop);
      band.bottom = bandTop + h;
      const lanes = fbCount.get(bi) || 0;
      bandTop += h + (lanes ? 14 + lanes * 10 : 0) + BAND_GAP;
      width = Math.max(width, ...band.map((n) => n.x + n.w));
    });

    // Pin coordinates.
    for (const n of nodes) {
      n.pinIn = {};
      n.pinOut = {};
      if (n.kind === "input" || n.kind === "coil") {
        n.pinIn[""] = { x: n.x, y: n.y + n.h / 2 };
        n.pinOut[""] = { x: n.x + n.w, y: n.y + n.h / 2 };
      } else {
        n.inputs.forEach((p, i) => {
          n.pinIn[p] = { x: n.x, y: n.y + n.titleH + (i + 0.5) * PIN_PITCH };
        });
        n.outputs.forEach((p, i) => {
          n.pinOut[p] = { x: n.x + n.w, y: n.y + n.titleH + (i + 0.5) * PIN_PITCH };
        });
      }
    }
    const bandOf = new Map();
    for (const band of bands) for (const n of band) bandOf.set(n.id, band);
    return { nodes, edges, byId, bands, bandOf, width: width + PAD, height: bandTop - BAND_GAP + PAD };
  }

  // layoutBand positions one network's nodes: columns by layer, rows ordered
  // by iterated barycenter sweeps (left→right by sources, right→left by
  // targets), then stacked and vertically centered. Returns the band height.
  function layoutBand(band, byId, srcOf, dstOf, top) {
    const inBand = new Set(band.map((n) => n.id));
    const layers = [];
    for (const n of band) (layers[n.layer] = layers[n.layer] || []).push(n);
    const cols = [];
    for (const col of layers) if (col && col.length) cols.push(col);

    // Provisional row centers drive the barycenter; refine over 3 rounds.
    const center = new Map();
    const stack = (col) => {
      let y = 0;
      for (const n of col) {
        center.set(n.id, y + n.h / 2);
        y += n.h + ROW_GAP;
      }
    };
    cols.forEach(stack);
    const orderBy = (col, neighborsOf) => {
      col.forEach((n) => {
        const ns = (neighborsOf.get(n.id) || []).filter((m) => inBand.has(m.id));
        n._bary = ns.length
          ? ns.reduce((a, m) => a + center.get(m.id) + m.dy, 0) / ns.length
          : center.get(n.id); // nothing to align to: hold position
      });
      col.sort((a, b) => a._bary - b._bary);
      stack(col);
    };
    for (let round = 0; round < 3; round++) {
      for (let i = 1; i < cols.length; i++) orderBy(cols[i], srcOf);
      for (let i = cols.length - 2; i >= 0; i--) orderBy(cols[i], dstOf);
    }
    // Final left→right pass so sources win the last word.
    for (let i = 1; i < cols.length; i++) orderBy(cols[i], srcOf);

    // Positions: columns left to right, centered within the band height.
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

  // ── render ────────────────────────────────────────────────────────────
  let vb = null; // current viewBox {x,y,w,h}
  let lastKey = ""; // node-id fingerprint: refit only on structural change
  let svg = null;

  function render(model, diffing) {
    const L = layout(model);
    canvas.innerHTML = "";
    if (!L.nodes.length) {
      canvas.appendChild(el("div", { class: "empty" }, "Empty FBD — add wires, blocks, or coils."));
      svg = null;
      return;
    }
    svg = svgEl("svg", { "aria-hidden": "true" });
    if (diffing) svg.classList.add("diffing");
    const root = svgEl("g");
    svg.appendChild(root);

    // Feedback/backward wires get lanes just below their own band.
    const laneOf = new Map(); // band -> next lane index

    for (const e of L.edges) {
      const from = L.byId.get(e.from);
      const to = L.byId.get(e.to);
      const p1 = from.pinOut[e.fromPin || ""] || firstPin(from.pinOut) || { x: from.x + from.w, y: from.y + from.h / 2 };
      const p2 = to.pinIn[e.toPin || ""] || firstPin(to.pinIn) || { x: to.x, y: to.y + to.h / 2 };
      const g = svgEl("g", { class: "edge" + (e.feedback ? " feedback" : "") + statusClass(e._status, diffing) });
      const endX = e.negated ? p2.x - 9 : p2.x;
      let d;
      if (p1.x < p2.x - 4) {
        const dx = Math.max(26, (p2.x - p1.x) / 2);
        d = `M ${p1.x} ${p1.y} C ${p1.x + dx} ${p1.y}, ${endX - dx} ${p2.y}, ${endX} ${p2.y}`;
      } else {
        // Backward wire (seal-in feedback or FB cycle): out, down under the
        // band, back left, and up into the pin.
        const band = L.bandOf.get(e.from);
        const lane = laneOf.get(band) || 0;
        laneOf.set(band, lane + 1);
        const y = band.bottom + 12 + lane * 10;
        const ox = p1.x + 14 + lane * 6;
        const ix = endX - 14 - lane * 6;
        d = `M ${p1.x} ${p1.y} L ${ox} ${p1.y} L ${ox} ${y} L ${ix} ${y} L ${ix} ${p2.y} L ${endX} ${p2.y}`;
      }
      g.appendChild(svgEl("path", { d }));
      if (e.negated) g.appendChild(svgEl("circle", { class: "neg", cx: p2.x - 4.5, cy: p2.y, r: 4.5 }));
      if (e.wire && p1.x < p2.x - 4 && !from.wire) {
        // Label the signal at its source unless the block already shows it.
        const t = svgEl("text", { class: "wire", x: p1.x + 6, y: p1.y - 5 });
        t.textContent = e.wire;
        g.appendChild(t);
      }
      if (!diffing && e.arg) {
        // Pin hit-target: click toggles NOT on this input (a text edit —
        // insert NOT at the consumer argument, or delete the existing one).
        const hit = svgEl("circle", { class: "not-hit", cx: p2.x - 4.5, cy: p2.y, r: 7.5 });
        const tip = svgEl("title");
        tip.textContent = e.negated ? "remove NOT" : "add NOT";
        hit.appendChild(tip);
        hit.addEventListener("click", (ev) => {
          ev.stopPropagation();
          vscode.postMessage({ type: "toggleNot", arg: e.arg, not: e.not ?? null, inner: e.inner ?? null });
        });
        g.appendChild(hit);
      }
      root.appendChild(g);
    }

    for (const n of L.nodes) {
      const g = svgEl("g", { class: "node" + (n.kind === "input" || n.kind === "coil" ? " chip" : "") + statusClass(n._status, diffing) });
      const body = svgEl("rect", { class: "body", x: n.x, y: n.y, width: n.w, height: n.h });
      if (n.kind === "input" || n.kind === "coil") {
        body.setAttribute("rx", 11);
        g.appendChild(body);
        g.appendChild(centeredText(n.x + n.w / 2, n.y + n.h / 2 + 4, n.label, 12));
      } else {
        body.setAttribute("rx", 2);
        g.appendChild(body);
        if (n.kind === "fb") {
          g.appendChild(centeredText(n.x + n.w / 2, n.y + 13, n.label, 12, "title"));
          const t = centeredText(n.x + n.w / 2, n.y + 26, n.type || "?", 10, "type");
          g.appendChild(t);
        } else {
          g.appendChild(centeredText(n.x + n.w / 2, n.y + 14, n.label, 12, "title"));
        }
        g.appendChild(svgEl("line", { class: "divider", x1: n.x, y1: n.y + n.titleH - 3, x2: n.x + n.w, y2: n.y + n.titleH - 3 }));
        n.inputs.forEach((p) => {
          const pt = n.pinIn[p];
          const t = svgEl("text", { class: "pin", x: pt.x + 4, y: pt.y + 3 });
          t.textContent = p;
          g.appendChild(t);
        });
        n.outputs.forEach((p) => {
          const pt = n.pinOut[p];
          const t = svgEl("text", { class: "pin", x: pt.x - 4, y: pt.y + 3, "text-anchor": "end" });
          t.textContent = p;
          g.appendChild(t);
        });
        if (n.wire) {
          const t = svgEl("text", { class: "wire pin", x: n.x + n.w + 4, y: (n.pinOut["OUT"] ? n.pinOut["OUT"].y : n.y) - 6 });
          t.textContent = n.wire;
          g.appendChild(t);
        }
      }
      const tip = svgEl("title");
      tip.textContent = n.kind === "fb" ? `${n.label} : ${n.type || "?"}` : n.label;
      if (!diffing && n.src) {
        g.classList.add("editable");
        tip.textContent = `${n.label} — double-click to edit`;
        g.addEventListener("dblclick", (ev) => {
          ev.stopPropagation();
          beginEditLiteral(n, body);
        });
      }
      g.appendChild(tip);
      root.appendChild(g);
    }

    const contentH = L.height; // lane space is already reserved per band
    const key = L.nodes.map((n) => n.id).sort().join("|");
    if (!vb || key !== lastKey) {
      vb = fitBox(L.width, contentH);
      lastKey = key;
    }
    applyViewBox();
    canvas.appendChild(svg);
    wireSvgEvents();
  }

  // beginEditLiteral floats an input over a constant chip; Enter commits the
  // new value as a span-anchored text edit in the .fbd document (the edit
  // round-trips through the extension, and the re-render replaces us).
  function beginEditLiteral(node, rectEl) {
    document.getElementById("lit-edit")?.remove();
    const r = rectEl.getBoundingClientRect();
    const input = el("input", { id: "lit-edit", spellcheck: "false" });
    input.value = node.label;
    input.style.left = r.left + "px";
    input.style.top = r.top - 2 + "px";
    input.style.width = Math.max(r.width, 64) + "px";
    document.body.appendChild(input);
    input.focus();
    input.select();
    let done = false;
    const close = () => {
      done = true;
      input.remove();
      canvas.focus();
    };
    input.addEventListener("keydown", (ev) => {
      ev.stopPropagation();
      if (ev.key === "Enter") {
        const newText = input.value.trim();
        if (newText && newText !== node.label) {
          vscode.postMessage({ type: "editLiteral", span: node.src, newText });
        }
        close();
      } else if (ev.key === "Escape") {
        close();
      }
    });
    input.addEventListener("blur", () => {
      if (!done) close();
    });
  }

  function firstPin(pins) {
    for (const k in pins) return pins[k];
    return null;
  }
  function statusClass(status, diffing) {
    if (!diffing) return "";
    return " " + (status || "same");
  }
  function centeredText(x, y, s, px, cls) {
    const t = svgEl("text", { x, y, "text-anchor": "middle", "font-size": px, class: cls || "" });
    t.textContent = s;
    return t;
  }

  // ── diff merge ────────────────────────────────────────────────────────
  // Node ids are stable diff keys (they encode role + name + arg position),
  // so matching is a straight id join. "changed" = same id, different
  // label/type/pins; edges match on endpoints and change on negation.
  function mergeDiff(base, head) {
    const bn = new Map(base.nodes.map((n) => [n.id, n]));
    const hn = new Map(head.nodes.map((n) => [n.id, n]));
    const nodes = [];
    for (const n of head.nodes) {
      const b = bn.get(n.id);
      const m = { ...n, inputs: (n.inputs || []).slice(), outputs: (n.outputs || []).slice() };
      if (!b) m._status = "added";
      else {
        for (const p of b.inputs || []) if (!m.inputs.includes(p)) m.inputs.push(p);
        for (const p of b.outputs || []) if (!m.outputs.includes(p)) m.outputs.push(p);
        m._status =
          b.label !== n.label || (b.type || "") !== (n.type || "") || (b.wire || "") !== (n.wire || "")
            ? "changed"
            : "same";
      }
      nodes.push(m);
    }
    const headMax = Math.max(0, ...head.nodes.map((n) => n.layer));
    for (const n of base.nodes) {
      if (hn.has(n.id)) continue;
      nodes.push({ ...n, _status: "removed", layer: Math.min(n.layer, headMax) });
    }
    const ekey = (e) => [e.from, e.fromPin || "", e.to, e.toPin || ""].join("→");
    const be = new Map(base.edges.map((e) => [ekey(e), e]));
    const he = new Map(head.edges.map((e) => [ekey(e), e]));
    const edges = [];
    for (const e of head.edges) {
      const b = be.get(ekey(e));
      edges.push({
        ...e,
        _status: !b ? "added" : !!b.negated !== !!e.negated || (b.wire || "") !== (e.wire || "") ? "changed" : "same",
      });
    }
    for (const e of base.edges) if (!he.has(ekey(e))) edges.push({ ...e, _status: "removed" });
    return { name: head.name || base.name, nodes, edges };
  }

  // ── pan & zoom ────────────────────────────────────────────────────────
  function fitBox(w, h) {
    const r = canvas.getBoundingClientRect();
    const cw = Math.max(r.width, 50);
    const ch = Math.max(r.height, 50);
    const scale = Math.min(cw / w, ch / h, 1.6);
    const vw = cw / scale;
    const vh = ch / scale;
    return { x: (w - vw) / 2, y: (h - vh) / 2, w: vw, h: vh };
  }
  function applyViewBox() {
    if (svg && vb) svg.setAttribute("viewBox", `${vb.x} ${vb.y} ${vb.w} ${vb.h}`);
  }
  function zoom(factor, cx, cy) {
    if (!svg || !vb) return;
    const r = svg.getBoundingClientRect();
    const px = cx === undefined ? 0.5 : (cx - r.left) / r.width;
    const py = cy === undefined ? 0.5 : (cy - r.top) / r.height;
    const nw = vb.w / factor;
    const nh = vb.h / factor;
    vb = { x: vb.x + (vb.w - nw) * px, y: vb.y + (vb.h - nh) * py, w: nw, h: nh };
    applyViewBox();
  }
  function wireSvgEvents() {
    if (!svg) return;
    let drag = null;
    svg.addEventListener("pointerdown", (ev) => {
      drag = { x: ev.clientX, y: ev.clientY };
      svg.classList.add("panning");
      svg.setPointerCapture(ev.pointerId);
    });
    svg.addEventListener("pointermove", (ev) => {
      if (!drag || !vb) return;
      const r = svg.getBoundingClientRect();
      vb.x -= ((ev.clientX - drag.x) * vb.w) / r.width;
      vb.y -= ((ev.clientY - drag.y) * vb.h) / r.height;
      drag = { x: ev.clientX, y: ev.clientY };
      applyViewBox();
    });
    const stop = () => {
      drag = null;
      svg.classList.remove("panning");
    };
    svg.addEventListener("pointerup", stop);
    svg.addEventListener("pointercancel", stop);
    svg.addEventListener(
      "wheel",
      (ev) => {
        ev.preventDefault();
        zoom(ev.deltaY < 0 ? 1.15 : 1 / 1.15, ev.clientX, ev.clientY);
      },
      { passive: false }
    );
  }
  zoomIn.addEventListener("click", () => zoom(1.25));
  zoomOut.addEventListener("click", () => zoom(1 / 1.25));
  zoomFit.addEventListener("click", refit);
  function refit() {
    lastKey = "";
    vb = null;
    if (lastMsg) show(lastMsg);
  }
  canvas.addEventListener("keydown", (ev) => {
    const pan = () => (vb ? vb.w / 12 : 0);
    switch (ev.key) {
      case "+": case "=": zoom(1.25); break;
      case "-": case "_": zoom(1 / 1.25); break;
      case "0": case "f": refit(); break;
      case "ArrowLeft": if (vb) { vb.x -= pan(); applyViewBox(); } break;
      case "ArrowRight": if (vb) { vb.x += pan(); applyViewBox(); } break;
      case "ArrowUp": if (vb) { vb.y -= pan(); applyViewBox(); } break;
      case "ArrowDown": if (vb) { vb.y += pan(); applyViewBox(); } break;
      default: return;
    }
    ev.preventDefault();
  });
  window.addEventListener("resize", () => {
    // Keep the diagram fitted if the user hasn't zoomed manually? Cheap and
    // predictable: leave the viewBox alone; "fit" is one keypress away.
  });

  // ── message handling ──────────────────────────────────────────────────
  let lastMsg = vscode.getState() || null;

  function show(msg) {
    if (msg.type === "model") {
      titleEl.textContent = (msg.model.name ? msg.model.name + " — " : "") + (msg.title || "");
      legendEl.classList.remove("on");
      hintEl.classList.add("on");
      errorEl.classList.remove("on");
      canvas.classList.remove("stale");
      render(msg.model, false);
    } else if (msg.type === "diff") {
      titleEl.textContent = (msg.head.name ? msg.head.name + " — " : "") + (msg.title || "");
      legendEl.classList.add("on");
      hintEl.classList.remove("on");
      errorEl.classList.remove("on");
      canvas.classList.remove("stale");
      render(mergeDiff(msg.base, msg.head), true);
    } else if (msg.type === "error") {
      titleEl.textContent = msg.title || "FBD Preview";
      errorEl.textContent = msg.message;
      errorEl.classList.add("on");
      canvas.classList.add("stale"); // keep the last good diagram, dimmed
    }
  }

  window.addEventListener("message", (ev) => {
    const msg = ev.data;
    if (msg.type !== "error") {
      lastMsg = msg;
      vscode.setState(msg);
    }
    show(msg);
  });

  if (lastMsg) show(lastMsg);
})();
