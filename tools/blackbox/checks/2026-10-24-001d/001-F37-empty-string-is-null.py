"""F-37 (papercut): a String tag set to the empty string is served over OPC UA as a null String. After
POST /api/tags {"name":"Str1","value":""} the HTTP API (GET /api/state) says Str1 is "", but an OPC UA Read
of Str1 decodes to None (length -1 on the wire), and StrArr = ['', 'b'] reads [None, 'b']. Part 6 §5.2.2.4
encodes a null String as length -1, distinct from a zero-length one; asyncua decodes the two apart (its own
server holding '' reads back '' — evidence/scripts/asyncua-empty-string-control.py). A customer expects the
value they wrote. Sets Str1 and StrArr through POST /api/tags and restores 'hello' and ['a', 'b'].
Project: SUT_PROJECT (evidence/typed). Oracle: asyncua + Part 6 §5.2.2.4; the HTTP value is nautilus itself."""
import os, sys, json, asyncio, urllib.request
from asyncua import Client, ua
EP, HTTP = os.environ["SUT_OPCUA"], os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
def post(name, value):
    req = urllib.request.Request(HTTP + "/api/tags", data=json.dumps({"name": name, "value": value}).encode(), headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req, timeout=3) as r: return r.status
def state(name):
    with urllib.request.urlopen(HTTP + "/api/state", timeout=3) as r: return json.load(r)["tags"].get(name)
async def main():
    post("Str1", ""); post("StrArr", ["", "b"]); await asyncio.sleep(0.4)
    try:
        print(f"   HTTP /api/state: Str1={state('Str1')!r} StrArr={state('StrArr')!r}")
        async with Client(EP, timeout=8) as c:
            v = await c.get_node("ns=1;s=Str1").read_value(); ok(v == "", f"OPC UA Read Str1 after writing '' is '' (got {v!r})")
            v = await c.get_node("ns=1;s=StrArr").read_value(); ok(v == ["", "b"], f"OPC UA Read StrArr after writing ['', 'b'] is ['', 'b'] (got {v!r})")
    finally:
        post("Str1", "hello"); post("StrArr", ["a", "b"])
asyncio.run(main()); sys.exit(1 if F else 0)
