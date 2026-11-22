// Verify.cs: does the CPU still run a deploy? (siemens-ide.md sections
// 3.5 and 7, D7; #789 D1b.)
//
// POST /v1/deploys/verify opens a copy of the deploy's archive as a
// session of its own (one project at a time, as every session), checks
// that the archived project's CPU is at the record's address, and compares
// it with the CPU online, as a session's compare does. It changes nothing
// in the project or on the CPU and adds nothing to the journal: what it
// found is in its response and the audit line.
//
// An archive whose copy does not hash to the record's sha256 (swapped,
// altered, gone, or a record tiad did not write) is BROKEN, never equal,
// and is never opened. A record that names another CPU than its archived
// project's is refused before anything goes online, naming both: a
// mixed-up record must not point a compare at another CPU.

using System;
using System.Collections.Generic;

namespace Tiad
{
    public sealed partial class Agent
    {
        // Verdicts of a verify.
        public const string VerifyEqual = "equal", VerifyDifferent = "different", VerifyBroken = "broken";

        // VerifyTrustFix is the fix a verify's fingerprint read TIA's TLS
        // question refused names.
        const string VerifyTrustFix = "to read it with the verify, trust the CPU's certificate blindly (fingerprint.trustUnverifiedCertificate: true); the agent's TIA instance then keeps that trust until the verify's session closes (the agent then retires that instance)";

        static string RecordStr(IDictionary<string, object> o, string key)
        {
            object v;
            return o != null && o.TryGetValue(key, out v) ? v as string : null;
        }

        // RecordFingerprintValue is the item id of the fingerprint a
        // record's outcome holds, or null.
        static string RecordFingerprintValue(IDictionary<string, object> record, string id)
        {
            object oc, fp, items;
            var outcome = record.TryGetValue("outcome", out oc) ? oc as IDictionary<string, object> : null;
            var f = outcome != null && outcome.TryGetValue("fingerprint", out fp) ? fp as IDictionary<string, object> : null;
            var list = f != null && f.TryGetValue("items", out items) ? items as System.Collections.IList : null;
            if (list == null) return null;
            foreach (object x in list)
            {
                var it = x as IDictionary<string, object>;
                if (RecordStr(it, "identifier") == id) return RecordStr(it, "value");
            }
            return null;
        }

        // Same is whether a read value is the record's: null when either
        // side has none.
        static object Same(string record, string now)
        {
            if (record == null || now == null) return null;
            return record == now;
        }

