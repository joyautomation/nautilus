// FingerprintCalls.cs: the online fingerprint read (#789) and the TIA
// instance's blind trust (siemens-ide.md section 3.8). A fingerprint read
// goes online from a session's CPU, reads what TIA's fingerprint service
// reports for the CPU (checksums and counters, not its identity) and goes
// offline. On a CPU whose certificate TIA does not know it asks a TLS
// question, which only the request's trustUnverifiedCertificate answers,
// blindly; TIA keeps that trust for the rest of its instance, so the
// agent records it, reports it on every later online call, and a download
// that would ride on it needs decisions.acceptBlindTrust (BlindTrustRule).

using System;
using System.Collections.Generic;
using System.Text;

namespace Tiad
{
    // FingerprintRule is what a fingerprint read must look like before the
    // agent passes it on: a read that is not whole fails, never answers
    // part of what TIA said.
    public static class FingerprintRule
    {
        // In UTF-8 bytes. The bench CPU's longest item is about 100 bytes;
        // these bound what the audit line can carry from the CPU.
        public const int MaxItems = 64, MaxIdentifier = 128, MaxValue = 1024;

        // LockedWhy is why a fingerprint read answers TIA's credentials
        // questions no (LoginUse.Refusing): on the bench 1510SP with access
        // control on (#1336), TIA's fingerprint service opens a connection
        // of its own, asks TLS trust with no certificate to check, then a
        // password-only logon (OnlineReadAccessPassword). Answering it
        // would send a password over a connection tiad cannot verify, so a
        // fingerprint read never logs on: a CPU that asks credentials is
        // read through verify instead.
        public const string LockedWhy = "a fingerprint read never logs on: the CPU asks credentials, so its access control is on, and TIA's fingerprint service would then need a blind certificate trust and a password-only logon, which tiad does not send";

        // UseVerify is the fix fingerprint_unavailable names.
        public const string UseVerify = "use verify (a compare logged on as \"nautilus\" with secrets.fullAccessPassword) as this CPU's reference";

        // PasswordOnly is the password-only logon TIA's fingerprint service
        // asks a CPU with access control on.
        public const string PasswordOnly = "OnlineReadAccessPassword";

        // Mark marks e, a fingerprint read's failure, AccessControlOn when
        // the CPU's access control is on: the read answered credentials no
        // with LockedWhy (refusedAll, LoginUse.RefusedAll), or answered any
        // credentials question no (asked holds the questions answered no,
        // as OnlineChecks.Ask names them): a read never logs on, so TIA
        // asking credentials of any type, on the project's connection or
        // in its fingerprint service (OnlineReadAccessPassword on the
        // bench), shows the access control. TrustedNothing then says that
        // no certificate question was answered trusted (trusted counts
        // them, LoginUse.TrustedAny among them). It returns e, unchanged
        // when the access control is not shown.
        public static TiaException Mark(TiaException e, bool refusedAll, IEnumerable<string> asked, int trusted)
        {
            bool credentials = false;
            foreach (string a in asked)
            {
                if (a.StartsWith("credentials (", StringComparison.Ordinal)) credentials = true;
            }
            if (refusedAll || credentials)
            {
                e.AccessControlOn = true;
                e.TrustedNothing = trusted == 0;
            }
            return e;
        }

        // Unavailable is the message of fingerprint_unavailable for the CPU
        // at address: e is the refusal (TiaException.AccessControlOn).
        public static string Unavailable(string address, TiaException e)
        {
            string how = e.Message.IndexOf("credentials (" + PasswordOnly + ")", StringComparison.Ordinal) >= 0
                ? "TIA's fingerprint service asked a password-only logon (" + PasswordOnly + ")"
                : "TIA asked credentials going online";
            return Unavailable(address, how, e.TrustedNothing, "");
        }

        // AnsweredBeforeHow and RetireFix are the refusal before going
        // online: tiad answered credentials for the CPU earlier in this TIA
        // instance (InstanceLogons.AnsweredBefore), so its access control
        // was on, and a TIA that kept that logon would ask the read's own
        // connection nothing and reach the fingerprint service (#1336
        // review). It holds until the instance retires.
        public const string AnsweredBeforeHow = "tiad answered its credentials question earlier in this TIA instance";
        public const string RetireFix = "; if its access control was turned off since, close every session (the agent then retires the TIA instance) and read again";

