"""Pass (F-30 edges): the advertised MaxNodesPerRead is the true boundary — a Read of exactly that many
nodes succeeds, one more is Bad_TooManyOperations at the service level (Part 4 §5.10.2 / Part 5
§6.3.31). Also records which other OperationLimits are advertised (today only MaxNodesPerRead; the
rest read Bad_AttributeIdInvalid — noted, not asserted). Project: SUT_PROJECT (evidence/typed),
read-only. Oracle: asyncua + Part 4/5."""
import os, sys, asyncio, time
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def read_n(c, n):
    p = ua.ReadParameters(); p.TimestampsToReturn = ua.TimestampsToReturn.Both
    for _ in range(n):
        rv = ua.ReadValueId(); rv.NodeId = ua.NodeId.from_string("ns=1;s=Level"); rv.AttributeId = ua.AttributeIds.Value; p.NodesToRead.append(rv)
    try: r = await c.uaclient.read(p); return "Good" if all(x.StatusCode.is_good() for x in r) else "mixed", len(r)
    except ua.UaStatusCodeError as e: return ua.StatusCode(e.code).name, 0
async def main():
    async with Client(EP, timeout=30) as c:
        lim = await c.get_node(ua.NodeId(11705)).read_value(); print(f"   MaxNodesPerRead = {lim}")
        ok(isinstance(lim, int) and lim > 0, f"MaxNodesPerRead is a positive integer ({lim})")
        t = time.time(); st, n = await read_n(c, lim); print(f"   Read of {lim} nodes -> {st}, {n} results in {time.time()-t:.2f} s")
        ok(st == "Good" and n == lim, f"a Read of exactly MaxNodesPerRead ({lim}) nodes succeeds")
        st, n = await read_n(c, lim + 1); print(f"   Read of {lim+1} nodes -> {st}")
        ok(st == "BadTooManyOperations", f"a Read of MaxNodesPerRead+1 nodes is Bad_TooManyOperations (got {st})")
        for i, name in [(11712, "MaxNodesPerWrite"), (11710, "MaxNodesPerBrowse"), (11714, "MaxMonitoredItemsPerCall"), (11711, "MaxNodesPerRegisterNodes"), (11709, "MaxNodesPerTranslateBrowsePathsToNodeIds")]:
            try: v = await c.get_node(ua.NodeId(i)).read_value(); print(f"   (note) {name} = {v}")
            except Exception as e: print(f"   (note) {name}: {type(e).__name__} — not advertised")
asyncio.run(main()); sys.exit(1 if F else 0)
