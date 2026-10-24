"""Pass (F-25 edges): CreateSubscription revises every RequestedPublishingInterval to a rate the server
supports and never to 0 — Part 4 §5.13.2.2. Requests of 0, 1, 50, 99, -1 and NaN are revised to the
100 ms sweep; 100..3600000 are granted as asked; anything slower than an hour is clamped to an hour.
The controller stays alive through all of them (Counter advances). Project: SUT_PROJECT (evidence/typed).
Oracle: asyncua + Part 4; the HTTP scan counter (nautilus itself) is the liveness witness."""
import os, sys, json, asyncio, urllib.request, math
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def counter():
    try: return json.load(urllib.request.urlopen(HTTP + "/api/state", timeout=2)).get("tags", {}).get("Counter")
    except Exception as e: return f"ERR:{type(e).__name__}"
async def main():
    c0 = counter()
    async with Client(EP, timeout=8) as c:
        for req, want in [(0, 100.0), (1, 100.0), (50, 100.0), (99, 100.0), (-1, 100.0), (float("nan"), 100.0), (100, 100.0), (150, 150.0), (250, 250.0), (1000, 1000.0), (3600000, 3600000.0), (3600001, 3600000.0), (1e9, 3600000.0)]:
            p = ua.CreateSubscriptionParameters(); p.RequestedPublishingInterval = req; p.RequestedLifetimeCount = 1000; p.RequestedMaxKeepAliveCount = 10; p.MaxNotificationsPerPublish = 0; p.PublishingEnabled = True; p.Priority = 0
            r = await c.uaclient.create_subscription(p, callback=lambda *a: None)
            print(f"   request {req!r:>12} -> revised {r.RevisedPublishingInterval}")
            ok(r.RevisedPublishingInterval == want and r.RevisedPublishingInterval > 0, f"request {req!r} revised to {want} (got {r.RevisedPublishingInterval})")
            try: await c.uaclient.delete_subscriptions([r.SubscriptionId])
            except Exception: pass
    await asyncio.sleep(0.5); c1 = counter()
    ok(isinstance(c0, int) and isinstance(c1, int) and c1 > c0, f"controller alive throughout (Counter {c0} -> {c1})")
asyncio.run(main()); sys.exit(1 if F else 0)
