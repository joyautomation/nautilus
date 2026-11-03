"""pass: drive the running pump station through the documented HTTP surface and see it react.

Project: evidence/pumps, served by `naut run` at SUT_HTTP. Sets its own state:
forces LevelPct (POST /api/forces, the forcing guide) and writes setpoints (POST /api/tags).
Checks: lead starts at 65 %, lag joins at 85 %, both stop at 15 % and the lead alternates;
an enum tag takes a member name and refuses a non-member with 400; a misspelt UDT member and an
undeclared tag are 400; "T#8h" written to the TIME tag HiLevelDelay lands (28 800 000 ms) and a
string that is no duration is 422. Clears its forces and puts HiLevelDelay back to 30 s at the end.
Changed 2026-11-03 per runs/RECONCILIATION-2026-11-03.md ruling 3 (the parent expected any string
to a TIME tag, "T#8h" included, to be refused).
"""
import json, os, sys, time, urllib.request, urllib.error

BASE = os.environ["SUT_HTTP"].rstrip("/")
fails = []

def req(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    r = urllib.request.Request(BASE + path, data=data, method=method)
    try:
        with urllib.request.urlopen(r, timeout=5) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

def tags():
    return json.loads(req("GET", "/api/state")[1])["tags"]

def settle():
    time.sleep(0.5)   # five 100 ms scans

def expect(label, cond, seen):
    print(("ok   " if cond else "FAIL ") + label + "  " + json.dumps(seen))
    if not cond:
        fails.append(label)

try:
    req("POST", "/api/forces/clear")
    for n, v in (("LeadStartPct", 60), ("LagStartPct", 80), ("StopPct", 20), ("HiLevelDelay", 30000)):
        req("POST", "/api/tags", {"name": n, "value": v})
    req("POST", "/api/forces", {"name": "LevelPct", "value": 10}); settle()
    lead0 = tags()["Lead"]
    other = {"Pump1": "Pump2", "Pump2": "Pump1"}[lead0]
    leadrun, lagrun = ("P1_Run", "P2_Run") if lead0 == "Pump1" else ("P2_Run", "P1_Run")

    req("POST", "/api/forces", {"name": "LevelPct", "value": 65}); settle(); t = tags()
    expect("lead runs alone at 65 %", t[leadrun] and not t[lagrun] and t["Station"] == "LeadRunning",
           {k: t[k] for k in ("Lead", "P1_Run", "P2_Run", "Station")})
    req("POST", "/api/forces", {"name": "LevelPct", "value": 85}); settle(); t = tags()
    expect("lag joins at 85 %", t["P1_Run"] and t["P2_Run"] and t["Station"] == "BothRunning",
           {k: t[k] for k in ("P1_Run", "P2_Run", "Station")})
    c0 = t["Cycles"]
    req("POST", "/api/forces", {"name": "LevelPct", "value": 15}); settle(); t = tags()
    expect("both stop at 15 % and the lead alternates",
           not t["P1_Run"] and not t["P2_Run"] and t["Lead"] == other and t["Cycles"] == c0 + 1,
           {k: t[k] for k in ("P1_Run", "P2_Run", "Lead", "Cycles")})
    # a setpoint write changes behaviour: lower the lead start below the level
    req("POST", "/api/forces", {"name": "LevelPct", "value": 30}); settle()
    s, _ = req("POST", "/api/tags", {"name": "LeadStartPct", "value": 25}); settle(); t = tags()
    expect("setpoint write starts the new lead at 30 %", s == 204 and t["P1_Run"] != t["P2_Run"],
           {"status": s, "P1_Run": t["P1_Run"], "P2_Run": t["P2_Run"]})
    req("POST", "/api/tags", {"name": "LeadStartPct", "value": 60})
    s, b = req("POST", "/api/tags", {"name": "Lead", "value": "Pump3"})
    expect("non-member enum write is 400 naming the members", s == 400 and "Pump1" in b, {"status": s, "body": b})
    s, b = req("POST", "/api/tags", {"name": "P1_Stats.Startz", "value": 1})
    expect("misspelt member is 400", s == 400 and "Starts" in b, {"status": s, "body": b})
    s, b = req("POST", "/api/tags", {"name": "Nope", "value": 1})
    expect("undeclared tag is 400", s == 400, {"status": s, "body": b})
    # a TIME tag takes an IEC literal and reads back in milliseconds (/guides/http-api/ "TIME values";
    # /guides/first-program/ "A TIME reads back as milliseconds"); a string that is no duration is
    # the wrong kind of value for a whole tag, 422 (/guides/http-api/ status table, /guides/tag-model/)
    s, b = req("POST", "/api/tags", {"name": "HiLevelDelay", "value": "T#8h"}); settle(); t = tags()
    expect("T#8h to a TIME tag lands as 8 h in milliseconds", s == 204 and t["HiLevelDelay"] == 8 * 3600 * 1000,
           {"status": s, "body": b, "HiLevelDelay": t["HiLevelDelay"]})
    s, b = req("POST", "/api/tags", {"name": "HiLevelDelay", "value": "eight hours"}); t = tags()
    expect("a non-duration string to a TIME tag is 422 and leaves the value", s == 422 and t["HiLevelDelay"] == 8 * 3600 * 1000,
           {"status": s, "body": b, "HiLevelDelay": t["HiLevelDelay"]})
    req("POST", "/api/tags", {"name": "HiLevelDelay", "value": 30000})
    meta = json.loads(req("GET", "/api/meta")[1])
    expect("/api/meta lists the enum members", [m["name"] for m in meta["tags"]["Lead"]["enum"]] == ["Pump1", "Pump2"],
           meta["tags"]["Lead"])
finally:
    req("POST", "/api/forces/clear")
    req("POST", "/api/tags", {"name": "HiLevelDelay", "value": 30000})

sys.exit(1 if fails else 0)
