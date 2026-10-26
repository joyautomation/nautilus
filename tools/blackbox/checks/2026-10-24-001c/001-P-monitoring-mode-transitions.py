"""Pass (F-27 edges): the monitoring mode is honoured at creation and on every transition — Part 4
§5.12.4 Table: Disabled neither samples nor reports; Sampling samples but does not report; Reporting
reports. An item created Disabled on Counter (changes every sweep) is silent; Sampling is silent;
Reporting resumes and the first notification is the current value within a sweep; a Reporting item
on an idle tag that is Disabled and re-enabled hears the current value once. SetMonitoringMode and
DeleteMonitoredItems with an id that is not an item answer Bad_MonitoredItemIdInvalid for that id,
Good for the real one, and the controller survives. Project: SUT_PROJECT (evidence/typed), read-only.
Oracle: asyncua + Part 4."""
import os, sys, json, time, asyncio, urllib.request
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def counter():
    try: return json.load(urllib.request.urlopen(HTTP + "/api/state", timeout=2)).get("tags", {}).get("Counter")
    except Exception as e: return f"ERR:{type(e).__name__}"
class H:
    def __init__(self): self.n = []
    def datachange_notification(self, node, val, data): self.n.append((time.time(), val))
async def main():
    async with Client(EP, timeout=8) as c:
        h = H(); sub = await c.create_subscription(100, h)
        mi = await sub.subscribe_data_change(c.get_node("ns=1;s=Counter"), monitoring=ua.MonitoringMode.Disabled)
        await asyncio.sleep(1.0); print(f"   created Disabled: {len(h.n)} notifications in 1 s")
        ok(len(h.n) == 0, f"an item created Disabled reports nothing (got {len(h.n)})")
        r = await sub.set_monitoring_mode(ua.MonitoringMode.Sampling); h.n.clear(); await asyncio.sleep(1.0)
        print(f"   SetMonitoringMode(Sampling) -> {[x.name for x in r]}; {len(h.n)} notifications in 1 s")
        ok(r[0].is_good() and len(h.n) == 0, f"Sampling answers Good and reports nothing (got {len(h.n)})")
        t = time.time(); r = await sub.set_monitoring_mode(ua.MonitoringMode.Reporting); h.n.clear(); await asyncio.sleep(1.0)
        first = (h.n[0][0] - t) if h.n else None
        print(f"   SetMonitoringMode(Reporting) -> {[x.name for x in r]}; {len(h.n)} notifications in 1 s; first after {first and round(first, 3)} s")
        ok(r[0].is_good() and len(h.n) >= 7, f"Reporting resumes delivery (got {len(h.n)} in 1 s; Counter changes every 100 ms)")
        ok(first is not None and first < 0.35, f"the first notification after Reporting arrives within a sweep or two (got {first})")
        h2 = H(); sub2 = await c.create_subscription(100, h2); await sub2.subscribe_data_change(c.get_node("ns=1;s=Fixed")); await asyncio.sleep(0.5)
        ok(len(h2.n) == 1, f"idle tag Fixed: one initial notification (got {len(h2.n)})")
        await sub2.set_monitoring_mode(ua.MonitoringMode.Disabled); h2.n.clear(); await asyncio.sleep(0.5)
        ok(len(h2.n) == 0, f"idle tag Disabled: silent (got {len(h2.n)})")
        await sub2.set_monitoring_mode(ua.MonitoringMode.Reporting); await asyncio.sleep(0.5)
        print(f"   idle tag Disabled->Reporting: {len(h2.n)} notifications, values {[v for _, v in h2.n]}")
        ok(len(h2.n) == 1, f"idle tag re-enabled hears the current value exactly once (got {len(h2.n)})")
        p = ua.SetMonitoringModeParameters(); p.SubscriptionId = sub.subscription_id; p.MonitoringMode = ua.MonitoringMode.Disabled; p.MonitoredItemIds = [999999, mi]
        r = await c.uaclient.set_monitoring_mode(p); print(f"   SetMonitoringMode(ids=[999999, {mi}]) -> {[x.name for x in r]}")
        ok([x.name for x in r] == ["BadMonitoredItemIdInvalid", "Good"], "a bogus id is Bad_MonitoredItemIdInvalid in its own result; the real one Good")
        r = await c.uaclient.delete_monitored_items(ua.DeleteMonitoredItemsParameters(SubscriptionId=sub.subscription_id, MonitoredItemIds=[999999, mi]))
        print(f"   DeleteMonitoredItems(ids=[999999, {mi}]) -> {[x.name for x in r]}")
        ok([x.name for x in r] == ["BadMonitoredItemIdInvalid", "Good"], "DeleteMonitoredItems: bogus id Bad_MonitoredItemIdInvalid, the real one Good")
        await sub.delete(); await sub2.delete()
    c1 = counter(); ok(isinstance(c1, int), f"controller alive after the bogus ids (Counter={c1})")
asyncio.run(main()); sys.exit(1 if F else 0)
