# node-3d — the office cluster, racked

Three Supermicro SYS-112B-WR nodes and three FS S3900 switches, each drawn
from its chassis profile (`hmi-3d/profiles/`) crossed with its tags (design
doc §3e), placed in the rack by `hmi/src/lib/hq.rack.json` (not racked yet:
a default of switches on top, ports front, nodes below), with every declared
cable drawn between its exact ports and coloured by its live check.
`/rack` is the app; `/` goes to `/rack?focus=NODE1` (it was node1's page).

- Click a device and it slides out of the front of the rack, clear of it,
  still on its cables; the camera circles it (drag to look at every side),
  and the rest fades: what its cables reach at 40 %, the rest at 10 % and
  unpickable. Click a part for its faceplate. A server has lid, x-ray,
  exploded and `codes` (the printed AR codes, off by default); every device
  has overlays. `?focus=NODE2`, `?view=iso|rear|front`, `?lid=on`, `?xray`,
  `?exploded`, `?codes`, `?overlay=heat|interfaces|free|cables|identify`.
- Every part comes from a controller with the it-drivers tags: fans, PSUs,
  temperatures, and the part tags `naut redfish import` generates (drives,
  DIMMs, the CPU, PCIe cards, NIC ports). A controller imported before the
  part tags existed shows those parts without values: re-import.
- Cables, from the site topology (`hmi/src/lib/hq.topology.json`), checked
  live: ✓ confirmed (LLDP / MAC), = consistent (both up, same speed), ✗
  contradicted, ↓ down, ? unverified (an end in the model not reporting). A
  far end outside the model (the site) is judged on the end in it.
- Alarms: a sign over anything in alarm, its shape by priority; in the rack
  one per device, focused one per part (the device keeps a sign only for
  what no part shows). The alarm box (top right) names each one; a click
  goes to it. On a phone the boxes fold to chips above the replay clock.
- **Simulation mode**: the whole cluster with no hardware. A plant
  (`examples/it-cluster`: 68 h of recorded history as the baseline, faults
  as tags) on `:8087`; stand-ins that answer from it
  (`naut snmp serve --from`, `naut redfish serve --from`); the monitoring
  controller polling them on `:8085` (`randd/node-3d-sim/sim`). Serve with
  `CONTROLLER_URL=http://localhost:8085 PLANT_URL=http://localhost:8087`:
  the replay clock (bottom right) steers the plant's `Replay_*` tags, the
  scenario panel runs its named scenarios and lists the faults that are
  set, and a right-click on a part offers that part's faults; all of it
  marked SIMULATION and shown only when the plant answers.
- `/rack?mesh` (or **mesh** in the toolbar): the same devices and links as a
  network floating in space — switches on a ring, each server under the two
  switches it is cabled to, every link an arc labelled with its two ports and
  coloured by the same check. Click a device to focus it in place (its
  links and what they reach stand out, the rest fades); click again, or
  **show in rack**, to go to it in the rack (`?mesh=node2`).
- `overlay=identify`: every part named and coloured by kind (drive,
  memory, CPU, card, fan, PSU, port).

```sh
(cd ../../hmi-3d && npm run package)
cd hmi && npm install && npm run build && npx vite preview --port 8097
```
