"""F-29: a single Browse of the Server object (i=2253) must not return the same (target, reference
type) more than once. Part 4 §5.8.2 / Part 3: Browse returns the set of references of a node; a
client builds its tree from them and duplicate references corrupt it (UAExpert shows a node
twice). On this build the forward Hierarchical browse of i=2253 returns many duplicate pairs.
Judged by the spec; the Server object is the stack's own, so this is in the gopcua family of
F-04. Project: SUT_PROJECT. Oracle: asyncua + Part 4."""
import os, sys, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def s(x): return x.to_string() if hasattr(x, "to_string") else str(x)
async def browse_dups(c, start):
    p = ua.BrowseParameters(); p.RequestedMaxReferencesPerNode = 0
    d = ua.BrowseDescription(); d.NodeId = ua.NodeId(start); d.BrowseDirection = ua.BrowseDirection.Forward
    d.ReferenceTypeId = ua.NodeId(ua.ObjectIds.HierarchicalReferences); d.IncludeSubtypes = True; d.NodeClassMask = 0; d.ResultMask = 63
    p.NodesToBrowse = [d]; res = (await c.uaclient.browse(p))[0]
    pairs = [(s(r.NodeId), s(r.ReferenceTypeId)) for r in res.References]
    dup = {x: pairs.count(x) for x in set(pairs) if pairs.count(x) > 1}
    return len(res.References), len(set(p[0] for p in pairs)), dup
async def main():
    async with Client(EP, timeout=8) as c:
        for start, name in [(2253, "Server"), (2268, "ServerCapabilities"), (2274, "ServerDiagnostics")]:
            total, distinct, dup = await browse_dups(c, start)
            print(f"   i={start} {name}: {total} references, {distinct} distinct targets, {len(dup)} duplicated (target,reftype) pairs")
            ok(len(dup) == 0, f"Browse of i={start} ({name}) returns no duplicate references")
asyncio.run(main()); sys.exit(1 if F else 0)