        // UnavailableBefore is fingerprint_unavailable refused before going
        // online: nothing was trusted, nothing sent.
        public static string UnavailableBefore(string address)
        {
            return Unavailable(address, AnsweredBeforeHow, true, RetireFix);
        }

        static string Unavailable(string address, string how, bool trustedNothing, string tail)
        {
            return "fingerprint unavailable: the CPU at " + address + " has access control on (" + how + "), and a fingerprint read never logs on: " +
                "TIA's fingerprint service reads over a connection of its own, which needs a blind certificate trust and then a password-only logon, " +
                "and tiad sends no password over a connection it cannot verify. " +
                (trustedNothing ? "Nothing was trusted and no password was sent; " : "No password was sent; ") + UseVerify + tail;
        }

        // CertificateOnly is true when a refusal (TiaNames.OnlineRefused)
        // names only certificate questions, which the opt-in answers; it
        // answers no credentials and no question tiad does not know.
        public static bool CertificateOnly(string message)
        {
            return message.IndexOf("tiad trusts no CPU certificate", StringComparison.Ordinal) >= 0 &&
                message.IndexOf("TIA asked: trust the TLS certificate", StringComparison.Ordinal) >= 0 &&
                message.IndexOf("credentials (", StringComparison.Ordinal) < 0 &&
                message.IndexOf("a question tiad does not know", StringComparison.Ordinal) < 0;
        }

        // Check returns null for a whole read, or what is wrong with it.
        public static string Check(FingerprintRead r)
        {
            if (r == null || r.Items == null || r.Items.Count == 0) return "TIA's fingerprint service returned no items";
            if (r.Items.Count > MaxItems) return "TIA's fingerprint service returned " + r.Items.Count + " items, more than " + MaxItems;
            var seen = new HashSet<string>(StringComparer.Ordinal);
            foreach (KeyValuePair<string, string> kv in r.Items)
            {
                string id = kv.Key ?? "", v = kv.Value ?? "";
                if (id == "") return "TIA's fingerprint service returned an item with no identifier";
                if (Encoding.UTF8.GetByteCount(id) > MaxIdentifier) return "TIA's fingerprint service returned an identifier longer than " + MaxIdentifier + " bytes";
                if (Encoding.UTF8.GetByteCount(v) > MaxValue) return "TIA's fingerprint service returned a value longer than " + MaxValue + " bytes for " + Json.Quote(id);
                if (!seen.Add(id)) return "TIA's fingerprint service returned " + Json.Quote(id) + " twice";
            }
            return null;
        }
    }

    // FingerprintRun is one audited fingerprint read: its opt-in and
    // address, what TIA returned, and the instance's trust after it.
    public sealed class FingerprintRun
    {
        public bool Trust;
        public string Address = "";
        public FingerprintRead Read;
        public BlindTrust BlindTrust;

        // Value is the item named id, or null.
        public string Value(string id)
        {
            if (Read == null) return null;
            foreach (KeyValuePair<string, string> kv in Read.Items)
            {
                if (kv.Key == id) return kv.Value ?? "";
            }
            return null;
        }

        // ToJson is the read for a response or the deploy record: the
        // items in TIA's order, this read's own trusted questions, and the
        // instance's trust (a read that asked nothing may ride on an
        // earlier one).
        public Obj ToJson()
        {
            var list = new List<object>();
            foreach (KeyValuePair<string, string> kv in Read.Items)
            {
                var x = new Obj();
                x["identifier"] = kv.Key;
                x["value"] = kv.Value ?? "";
                list.Add(x);
            }
            var o = new Obj();
            o["items"] = list;
            o["trustedBlindly"] = (Read.TrustedBlindly ?? new List<string>()).ToArray();
            o["blindTrust"] = BlindTrust == null ? null : BlindTrust.ToJson();
            return o;
        }
    }

    public sealed partial class Agent
    {
        // blindTrust is this TIA instance's blind trust (BlindTrust), or
        // null while it has none. Written only on the TIA thread, before
        // the call that gave it returns; read anywhere.
        volatile BlindTrust blindTrust;

