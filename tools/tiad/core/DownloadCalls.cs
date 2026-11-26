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

        // UiRefusesDownload is a download's refusal on an agent started
        // with TIAD_UI=1.
        public const string UiRefusesDownload = "this agent runs TIA Portal with its user interface (TIAD_UI=1), for viewing only, and refuses every download: a click or a dialog in the visible Portal could change or stall it. Restart the agent without TIAD_UI to download; nothing was sent to the CPU";

        bool Target(string address)
        {
            return cfg.Targets.Contains(address);
        }

        // Unknown is outcome_unknown: the CPU may or may not be loaded.
        // acting is the acting answers tiad gave TIA before it ended
        // (question ids): the answer names them, says plainly what they
        // may have done to the CPU, and lists them (data.actingAnswers).
        // LoginBlocked refuses a hardware load of a fullAccessUser station
        // while the instance trusts blindly (bt): the load turns the CPU's
        // access control on, so the compare after it has to log on, and no
        // password follows a blind trust: it could only end in doubt. A
        // software load is not refused: whether its CPU asks credentials
        // is not known before it goes online (section 3.9). Null when it
        // may go on.
        // A real download that asks a user-management reset by the logon
        // route (attestation.cpuAccessControl "enabled") has to log on
        // too, before TIA asks the check (UserManagementRule.Gate.At):
        // refused the same way.
        static TiadException LoginBlocked(Session s, DownloadRequest req, BlindTrust bt)
        {
            if (bt == null || !req.Hardware || s.Station == null) return null;
            if (s.Station.Cpu.AccessControl == StationRules.AccessFullAccessUser)
            {
                return TiadException.BadRequest("{0}", OnlineLogin.BlockedBy(bt) + "; a hardware load of a fullAccessUser station has to log on for the compare after it, so nothing was sent to the CPU");
            }
            if (!req.DryRun && req.Decisions.UserManagementResetToProject == true && req.Attestation.CpuAccessControl == Attestation.CpuAccessEnabled)
            {
                return TiadException.BadRequest("{0}", OnlineLogin.BlockedBy(bt) + "; a user-management reset by the logon route (attestation.cpuAccessControl \"enabled\") has to log on to the CPU first, so nothing was sent to the CPU");
            }
            return null;
        }

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
        // WithReason adds why tiad refused a user-management reset when TIA
        // asked (UserManagementRule.Gate.At) to a download that ended in
        // doubt after it.
        static TiadException WithReason(TiadException e, string why)
        {
            if (why == "") return e;
            return new TiadException(e.Status, e.Kind, e.Message + "; at TIA's user-management check, " + why, e.Payload, e.Fatal);
        }

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
            return "stopCpu=" + YesNo(d.StopCpu) + " resetData=" + YesNo(d.ResetData) + " overwrite=" + YesNo(d.Overwrite) + " consistentDownload=" + YesNo(d.ConsistentDownload) +
                " alarmTexts=" + YesNo(d.AlarmTexts) + " allBlocks=" + YesNo(d.AllBlocks) +
                " userManagementKeepOnline=" + YesNo(d.UserManagementKeepOnline) + " userManagementResetToProject=" + YesNo(d.UserManagementResetToProject) +
                " identificationDataLoadNothing=" + YesNo(d.IdentificationDataLoadNothing) +
                " startAfter=" + YesNo(d.StartAfter) +
                " keptDevices=[" + string.Join(",", Quoted(d.KeptDevices)) + "]" +
                " trustCertificate=" + (d.TrustCertificate == "" ? "unset" : Json.Quote(d.TrustCertificate)) +
                " acceptUnverifiedHardware=" + YesNo(d.AcceptUnverifiedHardware) + " acceptBlindTrust=" + YesNo(d.AcceptBlindTrust);
        }

        // GateMissed is e, led by the gate miss's words when the download
        // ended at one (LayoutRule.GateMiss): a cancel after an acting
        // answer is in doubt, and says why it was cancelled.
        static TiadException GateMissed(TiadException e, string gateMiss, string baseline)
        {
            if (gateMiss == "") return e;
            return new TiadException(e.Status, e.Kind, LayoutRule.GateMiss(gateMiss, baseline) + "; and " + e.Message, e.Payload, e.Fatal);
        }

        // LayoutRefusal is null, or the refusal of a download the data
        // layout gate does not let (LayoutRule): a digest that is not the
        // layout's, or a layout that needs the consent the request does
        // not give. Nothing has reached TIA's download.
        static TiadException LayoutRefusal(LayoutDiff layout, DownloadRequest req)
        {
            var d = new Obj();
            d["layout"] = layout.ToJson();
            if (req.LayoutDigest != "" && req.LayoutDigest != layout.Digest)
            {
                return new TiadException(409, Kinds.DecisionRequired, "the data layout is no longer the one the caller showed (layoutDigest " + Json.Quote(req.LayoutDigest) + ", now " + layout.Digest +
                    "): the project, the CPU's last deploy or its verify changed since; read POST /v1/sessions/{id}/layout again; nothing was sent to the CPU", d, false);
            }
            if (layout.ResetDataNeeded && req.Decisions.ResetData != true)
            {
                d["questions"] = Question.ToJson(new List<Question> { new Question { Id = QuestionIds.ResetData, Message = layout.Why, Objects = layout.DataBlocks } });
                return new TiadException(409, Kinds.DecisionRequired, "the download needs decisions.resetData: " + layout.Why + "; nothing was sent to the CPU", d, false);
            }
            return null;
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
                // rode is the blind trust the download was checked against:
                // the instance's when the call began, then at each check.
                BlindTrust rode = blindTrust;
                // run is the deploy record of a real download, once tiad
                // archived the project and wrote its intent line.
                var run = new DeployRun();
                try
                {
                    object result = DownloadChecked(s, req, ref asked, ref outcome, ref reset, ref rode, acting, run);
                    run.Kind = DeployStatus.Loaded;
                    return result;
                }
                catch (TiadException e)
                {
                    if (outcome == "") outcome = e.Kind;
                    run.Kind = e.Kind;
                    run.Message = e.Message;
                    run.Payload = e.Payload as Obj;
                    // Whatever the end, the caller can find its record.
                    if (run.IntentWritten) throw WithDeploy(e, run.Id);
                    throw;
                }
                catch (Exception e)
                {
                    if (outcome == "") outcome = "error";
                    run.Kind = "error";
                    run.Message = "internal error in the agent (" + e.GetType().Name + "); see its log";
                    throw;
                }
                finally
                {
                    // The outcome line, on every end once the intent line
                    // was written: a record with no outcome means the agent
                    // died during the download.
                    if (run.IntentWritten) WriteOutcome(run, rode, acting);
                    Log.Line("audit download session={0} project={1} address={2} hardware={3} software={4} dryRun={5} announced={6} {7} protection={8} configurationPassword={9} attestation={10} userManagementReset={11} blindTrust={12} source={13} outsideChanges={14} deploy={15} kept=[{16}] asked=[{17}] acting=[{18}] outcome={19} deviceAttestation={20}",
                        s.Id, Json.Quote(s.Project), Json.Quote(req.Address), req.Hardware, req.Software, req.DryRun, req.Announced,
                        AuditDecisions(req.Decisions), req.Secrets.Protection == "" ? "unset" : "set",
                        req.Secrets.ConfigurationPassword == "" ? "unset" : "set", req.Attestation.Audit(), reset, BlindTrust.Audit(rode),
                        DeploySource.Audit(req.Source), OutsideChanges.Audit(req.OutsideChanges), run.IntentWritten ? run.Id + (run.Recorded ? "" : "(outcome-not-recorded)") : "unset",
                        AuditKept(s.Kept), asked, string.Join(",", Quoted(acting).ToArray()), outcome,
                        req.DeviceAttested != null ? req.DeviceAttested.Attestation.Audit() : req.DeviceAttestation != null ? "given-not-used" : "unset");
                }
            });
        }

        object DownloadChecked(Session s, DownloadRequest req, ref string asked, ref string outcome, ref string resetAudit, ref BlindTrust rode, List<string> acting, DeployRun run)
        {
            string bad = req.Validate();
            if (bad != null) throw TiadException.BadRequest("{0}", bad);
            // An agent that shows TIA's window is for viewing only: a click
            // or a modal dialog in the visible Portal could change or stall
            // a load, dry runs included.
            if (cfg.Ui) throw TiadException.BadRequest("{0}", UiRefusesDownload);
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
            // A stop without the consent to what it does to the data (#1341).
            string resetWhy = ResetDataRule.Check(req.Decisions, req.LayoutDigest);
            if (resetWhy != null) throw TiadException.BadRequest("{0}", resetWhy);
            // A kept CPU's access control turned on by an apply on this
            // session loads only by the attestation route (TurnOnRule).
            if (s.AccessTurnOn && !s.AccessTurnOnLoaded)
            {
                string turnWhy = TurnOnRule.DownloadRefusal(req, s.AccessTurnOnTried);
                if (turnWhy != null) throw TiadException.BadRequest("{0}", turnWhy + "; nothing was sent to the CPU");
            }
            // A station with a Full access user turns the CPU's access
            // control on at its hardware load, and from then on going
            // online to it asks credentials (section 3.9): the compare
            // after this download would, so without the password it could
            // only end in doubt. Refused before anything reaches the CPU.
            if (s.Station.Cpu.AccessControl == StationRules.AccessFullAccessUser && req.Secrets.FullAccessPassword == "")
            {
                throw TiadException.BadRequest("{0}", LoginUse.DownloadNeedsPassword);
            }
            // Nor can it log on while the instance trusts blindly: checked
            // here, and again on the TIA thread before anything is recorded.
            TiadException noLogin = LoginBlocked(s, req, blindTrust);
            if (noLogin != null) throw noLogin;
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
                var facts = new UserManagementRule.Facts
                {
                    AccessControlKept = accessControlKept,
                    // The spec the project was last applied with, and an
                    // apply on this session set it: the project's user
                    // "nautilus" is tiad's.
                    FullAccessUserSet = s.Station != null && s.Station.Cpu != null && s.Station.Cpu.AccessControl == StationRules.AccessFullAccessUser && s.AccessControlSet,
                    RecordOnly = s.AccessRecordOnly,
                    UploadedFrom = s.UploadedFrom,
                    UploadRoute = UserManagementRule.UploadRouteEnabled,
                    ProjectAccessControl = delegate
                    {
                        return OnSession(s, delegate(ITia t)
                        {
                            ProjectCpu pc = t.ReadCpu(s.Handle);
                            return pc == null ? null : pc.AccessControl;
                        });
                    },
                    PasswordRecorded = delegate
                    {
                        string comment = OnSession(s, delegate(ITia t) { return t.UserComment(s.Handle, AccessSteps.UserName); });
                        return (bool?)(records != null && records.Matches(comment, AccessSteps.UserName, req.Secrets.FullAccessPassword));
                    },
                };
                reset = UserManagementRule.Check(req, facts);
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
            rode = blindTrust;
            TiadException btRefused = BlindTrustRefusal(rode, req, s);
            if (btRefused != null)
            {
                asked = Json.Quote(QuestionIds.BlindTrust);
                outcome = btRefused.Kind + ":[" + asked + "]";
                throw btRefused;
            }
            // The CPU must still be the one tiad pinned (section 3.8),
            // before a dry run too: it reaches the CPU.
            req.DeviceAttested = CheckDevicePin(s, req.Address, req.DryRun ? "dry run" : "download", req.DeviceAttestation);
            // One project per CPU: the first download claims the CPU for
            // this session, dry runs included, until it is closed.
            sessions.ClaimCpu(s, req.Address);
            // The attestation is accepted here, once every refusal tiad
            // makes itself has passed and just before TIA is called: by
            // tiad's clock, never the request's.
            if (req.Hardware && !req.DryRun) req.Attestation.At = DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ", System.Globalization.CultureInfo.InvariantCulture);

            // The download's logon, made by the agent on the TIA thread just
            // before TIA's call, so the user-management answer can read
            // whether it logged on (UserManagementRule.Gate.At).
            LoginUse use = null;
            // atWhy is why a reset let before TIA was refused when TIA
            // asked (UserManagementRule.Gate.At): a download that then ends
            // in doubt says so.
            string atWhy = "";
            // held is whether this TIA instance held the instance-logon
            // evidence for the download's address and password, read on the
            // TIA thread just before TIA's download (InstanceLogons): the
            // logon route's stand-in when the download asks no credentials
            // before TIA's user-management check (UserManagementRule.Gate.At).
            bool held = false;
            string heldWhat = "none";
            // answeredBefore is whether tiad answered credentials for the
            // address earlier in this TIA instance, read with held: TIA
            // may have kept that logon, so the off routes, which rest on
            // the CPU's access control being off, are refused.
            bool answeredBefore = false;
            bool haveProtection = req.Secrets.Protection != "";
            bool haveConfigurationPassword = req.Secrets.ConfigurationPassword != "";
            string given = "";
            var unanswered = new List<Question>();
            var declined = new List<Question>();
            var askedQs = new List<Question>();
            int calls = 0;
            bool cancelled = false;
            // The data layout gate's verdict, read on the TIA thread just
            // before TIA's download (LayoutRule): relaxed is a download
            // without the consent to a reset, so a stop, a reinitialize or
            // a load of every block TIA asks is a gate miss; gateMiss is
            // the check that missed, baseline the deploy compared with.
            bool relaxed = req.Decisions.ResetData != true;
            string gateMiss = "", gateBaseline = "";
            bool layoutRefused = false;
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
                // The RUN backstop (#1341): without the consent, TIA asking
                // to stop the CPU, reinitialize data blocks or load every
                // block means the layout gate missed a change. Never
                // answered: cancelled there, and logged.
                if (relaxed && LayoutRule.GateMissQuestion(q.Id))
                {
                    gateMiss = q.Id;
                    cancelled = true;
                    unanswered.Add(q);
                    Log.Line("audit layout-gate-miss session={0} address={1} question={2} baseline={3} acting=[{4}]", s.Id, Json.Quote(req.Address), Json.Quote(q.Id), gateBaseline == "" ? "none" : gateBaseline, string.Join(",", Quoted(acting).ToArray()));
                    return null;
                }
                var one = new List<Question> { q };
                int u = unanswered.Count, d = declined.Count;
                // The reset's verdict as it stands when TIA asks: the
                // logon route needs this download logged on by now, the
                // others a CPU that asked no credentials.
                UserManagementRule.Gate now = reset;
                if (q.Id == QuestionIds.UserManagement && reset.Allowed)
                {
                    now = reset.At(use != null && use.LoggedOn, use != null && use.MayHaveAskedCredentials, use != null && held, answeredBefore);
                    if (!now.Allowed) atWhy = now.Why;
                    if (!now.Allowed) Log.Line("audit user-management-reset address={0} route={1} refused when TIA asked: {2}", Json.Quote(req.Address), reset.Route, Json.Quote(now.Why));
                }
                DownloadRule.Resolve(one, req.Decisions, haveProtection, haveConfigurationPassword, accessControlKept, now, unanswered, declined);
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
                if (q.Id == QuestionIds.UserManagement && a.Choice == Answer.ChoiceReset)
                {
                    given = now.Route;
                    if (given == UserManagementRule.RouteInstanceLogon)
                    {
                        Log.Line("audit user-management-reset address={0} route={1}: the download asked no credentials before TIA's check, and this TIA instance holds an equal compare that logged on with the request's password ({2})", Json.Quote(req.Address), given, heldWhat);
                    }
                }
                return a;
            };

