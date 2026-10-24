"""F-09: a function-block instance is program state, not a tag, and has no node (guide,
"What a client sees"). The typed program declares a local `t : TON`; the HTTP API shows it
under locals and the OPC UA folder must not. Also: the folder's children are exactly the
manifest's tags. Project: evidence/typed. Oracle: asyncua; GET /api/state (nautilus itself)
names the local."""
import asyncio, os, sys, json, urllib.request
from asyncua import Client, ua
EP = os.environ["SUT_OPCUA"]; HTTP = os.environ["SUT_HTTP"]; F = []
def ok(c, m): print(("PASS  " if c else "FAIL  ") + m); (None if c else F.append(m))
async def main():
    st = json.load(urllib.request.urlopen(HTTP + "/api/state"))
    locals_ = st.get("locals") or {}
    print("HTTP /api/state locals:", list(locals_)[:10])
    ok("t" in locals_ and set(locals_["t"]) >= {"IN", "PT", "Q", "ET"}, "HTTP API shows the TON instance `t` with its pins")
    async with Client(EP) as c:
        folder = [n for n in await c.get_objects_node().get_children() if n.nodeid.NamespaceIndex != 0][0]
        names = {(await n.read_browse_name()).Name for n in await folder.get_children()}
        for l in locals_:
            ok(l not in names, f"local {l!r} has no node in the folder")
            dv = await c.get_node(f"ns=1;s={l}").read_data_value(raise_on_bad_status=False)
            ok(dv.StatusCode.name == "BadNodeIdUnknown", f"read ns=1;s={l}: {dv.StatusCode.name}")
        tags = set(st.get("tags", {}).keys()) if isinstance(st.get("tags"), dict) else set()
        if tags: ok(names == tags, f"folder children == manifest tags ({len(names)} vs {len(tags)}); diff {names ^ tags}")
asyncio.run(main()); sys.exit(1 if F else 0)