        // instanceLogons is the evidence the instance-logon route rests on
        // (InstanceLogons, UserManagementRule.Gate.At): equal compares that
        // logged on, per address, in the current TIA instance.
        readonly InstanceLogons instanceLogons = new InstanceLogons();

        // UnknownTrust stands for the questions of a call that took the
        // opt-in and then failed inside TIA's side: whether TIA trusted
        // anything before it failed is unknown, so it counts as trusted.
        const string UnknownTrust = "unknown: the call failed after its trustUnverifiedCertificate opt-in, so TIA may have trusted a certificate";

        // TrustFix is the fix a fingerprint read TIA's TLS question
        // refused names.
        const string TrustFix = "to read it anyway, trust the CPU's certificate blindly (trustUnverifiedCertificate: true); the agent's TIA instance then keeps that trust, and a download in it needs decisions.acceptBlindTrust, until every session is closed (the agent then retires that instance) or the agent restarts";

        void RegisterFingerprint(Router r)
        {
            r.Add("POST", "/v1/sessions/{id}/fingerprint", Fingerprint);
        }

        // RecordBlindTrust records that call, on session s to address,
        // answered certificate questions trusted, blindly. The first one
        // sets the instance's record; later ones are logged beside it. Run
        // on the TIA thread.
        void RecordBlindTrust(string call, Session s, string address, List<string> trusted)
        {
            if (trusted == null || trusted.Count == 0) return;
            // A blind trust may reach a CPU tiad cannot verify: no earlier
            // logon in this instance shows anything about a CPU from now.
            instanceLogons.ForgetAll(call + " trusted a CPU certificate blindly in this TIA instance");
            var q = new List<string>();
            foreach (string x in trusted) q.Add(Json.Quote(x));
            if (blindTrust == null)
            {
                blindTrust = new BlindTrust
                {
                    Since = DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ", System.Globalization.CultureInfo.InvariantCulture),
                    Call = call, Session = s.Id, Address = address, Questions = new List<string>(trusted),
                };
                Log.Line("audit blind-trust set call={0} session={1} address={2} questions=[{3}]: every later online call in this TIA instance rides on it until the instance is retired or the agent restarts",
                    call, s.Id, Json.Quote(address), string.Join(",", q.ToArray()));
                return;
            }
            Log.Line("audit blind-trust again call={0} session={1} address={2} questions=[{3}] instance={4}",
                call, s.Id, Json.Quote(address), string.Join(",", q.ToArray()), BlindTrust.Audit(blindTrust));
        }

        // OnlinePassword reads the one secret an online call takes:
        // secrets.fullAccessPassword, the password of the CPU's user
        // "nautilus", which tiad answers when TIA asks credentials going
        // online (OnlineLogin). "" when the request carries none. The root's
        // Done refuses any other secret.
        // LoginFor is the login an online call in TIA instance t to
        // address answers with (OnlineLogin.For): a credentials question
        // tiad answers no forgets the instance-logon evidence for address;
        // one it answers marks the address logged on in t (and forgets
        // evidence of another password).
        OnlineLogin LoginFor(ITia t, string password, BlindTrust bt, string address, string call)
        {
            return OnlineLogin.For(password, bt, delegate
            {
                instanceLogons.Forget(address, call + ": tiad answered TIA's credentials question no");
            }, delegate
            {
                instanceLogons.MarkAnswered(t, address, password);
            });
        }

