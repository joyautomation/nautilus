"""Pass (release: 10 clients for a few seconds): ten concurrent sessions each with five
monitored items all receive Counter at the scan rate; a client killed with SIGKILL mid-
subscription leaves the others, the scan loop and the HTTP API untouched, and a new client
can connect afterwards. Project: evidence/typed. Oracle: asyncua; the HTTP API for the scan
count (nautilus itself)."""
import asyncio, os, sys, json, subprocess, time, urllib.request, signal
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
TAGS = ["Counter", "Ramp", "Blink", "Level", "Int1"]
VICTIM = f"""
import asyncio
from asyncua import Client
class H:
    def datachange_notification(self, n, v, d): pass
async def main():
    async with Client({EP!r}) as c:
        s = await c.create_subscription(100, H()); await s.subscribe_data_change([c.get_node('ns=1;s='+t) for t in {TAGS!r}])
        await asyncio.sleep(60)
asyncio.run(main())
"""
class H:
    def __init__(self): self.n = 0
    def datachange_notification(self, node, val, data):
        if node.nodeid.Identifier == "Counter": self.n += 1
def counter(): return json.load(urllib.request.urlopen(HTTP + "/api/state"))["tags"]["Counter"]
async def one(i):
    async with Client(EP) as c:
        h = H(); s = await c.create_subscription(50, h); await s.subscribe_data_change([c.get_node(f"ns=1;s={t}") for t in TAGS])
        await asyncio.sleep(0.3); h.n = 0; await asyncio.sleep(2.0); await s.delete(); return h.n
async def main():
    victim = subprocess.Popen([sys.executable, "-I", "-c", VICTIM]); await asyncio.sleep(1.5)
    counts = await asyncio.gather(*[one(i) for i in range(10)])
    ok(all(16 <= n <= 24 for n in counts), f"10 clients x 5 items: Counter notifications in 2 s per client {counts}")
    c0 = counter(); victim.send_signal(signal.SIGKILL); victim.wait()
    print("INFO  victim client SIGKILLed")
    counts = await asyncio.gather(*[one(i) for i in range(3)])
    ok(all(16 <= n <= 24 for n in counts), f"after SIGKILL of a subscribed client, 3 clients still receive {counts}")
    c1 = counter(); ok(c1 > c0 + 15, f"scan loop advanced {c0} -> {c1} across the kill")
    async with Client(EP) as c: ok((await c.get_node("ns=1;s=Level").read_value()) == 1.5, "a new client connects and reads")
asyncio.run(main()); sys.exit(1 if F else 0)
