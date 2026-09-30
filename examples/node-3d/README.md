# node-3d — node1, opened up

The Supermicro SYS-112B-WR in the office cluster, drawn from its chassis
profile (`hmi-3d/profiles/supermicro-sys-112b-wr.json`) crossed with its
tags (design doc §3e). Click any part for its faceplate; lid, x-ray,
exploded and camera presets in the toolbar.

- Every part comes from a controller with NODE1's it-drivers tags: fans,
  PSUs, temperatures, and the part tags `naut redfish import` generates
  (drives, DIMMs, the CPU, PCIe cards, NIC ports). A controller imported
  before the part tags existed shows those parts without values: re-import.
- `?view=top|front|rear`,
  `?lid=on`, `?xray`, `?exploded`, `?codes` (the printed AR codes, off by default) and `?overlay=heat|interfaces|free` set the view.

- `?overlay=cables` labels every port with its far end from the site
  topology (`hmi/src/lib/hq.topology.json`) and checks each declared link
  live: ✓ confirmed (LLDP / MAC), = consistent (both up, same speed),
  ✗ contradicted, ↓ down, ? unverified.
- `/rack`: the whole cluster racked — three nodes and three S3900 switches
  (`hmi-3d/profiles/fs-s3900-24t4s-r.json`) placed by `hmi/src/lib/hq.rack.json`
  (not racked yet: a default of switches on top, ports front, nodes below), every cable drawn between its
  exact ports and coloured by its check. Click a device to zoom in (lid,
  x-ray, exploded, overlays), a part for its faceplate; `?focus=NODE2`,
  `?view=cables|rear|front`. The switches are live only where the controller
  polls them (hq-sw1 on `:8081` today): serve with
  `CONTROLLER_URL=http://localhost:8081`.
- **Simulation mode**: the whole cluster with no hardware. A plant
  (`examples/it-cluster`: 68 h of recorded history as the baseline, faults
  as tags) on `:8087`; stand-ins that answer from it
  (`naut snmp serve --from`, `naut redfish serve --from`); the monitoring
  controller polling them on `:8085` (`randd/node-3d-sim/sim`). Serve with
  `CONTROLLER_URL=http://localhost:8085 PLANT_URL=http://localhost:8087`:
  the replay clock (bottom right, marked SIMULATION) reads and steers the
  plant's `Replay_*` tags.
- `/rack?mesh` (or **mesh** in the toolbar): the same devices and links as a
  network floating in space — switches on a ring, each server under the two
  switches it is cabled to, every link an arc labelled with its two ports and
  coloured by the same check. Click a device to focus it in place (its
  links and what they reach stand out, the rest fades); click again, or
  **show in rack**, to go to it in the rack (`?mesh=node2`).
- Focus in the rack fades the same way — the device in full, what its
  cables reach at 40 %, the rest at 10 % and unpickable — and a server
  slides out 45 cm on its rails, lid off, so its parts have room.
- `?overlay=identify`: every part named and coloured by kind (drive,
  memory, CPU, card, fan, PSU, port).

```sh
(cd ../../hmi-3d && npm run package)
cd hmi && npm install && npm run build && npx vite preview --port 8097
```
