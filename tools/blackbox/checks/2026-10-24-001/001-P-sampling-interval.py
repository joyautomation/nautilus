"""Pass: MinimumSamplingInterval is 100 (the sweep) on every tag Variable, as the guide says
(Subscriptions). The revised sampling interval is F-20's check. Project: evidence/typed. Oracle: asyncua + guide
(Subscriptions). Creates and deletes one subscription."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
class H:
    def datachange_notification(self, node, val, data): pass
async def walk(n, out):
    for k in await n.get_children():
        if await k.read_node_class() == ua.NodeClass.Variable: out.append(k)
        else: await walk(k, out)
async def main():
    async with Client(EP) as c:
        folder = [n for n in await c.get_objects_node().get_children() if n.nodeid.NamespaceIndex != 0][0]
        vs = []; await walk(folder, vs)
        msi = {v.nodeid.to_string(): (await v.read_attribute(ua.AttributeIds.MinimumSamplingInterval)).Value.Value for v in vs}
        ok(set(msi.values()) == {100}, f"MinimumSamplingInterval on {len(vs)} Variables: {set(msi.values())}")
        sub = await c.create_subscription(50, H())
        p = ua.CreateMonitoredItemsParameters(); p.SubscriptionId = sub.subscription_id; p.TimestampsToReturn = ua.TimestampsToReturn.Both
        r = ua.MonitoredItemCreateRequest(); r.ItemToMonitor = ua.ReadValueId(NodeId=ua.NodeId.from_string("ns=1;s=Counter"), AttributeId=ua.AttributeIds.Value)
        r.MonitoringMode = ua.MonitoringMode.Reporting; r.RequestedParameters = ua.MonitoringParameters(ClientHandle=1, SamplingInterval=0, QueueSize=1)
        p.ItemsToCreate = [r]
        res = (await c.uaclient.create_monitored_items(p))[0]
        print(f"INFO  requested 0 ms sampling on a 50 ms subscription -> {res.StatusCode.name} revised={res.RevisedSamplingInterval} (see 001-F20)")
        await sub.delete()
asyncio.run(main()); sys.exit(1 if F else 0)
