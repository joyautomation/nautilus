## Field devices (`devices:` and `profiles/`)

A `devices:` row in `nautilus.yaml` names a profile in `profiles/`: the
profile is the device's register map (or OPC UA nodes), cited from its
public documentation, and nautilus derives the tags, drivers, alarms and
faceplate from it at load. `naut devices help` has the rest.

- Start from a built-in one: `naut devices profiles` lists them, `naut devices
  add <id>` vendors a copy into `profiles/`. That copy is the project's pin: a
  new naut never changes it, so don't edit it to match a different device;
  copy it to a new id.
- Writing a profile from a manual: take every row from the document, never
  from memory. Give each point a `doc:` that starts with the id of an entry
  under `docs:` and names the page or table ("manual p. 61"); the entry has a
  `url:` or the publisher's document number. A row you could not find in
  the document is `unverified: true`, kept visible and not hidden among the
  rest. Never invent an address to make a profile complete.
- Write the simulator from the manual's state machine (start, stop, fault,
  reset), not from the controller's program: a simulator that mirrors the
  program proves nothing.
- Run `naut devices lint profiles/<id>.yaml` before you stop. It fails on
  addresses two points share and on a simulator that does not compile, and
  warns on a citation that names no page, a document nothing cites, and each
  unverified row. Fix every error; leave a warning only when you can say why.
- Lint cannot tell a wrong register from a right one: both the controller
  and the simulator read the same profile. Someone who did not write it
  checks every cited row against the document before it is trusted.
