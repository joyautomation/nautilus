# Design brief: writes through the OPC UA server

**Context:** OPC UA server (`opcua/server`, `opcua/internal/uaserver`). It publishes a
runtime's store the way Sparkplug edge does, so it sits downstream of Runtime. The write
front door it shares with the Operator surface (HTTP API) belongs in Runtime.
Accepted 2026-10-31 (§7) against `main` at 0bae972a. This is a design note, so it does not go to
the black-box tester. §5.3 says what the tester gets instead.

## 0. The shape, in one paragraph

A tag a client may write is opted in by name under `opcua.writable:`. That list has the
same globs and the same "a match opens the member and everything under it" rule as
`sparkplug.writable:`. Inputs are never writable. Only a **writer session** may write: one
on a `SignAndEncrypt` channel whose client application certificate is in the controller's
trust list. Anonymous reads on `None` stay exactly as they are. A write goes through one
front door in `runtime`, which HTTP also adopts. That door does three things:

- It converts to the tag's declared type, and a value either lands exactly as sent or is
  refused. Nothing is wrapped, truncated or retyped.
- It respects forces.
- It lands in the store, so the next scan's snapshot sees it, retain persists it, and the
  existing sweep tells subscribers.

Every write is logged, whether it lands or is refused. Before any of this, the dead
`ChangeNotification` path is removed and sessions are bound to their channels.

## 1. What a client can write

| | v1 (slice 3) | later (slice 4) |
|---|---|---|
| Attribute | `Value` only. `WriteMask`/`UserWriteMask` stay 0 (`namespace.go:364-365`). Any other attribute is `Bad_NotWritable`, as today (`namespace.go:518`) | — |
| Scalar tags (BOOL, integers, enum, REAL/LREAL, TIME, STRING) | yes, if matched | — |
| Struct members (`ns=1;s=P101.Drive.Speed`) | yes, if the member or an ancestor is matched. Applied as a read-modify-write of the whole tag under one lock (`runtime/tags.go:554-583`), as HTTP does | — |
| Object nodes (UDT, FB, composite array) | `Bad_NotWritable`, as today (`namespace.go:515-518`) | — |
| Arrays of scalars, whole or with `IndexRange` | `Bad_WriteNotSupported`, the same line Sparkplug draws ("array writes are not supported", `sparkplug/command.go:100-105`) | whole array of the declared length; `IndexRange` as one element or a slice, through `SetPath("Levels[n]")`, which already indexes arrays (`lang/ir/setfield.go:62-87`) |

**Roles.** Inputs are refused even when a pattern matches: the driver replaces them before
every scan (`runtime/tagdef.go:19-22`). HTTP accepts a whole-tag write to an input as a
transient force (`server/server.go:1243-1251`), but a SCADA client has no use for that.
`naut check` warns on a writable pattern that matches only inputs. Setpoint and state tags
are the normal case. Outputs are writable when matched, because writing an output is how a
Sparkplug-host project commands an edge node (`server/server.go:1249-1251`). The guide
warns that a program writing the same output overwrites the client's value on its next
commit (`runtime/scanview.go:24-28, 86-96`).

**Opt-in: a separate list, with the same vocabulary and the same matcher.** Recommended:
`opcua.writable: [globs]`, not one switch shared with Sparkplug:

- **The threats differ.** On Sparkplug, anyone who can publish to the broker can command
  (`sparkplug/writegate.go:9-14`). On OPC UA, only a client the operator trusted can write.
  A site that lets its SCADA write setpoints over OPC UA should not open them to the whole
  broker as a side effect.
- **One shared tag-level `writable: true` would need a new key** on `TagConfig`
  (`internal/project/project.go:308-324`) and would change Sparkplug's existing meaning.
- **Sharing is still possible.** A site that wants one list uses a YAML anchor.

`writeAllowed`/`validateWritable` (`sparkplug/writegate.go:30-56`) move to a small shared
type that both packages use, so the two lists cannot drift in semantics. The default is
the same as Sparkplug's: empty means read-only.

## 2. Security before any write is accepted

### 2.1 What the stack gives us, and what we build

