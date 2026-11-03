"""The secure endpoints, from asyncua (a stack that is not gopcua's).

Usage: asyncua_secure.py <endpoint> <client-cert.der> <client-key.pem> <application-uri> <server-cert.der>

Prints one JSON object per line. Between the refusal and the trusted
connections it waits for a line on stdin: the Go side trusts the client's
certificate in the server's store meanwhile, as an operator would with
`naut opcua trust`.

The trusted client then writes each type with asyncua's plain write_value
(which sends a Good StatusCode and a SourceTimestamp, and an Int64 for a
Python int), across channel renewals too, and an anonymous client on None
is refused the same writes.
"""

import asyncio
import json
import sys

from asyncua import Client, ua

ENDPOINT, CERT, KEY, URI, SERVER_CERT = sys.argv[1:6]
POLICIES = ["Basic256Sha256", "Aes128Sha256RsaOaep", "Aes256Sha256RsaPss"]


def emit(**kw):
    print(json.dumps(kw), flush=True)


def reason(exc):
    """The status code name an exception carries, else its text."""
    for attr in ("code",):
        code = getattr(exc, attr, None)
        if isinstance(code, int):
            try:
                return ua.StatusCode(code).name
            except Exception:  # noqa: BLE001
                pass
    return f"{type(exc).__name__}: {exc}"


async def attempt(policy, mode, uri=URI, timeout_ms=None, work=None, direct=False):
    """Connect under policy and mode. direct names the server certificate
    so asyncua skips its endpoint lookup and asks the server for security
    no endpoint offers: the server, not the client, must refuse it."""
    client = Client(ENDPOINT, timeout=5)
    client.application_uri = uri
    if timeout_ms:
        client.secure_channel_timeout = timeout_ms
    extra = f",{SERVER_CERT}" if direct else ""
    await client.set_security_string(f"{policy},{mode},{CERT},{KEY}{extra}")
    try:
        async with client:
            state = await client.nodes.server_state.read_value()
            result = {"ok": True, "state": int(state)}
            if work:
                result.update(await work(client))
            return result
    except Exception as exc:  # noqa: BLE001
        return {"ok": False, "error": reason(exc)}


async def renewals(client):
    """Reads and writes across several channel renewals (lifetime 1.5 s)."""
    reads = writes = 0
    count = client.get_node("ns=1;s=Count")
    for i in range(20):
        await client.nodes.server_state.read_value()
        reads += 1
        await count.write_value(i)
        writes += 1
        await asyncio.sleep(0.2)
    return {"reads": reads, "writes": writes, "count": await count.read_value()}


async def outcome(node, value):
    """write_value, then the value read back, or the status it was refused."""
    try:
        await node.write_value(value)
    except Exception as exc:  # noqa: BLE001
        return {"error": reason(exc)}
    got = await node.read_value()
    return {"ok": True, "value": got}


WRITES = [
    ("Running", False),
    ("Count", 7),  # a Python int: an Int64 Variant to an Int32 node
    ("Count32", ("Count", ua.Variant(8, ua.VariantType.Int32))),
    ("Level", 4.25),
    ("Label", "Pump 9"),
    ("Empty", ("Label", "")),
    ("Elapsed", 1500.0),  # Duration: milliseconds
    ("State", ua.Variant(10, ua.VariantType.Int32)),  # Mode.Run
    ("P101.Drive.Speed", 1450.0),
    # refused
    ("CountFraction", ("Count", 1.5)),
    ("CountTooBig", ("Count", 2**40)),
    ("StateNotMember", ("State", ua.Variant(5, ua.VariantType.Int32))),
    ("ElapsedFraction", ("Elapsed", 1.5)),
    ("LevelNaN", ("Level", float("nan"))),
    ("Array", ("Levels", ua.Variant([9.0, 9.0, 9.0], ua.VariantType.Double))),
    ("Unmatched", ("P101.Running", True)),
]


async def writes(client):
    out = {}
    for name, spec in WRITES:
        addr, value = spec if isinstance(spec, tuple) else (name, spec)
        res = await outcome(client.get_node(f"ns=1;s={addr}"), value)
        if "value" in res:
            v = res["value"]
            res["value"] = v if isinstance(v, (bool, int, float, str)) and v == v else repr(v)
        out[name] = res
    level = client.get_node("ns=1;s=Level")
    out["userAccessLevel"] = (await level.read_attribute(ua.AttributeIds.UserAccessLevel)).Value.Value
    return {"writes": out}


async def main():
    # Untrusted: refused, and the certificate filed for approval.
    emit(step="untrusted", **(await attempt("Basic256Sha256", "SignAndEncrypt")))
    sys.stdin.readline()  # the Go side trusts the certificate now

    for policy in POLICIES:
        emit(step="trusted", policy=policy, **(await attempt(policy, "SignAndEncrypt")))
    emit(step="renewals", **(await attempt("Aes256Sha256RsaPss", "SignAndEncrypt", timeout_ms=1500, work=renewals)))
    emit(step="writes", **(await attempt("Basic256Sha256", "SignAndEncrypt", work=writes)))
    emit(step="sign-only", **(await attempt("Basic256Sha256", "Sign", direct=True)))
    emit(step="wrong-uri", **(await attempt("Basic256Sha256", "SignAndEncrypt", uri="urn:test:impostor")))

    # None stays open to anonymous clients, for reading only.
    client = Client(ENDPOINT, timeout=5)
    try:
        async with client:
            state = int(await client.nodes.server_state.read_value())
            level = client.get_node("ns=1;s=Level")
            refused = await outcome(level, 99.0)
            ual = (await level.read_attribute(ua.AttributeIds.UserAccessLevel)).Value.Value
            emit(step="none", ok=True, state=state, write=refused, userAccessLevel=ual)
    except Exception as exc:  # noqa: BLE001
        emit(step="none", ok=False, error=reason(exc))
    emit(done=True)


asyncio.run(main())
