"""F-28: a Read with an IndexRange must return the requested slice, and an out-of-range or a
range on a scalar must be refused (Part 4 §7.22: Bad_IndexRangeNoData / Bad_IndexRangeInvalid).
On this build IndexRange is ignored: an array read returns the whole value and a scalar range
answers Good. Project: SUT_PROJECT (uses RealArr = ARRAY[1..4] OF REAL init [10,20,0,0], and the
scalar Level). Oracle: asyncua + Part 4."""
import os, sys, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    async with Client(EP, timeout=8) as c:
        n = c.get_node("ns=1;s=RealArr"); whole = await n.read_value()
        ok(len(whole) == 4, f"RealArr whole value {whole}")
        dv = await n.read_attribute(ua.AttributeIds.Value, indexrange="0", raise_on_bad_status=False)
        v = dv.Value.Value if dv.Value else None
        ok(dv.StatusCode.is_good() and isinstance(v, list) and len(v) == 1, f"IndexRange '0' returns a 1-element slice (got {dv.StatusCode.name} {v})")
        dv = await n.read_attribute(ua.AttributeIds.Value, indexrange="1:2", raise_on_bad_status=False)
        v = dv.Value.Value if dv.Value else None
        ok(dv.StatusCode.is_good() and isinstance(v, list) and len(v) == 2, f"IndexRange '1:2' returns a 2-element slice (got {dv.StatusCode.name} {v})")
        dv = await n.read_attribute(ua.AttributeIds.Value, indexrange="9", raise_on_bad_status=False)
        ok(dv.StatusCode.name in ("BadIndexRangeNoData", "BadIndexRangeInvalid"), f"IndexRange '9' (out of bounds) refused (got {dv.StatusCode.name})")
        dv = await c.get_node("ns=1;s=Level").read_attribute(ua.AttributeIds.Value, indexrange="0", raise_on_bad_status=False)
        ok(dv.StatusCode.name in ("BadIndexRangeNoData", "BadIndexRangeInvalid"), f"IndexRange '0' on scalar Level refused (got {dv.StatusCode.name})")
asyncio.run(main()); sys.exit(1 if F else 0)
