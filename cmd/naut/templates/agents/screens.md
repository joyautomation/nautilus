## Operator screens

naut builds the operator screen from the project itself, with no npm, and
`naut run` serves it at `/` (the engineering dashboard moves to
`/_nautilus/`). Guide: https://nautilus.joyautomation.com/guides/operator-screens/

- A `*.mimic.json` is a screen: equipment placed on a canvas, pipes between
  it, tags bound to it. A project with mimics gets a page for each. Use
  the kit's equipment (`Tank`, `Pump`, `Valve`, `Gauge`, `CommandButton`,
  `WriteToggle`, `OffPage`, ...) and a `{Name}.svelte` of your own where the kit
  doesn't draw what you need, with its pipe ports in `{Name}.component.json`
  and its `examples`: the props for each state it draws
  (`"examples": {"open": {...}, "closed": {...}, "failed": {...}}`).
  `/components` draws every one, and `naut check` renders it.
- naut's pages put a bar over every screen: the project's name and the
  first mimic's `description`, the screens and Diagnostics, the active
  alarms with Ack all, the connection and the theme. Don't draw an alarm
  list, a connection light or a title on the canvas.
- The drawing shows the plant; an operator changes it in a panel. Give
  each piece of equipment an operator acts on a `panel`: a click on it
  opens the panel beside the drawing, with its live readings (a number
  with a trend of its last few minutes, a true/false as words), its
  setpoints, its commands and its switches:
  `"panel": {"title": "Backwash Pump · P-501", "readings": [{"tag":
  "P501_Running", "label": "State", "on": "On", "off": "Off"},
  {"tag": "FIT501_Flow", "label": "Flow", "unit": "gpm", "decimals": 0}],
  "setpoints": [{"tag": "BW_FlowSP", "label": "Backwash flow", "unit":
  "gpm"}], "commands": [{"tag": "P501_Start", "label": "Start", "kind":
  "start"}], "toggles": [{"tag": "P501_OOS", "label": "Out of service"}],
  "links": ["F1"]}`. Equipment that belongs to another's panel opens it
  by id (`"panel": "P501"` on the pump's flow meter). Every setpoint,
  command and switch goes in a panel; a setpoint of the whole process
  goes in the panel of the vessel or unit it runs. The drawing keeps only
  the sequence's own controls (Start, Hold, Abort, a recipe) and the
  plant's state and values.
- A tank's levels that matter go on the tank. A pump's start and stop are
  one `ranges` entry, drawn as a bracket from on to off:
  `"ranges": [{"label": "Lead", "on": "LeadOnSP", "off": "LeadOffSP"}]`
  (a number in % or a tag at each end). Other levels are `marks`, each an
  arrow at its level: `"marks": [{"label": "High alarm", "value": 90,
  "alarm": true}, {"label": "Level SP", "tag": "LevelSP"}]`. Both go in
  the tank's `props`. Ranges stand beside its scale, on the right; marks
  draw on its left (`"marksSide": "right"` beyond the ranges, on the side
  its pipes don't reach): leave them room. An alarm
  mark is also a dotted line across the tank, and stays grey until the
  level reaches it. The setpoints themselves are written in the tank's
  panel.
- Every analog value on the drawing is a `labels` entry with `bind` (and
  `unit`, `decimals`) by what it measures: on its line or vessel, or just
  under the equipment's name (a well's level under its name). naut's pages
  draw it with a trend of its last few minutes (`"min"`/`"max"` fix the
  trend's scale, a level's 0-100). A reading or state that stands on its
  own (a vessel's level, the station's flow, what the control is doing) is
  a row, `"as": "row"`: its name in a shaded cell, then the value, its
  trend and its state in words. Rows stacked together take one `width`, so
  they line up as a table, and a row can show a value and a state at once:
  `{"text": "Level", "x": 233, "y": 488, "as": "row", "width": 290,
  "bind": "LIT101_Level", "unit": "%", "min": 0, "max": 100, "state":
  [{"when": "HighLevelAlm", "text": "High Alarm", "kind": "critical"},
  {"text": "Normal"}]}`, and under it `{"text": "Pump Control", "x": 233,
  "y": 522, "as": "row", "width": 290, "bind": "SeqStep", "values": {...}}`.
  In an app the same row is `<ReadoutRow name value unit history state />`. Where a reading can't sit by
  what it measures, `"at": [x, y]` names that point and a dashed sense line
  joins them. A meter that reads a header's total sits on the header after
  the junction, not on one branch.
- Put instruments where they mount: a level float at the vessel's wall,
  clear of the pumps and their pipes.
- Everything an operator watches run (a pump, a blower, an agitator, a
  sequence) shows its state twice: on the symbol (the kit's change colour
  and show flow) and in words, a `labels` entry with `state`. A pump's
  words (On or Off, its speed, Lead or Lag) stack over its symbol as
  outlined text centred on it, `"as": "text", "align": "center"` with `x`
  the symbol's middle, one label per line about 20 apart:
  `{"text": "", "x": 640, "y": 650, "as": "text", "align": "center",
  "state": [{"when": "P101_Fault", "text": "Fault", "kind": "critical"},
  {"when": "P101_Running", "text": "On"}, {"text": "Off"}]}` (On and Off
  in plain ink: the symbol shows it running; colour is for a fault),
  then `{"text": "", "x": 640, "y": 670, "as": "text", "align": "center",
  "bind": "P101_SpeedHz", "unit": "Hz", "trend": false}`. A state that
  belongs to no one symbol (a sequence's step, a vessel's level state) is
  a row. The first state whose `when` tag is true shows; the
  one with no `when` comes last.
  A step or a state code is shown in words, never as a raw name or number:
  `"values": {"PostRun": "Running on past the off level", "3": "Backwashing"}`
  on its label.
- Write every label for an operator seeing the screen for the first time:
  it says what it is about and what the plant is doing, in their words.
  A level's state is in its "Level" row, not a bare "Normal" under the
  vessel; a sequence is a "Pump Control" row with values like "Lead Pump
  Pumping Down" and "Waiting For Level To Rise", not its step names; a
  count says what it counts. Numbers an
  operator seldom needs (cycle counts, run hours) go in the panel.
- What repeats (four filters, eight pumps) goes in the mimic's `units`,
  drawn once: `{n}` in any id, tag, label or pipe end becomes each value,
  and each copy moves by `step`:
  `"units": [{"id": "filter", "each": [1, 2, 3, 4], "step": [420, 0],
  "equipment": [{"id": "F{n}", "component": "FilterCell", "x": 230, "y": 370,
  "bind": {"level": "F{n}_Level"}}], "pipes": [...], "labels": [...]}]`.
- naut is the only tool a project needs, and often the only one there is
  (the playground has no Python or Node). Write every file by hand, never
  a script that writes one: `units` (or `{#each}` in an app) is how a
  screen repeats itself.
- Draw a mimic's canvas for the window it is seen in: about 1880×1000 fills
  a 1920×1080 screen under naut's bar. naut's pages fit the whole drawing
  to the window, so a taller canvas is drawn smaller, never cut off; in an
  app, `<Mimic fit="contain">` does the same.
- Draw every pipe by what it connects and let the screen route it:
  `"routing": "auto"`, with `from` and `to` each a port
  (`{"equip": "XV101", "port": "bottom"}`) or another pipe it tees into
  (`{"pipe": "AIR_HDR"}`), and no points. The screen draws it square,
  clear of equipment and labels, with the fewest bends and crossings; it
  dots each tee, and a pipe that crosses is drawn broken under the other.
  A header is a pipe too: route it between the equipment or `OffPage`
  connectors at its ends, and run each drop to it with
  `{"pipe": "AIR_HDR"}` (in a unit, `"from": {"equip": "XV{n}04", "port":
  "top"}, "to": {"pipe": "AIR_HDR"}`). Points on a routed pipe are
  waypoints it passes through; add one only to steer a line.
- Lay the pipes out so an operator can follow each one by eye, and leave
  each line a lane. Put a header on the side of the equipment its lines
  leave from (lines leaving the top run to headers above, lines leaving
  the bottom to headers below), nearest first in the order the drops reach
  them, so no drop crosses more than two headers to reach its own, and
  keep labels off the way between a port and its header. Every drop still
  runs to its header: an `OffPage` connector is for a line that goes on to
  another screen, never a way round a crossing.
- Name every valve, instrument and machine on the drawing as an operator
  says it, beside its symbol: Pump 1, Pump 2, Wetwell 101, Check Valve 1,
  Flow, Level, High Switch, Low Switch, Inlet Valve. ISA tag numbers
  (P-101, LIT-101, XV-201) mean nothing for running the plant or showing
  the screen to others; they tie the screen to the drawings, a second
  purpose, so they go in the panel a click opens, beside its title
  (`"panel": {"title": "Pump 1 · P-101", ...}`), not on the drawing. Where
  the project gives no tag numbers, number them by loop (tank 2's valves
  XV-201, XV-202, its level LT-201) and use the same numbers everywhere
  they appear. A level switch is High or Low; it is High-High or Low-Low
  only when there are High and Low switches between them. An inline instrument (a flow meter, a control
  valve) sits on the line it measures, drawn in it, not beside it.
- It is an operator screen, reviewed by ISA-101 and by how fast an
  operator reads it, not a drafting P&ID: draw the plant the way an
  operator recognises it (a tank in section, a submersible pump in its
  well), grey and flat, with no instrument bubbles. Colour is for state and
  alarms only: text in ink, never a coloured label or setpoint; liquid in
  the tank's neutral grey, and the tank's `alarm` (bound to a condition: too
  hot, out of spec) turns it the alarm colour while it holds. Use the whole
  canvas: what an operator wants at a glance (the lead pump and
  alternation, the sequence's step, a batch's phase) goes in readouts and
  state labels in the space beside what it describes, never a third of the
  screen left empty.
- An operator picks a light or a dark theme. Colour your components from
  the kit's tokens, never a hex (a hex reads in one theme only): text in
  `var(--ink)`, `var(--ink-2)` or `var(--muted)`; grounds in `var(--bg)`,
  `var(--surface)`, `var(--surface-2)`; outlines in `var(--edge)`; state
  in `var(--good)`, `var(--warn)`, `var(--crit)`. Text as the web asks of
  it: values and body text at 16 px, small labels (units, captions) at
  12 px or more, as drawn in the window.
- For more than mimics, write an app: `hmi/src/routes/+page.svelte` is `/`,
  components go in `hmi/src/lib`, and `server.hmi: hmi` in the manifest.
  It is SvelteKit's layout, client-side; Go is the only backend.
- Never hand-write an HTML or JavaScript page for the operator screen, and
  never point `server.hmi` at one: naut doesn't check it, it can't use the
  kit, and the editor can't show it.
- `naut check` checks screens: every tag a binding, a label, a control or
  a panel names, every pipe's ports, a prop or binding a kit component doesn't
  take (it shows nothing), and the screen builds. A screen imports only
  Svelte, `@joyautomation/nautilus-hmi`, `@joyautomation/nautilus-hmi-3d`,
  three and Threlte.
- With Chrome, Chromium or Edge on the machine, `naut check` also opens
  each page at desktop and phone size and reports what it sees: a page
  that throws or is blank, a pipe that draws nothing, a pipe that passes
  under three or more others, a pipe drawn through a tag or a reading, a
  shape drawn far outside its drawing, a drawing that runs off the bottom
  of the window or is drawn too small to read, text too dim to read in
  either theme or in an equipment's panel, drawn under 12 px or drawn over
  other text or a field,
  controls an operator has to scroll to reach on a desktop, sideways
  scroll on a phone. Fix them like compile errors. A screen you haven't
  seen rendered isn't done.
- In a component that uses `$state(...)`, never name a prop or variable
  `state`: Svelte then reads `$state` as a store subscription and the
  component throws when it mounts. Name it for what it holds
  (`filterState`).

