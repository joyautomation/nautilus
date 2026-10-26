"""F-04 (the rest, filed nautilus#424): the Server object identifies nautilus, not a
library - ApplicationUri set (Part 4 §7.1), ProductUri/SoftwareVersion/BuildDate of
BuildInfo not a placeholder - and the ServerType Mandatory variables ServerArray,
ServiceLevel and Auditing (Part 5 §8.2) have readable Values.
Project: evidence/typed. Oracle: asyncua + Part 4/5."""
import asyncio, os, sys, datetime
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    c = Client(EP)
    eps = await c.connect_and_get_server_endpoints()
    ad = eps[0].Server
    print(f"ApplicationUri={ad.ApplicationUri!r} ProductUri={ad.ProductUri!r} ApplicationName={ad.ApplicationName.Text!r}")
    ok(bool(ad.ApplicationUri), "ApplicationUri is set (Part 4 §7.1)")
    ok("gopcua" not in (ad.ProductUri or ""), "ProductUri does not name a library")
    async with Client(EP) as c:
        dv = await c.get_node("i=2260").read_data_value(raise_on_bad_status=False)  # ServerStatus.BuildInfo
        bi = dv.Value.Value if dv.StatusCode.is_good() else None
        print(f"BuildInfo (i=2260): {dv.StatusCode.name} {bi!r}")
        if bi is None:
            st = await c.get_node("i=2256").read_data_value(raise_on_bad_status=False)  # ServerStatus
            bi = st.Value.Value.BuildInfo if st.StatusCode.is_good() and st.Value.Value is not None else None
            print(f"ServerStatus (i=2256): {st.StatusCode.name} BuildInfo={bi!r}")
        ok(bi is not None, "BuildInfo is readable (i=2260 or inside ServerStatus)")
        if bi is not None:
            ok("gopcua" not in (bi.ProductUri or ""), f"BuildInfo.ProductUri ({bi.ProductUri!r}) does not name a library")
            ok(bi.SoftwareVersion not in ("", "0.0.0-dev"), f"BuildInfo.SoftwareVersion ({bi.SoftwareVersion!r}) is a version")
            ok(bi.BuildDate.year > 2000, f"BuildInfo.BuildDate ({bi.BuildDate}) is not the epoch placeholder")
        for nid, what in [("i=2254", "ServerArray"), ("i=2267", "ServiceLevel"), ("i=2268", "Auditing")]:
            dv = await c.get_node(nid).read_data_value(raise_on_bad_status=False)
            ok(dv.StatusCode.is_good(), f"{what} Value: {dv.StatusCode.name} {dv.Value.Value if dv.StatusCode.is_good() else ''}")
asyncio.run(main()); sys.exit(1 if F else 0)
