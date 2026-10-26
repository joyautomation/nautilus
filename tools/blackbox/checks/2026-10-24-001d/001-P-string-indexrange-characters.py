"""Pass (F-33 edges): IndexRange on String values counts characters, as the guide (840cb84) says ("A STRING
is an array of characters, so 0:3 on one is its first four, and an array of strings takes a second
dimension"); Part 4 §7.27 allows substrings but does not say bytes or characters, so the guide judges the
unit. Asserts on Str1 = 'héllo wörld': '1' -> 'é', '0:1' -> 'hé', '7' -> 'ö', '10' -> 'd', '11' NoData; on
'日本語': '1:2' -> '本語', '3' NoData; on '': '0' NoData; on StrArr ['日本', 'xyz']: '0,1' -> ['本'], '0:1,1'
-> ['本', 'y']; NamespaceArray (i=2255) '0,0:3' -> ['http']; and a range on a number, boolean, TIME, enum or
LINT scalar is Bad_IndexRangeNoData (guide: "a range on a number or a boolean"). Sets Str1 and StrArr
through POST /api/tags and restores them. Project: SUT_PROJECT (evidence/typed). Oracle: asyncua + the guide."""
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
async def cases(c, node, rows):
    for rng, want in rows:
        st, v = await rd(c, node, rng)
        good = (st == want) if isinstance(want, str) and want.startswith("Bad") else (st == "Good" and v == want)
        ok(good, f"{node} {rng!r} -> {want!r} (got {st} {v!r})")
async def main():
    try:
        async with Client(EP, timeout=8) as c:
            post("Str1", "héllo wörld"); await asyncio.sleep(0.4)
            await cases(c, "ns=1;s=Str1", [("1", "é"), ("0:1", "hé"), ("7", "ö"), ("10", "d"), ("11", "BadIndexRangeNoData")])
            post("Str1", "日本語"); await asyncio.sleep(0.4)
            await cases(c, "ns=1;s=Str1", [("1:2", "本語"), ("3", "BadIndexRangeNoData")])
            post("Str1", ""); await asyncio.sleep(0.4)
            await cases(c, "ns=1;s=Str1", [("0", "BadIndexRangeNoData")])
            post("StrArr", ["日本", "xyz"]); await asyncio.sleep(0.4)
            await cases(c, "ns=1;s=StrArr", [("0,1", ["本"]), ("0:1,1", ["本", "y"])])
            await cases(c, "i=2255", [("0,0:3", ["http"])])
            for n in ["Level", "Flag", "Time1", "Mode1", "Lint1"]: await cases(c, f"ns=1;s={n}", [("0", "BadIndexRangeNoData")])
    finally:
        post("Str1", "hello"); post("StrArr", ["a", "b"])
asyncio.run(main()); sys.exit(1 if F else 0)
