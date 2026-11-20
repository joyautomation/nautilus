"""UserName identity tokens, from asyncua (a stack that is not gopcua's).

Usage: asyncua_username.py <endpoint> <client-cert.der> <client-key.pem> <application-uri> <user> <password>

For each secure policy: signs in as <user> with <password> on
SignAndEncrypt, writes ns=1;s=Speed and ns=1;s=Hi, and reads Speed's
UserAccessLevel; then the same with a wrong password. Prints one JSON
object per line.
"""

import asyncio
import json
import sys

from asyncua import Client, ua

ENDPOINT, CERT, KEY, URI, USER, PASSWORD = sys.argv[1:7]
POLICIES = ["Basic256Sha256", "Aes128Sha256RsaOaep", "Aes256Sha256RsaPss"]


def emit(**kw):
    print(json.dumps(kw), flush=True)


def reason(exc):
    code = getattr(exc, "code", None)
    if isinstance(code, int):
        try:
            return ua.StatusCode(code).name
        except Exception:  # noqa: BLE001
            pass
    return f"{type(exc).__name__}: {exc}"


async def outcome(node, value):
    try:
        await node.write_value(ua.DataValue(ua.Variant(value, ua.VariantType.Double)))
        return "Good"
    except Exception as exc:  # noqa: BLE001
        return reason(exc)


async def session(policy, password):
    client = Client(ENDPOINT, timeout=10)
    client.application_uri = URI
    client.set_user(USER)
    client.set_password(password)
    await client.set_security_string(f"{policy},SignAndEncrypt,{CERT},{KEY}")
    try:
        async with client:
            speed = client.get_node("ns=1;s=Speed")
            hi = client.get_node("ns=1;s=Hi")
            result = {"ok": True, "speed": await outcome(speed, 12.5), "hi": await outcome(hi, 1.0)}
            dv = await speed.read_attribute(ua.AttributeIds.UserAccessLevel)
            result["userAccessLevel"] = int(dv.Value.Value)
            return result
    except Exception as exc:  # noqa: BLE001
        return {"ok": False, "error": reason(exc)}


async def main():
    for policy in POLICIES:
        emit(step="user", policy=policy, **await session(policy, PASSWORD))
    emit(step="wrong", policy=POLICIES[0], **await session(POLICIES[0], PASSWORD + "-wrong"))


asyncio.run(main())