| Piece | gopcua v0.9.1 / `uaserver` today | We build |
|---|---|---|
| Policy crypto: None, Basic256Sha256, Aes128_Sha256_RsaOaep, Aes256_Sha256_RsaPss, plus the deprecated Basic128Rsa15 and Basic256 | `uapolicy/securitypolicy.go:173-180` | Nothing. The deprecated two are never enabled |
| Endpoint list with security level and user-token policies | `uaserver/server.go:379-444`. Today only `None` plus Anonymous is enabled (`opcua/server/server.go:166-167`) | Enable the three secure policies, `SignAndEncrypt` only |
| Server cert and key handed to every channel | `Certificate`/`PrivateKey` options (`server_config.go:22-55`), `channel_broker.go:54-57` | Generation and storage (§2.2) |
| Which policy and mode a channel may use | Not enforced. The channel adopts whatever the client's OPN asks for, advertised or not (`uasc/secure_channel.go:477-499, 749-750`) | An allowlist: anything else is `Bad_SecurityPolicyRejected` |
| Client certificate trust | **None.** Any `SenderCertificate` is accepted and used (`uasc/secure_channel.go:478-499`) | A trust list, checked during OPN: unknown or rejected is `Bad_CertificateUntrusted`, expired or otherwise malformed `Bad_CertificateInvalid` |
| What a handler can see of its channel | Nothing useful. `SecureChannel.cfg` is unexported (`uasc/secure_channel.go:107-108`), with no accessor for mode, policy or peer certificate | Accessors |
| Proof that the client holds its key | `VerifySessionSignature` (`uasc/secure_channel_crypto.go:42-62`), called in ActivateSession (`session_service.go:102`) | Check that CreateSession's `ClientCertificate` (`session_service.go:53`) is the channel's certificate, and that `ClientDescription.ApplicationURI` matches the certificate URI (`Bad_CertificateUriInvalid`) |
| Identity tokens | **Not read.** ActivateSession ignores `UserIdentityToken` entirely (`session_service.go:87-125`) | Check the token's policy id against the endpoint's policies (`Bad_IdentityTokenInvalid`). Record the identity on the session |
| Session authentication token | `math/rand` Int31 (`session_broker.go:47`). Looked up by token alone (`server.go:178-180`). `handleService` dispatches without checking the session or its channel (`service_handlers.go:100-119`), and `readAttributes` never looks (`opcua/server/services.go:284-319`) | 32 bytes from `crypto/rand`. The session is bound to the channel that activated it. A request on any other channel gets `Bad_SecureChannelIdInvalid`, and service calls require an activated session |

The last row matters most. Today a client on a `None` channel that guesses a writer
session's token could issue writes as that session. Binding the session to its channel
closes that, and it must land before writes do.

**The fork.** Three of the "we build" rows live in `uasc`, which is not ours yet (only
`server/` was copied, `uaserver/doc.go:5-13`). Recommended: copy `uasc` (about 2,300 lines
without tests) into `opcua/internal/uasc`, as #445 did for the server package. Then add
the accessors, a `VerifyPeer func(der []byte) error` hook in the OPN path, and the
policy/mode allowlist. Offer the hook upstream separately, per `doc.go:11-13`. The
server-side channel is the least-exercised part of gopcua: `todo(fs)` comments at
`channel_broker.go:69`, and `RevisedLifetime` echoes whatever is requested
(`uasc/secure_channel.go:805`). So interop tests must cover token renewal with a short
lifetime (§5).

### 2.2 Certificates and the trust list

- **Store.** `opcua.pki` is a directory, default `pki` relative to the working directory
  (as `bdseq-file` and `retain.json` are), never inside the project, so `naut build` never
  embeds a private key. Its layout is the conventional one UA stacks use: `own/cert.der`,
  `own/private/key.pem` (0600), `trusted/certs/`, `rejected/certs/`. CA chains and CRLs
  (`issuers/`) are deferred; a site's CA can be supported later without changing the
  layout. In a cluster the directory is a mounted Secret shared by every replica, so a
  client trusts one certificate whichever replica answers.
