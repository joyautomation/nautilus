"""Pass (F-28 edges): Read applies IndexRange with the Part 4 §7.22 / §5.10.2 error codes. A slice is
counted from 0 ('3' on a 4-element array is its last element; '0:3' is all of it; '1:2' on IntArr is
[2,3]); an index past the end, a range on a scalar, a range with more dimensions than the array has,
and a range that runs past the end are Bad_IndexRangeNoData; a malformed range ('abc', '2:1', '0:0',
'1:1', '-1', '1.5') is Bad_IndexRangeInvalid (the first index shall be less than the second); the empty
range is the whole value; a row of the 2-D Grid is indexable. String values are F-33, not here.
Project: SUT_PROJECT (evidence/typed), read-only. Oracle: asyncua + Part 4."""
import os, sys, asyncio
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def nid(s): return ua.NodeId.from_string(s)
async def main():
    async with Client(EP, timeout=8) as c:
        cases = [("RealArr", "3", "Good", [0.0]), ("RealArr", "0:3", "Good", [10.0, 20.0, 0.0, 0.0]), ("RealArr", "", "Good", [10.0, 20.0, 0.0, 0.0]),
                 ("IntArr", "1:2", "Good", [2, 3]), ("BoolArr", "2", "Good", [True]), ("TimeArr", "1", "Good", [2000.0]), ("Grid[0]", "1", "Good", [0]),
                 ("RealArr", "4", "BadIndexRangeNoData", None), ("RealArr", "0:9", "BadIndexRangeNoData", None), ("RealArr", "3:4", "BadIndexRangeNoData", None),
                 ("RealArr", "0,1", "BadIndexRangeNoData", None), ("Grid[0]", "0,1", "BadIndexRangeNoData", None), ("Level", "0", "BadIndexRangeNoData", None),
                 ("RealArr", "abc", "BadIndexRangeInvalid", None), ("RealArr", "2:1", "BadIndexRangeInvalid", None), ("RealArr", "0:0", "BadIndexRangeInvalid", None),
                 ("RealArr", "1:1", "BadIndexRangeInvalid", None), ("RealArr", "-1", "BadIndexRangeInvalid", None), ("RealArr", "1.5", "BadIndexRangeInvalid", None)]
        for name, rng, wst, wv in cases:
            rv = ua.ReadValueId(); rv.NodeId = nid(f"ns=1;s={name}"); rv.AttributeId = ua.AttributeIds.Value; rv.IndexRange = rng
            p = ua.ReadParameters(); p.NodesToRead = [rv]; p.TimestampsToReturn = ua.TimestampsToReturn.Both
            r = (await c.uaclient.read(p))[0]; v = r.Value.Value if r.StatusCode.is_good() else None
            ok(r.StatusCode.name == wst and (wv is None or v == wv), f"{name:<8} IndexRange={rng!r:<6} -> {r.StatusCode.name} {v if v is not None else ''}  (want {wst} {wv if wv is not None else ''})")
asyncio.run(main()); sys.exit(1 if F else 0)
