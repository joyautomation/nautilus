// DownloadCalls.cs: download, compare, export, upload and save
// (siemens-ide.md sections 3.4 and 3.5).
//
// A download's refusals all come before TIA is asked anything: the
// request's shape (announced, dry runs included), the target allowlist,
// one station spec per CPU, a full compile since the last change, and one
// project per CPU, and (for a real download) the hardware opt-in with its
// attestation (HardwareRule), the kept devices, a user-management reset
// (UserManagementRule) and the start-after decision.
// Only then does ITia.Download run. TIA asks its checks one at a time
// (#789 S2b), so each is answered on its own, only from the request: the
// first one the request does not answer yes cancels there. Anything that
// leaves the CPU's state in doubt answers outcome_unknown, never success
// and never a plain refusal, and names any acting answer tiad gave before
// the cancel (DownloadChecks.Acting).

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

        // Unknown is outcome_unknown: the CPU may or may not be loaded.
        // acting is the acting answers tiad gave TIA before it ended
        // (question ids): the answer names them, says plainly what they
        // may have done to the CPU, and lists them (data.actingAnswers).
        static TiadException Unknown(string why, List<string> acting)
        {
            return Unknown(why, acting, false);
        }

        static TiadException Unknown(string why, List<string> acting, bool fatal)
        {
            return Unknown(why, acting, fatal, null);
        }

        // Unknown after TIA's download returned carries the CPU's mode as
        // TIA's messages said it (data.state, data.stateSource), when they
        // said one: evidence of what TIA did, as on a success.
        static TiadException Unknown(string why, List<string> acting, bool fatal, DownloadOutcome result)
        {
            string c = DownloadChecks.Consequence(acting);
            string msg = why + (c == "" ? "" : "; " + c) + "; the CPU may or may not be loaded";
            if (!fatal) msg += ": compare before anything else";
            Obj data = ActingData(acting);
            if (result != null && result.State != "")
            {
                if (data == null) data = new Obj();
                data["state"] = result.State;
                data["stateSource"] = result.StateSource;
            }
            return new TiadException(500, Kinds.OutcomeUnknown, msg, data, fatal);
        }

        static Obj ActingData(List<string> acting)
        {
            if (acting == null || acting.Count == 0) return null;
            var l = new List<object>();
            foreach (string a in acting) l.Add(a);
            var data = new Obj();
            data["actingAnswers"] = l;
            return data;
        }

        // WithActing is TIA's side's own outcome_unknown, which already
        // says the CPU is in doubt, with the acting answers tiad gave
        // named.
        static TiadException WithActing(TiadException e, List<string> acting)
        {
            string c = DownloadChecks.Consequence(acting);
            if (c == "") return e;
            return new TiadException(e.Status, e.Kind, e.Message + "; " + c, ActingData(acting), e.Fatal);
        }

        // MayHaveStarted adds start-after to the acting answers of a real
        // download whose request said start it, when it ends in doubt
        // after TIA's call: TIA's after-download callback may have written
        // StartModule before the failure (#1154 review).
        static void MayHaveStarted(DownloadRequest req, List<string> acting)
        {
            if (req.DryRun || !req.Decisions.StartAfter.HasValue || !req.Decisions.StartAfter.Value) return;
            if (!acting.Contains(QuestionIds.StartAfter)) acting.Add(QuestionIds.StartAfter);
        }

        // UnknownAfter is Unknown for a failure TIA's side reported once it
        // asked a check; one that ended the session (TIA died) keeps its
        // own text, which names the fix, and stays fatal.
        static TiadException UnknownAfter(TiadException e, List<string> acting)
        {
            if (e.Fatal) return Unknown(e.Message, acting, true);
            return Unknown("TIA failed during the download (" + e.Message + ")", acting);
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
        static List<string> Quoted(List<string> names)
        {
            var q = new List<string>();
            foreach (var n in names) q.Add(Json.Quote(n));
            return q;
        }

        static string AuditDecisions(Decisions d)
        {
            var reinit = new List<string>();
            foreach (var n in d.Reinitialize) reinit.Add(Json.Quote(n));
            return "stopCpu=" + YesNo(d.StopCpu) + " overwrite=" + YesNo(d.Overwrite) + " consistentDownload=" + YesNo(d.ConsistentDownload) +
                " alarmTexts=" + YesNo(d.AlarmTexts) + " allBlocks=" + YesNo(d.AllBlocks) +
                " userManagementKeepOnline=" + YesNo(d.UserManagementKeepOnline) + " userManagementResetToProject=" + YesNo(d.UserManagementResetToProject) +
                " identificationDataLoadNothing=" + YesNo(d.IdentificationDataLoadNothing) +
                " startAfter=" + YesNo(d.StartAfter) + " reinitialize=[" + string.Join(",", reinit) + "]" +
                " keptDevices=[" + string.Join(",", Quoted(d.KeptDevices)) + "]" +
                " trustCertificate=" + (d.TrustCertificate == "" ? "unset" : Json.Quote(d.TrustCertificate)) +
                " acceptUnverifiedHardware=" + YesNo(d.AcceptUnverifiedHardware) + " acceptBlindTrust=" + YesNo(d.AcceptBlindTrust);
        }

        // AuditKept is the devices an apply kept as found, for the audit
        // line, each quoted.
        static string AuditKept(List<KeptDevice> kept)
        {
            var l = new List<string>();
            foreach (var k in kept) l.Add(k.Describe());
            return string.Join(",", l.ToArray());
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
                // userManagementReset is what came of a reset the request
                // asked: unset (not asked, or the download ended before
                // tiad checked it: a bad request, the hardware gate, the
                // kept devices, a dry run), refused, allowed:<route> (TIA
                // never asked it) or given:<route> (tiad answered TIA's
                // check reset; a write TIA then refused still reads given,
                // the safe side).
                string reset = "unset";
                var acting = new List<string>();
                try
                {
                    object result = DownloadChecked(s, req, ref asked, ref outcome, ref reset, acting);
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
                    Log.Line("audit download session={0} project={1} address={2} hardware={3} software={4} dryRun={5} announced={6} {7} protection={8} configurationPassword={9} attestation={10} userManagementReset={11} blindTrust={12} kept=[{13}] asked=[{14}] acting=[{15}] outcome={16}",
                        s.Id, Json.Quote(s.Project), Json.Quote(req.Address), req.Hardware, req.Software, req.DryRun, req.Announced,
                        AuditDecisions(req.Decisions), req.Secrets.Protection == "" ? "unset" : "set",
                        req.Secrets.ConfigurationPassword == "" ? "unset" : "set", req.Attestation.Audit(), reset, BlindTrust.Audit(blindTrust),
                        AuditKept(s.Kept), asked, string.Join(",", Quoted(acting).ToArray()), outcome);
                }
            });
        }

        object DownloadChecked(Session s, DownloadRequest req, ref string asked, ref string outcome, ref string resetAudit, List<string> acting)
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
            // The hardware configuration loads unverified (#789 hardware
            // gate): a real download that loads it needs the opt-in and
            // the operator's attestation, checked before anything reaches
            // the CPU, on every CPU. A dry run lists the question instead.
            if (!req.DryRun)
            {
                bool hwDeclined;
                string hwWhy = HardwareRule.Check(req, out hwDeclined);
                if (hwWhy != null)
                {
                    asked = Json.Quote(QuestionIds.UnverifiedHardware);
                    string kind = hwDeclined ? Kinds.Declined : Kinds.DecisionRequired;
                    outcome = kind + ":[" + asked + "]";
                    var hd = new Obj();
                    hd["questions"] = Question.ToJson(new List<Question> { HardwareRule.Question() });
                    hd["keptDevices"] = KeptDevice.ToJson(s.Kept);
                    throw new TiadException(409, kind, hwWhy + "; nothing was sent to the CPU", hd, false);
                }
            }
            // A dry run passes the gate unanswered: it lists the question.
            // That holds only while a dry run cannot load. If TIA runs one
            // without asking, it loads (outcome_unknown, below), so before
            // CanDryRun is ever true for tiad.Openness, a hardware dry run
            // must need the opt-in too (#1166 review).
            // The devices an apply kept load too: a real download needs a
            // yes naming exactly them, checked before anything reaches the
            // CPU. A dry run lists the question instead.
            string keptWhy = KeptRule.Check(s.Kept, req.Decisions.KeptDevices);
            if (keptWhy != null && !req.DryRun)
            {
                asked = Json.Quote(QuestionIds.KeptDevices);
                outcome = Kinds.DecisionRequired + ":[" + asked + "]";
                var kd = new Obj();
                kd["questions"] = Question.ToJson(new List<Question> { KeptRule.Question(s.Kept) });
                kd["keptDevices"] = KeptDevice.ToJson(s.Kept);
                throw new TiadException(409, Kinds.DecisionRequired, keptWhy + "; nothing was sent to the CPU", kd, false);
            }
            // Kept only when the spec keeps it AND no apply on this session
            // ever set it: keep after fullAccessUser keeps what tiad set.
            bool accessControlKept = s.Station != null && s.Station.Cpu != null && s.Station.Cpu.AccessControl == StationRules.AccessKeep && !s.AccessControlSet;
            // A user-management reset acts on the CPU's users and access
            // control: a real download that asks it is checked here, before
            // anything reaches the CPU (UserManagementRule), and the same
            // verdict answers TIA's check below. Anything else leaves it
            // refused. A dry run never answers.
            var reset = new UserManagementRule.Gate();
            if (!req.DryRun && req.Decisions.UserManagementResetToProject == true)
            {
                reset = UserManagementRule.Check(req, accessControlKept, s.UploadedFrom, UserManagementRule.UploadRouteEnabled, delegate
                {
                    return OnSession(s, delegate(ITia t)
                    {
                        ProjectCpu pc = t.ReadCpu(s.Handle);
                        return pc == null ? null : pc.AccessControl;
                    });
                });
                if (!reset.Allowed)
                {
                    resetAudit = "refused";
                    asked = Json.Quote(QuestionIds.UserManagement);
                    outcome = Kinds.DecisionRequired + ":[" + asked + "]";
                    var ud = new Obj();
                    ud["questions"] = Question.ToJson(new List<Question> { UserManagementRule.Question(reset.Why) });
                    ud["keptDevices"] = KeptDevice.ToJson(s.Kept);
                    throw new TiadException(409, Kinds.DecisionRequired, reset.Why + "; nothing was sent to the CPU", ud, false);
                }
                resetAudit = "allowed:" + reset.Route;
            }
            // Whether the CPU starts after loading is decided before TIA
            // is called: TIA asks it, if at all, only after loading. A dry
            // run never loads, so it needs no answer.
            if (!req.DryRun && !req.Decisions.StartAfter.HasValue)
            {
                var sq = new Question { Id = QuestionIds.StartAfter, Message = "start the CPU after loading", Detail = DownloadDecider.StartAfterType };
                asked = Json.Quote(QuestionIds.StartAfter);
                outcome = Kinds.DecisionRequired + ":[" + asked + "]";
                var sd = new Obj();
                sd["questions"] = Question.ToJson(new List<Question> { sq });
                sd["keptDevices"] = KeptDevice.ToJson(s.Kept);
                throw new TiadException(409, Kinds.DecisionRequired, "the request does not say whether the CPU starts after loading (decisions.startAfter); nothing was sent to the CPU", sd, false);
            }
            // A download, a dry run too, that would ride on this TIA
            // instance's blind trust needs the request's yes (BlindTrustRule),
            // checked before anything reaches the CPU.
            TiadException btRefused = BlindTrustRefusal(blindTrust, req, s);
            if (btRefused != null)
            {
                asked = Json.Quote(QuestionIds.BlindTrust);
                outcome = btRefused.Kind + ":[" + asked + "]";
                throw btRefused;
            }
            // The CPU must still be the one tiad pinned (section 3.8),
            // before a dry run too: it reaches the CPU.
            CheckDevicePin(s, req.Address, req.DryRun ? "dry run" : "download");
            // One project per CPU: the first download claims the CPU for
            // this session, dry runs included, until it is closed.
            sessions.ClaimCpu(s, req.Address);
            // The attestation is accepted here, once every refusal tiad
            // makes itself has passed and just before TIA is called: by
            // tiad's clock, never the request's.
            if (req.Hardware && !req.DryRun) req.Attestation.At = DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ", System.Globalization.CultureInfo.InvariantCulture);

            bool haveProtection = req.Secrets.Protection != "";
            bool haveConfigurationPassword = req.Secrets.ConfigurationPassword != "";
            string given = "";
            var unanswered = new List<Question>();
            var declined = new List<Question>();
            var askedQs = new List<Question>();
            int calls = 0;
            bool cancelled = false;
            // answer runs once per check TIA asks, in TIA's order. Each is
            // answered on its own, only from the request; the first one
            // not answered yes cancels the download there, and from then
            // on nothing is approved. A dry run cancels at the first.
            Func<Question, Answer> answer = delegate(Question q0)
            {
                calls++;
                var q = q0 == null ? new Question { Id = QuestionIds.Other } : q0.Copy();
                askedQs.Add(q);
                if (cancelled || req.DryRun)
                {
                    cancelled = true;
                    return null;
                }
                var one = new List<Question> { q };
                int u = unanswered.Count, d = declined.Count;
                DownloadRule.Resolve(one, req.Decisions, haveProtection, haveConfigurationPassword, accessControlKept, reset, unanswered, declined);
                if (unanswered.Count > u || declined.Count > d)
                {
                    cancelled = true;
                    return null;
                }
                Answer a = DownloadRule.Answers(one, req.Decisions)[0];
                // Recorded when given, before TIA's side writes it: a write
                // that then fails over-reports, which is the safe side.
                // Acting by what the answer writes (start-after no and
                // user-management keep change nothing).
                if (DownloadChecks.ActingAnswer(q.Id, a.Yes, a.Choice)) acting.Add(q.Id);
                if (q.Id == QuestionIds.UserManagement && a.Choice == Answer.ChoiceReset) given = reset.Route;
                return a;
            };

            DownloadOutcome result;
            try
            {
                result = OnSession(s, delegate(ITia t)
                {
                    // Again on the TIA thread, where trust is recorded: a
                    // call queued ahead of this one may have given it.
                    TiadException late = BlindTrustRefusal(blindTrust, req, s);
                    if (late != null) throw late;
                    // A dry run never starts the CPU, whatever the request
                    // says (#1154 review): if TIA loads it without asking,
                    // start-after is NoAction.
                    return t.Download(s.Handle, req.Address, req.Hardware, req.Software, req.Secrets, req.DryRun ? (bool?)null : req.Decisions.StartAfter, answer);
                });
            }
            catch (TiadException e)
            {
                asked = Ids(askedQs);
                // An outcome_unknown from TIA's side (its download had
                // begun when it failed) passes through, with the acting
                // answers tiad gave named.
                if (e.Kind == Kinds.OutcomeUnknown)
                {
                    outcome = Kinds.OutcomeUnknown;
                    MayHaveStarted(req, acting);
                    throw WithActing(e, acting);
                }
                // TIA failed once it asked a check: whether it acted on an
                // answer, or loaded, is unknown, so from there on a failure
                // leaves the CPU in doubt.
                if (calls > 0 && e.Kind == Kinds.OperationFailed)
                {
                    outcome = Kinds.OutcomeUnknown;
                    MayHaveStarted(req, acting);
                    throw UnknownAfter(e, acting);
                }
                throw;
            }
            finally
            {
                // A reset is journalled given only once tiad answered it.
                if (given != "") resetAudit = "given:" + given;
            }
            asked = Ids(askedQs);

            // The ITia contract, checked: a load only when nothing was
            // cancelled, and a dry run never loads.
            if (result.Loaded && (cancelled || req.DryRun))
            {
                outcome = Kinds.OutcomeUnknown;
                if (calls == 0) throw Unknown("TIA ran the dry run's download without asking a single check, so tiad could not cancel it", acting);
                throw Unknown("TIA loaded the CPU although tiad cancelled the download", acting);
            }
            if (!result.Loaded && calls == 0)
            {
                outcome = Kinds.OutcomeUnknown;
                throw Unknown("TIA's side neither asked a check nor loaded", acting);
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
                // TIA asks one check at a time, so a dry run, cancelled at
                // the first, sees only that one: the list is incomplete.
                o["questionsIncomplete"] = true;
                var dq = new List<Question>(askedQs);
                if (req.Hardware) dq.Add(HardwareRule.Question());
                if (s.Kept.Count > 0) dq.Add(KeptRule.Question(s.Kept));
                o["questions"] = Question.ToJson(dq);
                o["keptDevices"] = KeptDevice.ToJson(s.Kept);
                o["blindTrust"] = BlindTrustJson();
                return o;
            }
            // A cancel after an acting answer is never a clean refusal: TIA
            // may have acted on that answer before it asked the next check.
            if ((unanswered.Count > 0 || declined.Count > 0) && acting.Count > 0)
            {
                outcome = Kinds.OutcomeUnknown;
                var at = unanswered.Count > 0 ? unanswered[0] : declined[0];
                throw Unknown("tiad cancelled the download at TIA's " + at.Id + " check, which the request does not answer yes", acting);
            }
            if (unanswered.Count > 0)
            {
                outcome = Kinds.DecisionRequired + ":[" + Ids(unanswered) + "]";
                var d = new Obj();
                d["questions"] = Question.ToJson(unanswered);
                d["keptDevices"] = KeptDevice.ToJson(s.Kept);
                throw new TiadException(409, Kinds.DecisionRequired, "TIA asked a check the request does not answer; nothing was loaded (TIA asks one check at a time: answer it, and it may ask another)", d, false);
            }
            if (declined.Count > 0)
            {
                outcome = Kinds.Declined + ":[" + Ids(declined) + "]";
                var d = new Obj();
                d["questions"] = Question.ToJson(declined);
                d["keptDevices"] = KeptDevice.ToJson(s.Kept);
                throw new TiadException(409, Kinds.Declined, "TIA asked a check the request answers no; nothing was loaded", d, false);
            }
            if (!result.Loaded)
            {
                outcome = Kinds.OutcomeUnknown;
                MayHaveStarted(req, acting);
                throw Unknown("TIA approved the download but did not report a load", acting);
            }
            // Success only on evidence (#789): TIA's result says loaded, and
            // an online compare right after, in this same call, is equal.
            // Anything less is in doubt, naming the part that failed. The
            // compare covers the PLC software only: Openness has no online
            // hardware compare, so a hardware download is never verified
            // here (hardwareVerified: false).
            var diffs = new List<Difference>();
            bool equal;
            OnlineCompare cmp;
            try
            {
                cmp = OnSession(s, delegate(ITia t) { return t.Compare(s.Handle, req.Address); });
                equal = TiaNames.CompareEqual(cmp == null ? null : cmp.Tree, cmp == null ? null : cmp.Blocks, diffs);
            }
            catch (TiadException e)
            {
                outcome = Kinds.OutcomeUnknown;
                MayHaveStarted(req, acting);
                throw Unknown("TIA reported the download loaded, but the compare right after it failed (" + e.Message + ")", acting, e.Fatal, result);
            }
            if (!equal)
            {
                outcome = Kinds.OutcomeUnknown;
                MayHaveStarted(req, acting);
                var names = new List<string>();
                foreach (var d in diffs) names.Add((d.Object + " " + d.State).Trim());
                throw Unknown("TIA reported the download loaded, but the compare right after it differs (" + (names.Count == 0 ? "no differences named" : string.Join(", ", names.ToArray())) + ")", acting, false, result);
            }
            outcome = "loaded state=" + (result.State == "" ? "unknown" : result.State + " (" + result.StateSource + ")") + " compare=equal blocks=[" + TiaNames.BlockLabels(cmp.Blocks, 20) + "]" + (req.Hardware ? " hardwareVerified=false" : "");
            o["dryRun"] = false;
            o["downloaded"] = true;
            o["compareEqual"] = true;
            // The blocks the compare saw identical, every block the project
            // holds: a caller checks the ones it sent are among them.
            o["blocks"] = new List<object>(TiaNames.Labels(cmp.Blocks).ToArray());
            if (req.Hardware)
            {
                o["hardwareVerified"] = false;
                o["warning"] = "the hardware configuration is not verified: Openness has no online hardware compare, so only the PLC software was compared after loading";
                // The operator's attestation that stood in for the check,
                // for the caller's deploy record.
                o["attestation"] = req.Attestation.ToJson();
            }
            // A user-management reset tiad wrote, and the route that let it
            // (UserManagementRule), for the deploy record.
            if (given != "") o["userManagementReset"] = given;
            // TIA's messages come back on success; on a failure they are in
            // the outcome_unknown text (DownloadDecider.Finish).
            if (result.Messages != "") o["messages"] = result.Messages;
            o["questions"] = Question.ToJson(askedQs);
            o["keptDevices"] = KeptDevice.ToJson(s.Kept);
            // The CPU's mode as TIA's messages say it, labelled by where it
            // comes from: evidence, not a read of the CPU (#789).
            if (result.State != "")
            {
                o["state"] = result.State;
                o["stateSource"] = result.StateSource;
            }
            // The TIA instance's blind trust the download rode on, if any,
            // for the deploy record (BlindTrustRule).
            o["blindTrust"] = BlindTrustJson();
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
                CheckDevicePin(s, address, "compare");
                var diffs = new List<Difference>();
                OnlineCompare cmp = OnSession(s, delegate(ITia t) { return t.Compare(s.Handle, address); });
                BlindTrust bt = blindTrust;
                if (bt != null)
                {
                    Log.Line("audit compare session={0} project={1} address={2} blindTrust={3}", s.Id, Json.Quote(s.Project), Json.Quote(address), BlindTrust.Audit(bt));
                }
                bool equal = TiaNames.CompareEqual(cmp == null ? null : cmp.Tree, cmp == null ? null : cmp.Blocks, diffs);
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
                o["blocks"] = new List<object>(TiaNames.Labels(cmp == null ? null : cmp.Blocks).ToArray());
                // A compare asks no TLS question of its own; it may ride on
                // the instance's blind trust.
                o["blindTrust"] = BlindTrustJson();
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
                // An explicit, per-call opt-in, only here: trust the CPU's
                // TLS certificate blindly for this read-only upload.
                bool trust = r.Bool("trustUnverifiedCertificate");
                r.Done();
                string address = s.Address;
                if (address == "" || !Target(address))
                {
                    throw new TiadException(403, Kinds.TargetRefused, "the project's CPU is not in TIAD_TARGETS");
                }
                DeviceIdentity read = null;
                ProjectCpu cpu = null;
                string cpuWhy = null;
                var removed = new List<string>();
                List<string> trusted = OnSession(s, delegate(ITia t)
                {
                    // An upload replaces devices, not users: users an apply
                    // left part-way stay that way.
                    if (s.UsersUnclean) throw CpuReuse.Unclean("an upload");
                    // The CPU's program replaces the project's: it no
                    // longer matches its spec, and it must take a spec and
                    // compile again before anything is downloaded. Until
                    // the upload is through, the project is unclean.
                    s.Compiled = false;
                    s.Station = null;
                    s.Kept = new List<KeptDevice>();
                    s.Unclean = true;
                    s.UploadedFrom = "";
                    // What this session made in the project's users and
                    // roles belongs to the station the upload replaces.
                    if (s.Made.Any)
                    {
                        removed = t.RemoveAccess(s.Handle, s.Made);
                        s.Made = new AccessMade();
                    }
                    List<string> tr = TrustingCall("upload", s, address, trust, delegate { return t.Upload(s.Handle, address, trust); });
                    RecordBlindTrust("upload", s, address, tr);
                    // The project is replaced now: a failed identity read
                    // must not skip the audit lines below.
                    try
                    {
                        read = t.ProjectCpu(s.Handle);
                    }
                    catch (Exception e)
                    {
                        Log.Line("upload: reading the CPU's identity failed: {0}", Json.Quote(e.Message));
                        read = null;
                    }
                    // What a spec must name to keep the CPU: reported, but
                    // never in the way of the pin check.
                    try
                    {
                        cpu = t.ReadCpu(s.Handle);
                    }
                    catch (Exception e)
                    {
                        Log.Line("upload: reading the project's CPU failed: {0}", Json.Quote(e.Message));
                        cpu = null;
                        cpuWhy = e.Message;
                    }
                    return tr;
                });
                if (trust || trusted.Count > 0 || blindTrust != null)
                {
                    var q = new List<string>();
                    foreach (string x in trusted) q.Add(Json.Quote(x));
                    Log.Line("audit upload session={0} project={1} address={2} trustUnverifiedCertificate={3} trustedBlindly=[{4}] blindTrust={5}",
                        s.Id, Json.Quote(s.Project), Json.Quote(address), trust ? "yes" : "no", string.Join(",", q.ToArray()), BlindTrust.Audit(blindTrust));
                }
                // Trust on first use: the first upload pins the CPU's
                // identity; every later one must match it (section 3.8).
                // The session stays unclean until the CPU is known to be the
                // pinned one: a refused identity leaves a project nobody
                // may take over, save or reuse.
                string pin = PinUpload(s, address, read, trust);
                OnSession(s, delegate(ITia t)
                {
                    s.Unclean = false;
                    // The project's CPU is now what this CPU reported
                    // (UserManagementRule's upload route).
                    s.UploadedFrom = address;
                    return true;
                });
                var o = new Obj();
                o["trustedBlindly"] = trusted.ToArray();
                o["blindTrust"] = BlindTrustJson();
                o["device"] = read.ToJson();
                o["pin"] = pin;
                // What a spec that reuses this CPU must name (section 3.3).
                o["cpu"] = cpu == null ? null : cpu.ToJson();
                if (cpuWhy != null) o["cpuError"] = cpuWhy;
                // What this session had made in the project's users and
                // roles, removed with the station it belonged to.
                if (removed.Count > 0) o["removedAccess"] = removed.ToArray();
                return o;
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
                // A project left part-way must not be saved, reopened and
                // its CPU reused. Checked on the TIA thread, where the
                // flag is written.
                string full = OnSession(s, delegate(ITia t)
                {
                    if (s.Unclean) throw CpuReuse.Unclean("saving it");
                    return t.Save(s.Handle, archive);
                });
                var o = new Obj();
                o["path"] = work.RelInside(full);
                return o;
            });
        }
    }
}