- **Server certificate.** Generated on first start when `own/` is empty: RSA-2048,
  SHA-256, self-signed. Its SAN URI is the ApplicationURI: `opcua.application-uri`,
  default `urn:joyautomation:nautilus:<project>`, which names the controller rather than
  the host, so every replica sharing the certificate shares the URI too (#424; a
  host-name URI would differ per pod). Its DNS and IP SANs are
  every name `endpointHosts` advertises (`opcua/server/services.go:61-113`). Key usages
  are as Part 6 requires. Validity is 5 years, with the expiry on the status row and a
  warning 30 days out. A certificate already in `own/` wins, and the ApplicationURI comes
  from it (`server_config.go:42-55`). The certificate is never regenerated silently: a new
  certificate breaks every client's trust, so a host-name change only logs that the SANs
  are stale.
- **Approving a client.** An untrusted client's certificate is written to `rejected/`,
  named by thumbprint, and the connection is refused. The operator approves it with
  `naut opcua trust <thumbprint>`, which moves the file. `naut opcua certs` lists both
  directories with subject, ApplicationURI, expiry and thumbprint. Moving files by hand is
  equivalent. The trust list is re-read on each OPN, which is cheap, so approval needs no
  restart. A dashboard button can come later. There is no auto-accept (see Q5).

### 2.3 Who may do what

| Session | Read, browse, subscribe | Write a `writable` tag |
|---|---|---|
| Anonymous on `None` | yes, as today | `Bad_UserAccessDenied` |
| Anonymous on `SignAndEncrypt` from a trusted certificate | yes | **yes**: the writer role |
| UserName / X.509 user token | not advertised in v1 | see Q2 |

`AccessLevel` is a property of the node: `CurrentRead|CurrentWrite` for a matched
non-input tag, otherwise `CurrentRead` (`namespace.go:390-393`). `UserAccessLevel` and
`AccessLevelEx` are answered per session in `readAttributes`: write for a writer session,
read for anyone else. The namespace's `Attribute` has no session (`namespace.go:341`), so
the Read handler takes the session from the request header and overrides those two
attributes. The standard Server object stays read-only for every session
(`services.go:362-367`).

## 3. Write semantics

### 3.1 One front door

HTTP's write rules live inline in its handler: the force check, member and enum writes,
declared tags only, and a scalar switch (`server/server.go:1124-1211`). Under them,
`Tags.Set` stores whatever kind it is given, with no coercion (`runtime/tags.go:163-168,
478-490`). That is #421: an out-of-range write was accepted, and the stored REAL flipped
the node's DataType to Double for good.

Recommended: `runtime.Tags.Write(batch []WriteOp) []error`. It applies one request's items
in order, under one store lock, so a scan snapshot (`runtime/scanview.go:58-80`) sees all
or none of a request's accepted items: a Hi/Lo limit pair cannot straddle a scan. Each
item:

- resolves through `SetPath`, so it never creates a tag;
- converts to the declared type, using `SetFieldTyped` (`lang/ir/setfield.go:62`) plus a
  width check, which `assignInto`/`coerceLeaf` lack today (`setfield.go:345-349` stores
  any int64 into an INT). The integer-range helpers already exist
  (`lang/ir/conversions.go:467-480`);
- refuses a forced address (`runtime/force.go:332`).

The front door returns typed errors (`ErrUndefined`, `ErrTypeMismatch`, `ErrOutOfRange`,
`ErrForced`) that each protocol maps to its own codes. HTTP moves onto it in the same PR,
which fixes #421 part 1. Part 2, Int64 rather than Double when widening in `value.go`, is
fixed alongside.

### 3.2 Conversions

The rule is that a write lands exactly as sent, or is refused.

