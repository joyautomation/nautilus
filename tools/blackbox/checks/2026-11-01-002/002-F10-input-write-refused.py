"""F-10: getting-started says inputs are read-only: "the driver rewrites an input before every scan
... so an edit there would be discarded within a scan"; the dashboard says "inputs and outputs are
owned by the driver and the logic". POST /api/tags to the `role: input` tag LevelPct (memory driver)
should be refused, or discarded within a scan. Today it is 204 and the value sticks and drives the
logic. Passes when the write is refused (4xx) or LevelPct is back to its prior value 0.5 s later.
Project: evidence/pumps at SUT_HTTP. Clears forces first; leaves LevelPct as it found it if it can.
"""
import json, os, sys, time, urllib.request, urllib.error
BASE = os.environ["SUT_HTTP"].rstrip("/")
def req(m, p, b=None):
    r = urllib.request.Request(BASE + p, data=None if b is None else json.dumps(b).encode(), method=m)
    try:
        with urllib.request.urlopen(r, timeout=5) as x: return x.status, x.read().decode()
    except urllib.error.HTTPError as e: return e.code, e.read().decode()
level = lambda: json.loads(req("GET", "/api/state")[1])["tags"]["LevelPct"]
req("POST", "/api/forces/clear"); time.sleep(0.3)
before = level(); target = 42.5 if before != 42.5 else 41.5
s, b = req("POST", "/api/tags", {"name": "LevelPct", "value": target}); time.sleep(0.5); after = level()
print(f"before={before} POST {target} -> {s} {b.strip()!r}; 0.5 s later LevelPct={after}")
if 400 <= s < 500 or after == before:
    print("PASS"); sys.exit(0)
req("POST", "/api/tags", {"name": "LevelPct", "value": before})
print("FAIL write to a role: input tag accepted and kept"); sys.exit(1)
