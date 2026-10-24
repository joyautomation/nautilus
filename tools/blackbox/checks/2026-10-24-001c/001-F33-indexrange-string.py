"""F-33 (papercut): an IndexRange on a String value is answered Bad_IndexRangeNoData. Part 4 §7.22
NumericRange treats String and ByteString values as one-dimensional arrays of characters/bytes, so
IndexRange '0:1' on Str1 = 'hello' selects 'he', and for an array of strings a second dimension selects
characters ('1,0' on ['a','b'] is 'b'). On the build this was written on, every range on Str1 is
Bad_IndexRangeNoData and '1,0' on StrArr is Bad_IndexRangeNoData; the array-level slice ('1' -> ['b'])
works. Project: SUT_PROJECT (evidence/typed), read-only. Oracle: asyncua + Part 4 §7.22."""
import os, sys, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def nid(s): return ua.NodeId.from_string(s)
async def rd(c, n, rng):
    rv = ua.ReadValueId(); rv.NodeId = nid(n); rv.AttributeId = ua.AttributeIds.Value; rv.IndexRange = rng
    p = ua.ReadParameters(); p.NodesToRead = [rv]; p.TimestampsToReturn = ua.TimestampsToReturn.Both
    r = (await c.uaclient.read(p))[0]; v = r.Value.Value if r.StatusCode.is_good() else None
    print(f"   {n:<14} IndexRange={rng!r:<6} -> {r.StatusCode.name} {v if v is not None else ''}"); return r.StatusCode.name, v
async def main():
    async with Client(EP, timeout=8) as c:
        s, _ = await rd(c, "ns=1;s=Str1", ""); print(f"   Str1 = {_!r}")
        ok(_ == "hello", "Str1 reads 'hello' (precondition)")
        st, v = await rd(c, "ns=1;s=Str1", "0:1"); ok(st == "Good" and v == "he", f"IndexRange '0:1' on Str1 returns 'he' (got {st} {v!r})")
        st, v = await rd(c, "ns=1;s=Str1", "4");   ok(st == "Good" and v == "o", f"IndexRange '4' on Str1 returns 'o' (got {st} {v!r})")
        st, v = await rd(c, "ns=1;s=Str1", "5");   ok(st == "BadIndexRangeNoData", f"IndexRange '5' on Str1 (past the end) is Bad_IndexRangeNoData (got {st})")
        st, v = await rd(c, "ns=1;s=StrArr", "1"); ok(st == "Good" and v == ["b"], f"IndexRange '1' on StrArr returns ['b'] (got {st} {v!r})")
        st, v = await rd(c, "ns=1;s=StrArr", "1,0"); ok(st == "Good" and v == ["b"], f"IndexRange '1,0' on StrArr returns ['b'] — element 1, character 0 (got {st} {v!r})")
asyncio.run(main()); sys.exit(1 if F else 0)
