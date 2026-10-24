"""F-20: RevisedSamplingInterval tells the truth. Part 4 §5.12.1.2: the server revises a
requested sampling interval to the closest rate it supports, never faster than it can
sample; MinimumSamplingInterval (the guide: "reports the sweep", 100 ms) is the floor. A
request for 1000 ms on a 10 ms subscription must not come back as 10. Project: evidence/typed.
Oracle: asyncua + Part 4. Creates and deletes subscriptions."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
class H:
    def datachange_notification(self, node, val, data): pass
async def main():
    async with Client(EP) as c:
        msi = (await c.get_node("ns=1;s=Str1").read_attribute(ua.AttributeIds.MinimumSamplingInterval)).Value.Value
        print(f"MinimumSamplingInterval(Str1) = {msi}")
        for pub in (10, 500):
            sub = await c.create_subscription(pub, H())
            for req in (0, 1000, -1):
                p = ua.CreateMonitoredItemsParameters(); p.SubscriptionId = sub.subscription_id; p.TimestampsToReturn = ua.TimestampsToReturn.Both
                r = ua.MonitoredItemCreateRequest(); r.ItemToMonitor = ua.ReadValueId(NodeId=ua.NodeId.from_string("ns=1;s=Str1"), AttributeId=ua.AttributeIds.Value)
                r.MonitoringMode = ua.MonitoringMode.Reporting; r.RequestedParameters = ua.MonitoringParameters(ClientHandle=1, SamplingInterval=req, QueueSize=1)
                p.ItemsToCreate = [r]; res = (await c.uaclient.create_monitored_items(p))[0]
                rv = res.RevisedSamplingInterval
                if req == 0: ok(rv >= msi, f"pub {pub} ms, sampling requested 0 (fastest) -> revised {rv} (>= MinimumSamplingInterval {msi})")
                elif req == -1: ok(rv >= msi, f"pub {pub} ms, sampling requested -1 (= publishing) -> revised {rv} (>= {msi})")
                else: ok(rv >= msi and abs(rv - req) <= 0.5 * req, f"pub {pub} ms, sampling requested {req} -> revised {rv} (a supported rate near the request, >= {msi})")
            await sub.delete()
asyncio.run(main()); sys.exit(1 if F else 0)