| Node DataType (declared) | Accepted Variant | Refused |
|---|---|---|
| Boolean | Boolean | anything else: TypeMismatch |
| SByte … Int64, Byte … UInt64 | any integer type, checked against the **declared** width | Float/Double: TypeMismatch. Outside the width, or above 2^63-1 (the store is int64): OutOfRange |
| Double (REAL, LREAL) | Float, Double; integers within ±2^53 | NaN or ±Inf: OutOfRange (a NaN setpoint poisons the logic, see #429) |
| Int32 enumeration | an integer that is a member's value | any other integer: OutOfRange |
| Duration (TIME, ms) | Double or integer with a whole number of ms | a fraction of a ms: OutOfRange |
| String | String. A null String is taken as `""`, the mirror of #434 | anything else: TypeMismatch |

Clients write with the DataType they browsed (UaExpert, Ignition), so exact matches are
the common case. Accepting any integer width is for clients like asyncua, whose
`Variant(5)` defaults to Int64.

### 3.3 Status codes, per item

| Case | Code |
|---|---|
| node id is nothing | `Bad_NodeIdUnknown` (as today) |
| attribute other than Value; Object node; tag not matched; input | `Bad_NotWritable` |
| matched tag, session not a writer | `Bad_UserAccessDenied` |
| conversion, per §3.2 | `Bad_TypeMismatch` / `Bad_OutOfRange` |
| DataValue carries a StatusCode or timestamps; `IndexRange` or an array before slice 4 | `Bad_WriteNotSupported` (Part 4 §5.10.4) |
| forced address | `Bad_Locked`; the audit line names the force |
| this replica is a standby | `Bad_InvalidState` |
| more nodes than `MaxNodesPerWrite` | `Bad_TooManyOperations`. The limit is advertised and enforced, like `MaxNodesPerRead` (`services.go:273-294`) |
| written | `Good` |

`Good` means stored, not acted on by the logic, as with HTTP's 204.

### 3.4 Timing, retain, standby

- **When a write takes effect.** It is in the store when the response goes out. The scan
  in progress runs on its own snapshot and does not see it; the next scan does
  (`runtime/scanview.go:9-28`). If a program writes the same tag, its commit wins, last
  writer first.
- **Retain.** A write to a setpoint persists because the default retained set is every
  setpoint (`runtime/runtime.go:454-464`), saved within 2 s (`runtime.go:58-62`). Nothing
  new is needed.
- **Standby.** A standby's store is stale. HTTP proxies to the leader
  (`server/server.go:38-43, 649`), but OPC UA cannot. The server gets
  `SetWriteGate(func() bool)`, the leadership-gate vocabulary the drivers already use
  (`io/multi.go:250-260`). `wireRedundancy` installs it next to the driver's
  (`cmd/naut/runcmd.go:69-73`).

### 3.5 Audit

Every write item is logged on the console logger:

- **Landed:** one Info line `opcua: write` with node, old → new, the session, the
  client's ApplicationURI, a short thumbprint and the remote address.
- **Refused:** a Warn line, logged once per client, node and reason, as Sparkplug does
  with `refuseCommand`/`warnCommandOnce` (`sparkplug/writegate.go:114-122`).

`Status` gains `Writes` and `WritesRefused` (`opcua/server/server.go:261-269`), so they
appear on the `/api/drivers` row (`internal/project/drivers.go:479-490`). Sparkplug's
counterpart is `CommandsRefused` (`sparkplug/node.go:751`). Part 4 audit events
(AuditWriteUpdateEventType) are not adopted, because the server has no event model
(`EventNotifier` is 0, `namespace.go:370-371`).

### 3.6 Notifying subscribers, and the deadlock

A write needs no notification code of its own. It moves the tag's generation, and the
sweep, every `Options.Sweep` (100 ms), notifies the covering items with sends that never
block (`opcua/server/server.go:301-383, 523-529`). Black-box check
`001-P-http-write-notified` already proves this path for HTTP writes.

The latent hazard: `MonitoredItemService.ChangeNotification` holds `Mu`
(`monitored_item_service.go:98-101`) and blocks in `notify` until the channel has room or
the subscription is done (`:146-151`). Meanwhile the publish loop takes `publishMu` and
then that same `Mu` in `dropUnreported` (`subscription_service.go:487-489`,
`monitored_item_service.go:156-158`) before it drains `NotifyChannel` again (`:465`).
When the channel is full, each side waits for the other. Today nothing reaches it: the
only caller is `NodeNameSpace.SetAttribute` (`namespace_node.go:274`), and the Write
handler never routes ns=0 there (`services.go:362-367`).

**Fix: delete it** rather than repair it: `MonitoredItemService.ChangeNotification` and
`notify` (`monitored_item_service.go:98-151`), `Server.ChangeNotification`
(`server.go:197-199`), `NodeNameSpace.ChangeNotification` (`namespace_node.go:68-72`),
and the call and `ExternalNotification` send in `NodeNameSpace.SetAttribute`
(`:274-278`). `notify_test.go` goes with them. Add one test that asserts no `uaserver`
code sends on `NotifyChannel` while holding `MonitoredItemService.Mu`. If ns=0 ever needs
a push, it copies out under the lock and sends without blocking, as the sweep does.

## 4. Config and docs

```yaml
opcua:
  endpoint: opc.tcp://0.0.0.0:4840
  writable: ["*_SP", "Pump*.SpeedSP", "Recipe.*"]   # omit = read-only, as today
  pki: /var/lib/nautilus/pki                        # default ./pki (working dir)
```

The two new keys go on `OPCUAConfig` (`internal/project/project.go:1555-1560`) and are
passed through `OPCUAServer` (`:1565-1571`). `naut check` validates the globs, as
`validateWritable` does (`writegate.go:30-39`), and warns on a pattern that matches
nothing, or only inputs. There is no key for security policies (the set is fixed) and
none for users (not in v1). No secret goes in the manifest: the private key lives in
`pki`, following the `NAUTILUS_TOKEN` / `NAUTILUS_MQTT_PASSWORD` rule
(`project.go:207-209, 263-266`).

**Guide** (`website/src/content/docs/guides/opcua-server.md`):

- Rewrite **Security** (`:186-200`): endpoints and policies, the PKI layout, approving a
  client, the roles table, and the `None` endpoint as read-only. It now promises that "the
  write token … hold[s] for OPC UA too" (`:197-198`). Change that: `NAUTILUS_TOKEN` is
  HTTP's credential, and the trust list is OPC UA's.
- Add a **Writes** section: what may be written, the conversion and status tables, timing,
  forces, standby, audit, and an asyncua snippet that writes with a client certificate.
- Change the opening's "The server is read-only" (`:44-46`) and drop "Read-only" from the
  preview caution (`:6-14`).

**Claims** (`docs/claims/opcua-server.yaml`): OPC-008 (`:192-209`) is rewritten from "read-only" to "read-only unless
writable, and only for a writer session". New: OPC-016 secure endpoints and the server
certificate; OPC-017 trust list and approval; OPC-018 writes and roles; OPC-019
conversions and status codes; OPC-020 effect, retain and notification; OPC-021 audit and
counters; OPC-022 standby refusal. The design notes here never cross the wall, so each
claim cites unit tests now and black-box checks once they are imported.

## 5. Test plan

### 5.1 Unit tests (`opcua/server`, `opcua/internal/uaserver`, `runtime`)

- **Front door.** The conversion table row by row, including the boundary values of each
  integer width. Batch atomicity: one scan snapshot sees all or none of a request. A
  forced address is refused. HTTP regression tests for #421: an out-of-range write is a
  400, and the stored kind never flips.
- **Write handler.** Every row of §3.3, and `UserAccessLevel` per session.
- **Session.** Tokens are unguessable, and a token used from another channel is refused.
  `None` plus a certificate is still not a writer. A policy or mode that was not
  advertised is rejected at OPN. An untrusted certificate lands in `rejected/`, and is
  accepted after `trust` with no restart. Certificate generation produces the right SANs
  and URI, and a certificate in `own/` is reused.
- **Deadlock.** A writer hammers a writable tag while a subscription with a full
  `NotifyChannel` and Disabled items churns. Run under `-race`: the server keeps answering
  and Status returns.

### 5.2 Foreign clients

- **asyncua** (already in `testdata/foreign`, gated on `NAUTILUS_OPCUA_PYTHON`,
  `foreign_test.go:27-31`). Extend the script for each of the three policies under
  SignAndEncrypt with a generated client certificate: rejected first, then trusted, then
  writes of each type, then a refused write as anonymous on `None`. Add a session with a
  lifetime of a few seconds so token renewal is exercised.
- **UA-.NETStandard.** Feasible as a small console program on the
  `OPCFoundation.NetStandard.Opc.Ua.Client` NuGet package (around 150 lines), gated on
  `NAUTILUS_OPCUA_DOTNET` in the same style, in a CI job with the dotnet SDK image. Run it
  on release, not on every PR. Its value is that it is the strictest certificate
  validator (SAN host check, key usage, URI match) and the de facto reference. A manual
  check with an Ignition gateway (Eclipse Milo) once per release covers the client
  argonaut's route will meet (`docs/design/edge-collector.md:227-238`).

### 5.3 Black-box

The tester gets only the guide and the specs, so the **Writes** and **Security** sections
must be complete enough to brief from. Proposed brief (next free number, following
`briefs/000-template.md`): **must prove** that a SCADA client can be trusted by an
operator and write the setpoints the manifest opens, and nothing else, with the values
landing exactly or being refused. Oracles are asyncua 2.1.0 (supports all three
policies), plus `openssl` for certificates. The checks we expect it to produce, with
dispositions:

| Area | Disposition |
|---|---|
| Secure endpoints advertised per policy, server certificate SANs and URI, and the `None` endpoint still anonymous and read-only | gate |
| Untrusted certificate refused and written to `rejected/`; `naut opcua trust` admits it with no restart | gate |
| An unadvertised policy or mode is refused (for example Basic128Rsa15, or Sign only) | gate |
| Writes per type: boundary values, out of range, type mismatch, NaN, enum member, TIME fraction, empty string | gate |
| Not writable, input, Object node, Server object, forced tag, status or timestamp in the DataValue, array or IndexRange | gate |
| `AccessLevel` and `UserAccessLevel` for anonymous and writer sessions | gate |
| A written value reaches another session's subscription within sweep plus publishing interval; HTTP and OPC UA writes agree | gate |
| A written setpoint survives a restart (`retain: {}`) | gate |
| Audit line and counters on `/api/drivers` | gate |
| Session token replayed from a second channel | gate |
| Writes under subscription churn, for 10 minutes, with no freeze | soak |
| UA-.NET and Ignition interop | release / manual |

## 6. Slices, smallest first

1. **Dead path out, sessions bound.** Delete `ChangeNotification` (§3.6). Session tokens
   from `crypto/rand`. Sessions bound to their channel. Service calls require an
   activated session. Behaviour is unchanged for real clients. Run the 001 gate.
2. **Secure channels, read-only.** Fork `uasc`, add the accessors, the OPN trust hook and
   the policy/mode allowlist. Add the PKI with generation, trust and reject, `naut opcua
   certs` and `trust`, identity-token checks, the CreateSession certificate and URI
   checks, and the ApplicationURI and ProductURI (part of #424). Endpoints: `None` plus
   the three secure policies under SignAndEncrypt. Everything still reads `CurrentRead`.
   Guide **Security** rewrite, OPC-016 and OPC-017, and the asyncua secure-channel test.
3. **Writes to opted-in tags.** `opcua.writable`, the shared matcher, the runtime front
   door with HTTP moved onto it (closing #421), the Write handler, per-session
   `UserAccessLevel`, the status-code table, forces, the standby gate, batch atomicity,
   the audit log and counters. Scalars and struct members only. Guide **Writes**,
   OPC-008 rewritten, OPC-018 to OPC-022. Run the black-box brief here.
4. **Arrays and IndexRange writes.** Plus UserName tokens if Q2 says so.

## 7. Decisions (James, 2026-10-31)

1. **Two writable lists.** `opcua.writable` is separate from `sparkplug.writable`, with
   one matcher and one meaning (§1). A shared switch would open both protocols at once.
2. **A trusted application certificate is enough to write in v1.** UserName tokens come
   in slice 4, when a site needs per-person audit.

Taken as recommended, open to change before the slice that needs them:

3. **Fork `uasc`** into `opcua/internal/uasc` now (§2.1), as #445 did, and offer the hook
   upstream in parallel.
4. **Keep the `None` endpoint** for anonymous reads. Add a switch to turn it off only when
   a site's security review asks.
5. **No auto-accept** of client certificates. Commissioning is one refused attempt and
   one `naut opcua trust`.
6. **Outputs are writable when matched**, with a guide warning that logic writing the
   output wins. `naut check` warns on a writable output a program writes, if that is
   cheap to compute.
7. **HTTP moves onto the one write front door in slice 3.** That is the #421 fix.
8. **A forced tag answers `Bad_Locked`.** `Bad_InvalidState` means only "standby".
