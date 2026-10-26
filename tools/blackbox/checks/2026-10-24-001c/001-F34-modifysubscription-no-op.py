"""F-34 (papercut): ModifySubscription answers Good but changes nothing — a request to slow a live
100 ms subscription to 500 ms is answered RevisedPublishingInterval = 100 and the subscription keeps
publishing every 100 ms, although 500 ms is a rate the server grants at CreateSubscription. Part 4
§5.13.3: the service modifies the subscription, revising the request only to a value the server
supports. The guide (71e700c) says it plainly: a ModifySubscription "is answered with the values the
subscription has". May be one with F-31 (nautilus#431: a client cannot throttle a subscription).
Fails while the service is a no-op; passes when a supported rate is granted and applied.
Project: SUT_PROJECT (evidence/typed), read-only. Oracle: asyncua + Part 4."""
import os, sys, time, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
class H:
    def __init__(self): self.n = []
    def datachange_notification(self, node, val, data): self.n.append(time.time())
async def main():
    async with Client(EP, timeout=8) as c:
        p = ua.CreateSubscriptionParameters(); p.RequestedPublishingInterval = 500; p.RequestedLifetimeCount = 1000; p.RequestedMaxKeepAliveCount = 10; p.MaxNotificationsPerPublish = 0; p.PublishingEnabled = True; p.Priority = 0
        r = await c.uaclient.create_subscription(p, callback=lambda *a: None); print(f"   CreateSubscription(500) -> revised {r.RevisedPublishingInterval}  (500 is a rate the server grants)")
        ok(r.RevisedPublishingInterval == 500.0, "precondition: CreateSubscription grants 500 ms"); await c.uaclient.delete_subscriptions([r.SubscriptionId])
        h = H(); sub = await c.create_subscription(100, h); await sub.subscribe_data_change(c.get_node("ns=1;s=Counter"))
        await asyncio.sleep(1.0); n0 = len(h.n); print(f"   subscription at 100 ms: {n0} notifications in 1 s")
        m = ua.ModifySubscriptionParameters(); m.SubscriptionId = sub.subscription_id; m.RequestedPublishingInterval = 500; m.RequestedLifetimeCount = 1000; m.RequestedMaxKeepAliveCount = 10; m.MaxNotificationsPerPublish = 0; m.Priority = 0
        try:
            r = await c.uaclient.update_subscription(m); print(f"   ModifySubscription(500) -> RevisedPublishingInterval={r.RevisedPublishingInterval}")
            ok(r.RevisedPublishingInterval == 500.0, f"ModifySubscription to 500 ms is granted (got {r.RevisedPublishingInterval})")
        except ua.UaStatusCodeError as e: ok(False, f"ModifySubscription(500) -> {ua.StatusCode(e.code).name}")
        h.n.clear(); await asyncio.sleep(2.0); n1 = len(h.n); gaps = sorted(round(b - a, 3) for a, b in zip(h.n, h.n[1:]))
        print(f"   after it: {n1} notifications in 2 s; median gap {gaps[len(gaps)//2] if gaps else None} s")
        ok(n1 <= 5, f"the subscription now publishes about every 500 ms (got {n1} notifications in 2 s; at 100 ms it would be ~20)")
        await sub.delete()
asyncio.run(main()); sys.exit(1 if F else 0)