        // CompareOnline runs ITia.Compare for call on session s to address,
        // on the TIA thread (t is the instance it runs in), and reports
        // whether it came back equal (TiaNames.CompareEqual, the
        // differences into diffs). It keeps the instance-logon evidence
        // (InstanceLogons): a compare that logged on as "nautilus" with
        // password, with no blind trust, and came back equal records it; one
        // that asked credentials and did not log on, or failed after TIA
        // asked any, forgets it.
        OnlineCompare CompareOnline(ITia t, Session s, string address, string password, BlindTrust bt, string call, List<Difference> diffs, out bool equal)
        {
            var use = new LoginUse(LoginFor(t, password, bt, address, call), call, address);
            OnlineCompare cmp;
            try
            {
                cmp = t.Compare(s.Handle, address, use);
            }
            catch (Exception)
            {
                if (use.MayHaveAskedCredentials) instanceLogons.Forget(address, call + " failed after TIA asked credentials");
                throw;
            }
            // The data types of the session's import group are required by
            // name (TiaNames.CompareEqual); a session with no import group
            // (a fingerprint read, a bare compare, an older deploy's
            // verify) requires none.
            List<ProjectBlock> types = null;
            if (s.ImportGroup != "")
            {
                types = new List<ProjectBlock>();
                foreach (var ty in t.Types(s.Handle)) if (Ownership.Owned(ty.Group, s.ImportGroup)) types.Add(ty);
            }
            equal = TiaNames.CompareEqual(cmp == null ? null : cmp.Tree, cmp == null ? null : cmp.Blocks, types, diffs);
            if (use.MayHaveAskedCredentials && !use.LoggedOn)
            {
                instanceLogons.Forget(address, call + ": TIA asked credentials and tiad did not log on");
            }
            else if (equal && use.LoggedOn && bt == null && blindTrust == null)
            {
                instanceLogons.Record(t, address, password, call, s.Id);
            }
            return cmp;
        }

        static string OnlinePassword(Reader r)
        {
            Reader sec = r.Child("secrets");
            return sec == null ? "" : sec.Str("fullAccessPassword");
        }

        // TrustingCall runs an ITia call that took trustUnverifiedCertificate:
        // if it fails inside TIA's side, TIA may have trusted a certificate
        // before it failed, so that is recorded as trust. That includes
        // failures before going online (no CPU, another address): the safe
        // side, since tiad cannot tell from the failure how far it got.
        // Run on the TIA thread.
        T TrustingCall<T>(string call, Session s, string address, bool trust, Func<T> f)
        {
            try
            {
                return f();
            }
            catch (Exception e)
            {
                // A read refused for access control that answered no
                // certificate question trusted trusted nothing (#1336).
                var te = e as TiaException;
                bool none = te != null && te.AccessControlOn && te.TrustedNothing;
                if (trust && !none) RecordBlindTrust(call, s, address, new List<string> { UnknownTrust });
                throw;
            }
        }

        // BlindTrustJson is the instance's blind trust for a response: null
        // while it has none.
        object BlindTrustJson()
        {
            BlindTrust b = blindTrust;
            return b == null ? null : b.ToJson();
        }

        // BlindTrustRefusal is the 409 for a download BlindTrustRule
        // refuses, or null when it may go on.
        static TiadException BlindTrustRefusal(BlindTrust b, DownloadRequest req, Session s)
        {
            return BlindTrustRefusal(b, req, s, "nothing was sent to the CPU");
        }

        // BlindTrustRefusal with the suffix that says how far the download
        // got: the backstop on the TIA thread runs after the pin check and
        // the claim, just before TIA's download is called.
        static TiadException BlindTrustRefusal(BlindTrust b, DownloadRequest req, Session s, string got)
        {
            bool declined;
            string why = BlindTrustRule.Check(b, req.Decisions.AcceptBlindTrust, out declined);
            if (why == null) return null;
            var d = new Obj();
            d["questions"] = Question.ToJson(new List<Question> { BlindTrustRule.Question(b) });
            d["keptDevices"] = KeptDevice.ToJson(s.Kept);
            d["blindTrust"] = b.ToJson();
            return new TiadException(409, declined ? Kinds.Declined : Kinds.DecisionRequired, why + "; " + got, d, false);
        }

