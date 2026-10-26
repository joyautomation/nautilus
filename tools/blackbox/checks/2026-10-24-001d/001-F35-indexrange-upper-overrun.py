"""F-35 (bug): an IndexRange whose lower bound is inside the value but whose upper bound runs past the end
is answered Bad_IndexRangeNoData. Part 4 v1.05.07 §7.27 (1.04 §7.22) NumericRange: "When reading a value
and any of the lower bounds of the indexes is out of range the Server shall return a Bad_IndexRangeNoData.
If any of the upper bounds of the indexes is out of range, the Server shall return partial results."
So on RealArr = [10, 20, 0, 0]: '0:9' is all four, '2:9' is [0, 0], '3:4' is [0]; '1:5' on IntArr
[1, 2, 3] is [2, 3]; '3:20' on Str1 'hello' is 'lo'; '0:5,0' on StrArr ['ab', 'xyz'] is ['a', 'x'];
'4:9' (lower bound out) stays Bad_IndexRangeNoData. A monitored item on RealArr '0:9' is created Good and
told the four elements. On the build this was written on every overrunning range is Bad_IndexRangeNoData,
on Read and at CreateMonitoredItems. Sets RealArr, Str1 and StrArr through POST /api/tags and restores
them to the project's values. Project: SUT_PROJECT (evidence/typed). Oracle: asyncua + Part 4 §7.27."""
import os, sys, json, asyncio, urllib.request
from asyncua import Client, ua
EP, HTTP = os.environ["SUT_OPCUA"], os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def post(name, value):
    req = urllib.request.Request(HTTP + "/api/tags", data=json.dumps({"name": name, "value": value}).encode(), headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req, timeout=3) as r: return r.status
def rvid(n, rng):
    rv = ua.ReadValueId(); rv.NodeId = ua.NodeId.from_string(n); rv.AttributeId = ua.AttributeIds.Value; rv.IndexRange = rng; return rv
async def rd(c, n, rng):
    p = ua.ReadParameters(); p.NodesToRead = [rvid(n, rng)]
    r = (await c.uaclient.read(p))[0]; v = r.Value.Value if r.StatusCode.is_good() else None
    return r.StatusCode.name, v
class H:
    def __init__(self): self.n = []
    def datachange_notification(self, node, val, data): self.n.append(val)
async def main():
    post("RealArr", [10.0, 20.0, 0.0, 0.0]); post("Str1", "hello"); post("StrArr", ["ab", "xyz"]); await asyncio.sleep(0.4)
    try:
        async with Client(EP, timeout=8) as c:
            for n, rng, want in [("RealArr", "0:9", [10.0, 20.0, 0.0, 0.0]), ("RealArr", "2:9", [0.0, 0.0]), ("RealArr", "3:4", [0.0]),
                                 ("IntArr", "1:5", [2, 3]), ("Str1", "3:20", "lo"), ("StrArr", "0:5,0", ["a", "x"])]:
                st, v = await rd(c, f"ns=1;s={n}", rng)
                ok(st == "Good" and v == want, f"Read {n} IndexRange {rng!r} is the partial result {want!r} (got {st} {v!r})")
            st, v = await rd(c, "ns=1;s=RealArr", "4:9")
            ok(st == "BadIndexRangeNoData", f"Read RealArr IndexRange '4:9' (lower bound past the end) is Bad_IndexRangeNoData (got {st})")
            h = H(); sub = await c.create_subscription(100, h)
            req = ua.MonitoredItemCreateRequest(); req.ItemToMonitor = rvid("ns=1;s=RealArr", "0:9"); req.MonitoringMode = ua.MonitoringMode.Reporting
            req.RequestedParameters = ua.MonitoringParameters(ClientHandle=0, SamplingInterval=100, QueueSize=1, DiscardOldest=True)
            res = (await sub.create_monitored_items([req]))[0]; await asyncio.sleep(0.5)
            st = res.name if hasattr(res, "name") else "Good"
            ok(st == "Good" and h.n[:1] == [[10.0, 20.0, 0.0, 0.0]], f"CreateMonitoredItems RealArr '0:9' is Good and told [10, 20, 0, 0] (got {st}, {h.n[:1]})")
            await sub.delete()
    finally:
        post("Str1", "hello"); post("StrArr", ["a", "b"])
asyncio.run(main()); sys.exit(1 if F else 0)
