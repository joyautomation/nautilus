// DownloadCalls.cs: download, compare, export, upload and save
// (siemens-ide.md sections 3.4 and 3.5).
//
// A download's refusals all come before TIA is asked anything: the
// request's shape, the target allowlist, one station spec per CPU, a full
// compile since the last change. Only then does ITia.Download run, and it
// loads nothing unless decide approves every question TIA asked.

using System;
using System.Collections.Generic;
using System.IO;

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

        object Download(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                DownloadRequest req = DownloadRequest.Read(r);
                r.Done();
                string bad = req.Validate();
                if (bad != null) throw TiadException.BadRequest("{0}", bad);
                if (!Target(req.Address))
                {
                    throw new TiadException(403, Kinds.TargetRefused, req.Address + " is not in TIAD_TARGETS");
                }
                if (s.Station == null) throw TiadException.BadRequest("the project has no station spec");
                if (s.Station.Cpu.Address != req.Address)
                {
                    throw TiadException.BadRequest("the project's CPU is at {0}, not {1}: one station spec per CPU", s.Station.Cpu.Address, req.Address);
                }
                if (!s.Compiled)
                {
                    throw new TiadException(500, Kinds.OperationFailed, "the project changed since the last full compile; compile hardware and software first");
                }

                bool haveProtection = req.Secrets.Protection != "";
                var unanswered = new List<Question>();
                var declined = new List<Question>();
                bool approved = false;
                Func<List<Question>, bool> decide = delegate(List<Question> qs)
                {
                    // A dry run lists the checks and cancels. Whether a
                    // cancelled download loads nothing is provisional
                    // until S2 proves it (section 3.5).
                    if (req.DryRun) return false;
                    DownloadRule.Resolve(qs, req.Decisions, haveProtection, unanswered, declined);
                    approved = unanswered.Count == 0 && declined.Count == 0;
                    return approved;
                };
                DownloadOutcome outcome = OnSession(s, delegate(ITia t)
                {
                    return t.Download(s.Handle, req.Address, req.Hardware, req.Software, req.Decisions, req.Secrets, decide);
                });
                if (outcome.Loaded && !approved)
                {
                    // TIA loaded what tiad cancelled. Never report that as
                    // anything but a failure.
                    throw new TiadException(500, Kinds.OperationFailed, "TIA loaded the CPU although tiad cancelled the download; compare before anything else", null, true);
                }
                var o = new Obj();
                if (req.DryRun)
                {
                    o["dryRun"] = true;
                    o["downloaded"] = false;
                    o["questions"] = Question.ToJson(outcome.Questions);
                    return o;
                }
                if (unanswered.Count > 0)
                {
                    var d = new Obj();
                    d["questions"] = Question.ToJson(unanswered);
                    throw new TiadException(409, Kinds.DecisionRequired, unanswered.Count + " question(s) have no answer; nothing was loaded", d, false);
                }
                if (declined.Count > 0)
                {
                    var d = new Obj();
                    d["questions"] = Question.ToJson(declined);
                    throw new TiadException(409, Kinds.Declined, declined.Count + " question(s) were answered no; nothing was loaded", d, false);
                }
                if (!outcome.Loaded)
                {
                    throw new TiadException(500, Kinds.OperationFailed, "TIA did not load the CPU");
                }
                o["dryRun"] = false;
                o["downloaded"] = true;
                o["questions"] = Question.ToJson(outcome.Questions);
                if (outcome.State != "") o["state"] = outcome.State;
                return o;
            });
        }

        object Compare(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                r.Done();
                if (s.Station == null) throw TiadException.BadRequest("the project has no station spec");
                string address = s.Station.Cpu.Address;
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
                if (opt.Blocks != "" && opt.Blocks != "scl" && opt.Blocks != "xml")
                {
                    throw TiadException.BadRequest("blocks {0}: one of scl, xml or empty", Json.Quote(opt.Blocks));
                }
                if ((opt.Hardware || opt.Gsdml) && s.Station == null)
                {
                    throw TiadException.BadRequest("the project has no station spec");
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
                if (s.Station == null || !Target(s.Station.Cpu.Address))
                {
                    throw new TiadException(403, Kinds.TargetRefused, "the project's CPU is not in TIAD_TARGETS");
                }
                string address = s.Station.Cpu.Address;
                OnSession<object>(s, delegate(ITia t)
                {
                    // The CPU's program replaces the project's: it must
                    // compile again before anything is downloaded.
                    s.Compiled = false;
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
