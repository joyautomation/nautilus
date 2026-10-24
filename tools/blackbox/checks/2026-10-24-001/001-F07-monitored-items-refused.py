"""F-07: CreateMonitoredItems answers per item with the code a Read of it gives
(Part 4 §5.12.2; the guide's Subscriptions section): Bad_NodeIdUnknown for an id that is
not a node, Bad_AttributeIdInvalid for Value on an Object or the folder; Good for a
Variable. Project: evidence/typed. Oracle: asyncua + Part 4. Creates and deletes one
subscription."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
class H:
    def datachange_notification(self, node, val, data): pass
async def main():
    async with Client(EP) as c:
        objs = c.get_objects_node()
        folder = [n for n in await objs.get_children() if n.nodeid.NamespaceIndex != 0][0]
        sub = await c.create_subscription(100, H())
        want = {"ns=1;s=Level": "Good", "ns=1;s=P101": "BadAttributeIdInvalid", folder.nodeid.to_string(): "BadAttributeIdInvalid",
                "ns=1;s=Nope": "BadNodeIdUnknown", "ns=2;s=Level": "BadNodeIdUnknown", "ns=1;s=Pumps": "BadAttributeIdInvalid"}
        p = ua.CreateMonitoredItemsParameters(); p.SubscriptionId = sub.subscription_id; p.TimestampsToReturn = ua.TimestampsToReturn.Both
        items = []
        for i, nid in enumerate(want):
            r = ua.MonitoredItemCreateRequest(); r.ItemToMonitor = ua.ReadValueId(NodeId=ua.NodeId.from_string(nid), AttributeId=ua.AttributeIds.Value)
            r.MonitoringMode = ua.MonitoringMode.Reporting; r.RequestedParameters = ua.MonitoringParameters(ClientHandle=i + 1, SamplingInterval=0, QueueSize=1)
            items.append(r)
        p.ItemsToCreate = items
        res = await c.uaclient.create_monitored_items(p)
        for nid, r in zip(want, res):
            ok(r.StatusCode.name == want[nid], f"CreateMonitoredItems {nid}: {r.StatusCode.name} (0x{r.StatusCode.value:08X}) id={r.MonitoredItemId} revised={r.RevisedSamplingInterval}; want {want[nid]}")
        await sub.delete()
asyncio.run(main()); sys.exit(1 if F else 0)