        object Fingerprint(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                HeldOnlineRefuses(s, "read the fingerprint");
                // Every read is journalled, refusals too: the request and
                // the gates are checked inside the audited read.
                FingerprintRun run = ReadFingerprintAudited(s, null, TrustFix, delegate(FingerprintRun r)
                {
                    var req = c.Json();
                    // An explicit, per-call opt-in, as an upload's.
                    r.Trust = req.Bool("trustUnverifiedCertificate");
                    // A fingerprint read never logs on (#1336): the
                    // password a client sends is read and not used.
                    OnlinePassword(req);
                    DeviceAttestation att = ReadDeviceAttestation(req);
                    req.Done();
                    r.Address = CpuOf(s);
                    if (!Target(r.Address)) throw new TiadException(403, Kinds.TargetRefused, r.Address + " is not in TIAD_TARGETS");
                    // An online call: the CPU must still be the pinned one.
                    CheckDevicePin(s, r.Address, "fingerprint read", att);
                });
                var o = new Obj();
                o["address"] = run.Address;
                foreach (var kv in run.ToJson()) o[kv.Key] = kv.Value;
                return o;
            });
        }

        // ReadFingerprintAudited reads the fingerprint of the session's CPU
        // through the trust path (TrustingCall, RecordBlindTrust), checks
        // the read is whole, and journals it on every outcome, refusals
        // included. prepare runs first, inside the journalled part: it sets
        // the opt-in and the address, and may refuse. deploy, when not
        // null, is the deploy the read belongs to (the post-load read);
        // fix is what a refusal of TIA's TLS question names.
        FingerprintRun ReadFingerprintAudited(Session s, string deploy, string fix, Action<FingerprintRun> prepare)
        {
            var run = new FingerprintRun { Address = s.Address };
            BlindTrust before = blindTrust;
            // after is the instance's trust as the TIA thread read it once
            // the read ended; ran says the read reached it.
            bool ran = false;
            string outcome = "";
            try
            {
                prepare(run);
                string address = run.Address;
                bool trust = run.Trust;
                run.Read = OnSession(s, delegate(ITia t)
                {
                    // A CPU tiad logged on to in this instance has access
                    // control on: refused before anything goes online, so
                    // nothing is trusted, even where TIA kept the logon
                    // (#1336).
                    if (instanceLogons.AnsweredBefore(t, address))
                    {
                        run.BlindTrust = blindTrust;
                        ran = true;
                        throw new TiadException(409, Kinds.FingerprintUnavailable, FingerprintRule.UnavailableBefore(address));
                    }
                    try
                    {
                        FingerprintRead fr = TrustingCall("fingerprint read", s, address, trust, delegate { return t.ReadFingerprint(s.Handle, address, trust); });
                        if (fr != null) RecordBlindTrust("fingerprint read", s, address, fr.TrustedBlindly);
                        return fr;
                    }
                    catch (TiaException e)
                    {
                        // The CPU's access control is on: no read, and no
                        // error either (#1336).
                        if (e.AccessControlOn) throw new TiadException(409, Kinds.FingerprintUnavailable, FingerprintRule.Unavailable(address, e));
                        throw;
                    }
                    finally
                    {
                        run.BlindTrust = blindTrust;
                        ran = true;
                    }
                });
                string bad = FingerprintRule.Check(run.Read);
                if (bad != null) throw new TiadException(500, Kinds.OperationFailed, "fingerprint read: " + bad);
                outcome = "ok";
            }
            catch (TiadException e)
            {
                outcome = e.Kind;
                // A certificate question answered no (TiaNames.OnlineRefused):
                // name the opt-in.
                if (!run.Trust && e.Kind == Kinds.OperationFailed && FingerprintRule.CertificateOnly(e.Message))
                {
                    throw new TiadException(e.Status, e.Kind, e.Message + "; " + fix, e.Payload, e.Fatal);
                }
                throw;
            }
            catch (Exception)
            {
                outcome = "error";
                throw;
            }
            finally
            {
                // Every value as TIA returned it, quoted and bounded
                // (FingerprintRule), only from a whole read.
                var items = new List<string>();
                var tr = new List<string>();
                FingerprintRead read = run.Read;
                if (read != null && outcome == "ok")
                {
                    foreach (KeyValuePair<string, string> kv in read.Items) items.Add(Json.Quote(kv.Key) + "=" + Json.Quote(kv.Value ?? ""));
                }
                if (read != null && read.TrustedBlindly != null)
                {
                    foreach (string x in read.TrustedBlindly) tr.Add(Json.Quote(x));
                }
                Log.Line("audit fingerprint session={0} project={1} address={2} trustUnverifiedCertificate={3} trustedBlindly=[{4}] blindTrustBefore={5} blindTrust={6} items=[{7}] outcome={8}{9}",
                    s.Id, Json.Quote(s.Project), Json.Quote(run.Address), run.Trust ? "yes" : "no", string.Join(",", tr.ToArray()),
                    BlindTrust.Audit(before), BlindTrust.Audit(ran ? run.BlindTrust : blindTrust), string.Join(", ", items.ToArray()), outcome,
                    deploy == null ? "" : " deploy=" + deploy);
            }
            return run;
        }
    }
}
