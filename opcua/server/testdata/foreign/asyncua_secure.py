"""The secure endpoints, from asyncua (a stack that is not gopcua's).

Usage: asyncua_secure.py <endpoint> <client-cert.der> <client-key.pem> <application-uri> <server-cert.der>

Prints one JSON object per line. Between the refusal and the trusted
connections it waits for a line on stdin: the Go side trusts the client's
certificate in the server's store meanwhile, as an operator would with
`naut opcua trust`.
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
    """Reads across several channel renewals (lifetime 1.5 s)."""
    reads = 0
    for _ in range(20):
        await client.nodes.server_state.read_value()
        reads += 1
        await asyncio.sleep(0.2)
    return {"reads": reads}


async def main():
    # Untrusted: refused, and the certificate filed for approval.
    emit(step="untrusted", **(await attempt("Basic256Sha256", "SignAndEncrypt")))
    sys.stdin.readline()  # the Go side trusts the certificate now

    for policy in POLICIES:
        emit(step="trusted", policy=policy, **(await attempt(policy, "SignAndEncrypt")))
    emit(step="renewals", **(await attempt("Aes256Sha256RsaPss", "SignAndEncrypt", timeout_ms=1500, work=renewals)))
    emit(step="sign-only", **(await attempt("Basic256Sha256", "Sign", direct=True)))
    emit(step="wrong-uri", **(await attempt("Basic256Sha256", "SignAndEncrypt", uri="urn:test:impostor")))

    # None stays open to anonymous clients.
    client = Client(ENDPOINT, timeout=5)
    try:
        async with client:
            emit(step="none", ok=True, state=int(await client.nodes.server_state.read_value()))
    except Exception as exc:  # noqa: BLE001
        emit(step="none", ok=False, error=reason(exc))
    emit(done=True)


asyncio.run(main())
