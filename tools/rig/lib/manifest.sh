# manifest.sh — the run's MANIFEST (schema 1): one JSON file describing a rig
# run for machines, as clips.html describes it for people. Sourced on the
# HOST by smoke/run.sh and selftest.sh (and so demo.sh); the docs site's
# proof pages (website/scripts/fetch-verified.mjs) read it out of the run's
# artifact. Contract: randd/handoffs/TEST-PLAN-PROOF.md, "Manifest (schema 1)".
#
#   rig_run_meta <part dir> <started>   write <part dir>/run.json: what this
#                                       entry point ran (sha, naut, vsix,
#                                       VS Code, date, pace, frame, runId, host).
#                                       Call it while the container is up.
#   rig_manifest <root> <part>          (re)write <root>/manifest.json from
#                                       <root>/smoke and <root>/selftest
#
# <root> is the run's RIG_OUT (tools/rig/out by default; demo.sh's is
# out/demo), <part> the folder the caller just filled (smoke | selftest).
# The manifest is rebuilt from what is on disk each time, so smoke then
# selftest into the same RIG_OUT — the nightly — leaves ONE manifest with
# both kinds of item, clip paths relative to <root> ("smoke/…", "selftest/…").
# A part is merged in only if its run.json names the same set (RIG_SET) and
# the same nautilus sha as the part just run: an out/smoke left over from an
# older build never passes for this one.
#
# Frame and pace come from <part dir>/frame.env, which the in-container side
# writes (smoke/lib.sh, selftest.sh) with the values it actually used, one
# KEY=value a line: CAP_W, CAP_H, REC_ZOOM, REC_FONT_SIZE, G_PACE.
#
# Additions to schema 1 (consumers may ignore them): run.set;
# "parts" {smoke|selftest: that part's own run block}; on smoke items "text"
# (the first FAIL, else the counts) and rows[].png; on verb items "verb" (the
# id without its "-variant" suffix: the INVENTORY's rig-verb name); a smoke
# check with only SKIP rows is "SKIP", one with no rows at all "FAIL".

# rig_run_meta <part dir> <started (ISO 8601 UTC)> — needs NAUT, VSIX,
# NAUTILUS_SHA (or the build's SHA file beside NAUT) and a running container.
rig_run_meta() {
  local dir=$1 started=$2 vscode naut
  vscode=$(rig_run "code --version 2>/dev/null" 2>/dev/null | head -1 | tr -d "\r")
  naut=$("$NAUT" version 2>/dev/null | head -1 | sed 's/^nautilus *//; s/^naut *//')
  mkdir -p "$dir"
  python3 - "$dir" "$started" "$vscode" "$naut" <<'PY'
import json, os, socket, sys, zipfile
d, started, vscode, naut = sys.argv[1:5]
env = os.environ
frame = {}
try:
    for line in open(os.path.join(d, "frame.env")):
        k, _, v = line.strip().partition("=")
        if k: frame[k] = v
except OSError:
    pass
def num(s):
    try:
        f = float(s)
        return int(f) if f.is_integer() else f
    except (TypeError, ValueError):
        return None
vsix = None
try:
    with zipfile.ZipFile(env["VSIX"]) as z:
        vsix = json.loads(z.read("extension/package.json")).get("version")
except Exception:
    pass
sha = env.get("NAUTILUS_SHA") or ""
if not sha or sha == "unknown":
    try:
        sha = open(os.path.join(os.path.dirname(env.get("NAUT", "")), "SHA")).read().strip()
    except OSError:
        sha = sha or None
part = os.path.basename(os.path.normpath(d))
meta = {
    "sha": sha, "naut": naut or None, "vsix": vsix, "vscode": vscode or None,
    "date": started, "pace": frame.get("G_PACE") or None,
    "frame": {"w": num(frame.get("CAP_W")), "h": num(frame.get("CAP_H")),
              "zoom": num(frame.get("REC_ZOOM")), "font": num(frame.get("REC_FONT_SIZE"))},
    "runId": env.get("GITHUB_RUN_ID") or "local",
    "host": socket.gethostname().split(".")[0],
    "set": env.get("RIG_SET") or "nightly", "part": part,
}
with open(os.path.join(d, "run.json"), "w") as f:
    json.dump(meta, f, indent=2)
    f.write("\n")
PY
}

rig_manifest() { # <root> <part just run>
  python3 - "$1" "$2" <<'PY'
import json, os, re, subprocess, sys
root, current = sys.argv[1], sys.argv[2]

def load(part):
    try:
        return json.load(open(os.path.join(root, part, "run.json")))
    except (OSError, ValueError):
        return None

def duration(rel):
    try:
        out = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                              "-of", "csv=p=0", os.path.join(root, rel)],
                             capture_output=True, text=True, timeout=30).stdout.strip()
        return round(float(out), 1)
    except Exception:
        return None

