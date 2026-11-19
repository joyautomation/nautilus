// DownloadCalls.cs: download, compare, export, upload and save
// (siemens-ide.md sections 3.4 and 3.5).
//
// A download's refusals all come before TIA is asked anything: the
// request's shape (announced, dry runs included), the target allowlist,
// one station spec per CPU, a full compile since the last change, and one
// project per CPU. Only then does ITia.Download run. It calls decide
// exactly once, before anything loads; decide cancels unless every
// question has an answer in the request, and then hands TIA one resolved
// answer per question. Anything that leaves the CPU's state in doubt
// answers outcome_unknown, never success and never a plain refusal.

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;

namespace Tiad
{
    public sealed partial class Agent
    {
        void RegisterDownload(Router r)
        {
            r.Add("POST", "/v1/sessions/{id}/download", Download);
            r.Add("POST", "/v1/sessions/{id}/compare", Compare);
            r.Add("POST", "/v1/sessions/{id}/export", Export);
            r.Add("POST", "/v1/sessions/{id}/upload", Upload);
            r.Add("POST", "/v1/sessions/{id}/save", Save);
        }

        bool Target(string address)
        {
            return cfg.Targets.Contains(address);
        }

        static TiadException Unknown(string why)
        {
            return new TiadException(500, Kinds.OutcomeUnknown,
                why + "; the CPU may or may not be loaded: compare before anything else", null, false);
        }

        static string Ids(List<Question> qs)
        {
            var ids = new List<string>();
            foreach (var q in qs) ids.Add(Json.Quote(q.Id));
            return string.Join(",", ids);
        }

        static string YesNo(bool? b)
        {
            return b.HasValue ? (b.Value ? "yes" : "no") : "unanswered";
        }

        // AuditDecisions is the decisions for the audit line. Names the
        // request supplies are quoted, so none can forge a line.
        static string AuditDecisions(Decisions d)
        {
            var reinit = new List<string>();
            foreach (var n in d.Reinitialize) reinit.Add(Json.Quote(n));
            return "stopCpu=" + YesNo(d.StopCpu) + " overwrite=" + YesNo(d.Overwrite) +
                " startAfter=" + YesNo(d.StartAfter) + " reinitialize=[" + string.Join(",", reinit) + "]" +
                " trustCertificate=" + (d.TrustCertificate == "" ? "unset" : Json.Quote(d.TrustCertificate));
        }