        // DeployIdShape: a deploy id as NewId makes one, or near enough:
        // what a session's project name and an audit line may carry.
        static bool DeployIdShape(string id)
        {
            if (id.Length == 0 || id.Length > 64) return false;
            foreach (char ch in id)
            {
                if (!((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '-')) return false;
            }
            return true;
        }

        object VerifyDeploy(Call c)
        {
            // A verify is a TIA call, broken or not: the gates first, as
            // every TIA call's.
            NeedTia();
            var r = c.Json();
            string id = r.Str("deploy");
            Reader fpr = r.Child("fingerprint");
            bool readFp = fpr != null;
            bool trust = readFp && fpr.Bool("trustUnverifiedCertificate");
            string password = OnlinePassword(r);
            r.Done();
            if (id == "") throw TiadException.BadRequest("verify: deploy is required");
            if (!DeployIdShape(id)) throw TiadException.NotFound("no deploy {0}", Json.Quote(id));

            // One project at a time: the verify's session is reserved
            // first, so a busy refusal touches nothing and no deploy runs
            // while the journal is read and the archive copied.
            Session s = sessions.Reserve("deploy:" + id, "", "");
            var o = new Obj();
            string dir = "", copy = "", address = "";
            IDictionary<string, object> record;
            try
            {
                record = VerifyRecord(id, o, out address);
                if (o.ContainsKey("broken") == false)
                {
                    object archive;
                    record.TryGetValue("archive", out archive);
                    string broken;
                    try
                    {
                        broken = deploys.VerifyStage(archive, out dir, out copy);
                    }
                    catch (Exception e)
                    {
                        throw new TiadException(500, Kinds.OperationFailed, "copying the deploy's archive for the verify failed (" + e.GetType().Name + ": " + e.Message + "); nothing was opened");
                    }
                    if (broken != null) o["broken"] = broken;
                }
            }
            catch
            {
                sessions.Drop(s);
                deploys.RemoveStaging(dir);
                RetireIfTrusted();
                throw;
            }
            if (o.ContainsKey("broken"))
            {
                sessions.Drop(s);
                RetireIfTrusted();
                Log.Line("audit verify deploy={0} address={1} verdict={2} why={3}", Json.Quote(id), Json.Quote(address), VerifyBroken, Json.Quote((string)o["broken"]));
                o["verdict"] = VerifyBroken;
                o["changedOutside"] = null;
                return o;
            }

            s.ProjectFull = copy;
            s.Staging = dir;
            try
            {
                s.Handle = OnTia(delegate(ITia t) { return t.OpenProject(copy); });
            }
            catch (Exception e)
            {
                sessions.Drop(s);
                deploys.RemoveStaging(dir);
                RetireIfTrusted();
                Log.Line("audit verify deploy={0} address={1} failed: opening the archive: {2}", Json.Quote(id), Json.Quote(address), Json.Quote(e.Message));
                throw;
            }
            Obj result = null;
            Exception failed = null;
            try
            {
                result = VerifyOpen(s, id, address, record, trust, readFp, password, o);
            }
            catch (Exception e)
            {
                failed = e;
                var te = e as TiadException;
                Log.Line("audit verify deploy={0} session={1} address={2} failed kind={3}: {4}", Json.Quote(id), s.Id, Json.Quote(address), te == null ? "error" : te.Kind, Json.Quote(e.Message));
            }
            string closeError = null;
            try
            {
                // Closed while still in flight, so no other call slips
                // onto the verify's session; End only once it is out.
                EndSession(s);
            }
            catch (TiadException ce)
            {
                // The session stays, broken, with its staging: a DELETE
                // closes it again. What the verify found still stands.
                closeError = "closing the verify's session " + s.Id + " failed (" + ce.Message + "): close it again (DELETE /v1/sessions/" + s.Id + "), or restart the agent";
                Log.Line("verify deploy={0}: {1}", Json.Quote(id), closeError);
            }
            finally
            {
                sessions.End(s);
            }
            if (failed != null)
            {
                var fe = failed as TiadException;
                if (closeError != null && fe != null) throw new TiadException(fe.Status, fe.Kind, fe.Message + "; and " + closeError, fe.Payload, fe.Fatal);
                System.Runtime.ExceptionServices.ExceptionDispatchInfo.Capture(failed).Throw();
            }
            if (closeError != null) result["closeError"] = closeError;
            return result;
        }

        // VerifyRecord reads deploy id's record from the journal into o
        // (deploy, address, status, latest, unreadableLines), refusing an
        // unknown deploy or a CPU that is not a target; a record whose
        // address is not an IPv4 address sets o's "broken".
        IDictionary<string, object> VerifyRecord(string id, Obj o, out string address)
        {
            int unreadable;
            List<object> all;
            try
            {
                all = deploys.List("", out unreadable);
            }
            catch (Exception e)
            {
                if (!(e is System.IO.IOException) && !(e is UnauthorizedAccessException)) throw;
                throw new TiadException(500, Kinds.OperationFailed, "the deploy journal cannot be read (" + e.Message + ")");
            }
            IDictionary<string, object> record = null;
            foreach (object x in all)
            {
                var d = (IDictionary<string, object>)x;
                if (RecordStr(d, "id") == id)
                {
                    record = d;
                    break;
                }
            }
            if (record == null) throw TiadException.NotFound("no deploy {0}", Json.Quote(id));
            address = RecordStr(record, "address") ?? "";
            o["deploy"] = id;
            o["address"] = address;
            o["status"] = RecordStr(record, "status") ?? "";
            // latest is the newest record for the address that may have
            // loaded it (loaded or in doubt): what the CPU should run.
            string latest = null;
            foreach (object x in all)
            {
                var d = (IDictionary<string, object>)x;
                if (RecordStr(d, "address") == address && RecordStr(d, "status") != DeployStatus.NotLoaded)
                {
                    latest = RecordStr(d, "id");
                    break;
                }
            }
            o["latest"] = latest;
            // A line tiad could not read may be a newer deploy: then
            // nothing is said of changes outside (VerifyOpen).
            o["unreadableLines"] = unreadable;
            if (!Ip.IsIPv4(address))
            {
                o["broken"] = "the record's address " + Json.Quote(address) + " is not an IPv4 address";
            }
            else if (!Target(address))
            {
                throw new TiadException(403, Kinds.TargetRefused, address + " is not in TIAD_TARGETS");
            }
            return record;
        }

        // VerifyOpen is the verify's part on its open session: the address
        // check, the device pin, the compare, the optional fingerprint read.
        Obj VerifyOpen(Session s, string id, string address, IDictionary<string, object> record, bool trust, bool readFp, string password, Obj o)
        {
            o["session"] = s.Id;
            string at = OnSession(s, delegate(ITia t) { return t.ProjectCpuAddress(s.Handle); }) ?? "";
            if (at != address)
            {
                var data = new Obj();
                data["recordAddress"] = address;
                data["projectAddress"] = at;
                Log.Line("audit verify deploy={0} address={1} refused: the archived project's CPU is at {2}", Json.Quote(id), Json.Quote(address), Json.Quote(at));
                throw new TiadException(409, Kinds.CpuMismatch,
                    "deploy " + id + " names CPU " + address + ", but its archived project's CPU is " + (at == "" ? "at no address" : "at " + at) +
                    ": the verify was refused before anything went online (a record and an archive that disagree name no CPU to compare)", data, false);
            }
            s.Address = address;
            CheckDevicePin(s, address, "verify");
            var diffs = new List<Difference>();
            BlindTrust rode = null;
            bool eq = false;
            OnlineCompare cmp = OnSession(s, delegate(ITia t)
            {
                BlindTrust bt = blindTrust;
                rode = bt;
                if (bt != null)
                {
                    Log.Line("audit compare session={0} project={1} address={2} blindTrust={3}", s.Id, Json.Quote(s.Project), Json.Quote(address), BlindTrust.Audit(bt));
                }
                return CompareOnline(t, s, address, password, bt, "verify", diffs, out eq);
            });
            bool equal = eq;
            var list = new List<object>();
            foreach (var d in diffs)
            {
                var x = new Obj();
                x["object"] = d.Object;
                x["state"] = d.State;
                list.Add(x);
            }
            string verdict = equal ? VerifyEqual : VerifyDifferent;
            o["verdict"] = verdict;
            o["differences"] = list;
            o["blocks"] = new List<object>(TiaNames.Labels(cmp == null ? null : cmp.Blocks).ToArray());
            o["blindTrust"] = rode == null ? null : rode.ToJson();
            // Only a loaded record says what the CPU ran: changed outside
            // is a different CPU under the newest such record for it.
            object changed = null;
            if (RecordStr(record, "status") == DeployStatus.Loaded && (int)o["unreadableLines"] == 0)
            {
                changed = !equal && (o["latest"] as string) == id;
            }
            o["changedOutside"] = changed;
            if (readFp)
            {
                FingerprintRun fp = null;
                try
                {
                    fp = ReadFingerprintAudited(s, null, VerifyTrustFix, delegate(FingerprintRun run)
                    {
                        run.Trust = trust;
                        run.Address = address;
                        run.Password = password;
                    });
                }
                catch (TiadException e)
                {
                    // The session or TIA gone is no read that failed.
                    if (e.Fatal) throw;
                    o["fingerprintError"] = e.Message;
                }
                if (fp != null)
                {
                    var f = fp.ToJson();
                    f["downloadIdSame"] = Same(RecordFingerprintValue(record, "DownloadId"), fp.Value("DownloadId"));
                    f["hardwareSame"] = Same(RecordFingerprintValue(record, "Hardware"), fp.Value("Hardware"));
                    o["fingerprint"] = f;
                }
                else
                {
                    o["fingerprint"] = null;
                }
            }
            Log.Line("audit verify deploy={0} session={1} address={2} verdict={3} changedOutside={4} differences={5}{6}",
                Json.Quote(id), s.Id, Json.Quote(address), verdict, changed == null ? "unknown" : ((bool)changed ? "yes" : "no"), diffs.Count,
                readFp ? " fingerprint=" + (o["fingerprint"] == null ? "failed" : "read") : "");
            return o;
        }
    }
}
