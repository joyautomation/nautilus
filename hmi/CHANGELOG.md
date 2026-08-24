# Changelog — @joyautomation/nautilus-hmi

## Unreleased — suggest **0.6.0** (minor: additive, no breaking changes)

Everything below came out of the Pomona WRD recreation (`pomona/wrd/host/hmi`), which ported an
Ignition Perspective application onto this kit and kept a running list of what it had to build
locally. These are those items, generalised — the app now imports them and its local copies are
gone. Nothing here changes an existing component's default rendering.

### Added — porting a legacy HMI

- **`CoordinateCanvas`** (+ `./canvas.ts`: `CanvasSpec`, `CanvasNode`, `CanvasRegistry`,
  `CONTAINER_TYPES`, `inlineStyle`, `flexStyle`, `walkSpec`). A fixed coordinate plane scaled to
  the viewport, with a component **registry** (and/or a `leaf` snippet) injected by the host app.
  The single most reusable thing for anyone porting an Ignition, FactoryTalk or WinCC project:
  every legacy screen is a fixed plane of absolutely-placed symbols that must keep its aspect
  ratio. Hooks for live `visible` / `style` / `href` bindings keep the transcriber out of the kit.
- **`EquipSymbol`** — a raster symbol with SCADA state chrome: run tint, simulate/comm-fail
  outline, fault bell, A/M · R/L chips, and `contain`-fit sizing when both `width` and `height`
  are given (legacy symbol PNGs have wildly different aspect ratios; sizing off `width` alone
  renders half of them four times too tall).
- **`ScaleBar`** — the moving analog indicator: a value against a scale cut into coloured alarm
  bands, vertical or horizontal. A disabled limit is `null` rather than a separate enable flag, so
  a UDT's per-limit enable maps straight onto the prop.
- **`StatusRow`** (+ `RowState`) — one device per line: mode chips, name, value snippet, on a
  background carrying `'on' | 'off' | 'fault' | 'unknown'`. `unknown` never collapses into `off`.
- **`LevelTank`** — a level-only vessel: range, band marks, an overflow mark, percentage and
  volume labels. The kit's `Tank` remains the heated-vessel twin (`tempC` / `heaterPct`), unchanged.
- **Schematic glyphs** `TankGlyph`, `PumpGlyph`, `ValveGlyph`, `FlowLink` — bare SVG `<g>`
  primitives for a network view you lay out yourself. `FlowLink` draws its marching dashes only
  when `flowing`, so a still line means still water.

### Added — writing back

- **`WriteNumber`**, **`WriteToggle`**, **`CommandButton`** — a labelled setpoint field, a
  lamp/switch and a momentary pushbutton, all API-agnostic: each takes `write(name, value)`
  resolving to `null` on success or the controller's refusal reason (the shape
  `RealtimeClient.writeTag` already returns). Refusals render on the control; `readonly` +
  `readonlyReason` covers a point the runtime will not accept.

### Added — trends and tags

- **`useTrend(client, tag, opts)`** (`./trend.svelte.ts`) — one tag's trend, live off the SSE
  stream and backfilled from `GET /api/history` on mount and on every stream reopen. Buffers are
  shared and refcounted per (client, tag, window).
- **`fetchHistory`**, **`historyAvailable`**, **`mergeHistory`** (`./history.ts`) — the historian
  query, a `/history/span` probe so "no historian configured" is reported as data rather than as a
  failed query, and the splice that lets the live buffer win over bucket-averaged history.
- **`tagAt`**, **`numAt`**, **`boolAt`**, **`hasTagAt`**, **`numericLeaves`** (`./tags.ts`) —
  dotted struct-path resolution against a frame's tag map, and an enumeration of every finite
  number a runtime is currently publishing. Build a tag picker from `numericLeaves` and it can
  never offer a tag that resolves to nothing.

### Added — subscriptions: deltas, filters, data quality

The two things that separate a demo HMI from one a plant runs on: how much a client has to pull to
draw a screen, and whether it can tell a live number from an old one. Both land in the controller
(`server`/`runtime`/`io`) and both are surfaced here. Measured on the WRD host: `/api/state` was
571 KB and one SSE client pulled ~2 MB per ten seconds, which is fine for one wall screen and
hopeless for tablets.

