#!/usr/bin/env python3
"""sfc_compare.py [--assoc-order] <built.sfc> <reference.sfc> — is the gesture-built chart
the reference, modulo layout?

Both files go through `naut sfc graph` (the render model the editor draws
from), so formatting, comments, `(* @layout *)` pins and declaration order
in the file do not count. What does:

  - the program name and every header declaration (name, type, init, section)
  - the steps, which one is initial, and each step's associations
    (qualifier, target, time) — as a multiset; with --assoc-order, in order
  - the transitions as (FROM set, TO set, condition) — a multiset
  - alternative-branch PRIORITY: for every two transitions that share a
    source step, which one is declared first
  - every ACTION body, whitespace-normalised

Prints one line per difference; exit 0 when there are none.
"""
import json, re, subprocess, sys

def graph(path):
    out = subprocess.run(["naut", "sfc", "graph", path], capture_output=True, text=True)
    if out.returncode != 0:
        sys.exit(f"naut sfc graph {path}: {out.stderr.strip()}")
    return json.loads(out.stdout)

ws = lambda s: re.sub(r"\s+", " ", (s or "").strip())

ORDER = "--assoc-order" in sys.argv
args = [a for a in sys.argv[1:] if not a.startswith("--")]

def model(g):
    trans = [(tuple(sorted(t["from"])), tuple(sorted(t["to"])), ws(t["cond"])) for t in g["trans"]]
    prio = set()
    for i, a in enumerate(trans):
        for b in trans[i + 1:]:
            if set(a[0]) & set(b[0]):
                prio.add((a, b))   # a is declared before b, and they share a source
    return {
        "name": g["name"],
        "vars": sorted((v["name"], v["type"], v.get("init", ""), v["section"]) for v in g["vars"]),
        "steps": {s["name"]: (s.get("initial", False),
                              (lambda l: l if ORDER else sorted(l))(
                                  [(a["qualifier"], a["target"], a.get("time", "")) for a in s.get("actions") or []]))
                  for s in g["steps"]},
        "trans": sorted(trans),
        "prio": prio,
        "actions": {a["name"]: ws(a["body"]) for a in g.get("actions") or []},
    }

built, ref = model(graph(args[0])), model(graph(args[1]))
diffs = []
if built["name"] != ref["name"]:
    diffs.append(f"program name: built {built['name']!r}, reference {ref['name']!r}")
for v in sorted(set(ref["vars"]) - set(built["vars"])): diffs.append(f"var missing: {v}")
for v in sorted(set(built["vars"]) - set(ref["vars"])): diffs.append(f"var extra:   {v}")
for n in sorted(set(ref["steps"]) | set(built["steps"])):
    r, b = ref["steps"].get(n), built["steps"].get(n)
    if r != b: diffs.append(f"step {n}: built {b}, reference {r}")
rt, bt = list(ref["trans"]), list(built["trans"])
for t in list(rt):
    if t in bt: bt.remove(t); rt.remove(t)
for t in rt: diffs.append(f"transition missing: FROM {t[0]} TO {t[1]} := {t[2]}")
for t in bt: diffs.append(f"transition extra:   FROM {t[0]} TO {t[1]} := {t[2]}")
for a, b in sorted(ref["prio"] - built["prio"]):
    diffs.append(f"priority: reference declares {a[0]}->{a[1]} before {b[0]}->{b[1]}; the build does not")
for n in sorted(set(ref["actions"]) | set(built["actions"])):
    r, b = ref["actions"].get(n), built["actions"].get(n)
    if r != b: diffs.append(f"ACTION {n}: built {b!r}, reference {r!r}")
print("\n".join(diffs) if diffs else "same chart as the reference (modulo layout)")
sys.exit(1 if diffs else 0)
