#!/usr/bin/env python3
"""Generate the plant's tags, replay manifest and plant.st from a recorded
cluster: the monitoring project's manifests (which devices, which tags), its
recording (history.json.gz, the SNMP walks). Re-run it; do not edit what it
writes.

  python3 gen.py --dataset <dir> --monitor <dir> [--at 2026-09-28T18:00Z]

  --dataset   history.json.gz + snmp/<host>.snmpwalk (also linked as ./data,
              which the replay driver reads at run time; not committed)
  --monitor   the monitoring project: snmp_manifest.yaml, redfish_manifest.yaml
  --at        the recorded moment the device tags start at, and hold when
              the recording is absent
  --naut      the naut binary (default: naut on PATH)
  --walk      each switch's walk under <dataset>/snmp (default {tag}.snmpwalk)
  --mockup    each BMC's mockup under <dataset>/redfish (default {tag})

Per recorded device tag X (SW1_Port25, NODE1_Fan3, ...):

  Rec_X   input   the recording, delivered by the replay driver
  Sim_X   state   the fault inputs (switch ports and switches, so far)
  X       state   what the device reports: identity from the walk read
                  through the manifest (`naut snmp read`), the recorded
                  members bent by the faults (plant.st); the stand-ins serve
                  it through the monitoring project's own manifest
"""
import argparse, datetime, gzip, hashlib, json, os, re, subprocess

HERE = os.path.dirname(os.path.abspath(__file__))

ap = argparse.ArgumentParser()
ap.add_argument("--dataset", required=True)
ap.add_argument("--monitor", required=True)
ap.add_argument("--at", default="2026-09-28T18:00Z")
ap.add_argument("--naut", default="naut")
ap.add_argument("--walk", default="{tag}.snmpwalk", help="walk file name under <dataset>/snmp, {tag} = sw1")
ap.add_argument("--mockup", default="{tag}", help="mockup dir under <dataset>/redfish, {tag} = node1")
args = ap.parse_args()


def manifest_tags(path):
    """(name, type, source) in manifest order: the monitoring project's device list."""
    out = []
    for line in open(path):
        m = re.match(r"- name: (\S+)", line)
        if m:
            out.append([m.group(1), None, None])
        m = re.match(r"  (type|source): (\S+)", line)
        if m and out:
            out[-1][1 if m.group(1) == "type" else 2] = m.group(2)
    return out


snmp_tags = manifest_tags(os.path.join(args.monitor, "snmp_manifest.yaml"))
redfish_tags = manifest_tags(os.path.join(args.monitor, "redfish_manifest.yaml"))
switches = [n for n, t, _ in snmp_tags if t == "Switch"]
servers = [n for n, t, _ in redfish_tags if t == "Server"]
node_tags = [n for n, _, _ in redfish_tags]
ports = [n for n, t, _ in snmp_tags if t == "SwitchPort"]
types = {n: t for n, t, _ in snmp_tags + redfish_tags}

h = json.load(gzip.open(os.path.join(args.dataset, "history.json.gz")))
recorded = sorted({k.split(".")[0] for k in h["series"]} & set(types), key=list(types).index)
at = datetime.datetime.strptime(args.at, "%Y-%m-%dT%H:%MZ").replace(tzinfo=datetime.timezone.utc).timestamp()
idx = min(max(0, round((at - h["t0"]) / h["step"])), h["n"] - 1)
stamp = datetime.datetime.fromtimestamp(h["t0"] + idx * h["step"], datetime.timezone.utc).strftime("%Y-%m-%dT%H:%MZ")

# Identity: each switch's walk read forwards through the monitor's manifest.
walks = {}
for sw in switches:
    walk = os.path.join(args.dataset, "snmp", args.walk.format(tag=sw.lower()))
    out = subprocess.run([args.naut, "snmp", "read", "--walk", walk, "--source", sw,
                          "--manifest", os.path.join(args.monitor, "snmp_manifest.yaml")],
                         check=True, capture_output=True, text=True).stdout
    walks.update(json.loads(out))
for node in servers:
    mock = os.path.join(args.dataset, "redfish", args.mockup.format(tag=node.lower()))
    out = subprocess.run([args.naut, "redfish", "read", "--mockup", mock, "--source", node,
                          "--manifest", os.path.join(args.monitor, "redfish_manifest.yaml")],
                         check=True, capture_output=True, text=True).stdout
    walks.update(json.loads(out))