def rel_if(part, name):
    return f"{part}/{name}" if name and os.path.isfile(os.path.join(root, part, name)) else None

cur = load(current)
if cur is None:
    sys.exit(f"rig_manifest: no {root}/{current}/run.json")
parts = {}
for p in ("smoke", "selftest"):
    m = load(p)
    if m is None:
        continue
    if p != current and (m.get("set") != cur.get("set") or m.get("sha") != cur.get("sha")):
        print(f"  manifest: leaving out {p}/ (set {m.get('set')}, sha {m.get('sha')} — not this run)")
        continue
    parts[p] = m

items = []

if "smoke" in parts:
    d = os.path.join(root, "smoke")
    rows = []
    try:
        for line in open(os.path.join(d, "results.tsv"), encoding="utf-8", errors="replace"):
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            f = (line.split("\t") + ["", "", "", ""])[:4]
            rows.append(f)
    except OSError:
        pass
    ids = []
    for f in rows:
        if f[0] not in ids:
            ids.append(f[0])
    for name in sorted(os.listdir(d)):
        m = re.match(r"^(\d\d-.*)\.mp4$", name)
        if m and m.group(1) not in ids:
            ids.append(m.group(1))
    for cid in ids:
        mine = [f for f in rows if f[0] == cid]
        out_rows, pngs, cnt, first_fail = [], [], {}, None
        for _, v, text, png in mine:
            cnt[v] = cnt.get(v, 0) + 1
            if v == "FAIL" and first_fail is None:
                first_fail = text
            r = {"verdict": v, "text": text}
            p = rel_if("smoke", os.path.basename(png)) if png else None
            if p:
                r["png"] = p
                if p not in pngs:
                    pngs.append(p)
            out_rows.append(r)
        # Evidence the rows do not name (a check's extra snaps), after theirs.
        for name in sorted(os.listdir(d)):
            if name.startswith(cid + "-") and name.endswith(".png") and f"smoke/{name}" not in pngs:
                pngs.append(f"smoke/{name}")
        verdict = ("FAIL" if cnt.get("FAIL") else "WARN" if cnt.get("WARN") else
                   "PASS" if cnt.get("PASS") else "SKIP" if cnt.get("SKIP") else "FAIL")
        text = first_fail if first_fail is not None else (
            "%d pass, %d warn, %d skip, %d note" % tuple(cnt.get(k, 0) for k in ("PASS", "WARN", "SKIP", "NOTE"))
            if mine else "no verdict recorded")
        clip = rel_if("smoke", cid + ".mp4")
        items.append({"kind": "smoke", "id": cid, "verdict": verdict, "text": text, "rows": out_rows,
                      "clip": clip, "pngs": pngs, "durationS": duration(clip) if clip else None})

EDITORS = {"sfc_": "sfc", "ld_": "ld", "fbd_": "fbd", "mimic_": "mimic", "component_": "component"}
if "selftest" in parts:
    editor = None
    try:
        lines = open(os.path.join(root, "selftest", "verbs-selftest.tsv"), encoding="utf-8", errors="replace").read().splitlines()
    except OSError:
        lines = []
    for line in lines:
        f = (line.split("\t") + ["", "", "", ""])[:4]
        if not f[0].isdigit():
            if f[1] == "naut check":
                parts["selftest"]["nautCheck"] = f[3]
            continue
        n, vid, verdict, detail = int(f[0]), f[1], f[2], f[3]
        # The editor: the verb's prefix; ed_open_diagram-<ed> names it; a
        # shared verb (diagram_zoom) belongs to the editor open at the time.
        for pre, ed in EDITORS.items():
            if vid.startswith(pre):
                editor = ed
                break
        else:
            m = re.match(r"^ed_open_diagram-(\w+)$", vid)
            if m:
                editor = m.group(1)
        base = "verbs-%02d-%s" % (n, vid)
        clip = rel_if("selftest", base + ".mp4")
        png = rel_if("selftest", base + ".png")
        items.append({"kind": "verb", "id": vid, "verb": vid.split("-")[0], "n": n, "verdict": verdict,
                      "text": detail, "clip": clip, "pngs": [png] if png else [],
                      "durationS": duration(clip) if clip else None, "editor": editor})

run = {k: v for k, v in cur.items() if k not in ("part", "nautCheck")}
run["date"] = min(m.get("date") or "~" for m in parts.values())
manifest = {"schema": 1, "set": cur.get("set") or "nightly", "run": run, "parts": parts, "items": items}
path = os.path.join(root, "manifest.json")
with open(path + ".tmp", "w") as fh:
    json.dump(manifest, fh, indent=2, ensure_ascii=False)
    fh.write("\n")
os.replace(path + ".tmp", path)
kinds = {}
for it in items:
    kinds[it["kind"]] = kinds.get(it["kind"], 0) + 1
print(f"  manifest: {path} ({', '.join(f'{v} {k}' for k, v in kinds.items()) or 'no items'})")
PY
}
