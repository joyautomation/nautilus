"""Pass (F-32 edges): a monitored item's IndexRange means what Read's does (guide, 840cb84: "A monitored
item's IndexRange means the same: the item hears its slice, and only when the slice changes"; Part 4
§5.12.2, §7.27). Asserts: a range on a REAL or BOOL scalar is Bad_IndexRangeNoData at creation; '2:1', '-1',
'1:1' are Bad_IndexRangeInvalid; a String item '0:1' starts with 'he', is silent when only character 4
changes and hears 'je' when character 0 does; three items on RealArr ('0', '1', '2:3') in one subscription
each hear only a write that changes their own slice; an item created Disabled and set Reporting hears the
slice, not the array; NamespaceArray (i=2255) '0' is the one-element slice. Sets RealArr and Str1 through
POST /api/tags and restores them. Project: SUT_PROJECT (evidence/typed). Oracle: asyncua + Part 4."""
import os, sys, json, asyncio, urllib.request
from asyncua import Client, Node, ua
from asyncua.common.subscription import SubscriptionItemData
EP, HTTP = os.environ["SUT_OPCUA"], os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def post(name, value):
    req = urllib.request.Request(HTTP + "/api/tags", data=json.dumps({"name": name, "value": value}).encode(), headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req, timeout=3) as r: return r.status
class H:
    def __init__(self): self.n = []
    def datachange_notification(self, node, val, data): self.n.append((data.monitored_item.ClientHandle, val))
    def of(self, k): return [v for h, v in self.n if h == k]
async def create(sub, node, rng, handle, mode=ua.MonitoringMode.Reporting):
    rv = ua.ReadValueId(); rv.NodeId = ua.NodeId.from_string(node); rv.AttributeId = ua.AttributeIds.Value; rv.IndexRange = rng
    req = ua.MonitoredItemCreateRequest(); req.ItemToMonitor = rv; req.MonitoringMode = mode
    req.RequestedParameters = ua.MonitoringParameters(ClientHandle=handle, SamplingInterval=100, QueueSize=1, DiscardOldest=True)
    p = ua.CreateMonitoredItemsParameters(); p.SubscriptionId = sub.subscription_id; p.TimestampsToReturn = ua.TimestampsToReturn.Both; p.ItemsToCreate = [req]
    r = (await sub.server.create_monitored_items(p))[0]
    if r.StatusCode.is_good():
        d = SubscriptionItemData(); d.node = Node(sub.server, rv.NodeId); d.client_handle = handle; d.server_handle = r.MonitoredItemId
        d.attribute = ua.AttributeIds.Value; sub._monitored_items[handle] = d
    return r
async def main():
    post("RealArr", [10.0, 20.0, 0.0, 0.0]); post("Str1", "hello"); await asyncio.sleep(0.4)
    try:
        async with Client(EP, timeout=8) as c:
            h = H(); sub = await c.create_subscription(100, h)
            for k, (node, rng, want) in enumerate([("ns=1;s=Level", "0", "BadIndexRangeNoData"), ("ns=1;s=Flag", "0", "BadIndexRangeNoData"),
                                                   ("ns=1;s=RealArr", "2:1", "BadIndexRangeInvalid"), ("ns=1;s=RealArr", "-1", "BadIndexRangeInvalid"),
                                                   ("ns=1;s=RealArr", "1:1", "BadIndexRangeInvalid")], start=100):
                r = await create(sub, node, rng, k); ok(r.StatusCode.name == want, f"CreateMonitoredItems {node} {rng!r} -> {want} (got {r.StatusCode.name})")
            r = await create(sub, "i=2255", "0", 1); await asyncio.sleep(0.4)
            ok(r.StatusCode.is_good() and h.of(1)[:1] == [["http://opcfoundation.org/UA/"]], f"NamespaceArray '0' is created and told ['http://opcfoundation.org/UA/'] (got {r.StatusCode.name} {h.of(1)[:1]})")
            r = await create(sub, "ns=1;s=Str1", "0:1", 2); await asyncio.sleep(0.4)
            ok(r.StatusCode.is_good() and h.of(2) == ["he"], f"Str1 '0:1' is created and told 'he' (got {r.StatusCode.name} {h.of(2)})"); h.n.clear()
            post("Str1", "hellx"); await asyncio.sleep(0.6); ok(h.of(2) == [], f"Str1 -> 'hellx' (outside the slice): item silent (heard {h.of(2)})"); h.n.clear()
            post("Str1", "jellx"); await asyncio.sleep(0.6); ok(h.of(2) == ["je"], f"Str1 -> 'jellx': item hears 'je' (heard {h.of(2)})"); h.n.clear()
            for k, rng in [(10, "0"), (11, "1"), (12, "2:3")]: await create(sub, "ns=1;s=RealArr", rng, k)
            await asyncio.sleep(0.5)
            ok(sorted((k, v) for k, v in h.n if k >= 10) == [(10, [10.0]), (11, [20.0]), (12, [0.0, 0.0])], f"RealArr items '0','1','2:3' start with their slices (got {sorted((k, v) for k, v in h.n if k >= 10)})"); h.n.clear()
            for arr, want in [([10.0, 22.0, 0.0, 0.0], [(11, [22.0])]), ([12.0, 22.0, 0.0, 0.0], [(10, [12.0])]), ([12.0, 22.0, 5.0, 0.0], [(12, [5.0, 0.0])])]:
                post("RealArr", arr); await asyncio.sleep(0.6)
                got = sorted((k, v) for k, v in h.n if k >= 10); ok(got == want, f"POST RealArr={arr}: only {want} heard (got {got})"); h.n.clear()
            r = await create(sub, "ns=1;s=RealArr", "1", 20, ua.MonitoringMode.Disabled); await asyncio.sleep(0.4)
            ok(r.StatusCode.is_good() and h.of(20) == [], f"RealArr '1' created Disabled is silent (got {r.StatusCode.name} {h.of(20)})")
            p = ua.SetMonitoringModeParameters(); p.SubscriptionId = sub.subscription_id; p.MonitoringMode = ua.MonitoringMode.Reporting; p.MonitoredItemIds = [r.MonitoredItemId]
            await c.uaclient.set_monitoring_mode(p); await asyncio.sleep(0.5)
            ok(h.of(20) == [[22.0]], f"set Reporting, it hears the slice [22.0] (got {h.of(20)})")
            await sub.delete()
    finally:
        post("Str1", "hello"); post("RealArr", [10.0, 20.0, 0.0, 0.0])
asyncio.run(main()); sys.exit(1 if F else 0)
