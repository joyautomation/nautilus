# Accessibility

This page states what nautilus does for accessibility today, what it
doesn't do yet, and how to report a problem. It describes the code as it
is, not a target we've met.

nautilus doesn't claim conformance with WCAG or any other standard. No
accessibility audit has been published, and only colour contrast is
tested automatically.

## Where people meet nautilus

| Surface | State |
| --- | --- |
| `naut` CLI | Commands write plain text to stdout and stderr. `naut new` asks its questions in an interactive terminal form, which doesn't use the form library's screen-reader mode; `--no-input` with flags skips the form. |
| VS Code extension (`tools/vscode-iec`) | Built on VS Code, so the editor, commands, settings, high-contrast themes and screen-reader mode are VS Code's own. The extension's webviews (ladder, FBD and SFC diagrams, screen and scene editors) are its own drawings: they use ARIA labels in places but have had no accessibility review. The text is always the source, so everything a diagram shows can also be read and edited as text in the editor. |
| Operator-screen (HMI) kit (`hmi/`) | See below. |
| 3D view (`hmi-3d/`) | See below. |
| Docs site (`website/`) | Built with Astro Starlight; it inherits Starlight's accessibility. |

## The HMI kit

The operator screens a project builds come from the HMI kit. Plant
operators use this most, so it gets the most detail here.

**Colour and contrast**

- The default look is ISA-101 style: flat grey surfaces, with colour kept
  for abnormal states and alarms. A `realistic` look is also available.
- Colours are design tokens (`hmi/src/lib/palette.css`), in a dark and a
  light theme. The theme follows the operating system's preference, or
  the operator's choice.
- A test (`hmi/tests/palette.test.ts`) checks that body text (`--ink`,
  `--ink-2`, `--muted`) is at least 4.5:1 against every background, and
  outlines (`--edge`) at least 3:1, in both themes.
- `naut check` measures text contrast on a project's own screens.

**Not colour alone**

- Alarm priority is shown by shape, glyph and an urgency word as well as
  colour. Alarm state always has a text label.
- In the ISA-101 look, pumps and valves show their state by fill (solid
  or hollow), not by hue. The running pump's impeller also spins.
- Pumps and valves carry an accessible label (`role="img"`) with their
  live state: a pump says running or stopped, and a valve gives its
  percent open.

**Keyboard**

- Most operator controls are native elements: equipment on a mimic is a
  `<button>`, and a setpoint is an `<input>` (Enter commits it) with a
  Set button. The exceptions are listed under known gaps.
- Escape closes drawers, faceplates, panels and tooltips.
- Buttons and inputs show a visible focus outline.

**Screen readers**

- New alarms are announced through live regions: the alarm banner (as
  assertive while anything is unacknowledged), the alarm table (for
  critical and high alarms), and toasts. The connection badge is a
  status region.

**Motion**

- Unacknowledged alarms flash once every 1.1 s (about 0.9 Hz), well
  under the 3 Hz seizure threshold.
- With `prefers-reduced-motion`, the main alarm components stop flashing
  and show a steady border, and animated equipment stops moving.

**Touch**

- On a touch screen (`pointer: coarse`), buttons, toggles, menus and
  navigation get a 44 px minimum size.

## Known gaps

- **No automated accessibility testing.** There's no axe or similar check
  in CI; only the palette contrast is tested.
- **State colours are not in the contrast test.** `--good`, `--warn`,
  `--serious` and `--crit` aren't checked, though the operator-screens
  guide says they meet AA.
- **No high-contrast mode.** There's no `forced-colors` or
  `prefers-contrast` support.
- **Equipment state relies on fill and motion, not text drawn on the
  screen.** The spoken label carries the state, but a sighted operator
  who can't tell fills apart has no on-screen word. In the `realistic`
  look, the running lamp is a colour change only.
- **Changing values aren't announced.** Only alarms, toasts and the
  connection status are; a screen-reader user has to move to a value to
  hear it.
- **Some clickable wrappers aren't keyboard elements.** Eight places
  suppress Svelte's accessibility warnings for clickable wrappers: the
  drawer, modal, faceplate, menus, tooltip and equipment card. These need
  checking one by one. Alarm-table rows respond to a click; keyboard
  access to them hasn't been verified.
- **Mimics don't follow the user's font-size setting.** A mimic is a
  fixed-size drawing scaled to fit its container. Browser zoom, reflow
  and 200 % text haven't been tested.
- **Reduced motion isn't confirmed for every animation.** It's handled in
  the main alarm and equipment components, but not checked one by one
  for the rest (for example, pipe flow).
- **The 3D view (`hmi-3d`) has no keyboard or screen-reader access to the
  scene.** Its alarm signs and errors panel carry labels, and alarm
  markers respect reduced motion. The scene itself has no keyboard way to
  select equipment and no text alternative.

## Reporting a problem

Open an issue with "Accessibility" in the title. Include:

- what you were trying to do;
- your assistive technology (screen reader, magnifier, switch access,
  OS high-contrast or zoom) and its version;
- your browser or VS Code version;
- which screen or component it was.

