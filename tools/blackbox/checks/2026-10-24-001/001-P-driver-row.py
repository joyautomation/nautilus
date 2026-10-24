"""Pass: GET /api/drivers carries an opcua-server row with the endpoint, its state and the
subscription / monitored-item counts, and the counts follow a client: one subscription
with five items adds 1 and 5. Project: evidence/typed. Oracle: asyncua for the client side;
the row itself is nautilus's own word (marked weaker)."""
import asyncio, os, sys, json, urllib.request
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def row():
    d = json.load(urllib.request.urlopen(HTTP + "/api/drivers"))
    rows = d if isinstance(d, list) else d.get("drivers", d)
    for r in rows:
        if r.get("kind") == "opcua-server" or r.get("type") == "opcua-server": return r
    raise SystemExit(f"no opcua-server row in {json.dumps(d)[:400]}")
def counts(r):
    m = {x["label"]: x["value"] for x in r.get("metrics", [])}   # [{label, value}, ...]
    def find(key):
        for k, v in m.items():
            if key in k.lower(): return v
        return None
    return find("subscription"), find("monitored")
class H:
    def datachange_notification(self, node, val, data): pass
async def main():
    r0 = row(); print("row:", json.dumps(r0))
    ok("endpoint" in json.dumps(r0) and "state" in r0, "row has endpoint and state")
    s0, m0 = counts(r0); ok(s0 is not None and m0 is not None, f"row has subscription/monitored-item counts: {s0}/{m0}")
    async with Client(EP) as c:
        sub = await c.create_subscription(100, H())
        await sub.subscribe_data_change([c.get_node(f"ns=1;s={t}") for t in ["Counter", "Ramp", "Blink", "Level", "Int1"]])
        await asyncio.sleep(0.5)
        s1, m1 = counts(row()); ok(s1 == s0 + 1 and m1 == m0 + 5, f"with one subscription of 5 items: {s1}/{m1} (was {s0}/{m0})")
        await sub.delete(); await asyncio.sleep(0.5)
        s2, m2 = counts(row()); ok((s2, m2) == (s0, m0), f"after DeleteSubscriptions: {s2}/{m2}")
asyncio.run(main()); sys.exit(1 if F else 0)
