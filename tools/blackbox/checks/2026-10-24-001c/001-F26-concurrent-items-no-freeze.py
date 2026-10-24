"""F-26: concurrent monitored-item creation must not freeze the controller (Part 4 §5.12; guide "the controller runs regardless"). The freeze was seen once in run 001b and never on demand; this is the strongest attempt made at it, as a check. The fix note names
the first-value send for a new item (project tag or Server-object node) as the lock holder, so this
aims at exactly that: N clients each repeatedly creating/deleting monitored items on BOTH project tags
and Server-object Variables (ServerStatus, CurrentTime, State, NamespaceArray, ...) while Counter
changes every sweep, at a safe 100 ms publishing interval, with tiny queues so channels fill.
Liveness oracle: HTTP Counter (nautilus itself). Addresses from SUT_OPCUA/SUT_HTTP; 20 clients x 30 rounds (~60 s) -> `release`. Exit 1 if the HTTP API stops answering (freeze or crash). Project: SUT_PROJECT (evidence/typed), read-only."""
import asyncio, sys, os, time, json, urllib.request
from asyncua import Client, ua
EP, HTTP = os.environ["SUT_OPCUA"], os.environ["SUT_HTTP"]; N, ROUNDS = 20, 30
SERVER_NODES = [2256, 2258, 2259, 2255, 2260, 2261, 2262, 2263, 2264, 2265, 2266, 2267, 2992, 2993, 2994, 2267, 2254, 2257]
def counter():
    try: return json.load(urllib.request.urlopen(HTTP + "/api/state", timeout=2)).get("tags", {}).get("Counter")
    except Exception as e: return f"ERR:{type(e).__name__}"
class H:
    def __init__(self): self.n = 0
    def datachange_notification(self, node, val, data): self.n += 1
async def one(i):
    try:
        async with Client(EP, timeout=8) as c:
            folder = [k for k in await c.get_node(85).get_children() if k.nodeid.NamespaceIndex == 1][0]
            kids = await folder.get_children()
            nodes = [c.get_node(ua.NodeId(n)) for n in SERVER_NODES] + kids
            h = H(); sub = await c.create_subscription(100, h)
            for r in range(4):
                rot = nodes[(i + r) % len(nodes):] + nodes[:(i + r) % len(nodes)]
                items = await sub.subscribe_data_change(rot, queuesize=1)
                await asyncio.sleep(0.02 * (i % 5))
                try: await sub.unsubscribe(items)
                except Exception: pass
            await sub.delete()
            return h.n
    except Exception as e: return f"{type(e).__name__}"
async def main():
    print(f"deadlock2 {N} clients x {len(SERVER_NODES)}+project items, {ROUNDS} rounds, pub 100; Counter={counter()}")
    t0 = time.time()
    for r in range(ROUNDS):
        res = await asyncio.gather(*[one(i) for i in range(N)], return_exceptions=True)
        c = counter()
        if isinstance(c, str):
            await asyncio.sleep(1.5); c2 = counter()
            if isinstance(c2, str): print(f"FAIL  round {r}: HTTP down ({c}, {c2}) -> FREEZE or CRASH; results {res}"); sys.exit(1)
        if r % 10 == 0: print(f"   round {r}: Counter={c} results={res[:3]}... {time.time()-t0:.0f}s")
    c = counter(); print(f"{'PASS' if isinstance(c, int) else 'FAIL'}  survived {ROUNDS} rounds in {time.time()-t0:.0f}s; Counter={c}"); sys.exit(0 if isinstance(c, int) else 1)
asyncio.run(main())
