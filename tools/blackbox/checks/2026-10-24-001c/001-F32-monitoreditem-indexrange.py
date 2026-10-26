"""F-32: CreateMonitoredItems ignores the ItemToMonitor.IndexRange that Read honours. Part 4 §5.12.2
CreateMonitoredItems takes a ReadValueId (§7.24) whose IndexRange selects the array elements the item
covers, exactly as in Read (§5.10.2); the data change notifications carry the slice, and an index past
the end is Bad_IndexRangeNoData at creation. On the build this was written on, Read honours IndexRange
(F-28 verified) but a monitored item on `RealArr` with IndexRange '1' answers Good and is notified with
the whole array, and '9' (out of range) and 'abc' (malformed) are accepted as Good.
Project: SUT_PROJECT (evidence/typed). Writes RealArr through the HTTP tag API and restores it.
Oracle: asyncua + Part 4."""
import os, sys, json, time, asyncio, urllib.request
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def nid(s): return ua.NodeId.from_string(s)
def post(name, value):
    req = urllib.request.Request(HTTP + "/api/tags", data=json.dumps({"name": name, "value": value}).encode(), headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req, timeout=3) as r: return r.status
class H:
    def __init__(self): self.n = []
    def datachange_notification(self, node, val, data): self.n.append(val)
async def mk(sub, rng):
    rv = ua.ReadValueId(); rv.NodeId = nid("ns=1;s=RealArr"); rv.AttributeId = ua.AttributeIds.Value; rv.IndexRange = rng
    req = ua.MonitoredItemCreateRequest(); req.ItemToMonitor = rv; req.MonitoringMode = ua.MonitoringMode.Reporting
    req.RequestedParameters = ua.MonitoringParameters(ClientHandle=0, SamplingInterval=100, QueueSize=1, DiscardOldest=True)
    r = (await sub.create_monitored_items([req]))[0]
    return r if isinstance(r, int) else None, (None if isinstance(r, int) else (r.name if hasattr(r, "name") else str(r)))
async def main():
    post("RealArr", [10.0, 20.0, 0.0, 0.0]); await asyncio.sleep(0.3)
    async with Client(EP, timeout=8) as c:
        rv = ua.ReadValueId(); rv.NodeId = nid("ns=1;s=RealArr"); rv.AttributeId = ua.AttributeIds.Value; rv.IndexRange = "1"
        p = ua.ReadParameters(); p.NodesToRead = [rv]; p.TimestampsToReturn = ua.TimestampsToReturn.Both
        r = (await c.uaclient.read(p))[0]; print(f"   Read(RealArr, IndexRange='1') -> {r.StatusCode.name} {r.Value.Value}")
        h = H(); sub = await c.create_subscription(100, h)
        mi, st = await mk(sub, "1"); await asyncio.sleep(0.5)
        print(f"   CreateMonitoredItems(RealArr, IndexRange='1') -> {'Good' if mi else st}; initial notification {h.n}")
        ok(mi is not None, "a monitored item with IndexRange '1' is created")
        ok(h.n == [[20.0]], f"its initial notification is the slice [20.0] (got {h.n})")
        h.n.clear(); post("RealArr", [10.0, 21.0, 0.0, 0.0]); await asyncio.sleep(0.5)
        print(f"   after POST RealArr=[10,21,0,0]: {h.n}")
        ok(h.n == [[21.0]], f"a change to element 1 is notified as the slice [21.0] (got {h.n})")
        if mi:
            try: await sub.unsubscribe(mi)
            except Exception: pass
        mi9, st9 = await mk(sub, "9"); await asyncio.sleep(0.3)
        print(f"   CreateMonitoredItems(RealArr, IndexRange='9') -> {'Good' if mi9 else st9}")
        ok(st9 == "BadIndexRangeNoData", f"IndexRange '9' (past the end) is refused at creation with Bad_IndexRangeNoData (got {'Good' if mi9 else st9})")
        mia, sta = await mk(sub, "abc"); await asyncio.sleep(0.3)
        print(f"   CreateMonitoredItems(RealArr, IndexRange='abc') -> {'Good' if mia else sta}")
        ok(sta == "BadIndexRangeInvalid", f"IndexRange 'abc' (malformed) is refused at creation with Bad_IndexRangeInvalid (got {'Good' if mia else sta})")
        await sub.delete()
    post("RealArr", [10.0, 20.0, 0.0, 0.0])
asyncio.run(main()); sys.exit(1 if F else 0)