- **`RealtimeOptions.delta`** (default **`true`**) — ask the controller for delta frames. After the
  first full snapshot only changed tags cross the wire, and the client merges them, so
  `frame.tags` is always complete and nothing downstream changes. **~17× less on the wire** at 10k
  tags / 5% churn (280 kB → 17 kB per client per tick); ~51× at 1% churn. A controller that
  predates deltas sends frames with no `seq` and is passed through untouched, so turning it on is
  never a compatibility risk.
- **`RealtimeOptions.tags`** — glob patterns (`['RTU9_*', '*_LIT_*']`) narrowing the subscription
  server-side, applied to the first frame too. Composes with `delta`, and each is useful alone:
  filters help most when a screen is small, deltas when the plant is quiet.
- **`RealtimeClient.quality(tag)`** / **`.isGood(tag)`**, and **`Quality`**
  (`'good' | 'stale' | 'bad' | 'notConnected'`) — per-tag data quality as the controller reports
  it. This is the axis the kit could not see: `tags.ts` detects a tag's *absence* (a screen bound
  to an unpublished point shows "—", never a confident zero), but a tag that was published and had
  merely gone stale was indistinguishable from a live one — finite number, resolving binding,
  gauge rendered at full confidence. Apps were left inferring "comms bad" from magic values.
  `quality()` resolves a dotted member path to its root tag: a UDT arrives from its source whole.
  **Check `ControllerMeta.quality` before drawing a badge** — an empty quality map looks exactly
  like a healthy plant, and on a controller that cannot report quality a green badge is a lie.
- **`RealtimeClient.seq` / `.resyncs`** — the current connection's frame counter and the number of
  gap-triggered reconnects (normally zero for a session's life; a climbing number is a transport
  problem, not a tuning knob).
- **`mergeDelta` / `emptyDelta` / `DeltaState`** (`./delta.ts`) — the merge rules as a pure,
  dependency-free function, extracted because this is the one piece of the kit that can silently
  mislead an operator: a merge bug does not throw or blank a screen, it freezes one number at
  whatever it last was on a page that otherwise looks alive.
- **`NautilusFrame.quality` / `.seq` / `.full`** and **`ControllerMeta.quality` / `.deltas`** —
  the wire shapes, all optional and all absent on older controllers.
- **`packTagPatterns(names, {max})`**, **`mergeTagPatterns`**, **`tagPatternMatches`**,
  **`tagInPatterns`**, **`MAX_TAG_PATTERNS`**, **`NO_TAGS`** (`./tags.ts`) — turning "this screen
  draws these tags" into the handful of globs one subscription accepts. `RealtimeOptions.tags`
  takes patterns and the controller caps them at 40 per connection; a real screen routinely names
  more than that (the Pomona `/system` schematic binds **217** top-level tags), so something has
  to pack the list — and the one property that must never be traded away is that the result is a
  **superset** of what was asked for. A pattern set that drops a tag does not error: it leaves one
  live instrument reading "—" forever, on a screen that otherwise looks healthy. Every merge
  widens, greedily and cheapest-first, preferring `?` (bounded: one character's worth of siblings
  per position) over `*` (unbounded: `RTU32_*` is 514 tags on that host). Measured: those 217 tags
  pack into 40 patterns matching **200** of the controller's 10,236 — 187 asked for, 13 swept up.
  A prefix-only packer matches 7,593. Tested at every cap down to one pattern.
  `NO_TAGS` is how a client says *no tags at all*, which `tags: []` cannot: `[]` has always meant
  everything. It is worth having — the frame's alarm summary, driver status and scan diagnostics
  ride every frame whatever the filter says, so a screen that draws no tags can still be live.