# This project is public: no hostnames, no serials (docs: sourcing). Models
# and port names are the commodity hardware's own and stay.
def mac(n):
    """A locally administered MAC, fixed per tag: 02:xx:xx:xx:xx:xx."""
    h = hashlib.sha256(n.encode()).hexdigest()
    return "02:" + ":".join(h[i:i + 2].upper() for i in range(0, 10, 2))


SCRUB = {
    "Switch": lambda n: {"Name": n.lower(), "Serial": f"SIM-{n}"},
    "Server": lambda n: {"Serial": f"SIM-{n}"},
    "Drive": lambda n: {"Serial": f"SIM-{n}"},
    "NetPort": lambda n: {"MAC": mac(n)},
}


def snapshot(name):
    """The device at --at: identity from the walk, recorded members from history."""
    v = dict(walks[name])
    v.update(SCRUB.get(types[name], lambda n: {})(name))
    for k in list(v):
        s = h["series"].get(f"{name}.{k}")
        if s and s[idx] is not None:
            v[k] = bool(s[idx]) if f"{name}.{k}" in h["bool"] else s[idx]
    return v


def lit(v):
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, str):
        return json.dumps(v)
    if isinstance(v, float) and v == int(v) and abs(v) < 1e15:
        return f"{v:.1f}"
    return repr(v)


def init(d):
    return "{ " + ", ".join(f"{k}: {lit(v)}" for k, v in d.items()) + " }"


head = "# Generated by gen.py from the recorded cluster. Re-run it; do not edit.\n"

# replay_manifest.yaml: every recorded tag whose contract type exists.
rm = [head, "# The replay driver's recording: ./data is a link to the dataset (not committed).",
      "history: data/history.json.gz", "tags:"]
for n in recorded:
    rm.append(f"  - {{ name: Rec_{n}, type: {types[n]}, series: {n} }}")
open(os.path.join(HERE, "replay_manifest.yaml"), "w").write("\n".join(rm) + "\n")

rec = [head + "# The recording, one input per recorded device tag (driver: replay)."]
for n in recorded:
    rec.append(f"- {{ name: Rec_{n}, role: input, type: {types[n]}, init: {{}}, desc: \"{n} as recorded\" }}")
open(os.path.join(HERE, "tags", "recorded.yaml"), "w").write("\n".join(rec) + "\n")

# ── the fault inputs: one Sim_ tag per part that can fail, one per cable ──

SIM_OF = {"Switch": "SimSwitch", "SwitchPort": "SimPort", "Server": "SimServer", "Fan": "SimFan",
          "PSU": "SimPSU", "Drive": "SimDrive", "NetPort": "SimNetPort"}


def end_tag(ep):
    """A topology end ("sw1/te0/25", "node1/nicSlot2p2") as its tag, or None."""
    dev, _, port = ep.partition("/")
    m = re.match(r"(sw|node)(\d+)$", dev)
    if not m or not port:
        return None
    root = f"{m.group(1).upper()}{m.group(2)}"
    if m.group(1) == "sw":
        n = re.search(r"(\d+)$", port)
        return f"{root}_Port{int(n.group(1)):02d}" if n else None
    if port == "bmc":
        return f"{root}_Nic_BMC"
    m2 = re.match(r"nicSlot(\d+)p(\d+)$", port) or re.match(r"lan(\d+)()$", port)
    if not m2:
        return None
    return f"{root}_Nic_Slot{m2.group(1)}_P{m2.group(2)}" if m2.group(2) else f"{root}_Nic_LAN{m2.group(1)}"


topo = json.load(open(os.path.join(args.dataset, "topology.json")))
cables = []  # (name, a, b)
for l in topo["links"]:
    a, b = end_tag(l["a"]), end_tag(l["b"])
    if not a or not b or a not in types or b not in types:
        continue
    if types[b] == "SwitchPort" and types[a] != "SwitchPort":
        a, b = b, a  # the switch end names the cable
    cables.append((f"Cable_{a}", a, b))
peer = {}
for name, a, b in cables:
    peer[a], peer[b] = name, name

faultable = [n for n in switches + ports + node_tags if types[n] in SIM_OF]
fl = [head + "# Every fault input of the plant: write any of them, from the HMI, curl or a test's given:.",
      "# They compose; ClearFaults (or Scenario 'normal') clears them all.",
      "- { name: Scenario, role: state, init: \"normal\", desc: \"A named preset of faults (scenarios.st); normal clears them all\" }",
      "- { name: ClearFaults, role: state, init: false, desc: \"A pulse: clear every fault input\" }",
      "- { name: Sim_Room, role: state, type: SimRoom, desc: \"The room (SIMULATION)\" }"]
