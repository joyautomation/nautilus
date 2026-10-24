"""Pass (F-29 edges): Browse and BrowseNext over the Server object list each (target, reference type,
direction) once, and paging with RequestedMaxReferencesPerNode 1, 5 and 7 returns the same distinct set
as one unpaged Browse (Part 4 §5.8.2, §5.8.3). Forward, inverse and both directions on i=2253; forward
on i=2268 and i=2274; and a recursive HasChild walk under i=2253 finds no node with a duplicated
reference. Project: SUT_PROJECT (evidence/typed), read-only. Oracle: asyncua + Part 4."""
import os, sys, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def browse_all(c, node, direction, reftype, maxrefs):
    d = ua.BrowseDescription(); d.NodeId = ua.NodeId(node); d.BrowseDirection = direction; d.ReferenceTypeId = ua.NodeId(reftype); d.IncludeSubtypes = True; d.NodeClassMask = 0; d.ResultMask = 63
    p = ua.BrowseParameters(); p.View = ua.ViewDescription(); p.RequestedMaxReferencesPerNode = maxrefs; p.NodesToBrowse = [d]
    res = (await c.uaclient.browse(p))[0]; refs = list(res.References); cp = res.ContinuationPoint; pages = 1
    while cp and pages < 200:
        bp = ua.BrowseNextParameters(); bp.ReleaseContinuationPoints = False; bp.ContinuationPoints = [cp]
        nxt = (await c.uaclient.browse_next(bp))[0]; refs += list(nxt.References); cp = nxt.ContinuationPoint; pages += 1
    return res.StatusCode.name, [(r.NodeId.to_string(), r.ReferenceTypeId.to_string(), r.IsForward) for r in refs], pages
async def main():
    async with Client(EP, timeout=8) as c:
        for node, direction in [(2253, ua.BrowseDirection.Forward), (2253, ua.BrowseDirection.Inverse), (2253, ua.BrowseDirection.Both), (2268, ua.BrowseDirection.Forward), (2274, ua.BrowseDirection.Forward)]:
            st, full, _ = await browse_all(c, node, direction, 31, 0)
            print(f"   i={node} {direction.name}: {st}, {len(full)} references, {len(set(full))} distinct")
            ok(st == "Good" and len(full) == len(set(full)) and len(full) > 0, f"i={node} {direction.name}: no duplicate references in one Browse")
            for m in [1, 5, 7]:
                st, paged, pages = await browse_all(c, node, direction, 31, m)
                ok(st == "Good" and paged == full, f"i={node} {direction.name} paged by {m} ({pages} pages): same list, same order, no duplicates")
        dup = 0; stack = [2253]; visited = set()
        while stack:
            n = stack.pop()
            if n in visited: continue
            visited.add(n)
            st, refs, _ = await browse_all(c, n, ua.BrowseDirection.Forward, 33, 0)
            pairs = [(a, b) for a, b, _ in refs]
            if len(pairs) != len(set(pairs)): dup += 1; print(f"   DUP under i={n}: {len(pairs)} refs, {len(set(pairs))} distinct")
            for a, _, _ in refs:
                nid = ua.NodeId.from_string(a)
                if nid.NamespaceIndex == 0 and isinstance(nid.Identifier, int): stack.append(nid.Identifier)
        print(f"   recursive HasChild walk under i=2253: {len(visited)} nodes")
        ok(dup == 0 and len(visited) > 100, f"no node under Server has a duplicated reference ({dup} of {len(visited)})")
asyncio.run(main()); sys.exit(1 if F else 0)
