"""F-27: SetMonitoringMode(Disabled) must stop an item sampling and reporting (Part 4 §5.12.4:
"Disabled - The item ... shall not sample or ... report Notifications"). On this build the call
answers Good but the item keeps delivering. Project: SUT_PROJECT (needs a tag that changes every
scan; uses Counter). Oracle: asyncua + Part 4."""
import os, sys, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
class H:
    def __init__(self): self.n = 0
    def datachange_notification(self, node, val, data): self.n += 1
async def main():
    async with Client(EP, timeout=8) as c:
        h = H(); sub = await c.create_subscription(100, h); mi = await sub.subscribe_data_change(c.get_node("ns=1;s=Counter"))
        await asyncio.sleep(1.0); before = h.n
        ok(before > 0, f"Reporting: {before} notifications in 1 s (item is live)")
        p = ua.SetMonitoringModeParameters(); p.SubscriptionId = sub.subscription_id; p.MonitoringMode = ua.MonitoringMode.Disabled; p.MonitoredItemIds = [mi]
        res = await c.uaclient.set_monitoring_mode(p); results = [r.name for r in (res.Results if hasattr(res, "Results") else res)]
        print(f"   SetMonitoringMode(Disabled) -> {results}")
        k = h.n; await asyncio.sleep(1.5); after = h.n - k
        ok(after == 0, f"after Disabled: {after} notifications in 1.5 s (Part 4 requires 0)")
        await sub.delete()
asyncio.run(main()); sys.exit(1 if F else 0)