for n in faultable:
    fl.append(f"- {{ name: Sim_{n}, role: state, type: {SIM_OF[types[n]]}, desc: \"{n} fault inputs (SIMULATION)\" }}")
for name, a, b in cables:
    fl.append(f"- {{ name: Sim_{name}, role: state, type: SimCable, desc: \"The cable {a} to {b} (SIMULATION)\" }}")
open(os.path.join(HERE, "tags", "faults.yaml"), "w").write("\n".join(fl) + "\n")

sw = [head + f"# The switches as the plant reports them, starting at the recording's {stamp}."]
for n in switches + ports:
    sw.append(f"- {{ name: {n}, role: state, type: {types[n]}, init: {init(snapshot(n))}, desc: \"{n} as the plant reports it\" }}")
open(os.path.join(HERE, "tags", "switches.yaml"), "w").write("\n".join(sw) + "\n")

nd = [head + f"# The servers and their parts as the plant reports them, starting at the recording's {stamp}."]
for n in node_tags:
    nd.append(f"- {{ name: {n}, role: state, type: {types[n]}, init: {init(snapshot(n))}, desc: \"{n} as the plant reports it\" }}")
open(os.path.join(HERE, "tags", "nodes.yaml"), "w").write("\n".join(nd) + "\n")

# ── plant.st ──────────────────────────────────────────────────────────

# How far each sensor sits from the CPU's heat: the share of the CPU's rise
# it sees (lib/server.st). The inlet sees only the room.
def weight(sensor):
    s = sensor.split("_Temp_", 1)[1]
    for pat, w in (("^CPU$", 1.0), ("^CPU_VRM", 0.7), ("^DIMM", 0.4), ("^Inlet", 0.0), ("^AOC_NIC", 0.2)):
        if re.match(pat, s):
            return w
    return 0.3


def children(node, typ):
    return [n for n in node_tags if n.startswith(node + "_") and types[n] == typ]


ext, fbs, body = [], [], []


def var(n):
    r = f" Rec_{n} : {types[n]};" if n in recorded else ""
    sim = f" Sim_{n} : {SIM_OF[types[n]]};" if n in faultable else ""
    ext.append(f"  {n} : {types[n]};{sim}{r}")


for n in switches + ports + node_tags:
    var(n)
for name, _, _ in cables:
    ext.append(f"  Sim_{name} : SimCable;")
    fbs.append(f"  {name} : BOOL;")


def rec(n):
    return f"Rec_{n}" if n in recorded else n  # a part the recording lacks is its own baseline


def end_faults(t):
    """What takes this end of a cable down, besides the cable itself."""
    root = t.split("_")[0]
    if types[t] == "SwitchPort":
        return f"Sim_{t}.Down OR Sim_{t}.AdminDown OR {root}_M.Down"
    return f"Sim_{t}.Down OR {root}_M.Off"


body.append("  IF ClearFaults THEN")
body.append("    Sim_Room.InletDeltaC := 0.0;")
clear = {"SimSwitch": ["Dark", "Reboot"], "SimPort": ["Down", "AdminDown", "SpeedMbps", "ErrorRate"],
         "SimServer": ["Dark", "PowerOff", "CpuLoad"], "SimFan": ["Fail"], "SimPSU": ["InputLost", "Fail"],
         "SimDrive": ["Pulled", "Failing"], "SimNetPort": ["Down"]}
for n in faultable:
    for m in clear[SIM_OF[types[n]]]:
        zero = "0.0" if m in ("SpeedMbps", "ErrorRate", "CpuLoad") else "FALSE"
        body.append(f"    Sim_{n}.{m} := {zero};")
for name, _, _ in cables:
    body.append(f"    Sim_{name}.Pulled := FALSE;")
body.append("    ClearFaults := FALSE;")
body.append("  END_IF;")

body.append("  (* the chassis first: dark, off, rebooting, the thermal model *)")
for s in switches:
    fbs.append(f"  {s}_M : SwitchPlant;")
    body.append(f"  {s}_M(Dt := PlantDtS, F := Sim_{s}, P := {s});")
for node in servers:
    fans = children(node, "Fan")
    cpu = [t for t in children(node, "TempSensor") if t.endswith("_Temp_CPU")]
    fbs.append(f"  {node}_M : ServerPlant;")
    failed = " + ".join(f"BOOL_TO_INT(Sim_{f}.Fail)" for f in fans) or "0"
    rise = f"{cpu[0]}_M.Rise" if cpu else "0.0"
    body.append(f"  {node}_M(Rec := {rec(node)}, F := Sim_{node}, Room := Sim_Room, FansFailed := {failed}, CpuRiseNow := {rise}, Live := Live, P := {node});")