        object Download(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                DownloadRequest req = DownloadRequest.Read(r);
                r.Done();

                // One audit line per download, refusals included (#709):
                // what was asked and decided, and what came of it. Secrets
                // appear only as set or unset.
                string asked = "";
                string outcome = "";
                try
                {
                    object result = DownloadChecked(s, req, ref asked, ref outcome);
                    return result;
                }
                catch (TiadException e)
                {
                    if (outcome == "") outcome = e.Kind;
                    throw;
                }
                catch (Exception)
                {
                    if (outcome == "") outcome = "error";
                    throw;
                }
                finally
                {
                    Log.Line("audit download session={0} project={1} address={2} hardware={3} software={4} dryRun={5} announced={6} {7} protection={8} asked=[{9}] outcome={10}",
                        s.Id, Json.Quote(s.Project), Json.Quote(req.Address), req.Hardware, req.Software, req.DryRun, req.Announced,
                        AuditDecisions(req.Decisions), req.Secrets.Protection == "" ? "unset" : "set", asked, outcome);
                }
            });
        }

        object DownloadChecked(Session s, DownloadRequest req, ref string asked, ref string outcome)
        {
            string bad = req.Validate();
            if (bad != null) throw TiadException.BadRequest("{0}", bad);
            if (!Target(req.Address))
            {
                throw new TiadException(403, Kinds.TargetRefused, req.Address + " is not in TIAD_TARGETS");
            }
            if (s.Station == null) throw TiadException.BadRequest("the project has no station spec");
            // A dry run is a download cancelled before anything loads. A TIA
            // side that cannot promise that cancel (tiad.Openness, until S2b
            // shows TIA always asks before it loads) gets none.
            if (req.DryRun && !OnSession(s, delegate(ITia t) { return t.CanDryRun; }))
            {
                throw TiadException.BadRequest("{0}", "dry runs are refused: this agent's TIA side cannot promise a dry run loads nothing until S2b proves TIA always asks before it loads (siemens-ide.md section 3.5); nothing was sent to the CPU");
            }
            if (s.Station.Cpu.Address != req.Address)
            {
                throw TiadException.BadRequest("the project's CPU is at {0}, not {1}: one station spec per CPU", s.Station.Cpu.Address, req.Address);
            }
            if (!s.Compiled)
            {
                throw new TiadException(409, Kinds.NotCompiled, "the project changed since the last full compile; compile hardware and software first");
            }
            // One project per CPU: the first download claims the CPU for
            // this session, dry runs included, until it is closed.
            sessions.ClaimCpu(s, req.Address);

            bool haveProtection = req.Secrets.Protection != "";
            var unanswered = new List<Question>();
            var declined = new List<Question>();
            List<Question> askedQs = null;
            int decided = 0;
            bool approved = false;
            Func<List<Question>, List<Answer>> decide = delegate(List<Question> qs)
            {
                decided++;
                if (decided > 1) return null; // a second decide is a broken ITia: cancel it
                askedQs = new List<Question>();
                foreach (var q in qs ?? new List<Question>()) askedQs.Add(q.Copy());
                if (req.DryRun) return null;
                DownloadRule.Resolve(askedQs, req.Decisions, haveProtection, unanswered, declined);
                if (unanswered.Count > 0 || declined.Count > 0) return null;
                approved = true;
                return DownloadRule.Answers(askedQs, req.Decisions);
            };

            DownloadOutcome result;
            try
            {
                result = OnSession(s, delegate(ITia t)
                {
                    return t.Download(s.Handle, req.Address, req.Hardware, req.Software, req.Secrets, decide);
                });
            }
            catch (TiadException e)
            {
                // An outcome_unknown from TIA's side (its download had begun
                // when it failed) passes through as it is.
                if (askedQs != null) asked = Ids(askedQs);
                // TIA failed once the download was decided. Whether a
                // cancel loads nothing is unproven, so from there on a
                // failure leaves the CPU in doubt.
                if (decided > 0 && e.Kind == Kinds.OperationFailed)
                {
                    if (askedQs != null) asked = Ids(askedQs);
                    outcome = Kinds.OutcomeUnknown;
                    throw Unknown("TIA failed during the download (" + e.Message + ")");
                }
                throw;
            }
            if (askedQs != null) asked = Ids(askedQs);

            // The ITia contract, checked: decide exactly once, and a load
            // only when decide approved.
            if (decided != 1)
            {
                outcome = Kinds.OutcomeUnknown;
                throw Unknown("TIA's side called decide " + decided + " times, not once");
            }
            if (result.Loaded && !approved)
            {
                outcome = Kinds.OutcomeUnknown;
                throw Unknown("TIA loaded the CPU although tiad cancelled the download");
            }
            var o = new Obj();
            if (req.DryRun)
            {
                outcome = "dry-run";
                o["dryRun"] = true;
                o["downloaded"] = false;
                // Provisional until S2 proves a cancelled download loads
                // nothing (section 3.5).
                o["provisional"] = true;
                o["questions"] = Question.ToJson(askedQs);
                return o;
            }
            if (unanswered.Count > 0)
            {
                outcome = Kinds.DecisionRequired + ":[" + Ids(unanswered) + "]";
                var d = new Obj();
                d["questions"] = Question.ToJson(unanswered);
                throw new TiadException(409, Kinds.DecisionRequired, unanswered.Count + " question(s) have no answer; nothing was loaded", d, false);
            }
            if (declined.Count > 0)
            {
                outcome = Kinds.Declined + ":[" + Ids(declined) + "]";
                var d = new Obj();
                d["questions"] = Question.ToJson(declined);
                throw new TiadException(409, Kinds.Declined, declined.Count + " question(s) were answered no; nothing was loaded", d, false);
            }
            if (!result.Loaded)
            {
                outcome = Kinds.OutcomeUnknown;
                throw Unknown("TIA approved the download but did not report a load");
            }
            outcome = "loaded state=" + (result.State == "" ? "unknown" : result.State);
            o["dryRun"] = false;
            o["downloaded"] = true;
            o["questions"] = Question.ToJson(askedQs);
            if (result.State != "") o["state"] = result.State;
            return o;
        }

        // CpuOf is the CPU a session's project belongs to, which compare,
        // upload and export use; a project no spec has ever been applied
        // to has none.
        string CpuOf(Session s)
        {
            if (s.Address == "") throw TiadException.BadRequest("the project has no station spec");
            return s.Address;
        }

        object Compare(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                r.Done();
                string address = CpuOf(s);
                if (!Target(address)) throw new TiadException(403, Kinds.TargetRefused, address + " is not in TIAD_TARGETS");
                var diffs = new List<Difference>();
                bool equal = OnSession(s, delegate(ITia t) { return t.Compare(s.Handle, address, diffs); });
                var list = new List<object>();
                foreach (var d in diffs)
                {
                    var x = new Obj();
                    x["object"] = d.Object;
                    x["state"] = d.State;
                    list.Add(x);
                }
                var o = new Obj();
                o["equal"] = equal;
                o["differences"] = list;
                return o;
            });
        }

        object Export(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                string dir = r.Str("dir");
                var opt = new ExportOptions
                {
                    Blocks = r.Str("blocks"), TagTables = r.Bool("tagTables"), Hardware = r.Bool("hardware"), Gsdml = r.Bool("gsdml"),
                };
                r.Done();
                string dirFull = work.Resolve(dir);
                string clean = Paths.CleanPath(dir);
                if (opt.Blocks != "" && opt.Blocks != "scl" && opt.Blocks != "xml")
                {
                    throw TiadException.BadRequest("blocks {0}: one of scl, xml or empty", Json.Quote(opt.Blocks));
                }
                if (opt.Hardware || opt.Gsdml) CpuOf(s);
                // An export never writes into an open project's directory.
                Session holder = sessions.ProjectDirHolding(clean);
                if (holder != null)
                {
                    throw new TiadException(409, Kinds.Busy, clean + " is inside the directory of the open project " + holder.Project);
                }
                if (File.Exists(dirFull)) throw TiadException.BadRequest("{0} is a file", work.Rel(dirFull));
                Directory.CreateDirectory(dirFull);
                List<string> written = OnSession(s, delegate(ITia t) { return t.Export(s.Handle, dirFull, opt); });
                var files = new List<object>();
                foreach (string f in written) files.Add(work.RelInside(f));
                var o = new Obj();
                o["files"] = files;
                return o;
            });
        }

        object Upload(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                r.Done();
                string address = s.Address;
                if (address == "" || !Target(address))
                {
                    throw new TiadException(403, Kinds.TargetRefused, "the project's CPU is not in TIAD_TARGETS");
                }
                OnSession<object>(s, delegate(ITia t)
                {
                    // The CPU's program replaces the project's: it no
                    // longer matches its spec, and it must take a spec and
                    // compile again before anything is downloaded.
                    s.Compiled = false;
                    s.Station = null;
                    t.Upload(s.Handle, address);
                    return null;
                });
                return new Obj();
            });
        }

        object Save(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                bool archive = r.Bool("archive");
                r.Done();
                string full = OnSession(s, delegate(ITia t) { return t.Save(s.Handle, archive); });
                var o = new Obj();
                o["path"] = work.RelInside(full);
                return o;
            });
        }
    }
}
