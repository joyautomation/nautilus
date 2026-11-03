"""pass (wall clock): the high-level alarm's on-delay holds on a running controller.

Project: evidence/pumps at SUT_HTTP. Sets HiLevelDelay to 2000 ms (POST /api/tags), forces
LevelPct to 95 (POST /api/forces), and checks HiLevelAlm is FALSE at 1.5 s and TRUE by 2.6 s;
a write to a forced tag is 409. Restores the delay and clears forces.
"""
import json, os, sys, time, urllib.request, urllib.error
BASE = os.environ["SUT_HTTP"].rstrip("/")
def req(m, p, b=None):
    r = urllib.request.Request(BASE + p, data=None if b is None else json.dumps(b).encode(), method=m)
    try:
        with urllib.request.urlopen(r, timeout=5) as x: return x.status, x.read().decode()
    except urllib.error.HTTPError as e: return e.code, e.read().decode()
alm = lambda: json.loads(req("GET", "/api/state")[1])["tags"]["HiLevelAlm"]
fails = []
try:
    req("POST", "/api/forces/clear"); req("POST", "/api/forces", {"name": "LevelPct", "value": 10})
    req("POST", "/api/tags", {"name": "HiLevelDelay", "value": 2000}); time.sleep(0.5)
    req("POST", "/api/forces", {"name": "LevelPct", "value": 95}); t0 = time.time()
    time.sleep(1.5); a1 = alm(); time.sleep(1.1); a2 = alm()
    print(f"HiLevelAlm at {1.5}s={a1}, at {time.time()-t0:.1f}s={a2}")
    if a1 or not a2: fails.append("delay")
    s, b = req("POST", "/api/tags", {"name": "LevelPct", "value": 10})
    print("write to forced tag:", s, b.strip())
    if s != 409: fails.append("forced write")
finally:
    req("POST", "/api/forces/clear"); req("POST", "/api/tags", {"name": "HiLevelDelay", "value": 30000})
print("FAIL " + ",".join(fails) if fails else "PASS"); sys.exit(1 if fails else 0)