#if TIAD_TEST_SEAMS
            if (TestSeams.TrustBeforeDownloadDispatch)
            {
                OnSession(s, delegate(ITia t)
                {
                    RecordBlindTrust("test seam", s, req.Address, new List<string> { "test seam: trust given between the download's check and its dispatch" });
                    return true;
                });
            }
#endif
            DownloadOutcome result;
            BlindTrust ranWith = rode;
            bool lateRefused = false;
            try
            {
                result = OnSession(s, delegate(ITia t)
                {
                    // Again on the TIA thread, where trust is recorded: a
                    // call queued ahead of this one may have given it. What
                    // it reads here is what the download rides on.
                    ranWith = blindTrust;
                    TiadException blocked = LoginBlocked(s, req, ranWith);
                    if (blocked != null) throw blocked;
                    TiadException late = BlindTrustRefusal(ranWith, req, s, "TIA's download was not called");
                    if (late != null)
                    {
                        lateRefused = true;
                        throw late;
                    }
                    // The deploy record, in this same work item, so what is
                    // archived is what TIA loads: the project archived, then
                    // the intent line. Either failing refuses the download.
                    // A dry run leaves no record: before CanDryRun is ever
                    // true for tiad.Openness, a dry run TIA loads without
                    // asking (outcome_unknown below) must get a record too
                    // (#1180 review), as it must get the hardware opt-in,
                    // and with it any outsideChanges accept the request
                    // carries: an overwrite must be on the record (#789).
                    // The data layout gate (#1341), on this same work item,
                    // so the layout checked is the one archived and loaded:
                    // before the record, so a refusal records nothing.
                    List<LayoutObject> layoutNow;
                    LayoutDiff layout = ReadLayout(t, s, req.Address, req.Hardware, out layoutNow);
                    gateBaseline = layout.Baseline;
                    string digestSeen = req.LayoutDigest == "" ? "unset" : req.LayoutDigest == layout.Digest ? "match" : "mismatch";
                    Log.Line("audit layout-gate session={0} address={1} baseline={2} changes={3} dataBlocks={4} resetDataNeeded={5} resetData={6} digest={7}",
                        s.Id, Json.Quote(req.Address), layout.Baseline == "" ? "none" : layout.Baseline, layout.Changes.Count,
                        layout.AllDataBlocks ? "all" : string.Join(",", Quoted(layout.DataBlocks).ToArray()), layout.ResetDataNeeded ? "yes" : "no", YesNo(req.Decisions.ResetData), digestSeen);
                    TiadException layoutWhy = LayoutRefusal(layout, req);
                    if (layoutWhy != null)
                    {
                        layoutRefused = true;
                        throw layoutWhy;
                    }
                    bool everyBlock = req.Hardware || (req.Decisions.ResetData == true && layout.Baseline == "");
                    bool marked = !req.DryRun && s.AccessTurnOn && !s.AccessTurnOnLoaded;
                    if (marked) MarkTurnOnPending(s, req);
                    if (!req.DryRun) Record(t, s, req, ranWith, run, layoutNow, layout.Baseline);
                    // A download to the CPU runs: the verify that found it
                    // equal to its last deploy no longer stands.
                    if (!req.DryRun) ForgetVerified(req.Address);
                    // Recorded: TIA's download comes next, and may load.
                    if (marked) s.AccessTurnOnTried = true;
                    try
                    {
                        // A dry run never starts the CPU, whatever the request
                        // says (#1154 review): if TIA loads it without asking,
                        // start-after is NoAction.
                        // Its logon answers by the blind trust read here,
                        // on the TIA thread.
                        use = new LoginUse(LoginFor(t, req.Secrets.FullAccessPassword, ranWith, req.Address, "download"), "download", req.Address);
                        // The evidence as it stands now, in this instance,
                        // before anything of this download reaches TIA.
                        held = ranWith == null && blindTrust == null && instanceLogons.Holds(t, req.Address, req.Secrets.FullAccessPassword);
                        answeredBefore = instanceLogons.AnsweredBefore(t, req.Address);
                        if (held) heldWhat = instanceLogons.Describe(req.Address);
                        DownloadOutcome got = null;
                        try
                        {
                            got = t.Download(s.Handle, req.Address, req.Hardware, req.Software, everyBlock, req.Secrets, req.DryRun ? (bool?)null : req.Decisions.StartAfter, answer, use);
                            return got;
                        }
                        finally
                        {
                            // Only a download TIA ended cleanly, loading
                            // nothing, with no acting answer and no
                            // credentials refused, leaves the evidence: any
                            // other may have changed the CPU's users or
                            // access control. The compare after a load may
                            // record it again.
                            bool clean = got != null && !got.Loaded && acting.Count == 0 && !(use.MayHaveAskedCredentials && !use.LoggedOn);
                            if (!clean) instanceLogons.Forget(req.Address, "a download to it ran in TIA and may have changed the CPU's users or access control");
                            // A dry run TIA did not end cleanly, loading
                            // nothing (it may have loaded: TIA asked no
                            // check, or it failed), voids every adopt
                            // baseline for the CPU. One cancelled at its
                            // first check loaded nothing and keeps it, so a
                            // deploy's dry run and its download that follows
                            // still compare with it (#1347).
                            if (req.DryRun && !(got != null && !got.Loaded)) BumpLoad(req.Address);
                        }
                    }
                    catch (Exception e)
                    {
                        // An internal error in tiad's side of the download,
                        // not TIA's: whether TIA had begun is unknown, so a
                        // recorded deploy is in doubt, never not-loaded
                        // (#1180 review).
                        if (e is TiaException || e is TiadException || !run.IntentWritten) throw;
                        Log.Line("download: internal error in the agent's TIA call: {0}", e);
                        throw Unknown("internal error in the agent's TIA call during the download (" + e.GetType().Name + "); see its log", acting);
                    }
                });
            }
            catch (TiadException e)
            {
                rode = ranWith;
                if (layoutRefused)
                {
                    asked = Json.Quote(QuestionIds.ResetData);
                    outcome = e.Kind + ":[" + asked + "]";
                    throw;
                }
                if (lateRefused)
                {
                    asked = Json.Quote(QuestionIds.BlindTrust);
                    outcome = e.Kind + ":[" + asked + "]";
                    throw;
                }
                asked = Ids(askedQs);
                // An outcome_unknown from TIA's side (its download had
                // begun when it failed) passes through, with the acting
                // answers tiad gave named.
                if (e.Kind == Kinds.OutcomeUnknown)
                {
                    outcome = Kinds.OutcomeUnknown;
                    MayHaveStarted(req, acting);
                    throw GateMissed(WithReason(WithActing(e, acting), atWhy), gateMiss, gateBaseline);
                }
                // TIA failed once it asked a check: whether it acted on an
                // answer, or loaded, is unknown, so from there on a failure
                // leaves the CPU in doubt.
                if (calls > 0 && e.Kind == Kinds.OperationFailed)
                {
                    outcome = Kinds.OutcomeUnknown;
                    MayHaveStarted(req, acting);
                    throw GateMissed(WithReason(UnknownAfter(e, acting), atWhy), gateMiss, gateBaseline);
                }
                throw;
            }
            finally
            {
                // A reset is journalled given only once tiad answered it,
                // and refused at TIA's check when Gate.At refused it there.
                // The deploy record carries a given reset's route whatever
                // the end.
                if (given != "") resetAudit = "given:" + given;
                else if (atWhy != "") resetAudit = "refused-at-check:" + reset.Route;
                if (run != null) run.UserManagementReset = given;
                rode = ranWith;
            }
            asked = Ids(askedQs);

            // The ITia contract, checked: a load only when nothing was
            // cancelled, and a dry run never loads.
            if (result.Loaded && (cancelled || req.DryRun))
            {
                outcome = Kinds.OutcomeUnknown;
                if (calls == 0) throw Unknown("TIA ran the dry run's download without asking a single check, so tiad could not cancel it", acting);
                throw GateMissed(Unknown("TIA loaded the CPU although tiad cancelled the download", acting), gateMiss, gateBaseline);
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
                o["blindTrust"] = rode == null ? null : rode.ToJson();
                return o;
            }
            // A cancel after an acting answer is never a clean refusal: TIA
            // may have acted on that answer before it asked the next check.
            if ((unanswered.Count > 0 || declined.Count > 0) && acting.Count > 0)
            {
                outcome = Kinds.OutcomeUnknown;
                var at = unanswered.Count > 0 ? unanswered[0] : declined[0];
                string missed = gateMiss != "" ? LayoutRule.GateMiss(gateMiss, gateBaseline) + "; and " : "";
                throw WithReason(Unknown(missed + "tiad cancelled the download at TIA's " + at.Id + " check, which the request does not answer yes", acting), at.Id == QuestionIds.UserManagement ? atWhy : "");
            }
            if (unanswered.Count > 0)
            {
                outcome = Kinds.DecisionRequired + ":[" + Ids(unanswered) + "]";
                var d = new Obj();
                d["questions"] = Question.ToJson(unanswered);
                d["keptDevices"] = KeptDevice.ToJson(s.Kept);
                if (gateMiss != "")
                {
                    d["gateMiss"] = gateMiss;
                    throw new TiadException(409, Kinds.DecisionRequired, LayoutRule.GateMiss(gateMiss, gateBaseline) + "; nothing was loaded", d, false);
                }
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
            bool equal = false;
            OnlineCompare cmp;
            try
            {
                cmp = OnSession(s, delegate(ITia t) { return CompareOnline(t, s, req.Address, req.Secrets.FullAccessPassword, blindTrust, "download compare", diffs, out equal); });
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
            if (s.AccessTurnOn && !s.AccessTurnOnLoaded)
            {
                s.AccessTurnOnLoaded = true;
                Log.Line("audit access-turn-on session={0} address={1} loaded (route {2})", s.Id, Json.Quote(req.Address), Json.Quote(run == null ? "" : run.UserManagementReset));
                string marker = work.Resolve(s.Project) + TurnOnRule.MarkerSuffix;
                try
                {
                    File.Delete(marker);
                }
                catch (Exception e)
                {
                    // The gate stays on the next open: never looser.
                    Log.Line("audit access-turn-on session={0} removing the turn-on pending mark failed: {1}", s.Id, Json.Quote(e.GetType().Name + ": " + e.Message));
                }
            }
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
            // The operator's reading of the CPU's identity that stood in for
            // the online read of a pinned CPU, for the caller's deploy record.
            if (req.DeviceAttested != null) o["deviceAttestation"] = req.DeviceAttested.ToJson();
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
            // for the deploy record (BlindTrustRule): as the TIA thread read
            // it just before TIA's download, not later.
            o["blindTrust"] = rode == null ? null : rode.ToJson();
            run.State = result.State;
            run.StateSource = result.StateSource;
            run.Blocks = TiaNames.Labels(cmp.Blocks);
            run.Hardware = req.Hardware;
            run.UserManagementReset = given;
            // The CPU's fingerprint after the load, in this session, read
            // through the same trust path as a fingerprint call: its
            // DownloadId names this load. A read that fails changes nothing
            // of the verdict: the download loaded, and compared equal.
            ReadDeployFingerprint(s, req, run);
            o["deploy"] = run.Response;
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
                HeldOnlineRefuses(s, "compare");
                var r = c.Json();
                string password = OnlinePassword(r);
                DeviceAttestation att = ReadDeviceAttestation(r);
                // layoutBaseline: an equal compare of the untouched project
                // also takes the session's adopt baseline (#1347).
                bool wantBaseline = r.Bool("layoutBaseline");
                r.Done();
                // A project opened as found (no spec applied yet: an
                // adopted one, #1272) compares with its own CPU, at the
                // address the project holds; still only a target.
                string address = s.Address;
                if (address == "")
                {
                    address = OnSession(s, delegate(ITia t) { return t.ProjectCpuAddress(s.Handle); });
                    if (address == "") throw TiadException.BadRequest("the project has no station spec, and its CPU no address to compare with");
                }
                if (!Target(address)) throw new TiadException(403, Kinds.TargetRefused, address + " is not in TIAD_TARGETS");
                CheckDevicePin(s, address, "compare", att);
                // The CPU's load count and journal head before the compare
                // goes online: a download from here on voids what it takes.
                string baselineWhy = null;
                long gen = 0;
                string head = null;
                AdoptBaseline taken = null;
                if (wantBaseline)
                {
                    // Asking again drops what an earlier compare took.
                    s.Adopted = null;
                    s.AdoptRefused = "";
                    gen = LoadGen(address);
                    head = JournalHead(address, out baselineWhy);
                    if (baselineWhy == null) baselineWhy = AdoptBaselineRefusal(s);
                }
                var diffs = new List<Difference>();
                // Read on the TIA thread, where trust is recorded: what the
                // compare rode on, also once a later close retired it.
                BlindTrust rode = null;
                bool equal = false;
                // Journalled before it goes online, so a compare that fails
                // is journalled as riding on the trust too.
                OnlineCompare cmp;
                try
                {
                cmp = OnSession(s, delegate(ITia t)
                {
                    BlindTrust bt = blindTrust;
                    rode = bt;
                    if (bt != null)
                    {
                        Log.Line("audit compare session={0} project={1} address={2} blindTrust={3}", s.Id, Json.Quote(s.Project), Json.Quote(address), BlindTrust.Audit(bt));
                    }
                    OnlineCompare oc = CompareOnline(t, s, address, password, bt, "compare", diffs, out equal);
                    if (wantBaseline && baselineWhy == null)
                    {
                        if (!equal) baselineWhy = "the compare found the project not equal to the CPU";
                        else taken = TakeAdoptBaseline(t, s, address, gen, head, out baselineWhy);
                    }
                    return oc;
                });
                }
                catch
                {
                    if (wantBaseline) s.AdoptRefused = "the compare failed";
                    throw;
                }
                if (taken != null) s.Adopted = taken;
                else if (wantBaseline) s.AdoptRefused = baselineWhy;
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
                // The CPU compared with: the spec's, or the project's own
                // for a project opened as found.
                o["address"] = address;
                o["differences"] = list;
                o["blocks"] = new List<object>(TiaNames.Labels(cmp == null ? null : cmp.Blocks).ToArray());
                // A compare asks no TLS question of its own; it may ride on
                // the instance's blind trust.
                o["blindTrust"] = rode == null ? null : rode.ToJson();
                if (wantBaseline)
                {
                    var b = new Obj();
                    b["taken"] = taken != null;
                    if (taken != null)
                    {
                        b["id"] = taken.Id;
                        b["objects"] = taken.Objects.Count;
                        b["unreadable"] = taken.Unreadable;
                    }
                    else
                    {
                        b["why"] = baselineWhy;
                    }
                    o["layoutBaseline"] = b;
                    Log.Line("audit adopt-baseline session={0} project={1} address={2} taken={3} {4}", s.Id, Json.Quote(s.Project), Json.Quote(address),
                        taken != null ? "yes" : "no", taken != null ? "id=" + taken.Id + " objects=" + taken.Objects.Count + " unreadable=" + taken.Unreadable + " journalHead=" + Json.Quote(taken.JournalHead) : "why=" + Json.Quote(baselineWhy));
                }
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
                HeldOnlineRefuses(s, "upload");
                var r = c.Json();
                // An explicit, per-call opt-in, only here: trust the CPU's
                // TLS certificate blindly for this read-only upload.
                bool trust = r.Bool("trustUnverifiedCertificate");
                string password = OnlinePassword(r);
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
                // The instance's trust as the TIA thread read it after the
                // upload: what this upload rode on or gave.
                BlindTrust upTrust = null;
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
                    List<string> tr = TrustingCall("upload", s, address, trust, delegate { return t.Upload(s.Handle, address, trust, LoginFor(t, password, blindTrust, address, "upload")); });
                    RecordBlindTrust("upload", s, address, tr);
                    upTrust = blindTrust;
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
                if (trust || trusted.Count > 0 || upTrust != null)
                {
                    var q = new List<string>();
                    foreach (string x in trusted) q.Add(Json.Quote(x));
                    Log.Line("audit upload session={0} project={1} address={2} trustUnverifiedCertificate={3} trustedBlindly=[{4}] blindTrust={5}",
                        s.Id, Json.Quote(s.Project), Json.Quote(address), trust ? "yes" : "no", string.Join(",", q.ToArray()), BlindTrust.Audit(upTrust));
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
                o["blindTrust"] = upTrust == null ? null : upTrust.ToJson();
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

        // MarkTurnOnPending marks the session's project turn-on pending
        // before a gated download's deploy record saves it (TurnOnRule
        // .MarkerSuffix); failing refuses the download, with nothing sent.
        void MarkTurnOnPending(Session s, DownloadRequest req)
        {
            string marker = work.Resolve(s.Project) + TurnOnRule.MarkerSuffix;
            try
            {
                File.WriteAllText(marker, "{\"session\":" + Json.Quote(s.Id) + ",\"address\":" + Json.Quote(req.Address) + "}\n");
            }
            catch (Exception e)
            {
                throw new TiadException(500, Kinds.OperationFailed, "marking the project turn-on pending failed (" + e.GetType().Name + ": " + e.Message + "); nothing was sent to the CPU");
            }
            Log.Line("audit access-turn-on session={0} project={1} marked turn-on pending before the deploy record", s.Id, Json.Quote(s.Project));
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
                    if (s.ForeignChanged) throw Ownership.Unusable("saving it");
                    if (s.AccessTurnOn && !s.AccessTurnOnLoaded) throw TiadException.BadRequest("{0}", TurnOnRule.SaveRefused);
                    return t.Save(s.Handle, archive);
                });
                var o = new Obj();
                o["path"] = work.RelInside(full);
                return o;
            });
        }
    }
}
