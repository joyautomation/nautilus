"""F-13 (filed nautilus#422): CloseSession with deleteSubscriptions=true deletes the
session's subscriptions (Part 4 §5.6.4); the driver row's counts return to their previous
values. Project: evidence/typed. Oracle: asyncua + Part 4; counts from GET /api/drivers
(nautilus itself, marked weaker)."""
import asyncio, os, sys, json, urllib.request
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def counts():
    d = json.load(urllib.request.urlopen(HTTP + "/api/drivers")); rows = d if isinstance(d, list) else d.get("drivers", d)
    r = [r for r in rows if r.get("kind") == "opcua-server"][0]; m = {x["label"]: x["value"] for x in r.get("metrics", [])}
    s = [v for k, v in m.items() if "subscription" in k.lower()][0]; i = [v for k, v in m.items() if "monitored" in k.lower()][0]
    return s, i
class H:
    def datachange_notification(self, node, val, data): pass
async def main():
    s0 = counts(); print("before:", s0)
    c = Client(EP); await c.connect()
    sub = await c.create_subscription(100, H())
    await sub.subscribe_data_change([c.get_node(f"ns=1;s={t}") for t in ["Counter", "Ramp", "Blink"]])
    await asyncio.sleep(0.5); s1 = counts(); ok(s1 == (s0[0] + 1, s0[1] + 3), f"subscribed (3 items): {s1}")
    await c.close_session()   # asyncua sends deleteSubscriptions=True
    await c.close_secure_channel(); c.disconnect_socket()
    await asyncio.sleep(1.0); s2 = counts()
    ok(s2 == s0, f"after CloseSession(deleteSubscriptions=true): {s2} (want {s0})")
    if s2 != s0:
        await asyncio.sleep(5); print("INFO  5 s later:", counts())
asyncio.run(main()); sys.exit(1 if F else 0)
