"""Pass: a value written through the HTTP API (POST /api/tags) reaches a subscriber within
one 100 ms sweep plus the 50 ms publishing interval, with status Good, the written value,
and a SourceTimestamp at the time of the change. Sets state: writes Level, Str1, Mode1
(by member name), IntArr and Recipe.TargetL - tags no program writes, in a UDT no program
touches - through the HTTP API and restores them.
Project: evidence/typed. Oracle: asyncua; the HTTP API is the actuator."""
import asyncio, os, sys, json, time, urllib.request, datetime
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def post(name, value):
    req = urllib.request.Request(HTTP + "/api/tags", data=json.dumps({"name": name, "value": value}).encode(), headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req) as r: return r.status
class H:
    def __init__(self): self.q = asyncio.Queue()
    def datachange_notification(self, node, val, data): self.q.put_nowait((time.monotonic(), node.nodeid.to_string(), val, data.monitored_item.Value))
async def main():
    async with Client(EP) as c:
        h = H(); sub = await c.create_subscription(50, h)
        tags = {"Level": 7.25, "Str1": "written", "Mode1": "Fault", "IntArr": [4, 5, 6], "Recipe.TargetL": 42.5}
        want = {"Level": 7.25, "Str1": "written", "Mode1": 11, "IntArr": [4, 5, 6], "Recipe.TargetL": 42.5}
        nodes = [c.get_node(f"ns=1;s={t}") for t in tags]
        await sub.subscribe_data_change(nodes)
        await asyncio.sleep(0.4)
        while not h.q.empty(): h.q.get_nowait()  # initial values
        for t, v in tags.items():
            t0 = time.monotonic(); wall = datetime.datetime.now(datetime.timezone.utc)
            st = post(t, v)
            try:
                while True:
                    got_at, nid, val, dv = await asyncio.wait_for(h.q.get(), 2.0)
                    if nid == f"ns=1;s={t}": break
                    print(f"INFO  stray notification {nid}={val!r}")
            except asyncio.TimeoutError:
                ok(False, f"{t}: POST -> {st}; no notification for {t} in 2 s"); continue
            ms = (got_at - t0) * 1000
            ok(nid == f"ns=1;s={t}" and val == want[t] and dv.StatusCode.is_good() and ms < 400, f"{t}: POST {v!r} -> {st}; notified {nid}={val!r} {dv.StatusCode.name} after {ms:.0f} ms")
            ok(abs((dv.SourceTimestamp - wall).total_seconds()) < 0.5, f"{t}: SourceTimestamp {dv.SourceTimestamp.time()} within 0.5 s of the write ({wall.time()})")
        post("Level", 1.5); post("Str1", "hello"); post("Mode1", "Run"); post("IntArr", [1, 2, 3]); post("Recipe.TargetL", 10.0)
        await sub.delete()
asyncio.run(main()); sys.exit(1 if F else 0)
