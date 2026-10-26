"""F-06 (filed nautilus#421): an integer that does not fit its declared width is sent as
Int64, not Double (guide, Types), and the node's DataType stays the declared type. Sets
state: writes Int1=70000 through the HTTP API, then restores -1234. Run it last against a
server: on the reported build the DataType flips to Double permanently, which breaks
001-P-type-table until the server restarts. Project: evidence/typed. Oracle: asyncua."""
import asyncio, os, sys, json, urllib.request
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def post(name, value):
    req = urllib.request.Request(HTTP + "/api/tags", data=json.dumps({"name": name, "value": value}).encode(), headers={"Content-Type": "application/json"}, method="POST")
    try:
        with urllib.request.urlopen(req) as r: return r.status
    except urllib.error.HTTPError as e: return e.code
async def main():
    async with Client(EP) as c:
        n = c.get_node("ns=1;s=Int1")
        dt0 = (await n.read_data_type()).to_string(); dv0 = await n.read_data_value()
        print(f"before: DataType={dt0} variant={dv0.Value.VariantType.name} value={dv0.Value.Value}")
        st = post("Int1", 70000); print(f"POST Int1=70000 -> HTTP {st}")
        await asyncio.sleep(0.4)
        dt1 = (await n.read_data_type()).to_string(); dv1 = await n.read_data_value()
        print(f"after overflow: DataType={dt1} variant={dv1.Value.VariantType.name} value={dv1.Value.Value}")
        if st < 300:
            ok(dv1.Value.VariantType == ua.VariantType.Int64 and dv1.Value.Value == 70000, "out-of-range value is sent as Int64 70000")
            ok(dt1 == dt0, f"DataType unchanged by the overflow ({dt1})")
        else:
            print("INFO  the HTTP API refused the out-of-range write; the guide's premise (a declared variable never produces one) holds")
        post("Int1", -1234); await asyncio.sleep(0.4)
        dt2 = (await n.read_data_type()).to_string(); dv2 = await n.read_data_value()
        ok(dt2 == dt0 and dv2.Value.VariantType == ua.VariantType.Int16 and dv2.Value.Value == -1234, f"restored: DataType={dt2} variant={dv2.Value.VariantType.name} value={dv2.Value.Value}")
asyncio.run(main()); sys.exit(1 if F else 0)
