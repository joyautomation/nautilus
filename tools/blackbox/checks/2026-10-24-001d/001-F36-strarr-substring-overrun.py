"""F-36 (bug): on an array of Strings, a substring range that runs past the end of some elements is
answered Bad_IndexRangeNoData for the whole read. Part 4 v1.05.07 §7.27: "Arrays of ByteString and String
values are treated as two dimensional arrays where the final index specifies the substring range within
the ByteString or String value ... If the last element of the specified IndexRange refers to a substring,
the Server shall return partial results accordingly. For any element in the array where the lower and
upper bound of the specified IndexRange for the substring are out of bounds, the Server shall return a
null or empty value." So on StrArr = ['ab', 'xyz']: '0:1,1:5' is ['b', 'yz']; '1,0:9' is ['xyz'];
'0:1,2' is [null or '', 'z']. On the build this was written on all three are Bad_IndexRangeNoData. May be
one with F-35. Sets StrArr through POST /api/tags and restores ['a', 'b']. Project: SUT_PROJECT
(evidence/typed). Oracle: asyncua + Part 4 §7.27."""
import os, sys, json, asyncio, urllib.request
from asyncua import Client, ua
EP, HTTP = os.environ["SUT_OPCUA"], os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def post(name, value):
    req = urllib.request.Request(HTTP + "/api/tags", data=json.dumps({"name": name, "value": value}).encode(), headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req, timeout=3) as r: return r.status
async def rd(c, n, rng):
    rv = ua.ReadValueId(); rv.NodeId = ua.NodeId.from_string(n); rv.AttributeId = ua.AttributeIds.Value; rv.IndexRange = rng
    p = ua.ReadParameters(); p.NodesToRead = [rv]
    r = (await c.uaclient.read(p))[0]; return r.StatusCode.name, (r.Value.Value if r.StatusCode.is_good() else None)
async def main():
    post("StrArr", ["ab", "xyz"]); await asyncio.sleep(0.4)
    try:
        async with Client(EP, timeout=8) as c:
            st, v = await rd(c, "ns=1;s=StrArr", ""); ok(v == ["ab", "xyz"], f"StrArr reads ['ab', 'xyz'] (precondition; got {st} {v!r})")
            st, v = await rd(c, "ns=1;s=StrArr", "0:1,1:5"); ok(st == "Good" and v == ["b", "yz"], f"'0:1,1:5' is ['b', 'yz'] (got {st} {v!r})")
            st, v = await rd(c, "ns=1;s=StrArr", "1,0:9");   ok(st == "Good" and v == ["xyz"], f"'1,0:9' is ['xyz'] (got {st} {v!r})")
            st, v = await rd(c, "ns=1;s=StrArr", "0:1,2")
            ok(st == "Good" and isinstance(v, list) and len(v) == 2 and v[0] in (None, "") and v[1] == "z", f"'0:1,2' is [null or '', 'z'] (got {st} {v!r})")
    finally:
        post("StrArr", ["a", "b"])
asyncio.run(main()); sys.exit(1 if F else 0)