- **`RealtimeClient.bytesReceived` / `.frames`** — payload characters and frames received since the
  client was created, across reconnects (unlike `seq`, which is the connection's counter). They
  exist to be measured: "filtering the subscription made this screen cheaper" is a claim about
  bytes, and the alternative is reading a browser network panel by hand, which cannot separate two
  streams on one page and cannot be asserted on at all.
- **`RealtimeClient.tagFilter`** — the patterns this client subscribed to, `[]` for unfiltered.
  Fixed at construction, because the controller applies `?tags=` per connection: changing a
  subscription means opening a new client, and this is what a diagnostics panel prints.
- **`FrameSource<T>`** — the half of `RealtimeClient` that `useTrend` and `AlarmClient` actually
  use (`frame`, `onFrame`, `onOpen`), now an interface those two accept instead of the class.
  `RealtimeClient` satisfies it structurally, so every existing caller is unaffected. It exists so
  an app can put its OWN object in front of the client: the Pomona HMI swaps the underlying
  connection whenever the open screen changes what it needs, and hands the kit a stable facade
  that forwards to whichever connection is live — trend buffers are keyed on that identity, and a
  callback bound to a replaced client simply stops being called.

### Added — the house style, and the token layer it needed

`theme.css` grows from a palette into the full house token layer, surveyed off `@joyautomation/salt`
and the two web properties that already use it. **Every existing token name still resolves** — see
the mapping table below — so nothing that consumes 0.5.0 breaks.

- **Type**: `--font` is now Space Grotesk, `--mono` is IBM Plex Mono, and `--font-display`
  (Righteous) joins them for a header wordmark — **chrome only, never a process value**. The size
  scale is unchanged. Three weight tokens name the house half-steps that carry as much of the
  hierarchy as size does: `--weight-control` 550 · `--weight-numeric` 650 · `--weight-eyebrow` 700.
- **Space**: `--space-unit` (salt's `0.325rem`) and `--space-1 … --space-8` on it — the layer the
  kit was missing, and the reason literal px kept reappearing in component styles.
- **Radii**: `--radius-sm` (6px, controls and chips) and `--radius-pill` join `--radius` (10px,
  unchanged, panels and faceplates).
- **Depth**: `--elevation-float`, the ONE elevation, for things that actually float. Plus
  `--tint-strength` / `--wash-strength` so a tint is the same tint everywhere, `--overlay` for the
  ground a modal is seen against, and `--transition` / `--transition-slow` (0.12/0.15s).
- **Utility classes**: `.eyebrow` (the house micro-label, now a class instead of a copied five-line
  rule), `.readout` (mono · tabular · 650), `.float`, `.inset`, `.tint`.
- **Meaning-carrying colour, named.** The fifteen literals a legacy port keeps because they record
  a *fact* rather than a taste are re-expressed as roles on the reserved safety palette:
  `--q-good`, `--q-stale`, `--q-bad`, **`--q-notpublished`** (the source's 3.5px magenta comm-fail
  border — *this point is not being published*), **`--q-simulated`** (its 3px orange outline), and
  the equipment-state family `--eq-state-run` / `-stop` / `-fault` / `-unknown` / `-run-wash`
  (`unknown` deliberately not collapsing into `stop`). One line each restores the literal magenta
  and orange for a site whose operators are trained on them.
- **`./fonts.css`** — a new export that self-hosts the three faces through `@fontsource`
  (`@fontsource-variable/space-grotesk`, `@fontsource/ibm-plex-mono`, `@fontsource/righteous`,
  now dependencies). Deliberately **separate from `theme.css`**, which only names the families with
  full fallback stacks: an app with its own typography pays nothing, and an app that wants the house
  faces gets them from its own bundle. **Never a Google Fonts `<link>`** — an HMI runs on an
  isolated OT network as often as not.

#### Token mapping — 0.5.0 → 0.6.0

Nothing was renamed or removed; two families changed *value* and everything else is additive.

| 0.5.0 | 0.6.0 | note |
| --- | --- | --- |
| `--font` `system-ui, …` | `'Space Grotesk', system-ui, …` | same name, house face first, same fallbacks |
| `--mono` `ui-monospace, …` | `'IBM Plex Mono', ui-monospace, …` | same name, house face first |
| `--accent` `#3987e5`/`#2a6fc9` | `#06b6d4` (dark) / `#0e7490` (light) | the house cyan brand |
| `--primary-bg/-border/-hover` | cyan steps `#155e75`/`#0e7490`/`#0891b2` (dark), `#0e7490`/`#22a3bd`/`#155e75` (light) | follow `--accent` |
| `--radius` 10px | unchanged | |
| `--font-2xs … --font-lg` | unchanged | |
| `--bg --surface --surface-2 --border --ink --ink-2 --muted --grid --axis --hover` | unchanged | the kit's light/dark sets are adopted verbatim |
| `--s1 --s2 --s3 --s5`, `--good --warn --serious --crit` | unchanged | safety colours stay reserved |
| literal `6px`/`8px` radii inside components | `var(--radius-sm)` | same rendering |
| literal `4/8/10/14px` gaps and padding | `var(--space-1 … --space-3)` | ±0.4px where the scale lands nearby |
| `button` / `.btn` base rule | `--radius-sm`, `--space-2 var(--space-3)` padding, `:active { translateY(1px) }` | the house button idiom; ~3px taller, 6px corners |
| `.card` base rule | padding `var(--space-3)` (15.6px, was 16px) | |
| — | `--font-display`, `--weight-*`, `--space-*`, `--radius-sm`, `--radius-pill`, `--elevation-float`, `--tint-strength`, `--wash-strength`, `--overlay`, `--transition*`, `--q-*`, `--eq-state-*` | new |

### Added — the faceplate standard, cards, and confirmation

- **`FaceplateShell`** — one faceplate layout for every equipment family: header (label · tag path ·
  quality chip · `SIM` chip · close) → state strip → hero → tabs → footer action row. A plant has
  four or five families and no more; it does not have one faceplate per device, which is how a
  project ends up with six hand-written valve popups that drift apart. **Two hosts from one prop**:
  `as="modal"` is a native `<dialog>` (focus trap, top layer, Escape); `as="page"` lays the same
  regions out as a route, for the small-screen rule where tapping a card navigates instead of
  opening a popup; `as="auto"` (default) picks by viewport width against `breakpoint` (900px).
  Supplying the `sim` snippet appends a standard **Sim** tab — per-equipment `SIMULATE`/`SIMVALUE`
  is a production feature of the controller, not a demo affordance. The `footer` snippet receives
  **`writable`**, which is quality-gated but *sim-permitted*: stale, bad and unpublished disable a
  control; simulated leaves it enabled and raises a persistent
  `SIMULATED — not commanding the plant` banner instead. `Faceplate` is unchanged and remains the
  bare chrome underneath.
- **`EquipmentCard`** — what a schematic becomes when the screen is too narrow to hold it: symbol
  slot (or an `EquipSymbol` from `src`), an eyebrow description with the alarm bell at its right,
  one to three quality-aware values, a state-chip row, an optional sparkline, and **the whole card
  as one tap target**. **Quality drives the border** — solid `--q-notpublished`, dashed
  `--q-simulated` — the legacy magenta/orange convention, tokenised, so a card can never show a
  confident number for a point the runtime is not publishing.
- **`ValueText`** — the quality-aware value primitive, and now the only place in the kit that
  renders a number an operator reads. Takes the value plus the two facts about it and draws the
  indication once: `notPublished > bad > simulated > stale > good`. The value is **never blanked**
  for stale or bad — it is what the plant last was, and the number plus its age beats a dash. Only
  an unpublished point loses its number.
- **`StateChip`** — the house state chip (`--font-2xs`, pill radius, `1px var(--space-1)`, colour
  from the safety set), taking either a `StatusKind` or a `ValueStatus`, so a quality chip and a
  state chip are one component. Denser than `StatusPill`, which is unchanged.
- **`ConfirmDialog`** + **`confirm()`** — one dialog, mounted once next to `<Toast/>`, for every
  irreversible or plant-affecting action. Promise-based on purpose: `if (await confirm({…}))` at
  the call site cannot forget to ask, which a per-call-site `open` boolean plus a callback very much
  can. Requests **queue**, each keeping its own promise, so an operator who triggers two confirmable
  actions before answering either loses neither. Escape and the backdrop cancel; **the confirm
  button is never the focused default**. `operator: true` shows the editable name field, prefilled
  from `localStorage` — the last point before an unauthenticated, permanent record is written.
  Called with no host mounted it warns once and falls through to `window.confirm` rather than
  returning a promise that never settles.
- **`AckButton`** (+ `worstFirst`, `ackLine`) and **`AlarmTable.confirmAck`** — ack routed through
  the dialog with the alarms enumerated worst first. **Both paths confirm**: Ack All *and* a single
  row, because there is no role gate to slow an accidental click and the record cannot be undone.
  `confirmAck` defaults to `false`, so nothing changes for an existing caller.
- **`./quality.ts`** — `valueStatus`, `isWritable`, `formatValue`, `formatAge`, `STATUS_META`. The
  precedence order and the never-a-confident-zero rule as pure functions, so they are testable and
  so an app can gate its own controls on exactly what the kit draws.
- **`./confirm.ts`** — `createConfirmQueue`, `splitItems`, `getOperator`, `setOperator`. The whole
  decidable half of the dialog, runes-free and unit-tested.

### Added — testing

- **`npm test`** — the kit now has a test rig: `tests/harness.ts`, a small stand-in for vitest
  (a strict subset of its API) bundled with esbuild and run on node, so the kit's pure logic can be
  tested without acquiring a test-runner dependency. **59 specs** across four files: the delta
  merge (10), the quality precedence and value formatting rules (17), the confirm queue's promise
  flow (13), and tag-pattern packing (19). If the kit ever does adopt vitest, each spec changes one
  import line.
- The pattern-packing specs run against a **real 217-tag fleet subscription** rather than toy
  input, because the property they protect — the packed set still matches every tag asked for —
  only breaks at scale: with a handful of names nothing is ever merged.
- The harness accepts **async specs** (anything microtask-only), and reports an async spec that
  never settled as a failure rather than letting it vanish into a green run.

### Changed

- **`theme.svelte.ts`** — `theme.init()` now defaults to **dark** rather than system. An HMI's
  design case is an ops room at 03:00, and a control screen that comes up white because the
  workstation happens to be set to a light desktop theme is the wrong default for the room it lives
  in. A saved choice always wins, and `theme.init('system')` restores the old behaviour in one
  argument. `ThemeSwitch` is unchanged and now shows Dark selected on a first visit.
- **`AlarmTable`** and **`AlarmBanner`** — token swap only, no layout change: literal px gaps,
  padding and radii become `--space-*` / `--radius-sm` / `--radius`, hard-coded mix percentages
  become `--tint-strength`, and `font-weight: 650` becomes `--weight-numeric`. `AlarmTable` also
  gains `confirmAck` (above).
- **`Gauge`** gained `sweep` (degrees of scale, default 240 — unchanged) and `gap` (its
  complement, for the 360° donut idiom), plus `thickness`. The drawing box grows downward as the
  arc closes and the end labels move off the seam past 300°. **The default rendering is byte-for-byte
  what it was**: `sweep = 240` still yields the same 170×130 viewBox.
- **`RealtimeClient.onOpen`** now accepts multiple subscribers and returns an unsubscribe
  function (it previously kept a single callback, which `useTrend` would have clobbered). Existing
  single-callback callers are unaffected.
- **`theme.css`** defines `.blinking` (an unscoped attention-flash rule, with a reduced-motion
  outline fallback) and the legacy-port token families documented in the README's Theming section.

### Docs

- README: a **Subscriptions: deltas and data quality** section and a **Testing** section.
- A new controller guide, [Streaming and data quality](https://nautilus.joyautomation.com/guides/streaming/),
  covering the frame protocol, the measured delta ratios, the tag-filter globs, and the quality
  model end to end.
- README: a **Porting a legacy HMI** section, a **Writing back** section, a
  **Trends over history** section, the legacy-port token families under
  Theming, and every new component in the props table.
- README: a **Faceplates, cards and confirmation** section, and a rewritten **Theming**
  section covering the two layers, the space/radius/depth families, the meaning-carrying quality
  tokens, the dark default, the pre-paint stamp, and `fonts.css`.
- `examples/hmi-demo` gained a **`/legacy`** route exercising all of the
  above against a synthetic sine wave — it needs no controller running. It now also covers the five
  `ValueText` quality states, a four-card `EquipmentCard` grid, a `FaceplateShell` with hero, tabs,
  Sim tab and a quality-gated footer, and both confirm paths; `<ConfirmDialog/>` is mounted in its
  layout and `app.html` carries the pre-paint theme stamp.

### Fixed

- **`EquipSymbol` rendered no picture at all when both `width` and `height` were given and it sat
  next to anything else** — a chip column, a card's value stack, any flex row. A symbol file that
  carries only a `viewBox` (no width/height attributes) contributes a min-content width of **zero**
  to a flex row, so the wrapper collapsed and the `contain`-fit path the 0.6.0 notes describe drew
  nothing. The wrapper now gets a real `width × height` box with the picture `contain`-fit inside
  it. The `width`-only path is untouched. This is why the demo's own symbols were invisible.
- `TankGlyph` takes its liquid colour from the `fill` **attribute** and no longer sets `fill` on
  it from CSS. In the app this glyph came from, a `.glyph rect { fill: … }` rule was silently
  overriding the band colour every caller passed, so the level bar rendered in the surface colour.