body.append("  (* every cable, from both ends' faults: down at one end is down at both *)")
for name, a, b in cables:
    body.append(f"  {name} := NOT (Sim_{name}.Pulled OR {end_faults(a)} OR {end_faults(b)});")

body.append("  (* the switches: the recording bent by the faults, lib/switch.st *)")
for s in switches:
    body.append(f"  {s}.PortsUp := 0;")
    for p in [p for p in ports if p.startswith(s + "_")]:
        fbs.append(f"  {p}_M : PortPlant;")
        link = f", LinkOk := {peer[p]}" if p in peer else ""
        body.append(f"  {p}_M(Rec := {rec(p)}, F := Sim_{p}, Dark := {s}_M.Down, Live := Live{link}, P := {p});")
        body.append(f"  IF {p}.OperUp THEN {s}.PortsUp := {s}.PortsUp + 1; END_IF;")

body.append("  (* the servers: lib/server.st *)")
for node in servers:
    m = f"{node}_M"
    temps = children(node, "TempSensor")
    for f in children(node, "Fan"):
        fbs.append(f"  {f}_M : FanPlant;")
        body.append(f"  {f}_M(Rec := {rec(f)}, F := Sim_{f}, Boost := {m}.FanBoost, Off := {m}.Off, Live := Live, P := {f});")
    for t in temps:
        fbs.append(f"  {t}_M : TempPlant;")
        body.append(f"  {t}_M(Rec := {rec(t)}, RiseTarget := {m}.RiseTarget, Weight := {weight(t)}, RoomC := Sim_Room.InletDeltaC, "
                    f"InletC := {node}.InletTempC, Off := {m}.Off, Dt := PlantDtS, Live := Live, P := {t});")
    if temps:
        mx = temps[0] + ".Value"
        for t in temps[1:]:
            mx = f"MAX({mx}, {t}.Value)"
        body.append(f"  {node}.MaxTempC := {mx};")
    psus = children(node, "PSU")
    if len(psus) == 2:
        fbs.append(f"  {node}_Psu_M : PsuPair;")
        a, b = psus
        body.append(f"  {node}_Psu_M(Rec1 := {rec(a)}, Rec2 := {rec(b)}, F1 := Sim_{a}, F2 := Sim_{b}, LoadW := {m}.LoadW, Live := Live, P1 := {a}, P2 := {b});")
    for d in children(node, "Drive"):
        fbs.append(f"  {d}_M : DrivePlant;")
        body.append(f"  {d}_M(F := Sim_{d}, P := {d});")
    for nic in children(node, "NetPort"):
        fbs.append(f"  {nic}_M : NicPlant;")
        ok = peer.get(nic, f"NOT (Sim_{nic}.Down OR {m}.Off)")
        speed = "0.0"
        far = next((y if x == nic else x for c, x, y in cables if c == peer.get(nic)), None)
        if far and types[far] == "SwitchPort":
            speed = f"Sim_{far}.SpeedMbps / 1000.0"
        body.append(f"  {nic}_M(Rec := {rec(nic)}, LinkOk := {ok}, SpeedGbps := {speed}, Live := Live, P := {nic});")

nl = "\n"
st = f"""(* plant.st: generated by gen.py; the logic is lib/switch.st and
   lib/server.st, the named scenarios scenarios.st. Do not edit.
   Every device tag is its recorded baseline, Rec_..., bent by its fault
   inputs, Sim_...: write Sim_SW1_Port25.Down := TRUE and SW1_Port25 loses
   link, and so does the server port at the other end of its cable, on the
   wire of the stand-ins that serve them. *)
PROGRAM Plant
VAR_EXTERNAL
  (* the replay clock: the HMI steers the outputs, the driver reports At *)
  Replay_At : DINT; Replay_Speed : REAL; Replay_Pause : BOOL; Replay_SeekS : DINT;
  PlantDtS : REAL; ClearFaults : BOOL; Sim_Room : SimRoom;
{nl.join(ext)}
END_VAR
VAR
  Live : BOOL;
{nl.join(fbs)}
END_VAR
  Live := Replay_At > 0;
{nl.join(body)}
END_PROGRAM
"""
open(os.path.join(HERE, "plant.st"), "w").write(st)

link = os.path.join(HERE, "data")
if not os.path.lexists(link):
    os.symlink(os.path.abspath(args.dataset), link)
print(f"{len(switches)} switches, {len(ports)} ports, {len(recorded)} recorded tags; snapshot at {stamp}")
