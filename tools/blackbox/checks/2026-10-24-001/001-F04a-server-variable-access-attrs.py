"""F-04 (the access-level half): the standard Server object's Variables carry the
attributes Part 3 §5.6.2 makes mandatory for every Variable - AccessLevel,
UserAccessLevel, WriteMask, UserWriteMask - and AccessLevel is CurrentRead (1) as the
guide's Security section says. Project: evidence/typed. Oracle: asyncua + Part 3."""
import asyncio, os, sys
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
A = ua.AttributeIds
async def main():
    async with Client(EP) as c:
        for nid, what in [("i=2255", "Server.NamespaceArray"), ("i=2261", "ServerStatus.BuildInfo.ProductName"), ("i=2259", "ServerStatus.State"), ("i=2258", "ServerStatus.CurrentTime")]:
            n = c.get_node(nid)
            for attr in (A.AccessLevel, A.UserAccessLevel, A.WriteMask, A.UserWriteMask):
                dv = await n.read_attribute(attr, raise_on_bad_status=False)
                v = dv.Value.Value if dv.StatusCode.is_good() else None
                ok(dv.StatusCode.is_good(), f"{what} {attr.name}: {dv.StatusCode.name} value={v}")
                if attr in (A.AccessLevel, A.UserAccessLevel) and dv.StatusCode.is_good():
                    ok(v == 1, f"{what} {attr.name} == CurrentRead (1), got {v}")
asyncio.run(main()); sys.exit(1 if F else 0)
