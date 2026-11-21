// Download.cs: the download rule (siemens-ide.md section 3.5).
//
// A second copy of tiad.Resolve and DownloadRequest.Validate, checked
// against the Go copy over the wire (tiadtest.ResolveCases). Change both
// or neither.
//
// tiad never answers a pre-download question on its own. A real download
// answers each one only from the request's decisions; a question with no
// answer, or answered no, cancels the download and nothing is loaded.

using System;
using System.Collections.Generic;
using System.Text;

namespace Tiad
{
    public static class QuestionIds
    {
        public const string StopCpu = "stop-cpu";
        public const string Reinitialize = "reinitialize";
        public const string Overwrite = "overwrite";
        // ConsistentDownload is TIA's ConsistentBlocksDownload check
        // ("Download software to device"): loading the PLC software as one
        // consistent whole. It is asked after stop-cpu (#789 walk-past
        // window) and acts: yes loads.
        public const string ConsistentDownload = "consistent-download";
        // The rest of the software-load family a real download asks
        // (#789 restart window; naut-lead's mapping of V21's
        // configurations): each answered only by its own decision.
        // AlarmTexts is AlarmTextLibrariesDownload (acting: yes loads the
        // alarm and text list texts). AllBlocks is AllBlocksDownload
        // (acting: yes loads every block). UserManagement is
        // UserManagementDownload, answered by keeping the CPU's online
        // user management (passive: nothing on the CPU changes), only when
        // the spec keeps access control as found, or, on a hardware load,
        // by resetting it to the project's (acting), only under
        // UserManagementRule.
        // IdentificationData is LoadIdentificationData, answered only by
        // loading nothing (passive).
        public const string AlarmTexts = "alarm-texts";
        public const string AllBlocks = "all-blocks";
        public const string UserManagement = "user-management";
        public const string IdentificationData = "identification-data";
        public const string StartAfter = "start-after";
        public const string Password = "password";
        // ConfigurationPassword is answered only by the request's
        // configuration password, never by the protection password.
        public const string ConfigurationPassword = "configuration-password";
        public const string Certificate = "certificate";
        // KeptDevices is the agent's own question, never TIA's: a download
        // also loads the devices an apply kept as found (KeptRule).
        public const string KeptDevices = "kept-devices";
        // UnverifiedHardware is the agent's own question too: a download
        // that loads the hardware configuration, which tiad cannot verify
        // (HardwareRule).
        public const string UnverifiedHardware = "unverified-hardware";
        // BlindTrust is the agent's own question too: a download in a TIA
        // instance that has trusted a CPU's certificate blindly
        // (BlindTrustRule).
        public const string BlindTrust = "blind-trust";
        public const string Other = "other";
    }

    // Question is one pre-download check that needs an answer.
    public sealed class Question
    {
        public string Id = "", Message = "", Detail = "";
        public List<string> Objects = new List<string>();

        public Question Copy()
        {
            return new Question { Id = Id, Message = Message, Detail = Detail, Objects = new List<string>(Objects) };
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["id"] = Id;
            o["message"] = Message;
            if (Detail != "") o["detail"] = Detail;
            if (Objects.Count > 0) o["objects"] = Objects.ToArray();
            return o;
        }

        public static List<object> ToJson(List<Question> qs)
        {
            var l = new List<object>();
            foreach (var q in qs) l.Add(q.ToJson());
            return l;
        }
    }

    // Decisions are a download's answers. Null is "not answered", which
    // is different from false.
    public sealed class Decisions
    {
        public bool? StopCpu, Overwrite, ConsistentDownload, StartAfter;
        public bool? AlarmTexts, AllBlocks, UserManagementKeepOnline, IdentificationDataLoadNothing;
        // UserManagementResetToProject answers UserManagement by resetting
        // the CPU's user management to the project's
        // (DownloadAllUserManagementDataResetToProject: "Delete user
        // management data and disable access control in runtime" when the
        // project has access control off). It acts, and only
        // UserManagementRule lets it.
        public bool? UserManagementResetToProject;
        public List<string> Reinitialize = new List<string>();
        public string TrustCertificate = "";
        // KeptDevices names every device the caller agrees to load as the
        // project keeps it; it must be exactly the kept set (KeptRule).
        public List<string> KeptDevices = new List<string>();
        // AcceptUnverifiedHardware answers UnverifiedHardware: load the
        // hardware configuration although tiad cannot verify it. Yes
        // needs the request's attestation too (HardwareRule).
        public bool? AcceptUnverifiedHardware;
        // AcceptBlindTrust answers BlindTrust: download although this TIA
        // instance trusted a CPU's certificate blindly earlier. It answers
        // no TLS question of the download's own (BlindTrustRule).
        public bool? AcceptBlindTrust;

        public static Decisions Read(Reader r)
        {
            var d = new Decisions();
            if (r == null) return d;
            d.StopCpu = r.OptBool("stopCpu");
            d.Overwrite = r.OptBool("overwrite");
            d.ConsistentDownload = r.OptBool("consistentDownload");
            d.AlarmTexts = r.OptBool("alarmTexts");
            d.AllBlocks = r.OptBool("allBlocks");
            d.UserManagementKeepOnline = r.OptBool("userManagementKeepOnline");
            d.UserManagementResetToProject = r.OptBool("userManagementResetToProject");
            d.IdentificationDataLoadNothing = r.OptBool("identificationDataLoadNothing");
            d.StartAfter = r.OptBool("startAfter");
            d.Reinitialize = r.Strs("reinitialize");
            d.TrustCertificate = r.Str("trustCertificate");
            d.KeptDevices = r.Strs("keptDevices");
            d.AcceptUnverifiedHardware = r.OptBool("acceptUnverifiedHardware");
            d.AcceptBlindTrust = r.OptBool("acceptBlindTrust");
            return d;
        }
    }

    public sealed class DownloadRequest
    {
        public string Address = "";
        public bool Hardware, Software, DryRun, Announced;
        public Decisions Decisions = new Decisions();
        public Secrets Secrets = new Secrets();
        public Attestation Attestation = new Attestation();
        // Source is the caller's claim of what the project was built from,
        // for the deploy record (null: none); tiad checks only its shape.
        public DeploySource Source;
        // FingerprintTrust is the post-load fingerprint read's own opt-in
        // (fingerprint.trustUnverifiedCertificate), as a fingerprint
        // call's: TIA's TLS question answered trusted, blindly.
        public bool FingerprintTrust;

        public static DownloadRequest Read(Reader r)
        {
            var d = new DownloadRequest();
            d.Address = r.Str("address");
            d.Hardware = r.Bool("hardware");
            d.Software = r.Bool("software");
            d.DryRun = r.Bool("dryRun");
            d.Decisions = Decisions.Read(r.Child("decisions"));
            d.Announced = r.Bool("announced");
            d.Secrets = Secrets.Read(r.Child("secrets"));
            d.Attestation = Attestation.Read(r.Child("attestation"));
            d.Source = DeploySource.Read(r.Child("source"));
            Reader fp = r.Child("fingerprint");
            if (fp != null) d.FingerprintTrust = fp.Bool("trustUnverifiedCertificate");
            return d;
        }

        // Validate is tiad.DownloadRequest.Validate: the request's own
        // shape, with Go's messages. A dry run must be announced too: it
        // reaches the CPU, and that a cancelled download loads nothing is
        // provisional until S2 proves it (section 3.5).
        public string Validate()
        {
            if (!Ip.IsIPv4(Address)) return "download: address " + Json.Quote(Address) + " is not an IPv4 address";
            if (!Hardware && !Software) return "download: neither hardware nor software selected";
            if (!Announced) return "download: announce it first (announced: true); a dry run reaches the CPU too until S2 proves a cancel loads nothing";
            if (Encoding.UTF8.GetByteCount(Attestation.By) > Attestation.MaxBy) return "download: attestation.by is longer than " + Attestation.MaxBy + " bytes";
            if (Encoding.UTF8.GetByteCount(Attestation.Text) > Attestation.MaxText) return "download: attestation.text is longer than " + Attestation.MaxText + " bytes";
            if (!Attestation.CpuAccessControlValid) return "download: attestation.cpuAccessControl is not one of disabled, enabled";
            if (Decisions.UserManagementKeepOnline == true && Decisions.UserManagementResetToProject == true)
            {
                return "download: decisions.userManagementKeepOnline and decisions.userManagementResetToProject are both yes; user-management takes one answer";
            }
            if (Source != null)
            {
                string bad = Source.Validate();
                if (bad != null) return "download: " + bad;
            }
            return null;
        }
    }

    // Attestation is the operator's statement that the CPU's hardware
    // configuration is what they intend to load: the second part of #789's
    // hardware gate, since tiad cannot verify the hardware (HardwareRule).
    // By and Text come from the request; By is the caller's claim, since
    // tiad knows only a bearer token (naut fills it from the principal it
    // authenticated). At is tiad's own UTC clock when it accepted the
    // download, never the request's. The audit line journals all three on
    // every outcome; a successful download echoes them.
    public sealed class Attestation
    {
        // In UTF-8 bytes, as Go's len counts them (an unpaired surrogate
        // is U+FFFD on both sides).
        public const int MaxBy = 200, MaxText = 2000;
        // CpuAccessControl is the operator's statement of the CPU's access
        // control as it is now: "disabled", "enabled" or unset ("").
        // UserManagementRule's second route rests on "disabled".
        public const string CpuAccessDisabled = "disabled", CpuAccessEnabled = "enabled";
        public string By = "", Text = "", At = "", CpuAccessControl = "";

        public static Attestation Read(Reader r)
        {
            var a = new Attestation();
            if (r == null) return a;
            a.By = r.Str("by");
            a.Text = r.Str("text");
            a.CpuAccessControl = r.Str("cpuAccessControl");
            return a;
        }

        public bool CpuAccessControlValid
        {
            get { return CpuAccessControl == "" || CpuAccessControl == CpuAccessDisabled || CpuAccessControl == CpuAccessEnabled; }
        }

        // AuditAccess is CpuAccessControl for the audit line: only the
        // fixed words, never what a refused request sent.
        string AuditAccess()
        {
            if (CpuAccessControl == "") return "unset";
            return CpuAccessControlValid ? CpuAccessControl : "invalid";
        }

        // Given is whether both who and what are said.
        public bool Given
        {
            get { return By.Trim() != "" && Text.Trim() != ""; }
        }

        // Audit is the attestation for the audit line, every
        // request-supplied part quoted so none can forge a line.
        public string Audit()
        {
            if (By == "" && Text == "" && CpuAccessControl == "") return "unset";
            return "{by=" + Part(By, MaxBy) + " at=" + (At == "" ? "unset" : At) + " text=" + Part(Text, MaxText) + " cpuAccessControl=" + AuditAccess() + "}";
        }

        // Part is one part for the audit line: quoted, or only its size
        // when it is over the limit, so a refused request cannot flood the
        // journal (#1166 review).
        static string Part(string s, int max)
        {
            int n = Encoding.UTF8.GetByteCount(s);
            if (n > max) return "<" + n + " bytes, over the limit>";
            return Json.Quote(s);
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["by"] = By;
            o["at"] = At;
            o["text"] = Text;
            if (CpuAccessControl != "") o["cpuAccessControl"] = CpuAccessControl;
            return o;
        }
    }

    // HardwareRule is #789's hardware gate. Openness V21 has no online
    // hardware compare, so a download that loads the hardware
    // configuration loads something tiad cannot verify. It goes ahead
    // only on decisions.acceptUnverifiedHardware yes and an attestation
    // that says who and what, on every CPU, the bench included: tiad
    // cannot tell a bench CPU from a customer's, so it never tries.
    public static class HardwareRule
    {
        public const string Why = "the download loads the hardware configuration, which tiad cannot verify (Openness V21 has no online hardware compare; only the PLC software is compared after loading)";

        public static Question Question()
        {
            return new Question
            {
                Id = QuestionIds.UnverifiedHardware,
                Message = "load the hardware configuration unverified: set decisions.acceptUnverifiedHardware and give attestation.by and attestation.text, the operator's statement that the CPU's hardware configuration is what they intend to load",
            };
        }

        // Check returns null when the request may load hardware, or why
        // not, naming the fix; declined is set when the request answered
        // no. A software-only download needs nothing.
        public static string Check(DownloadRequest req, out bool declined)
        {
            declined = false;
            if (!req.Hardware) return null;
            var d = req.Decisions.AcceptUnverifiedHardware;
            if (!d.HasValue) return Why + ": set decisions.acceptUnverifiedHardware and give attestation.by and attestation.text to load it, or download the software only (hardware: false)";
            if (!d.Value)
            {
                declined = true;
                return Why + ", and decisions.acceptUnverifiedHardware says no: download the software only (hardware: false)";
            }
            if (!req.Attestation.Given) return Why + ": decisions.acceptUnverifiedHardware needs the operator's attestation too (attestation.by: who, attestation.text: that the CPU's hardware configuration is what they intend to load)";
            return null;
        }
    }

    // BlindTrust is when this agent's TIA instance first answered a CPU's
    // TLS certificate question trusted, blindly, on a request's
    // trustUnverifiedCertificate (an upload, a fingerprint read). TIA keeps
    // that trust for the rest of the instance: a later online call asks
    // nothing and rides on it (#789 stage 1b). The agent starts TIA once,
    // so only a restart clears it. Since is tiad's UTC clock; Call,
    // Session and Address say which call gave it; Questions are the
    // questions it answered, as tiad recorded them.
    public sealed class BlindTrust
    {
        public string Since = "", Call = "", Session = "", Address = "";
        public List<string> Questions = new List<string>();

        public Obj ToJson()
        {
            var o = new Obj();
            o["since"] = Since;
            o["call"] = Call;
            o["session"] = Session;
            o["address"] = Address;
            o["questions"] = Questions.ToArray();
            return o;
        }

        public string Describe()
        {
            return "since " + Since + ", by the " + Call + " of session " + Session + " from " + Address;
        }

        // Audit is the record for an audit line: "none", or every field,
        // each quoted.
        public static string Audit(BlindTrust b)
        {
            if (b == null) return "none";
            var q = new List<string>();
            foreach (string x in b.Questions) q.Add(Json.Quote(x));
            return "{since=" + Json.Quote(b.Since) + " call=" + Json.Quote(b.Call) + " session=" + Json.Quote(b.Session) +
                " address=" + Json.Quote(b.Address) + " questions=[" + string.Join(",", q.ToArray()) + "]}";
        }
    }

    // BlindTrustRule: a download, a dry run too, in a TIA instance that
    // trusted a CPU's certificate blindly rides on that trust, since TIA
    // asks no TLS question again. It needs decisions.acceptBlindTrust,
    // checked before anything reaches the CPU; the fix without it is an
    // agent restart. The yes answers no TLS question the download asks
    // itself: that still takes decisions.trustCertificate.
    public static class BlindTrustRule
    {
        public static Question Question(BlindTrust b)
        {
            return new Question
            {
                Id = QuestionIds.BlindTrust,
                Message = "download in a TIA instance that trusted a CPU's certificate blindly (" + b.Describe() + "): set decisions.acceptBlindTrust, or close every session (the agent then retires that instance) or restart the agent",
            };
        }

        // Check returns null when the request may go on, or why not,
        // naming the fix; declined is set when the request answered no.
        public static string Check(BlindTrust b, bool? accept, out bool declined)
        {
            declined = false;
            if (b == null) return null;
            string why = "this agent's TIA instance trusted a CPU's certificate blindly (" + b.Describe() + "), and TIA keeps that trust until that instance ends, so this download would ride on it";
            if (!accept.HasValue) return why + ": close every session (the agent then retires that instance) or restart the agent for one that trusts nothing, or set decisions.acceptBlindTrust";
            if (!accept.Value)
            {
                declined = true;
                return why + ", and decisions.acceptBlindTrust says no: close every session (the agent then retires that instance) or restart the agent for one that trusts nothing";
            }
            return null;
        }
    }

    // UserManagementRule is when tiad may answer TIA's user-management
    // check by resetting the CPU's user management to the project's
    // (decisions.userManagementResetToProject). A hardware load asks it,
    // TIA refuses keep there, and its reset deletes the CPU's users and
    // sets its access control as the project has it (#789 stage 4
    // logging window). tiad lets it only when that changes nothing that
    // protects the CPU: a hardware download, a spec that keeps access
    // control as found on a session that never set it, a project whose
    // access control reads exactly Disabled, and evidence that the CPU's
    // is off too: this session uploaded from that very CPU (the project's
    // setting is the CPU's), or the operator attests it
    // (attestation.cpuAccessControl "disabled"). An attestation that says
    // enabled refuses whatever else holds. The upload route is off
    // (UploadRouteEnabled) until a bench upload proves its premise.
    public static class UserManagementRule
    {
        // UploadRouteEnabled turns the upload route on. It is off
        // (naut-lead, 2026-11-21): that an upload brings a CPU's access
        // control into the project is proven only for the bench's
        // Disabled CPU. Turn it on, here and in Go's
        // tiad.UploadRouteEnabled, only with a bench upload of a CPU with
        // access control on that reads back as not Disabled. Not a const,
        // so the code behind it compiles and stays tested.
        public static readonly bool UploadRouteEnabled = false;
        const string UploadRouteWhy = "the upload route is not yet available: that an upload brings a CPU's access control into the project is unproven until a bench upload of a CPU with access control on shows it";

        // ProjectDisabled is PlcAccessControlConfiguration's name for
        // access control off, exactly.
        public const string ProjectDisabled = "Disabled";
        // The routes that show the CPU's access control is off.
        public const string RouteUpload = "upload", RouteAttestation = "attestation";

        // Gate is Check's verdict. Its zero value refuses: a download that
        // never checked cannot reset.
        public sealed class Gate
        {
            public bool Allowed;
            public string Why = "", Route = "";
        }

        const string Refused = "decisions.userManagementResetToProject (delete the CPU's user management data and set its access control as the project has it) is refused: ";
        static string Routes(bool uploadRoute)
        {
            if (uploadRoute) return "tiad resets it only on a hardware download, with cpu.accessControl keep, a project whose access control is Disabled, and either an upload from this CPU in this session or attestation.cpuAccessControl \"disabled\"";
            return "tiad resets it only on a hardware download, with cpu.accessControl keep, a project whose access control is Disabled, and attestation.cpuAccessControl \"disabled\" (" + UploadRouteWhy + ")";
        }

        public static Question Question(string why)
        {
            return new Question
            {
                Id = QuestionIds.UserManagement,
                Message = why,
                Detail = "UserManagementDownload",
            };
        }

        // Check decides a reset for req. accessControlKept is whether the
        // session's spec keeps access control and no apply on it ever set
        // it; uploadedFrom the address this session last uploaded from
        // ("" for none); uploadRoute whether the upload route counts
        // (UploadRouteEnabled, for the agent); projectAccessControl reads
        // the project's PlcAccessControlConfiguration, asked only once
        // everything cheaper holds. A read that fails or reads nothing
        // refuses.
        public static Gate Check(DownloadRequest req, bool accessControlKept, string uploadedFrom, bool uploadRoute, Func<string> projectAccessControl)
        {
            var g = new Gate();
            if (req.Decisions.UserManagementResetToProject != true)
            {
                g.Why = Refused + "it is not asked";
                return g;
            }
            if (!req.Hardware)
            {
                g.Why = Refused + "this download loads no hardware; " + Routes(uploadRoute);
                return g;
            }
            if (!accessControlKept)
            {
                g.Why = Refused + "the session's spec does not keep access control as found (cpu.accessControl keep on a session that never applied fullAccessUser); " + Routes(uploadRoute);
                return g;
            }
            if (req.Attestation.CpuAccessControl == Attestation.CpuAccessEnabled)
            {
                g.Why = Refused + "the attestation says the CPU's access control is enabled, and a reset would turn it off and delete its users; tiad never resets a CPU whose access control is on";
                return g;
            }
            string project;
            try
            {
                project = projectAccessControl == null ? null : projectAccessControl();
            }
            catch (TiadException e)
            {
                if (e.Fatal) throw;
                g.Why = Refused + "reading the project's access control failed (" + e.Message + "); " + Routes(uploadRoute);
                return g;
            }
            if (project != ProjectDisabled)
            {
                g.Why = Refused + "the project's access control is " + (project == null ? "unreadable" : project == AccessRule.Absent ? "absent (no such setting)" : Json.Quote(project)) + ", not Disabled; " + Routes(uploadRoute);
                return g;
            }
            if (uploadRoute && uploadedFrom != "" && uploadedFrom == req.Address)
            {
                g.Allowed = true;
                g.Route = RouteUpload;
                return g;
            }
            if (req.Attestation.Given && req.Attestation.CpuAccessControl == Attestation.CpuAccessDisabled)
            {
                g.Allowed = true;
                g.Route = RouteAttestation;
                return g;
            }
            if (uploadRoute) g.Why = Refused + "nothing shows the CPU's access control is off: upload from " + req.Address + " in this session first, or give attestation.cpuAccessControl \"disabled\" (with attestation.by and attestation.text), the operator's own observation of the CPU, never a value a caller fills in by default";
            else g.Why = Refused + "nothing shows the CPU's access control is off: give attestation.cpuAccessControl \"disabled\" (with attestation.by and attestation.text), the operator's own observation of the CPU, never a value a caller fills in by default; " + UploadRouteWhy;
            return g;
        }
    }

    // Answer is the agent's resolved answer to one question: Yes means do
    // what the question asks (stop the CPU, reinitialize the DBs it names,
    // overwrite, trust the certificate, use the protection password, start
    // the CPU after loading). The only question that can be answered no
    // and still load is start-after: no means stay in STOP. Choice is
    // which answer a question with more than one yes gets: user-management
    // keep (ChoiceKeep) or reset (ChoiceReset); "" for every other
    // question, and never reset by default.
    public sealed class Answer
    {
        public const string ChoiceKeep = "keep", ChoiceReset = "reset";
        public Question Question;
        public bool Yes;
        public string Choice = "";
    }

    public static class DownloadRule
    {
        // Answers turns approved decisions into one answer per question,
        // in order, so TIA's side never derives an answer itself. Call it
        // only when Resolve left nothing unanswered or declined.
        public static List<Answer> Answers(List<Question> qs, Decisions d)
        {
            var outp = new List<Answer>();
            foreach (var q in qs)
            {
                bool yes = true;
                string choice = "";
                if (q.Id == QuestionIds.StartAfter) yes = d.StartAfter.HasValue && d.StartAfter.Value;
                if (q.Id == QuestionIds.UserManagement)
                {
                    // Resolve let exactly one yes through.
                    if (d.UserManagementResetToProject == true) choice = Answer.ChoiceReset;
                    else if (d.UserManagementKeepOnline == true) choice = Answer.ChoiceKeep;
                }
                outp.Add(new Answer { Question = q.Copy(), Yes = yes, Choice = choice });
            }
            return outp;
        }

        // Resolve applies the download rule: every question is answered
        // only from d. It fills the questions with no answer and the ones
        // answered no; a download goes ahead only when both are empty.
        // accessControlKept is whether the session's spec keeps the CPU's
        // access control as found: only then can user-management be
        // answered keep. reset is UserManagementRule's verdict on a reset
        // (null refuses): only an allowed one answers it reset.
        //
        // user-management, keep x reset: a yes answers it (both yes is a
        // bad request, refused before this); with no yes, a no declines;
        // with neither, it is unanswered.
        public static void Resolve(List<Question> qs, Decisions d, bool haveProtection, bool haveConfigurationPassword, bool accessControlKept, UserManagementRule.Gate reset, List<Question> unanswered, List<Question> declined)
        {
            foreach (var q0 in qs)
            {
                var q = q0.Copy();
                switch (q.Id)
                {
                    case QuestionIds.StopCpu:
                        Sort(q, d.StopCpu, unanswered, declined);
                        break;
                    case QuestionIds.Overwrite:
                        Sort(q, d.Overwrite, unanswered, declined);
                        break;
                    case QuestionIds.ConsistentDownload:
                        Sort(q, d.ConsistentDownload, unanswered, declined);
                        break;
                    case QuestionIds.AlarmTexts:
                        Sort(q, d.AlarmTexts, unanswered, declined);
                        break;
                    case QuestionIds.AllBlocks:
                        Sort(q, d.AllBlocks, unanswered, declined);
                        break;
                    case QuestionIds.UserManagement:
                        bool keep = d.UserManagementKeepOnline == true, resetYes = d.UserManagementResetToProject == true;
                        if (keep && resetYes)
                        {
                            q.Message = q.Message + " (decisions.userManagementKeepOnline and decisions.userManagementResetToProject are both yes; user-management takes one answer)";
                            unanswered.Add(q);
                        }
                        else if (resetYes)
                        {
                            // Resetting is acting: only UserManagementRule
                            // lets it, the same verdict tiad checked before
                            // calling TIA.
                            if (reset == null || !reset.Allowed)
                            {
                                q.Message = q.Message + " (" + (reset == null || reset.Why == "" ? "decisions.userManagementResetToProject is refused" : reset.Why) + ")";
                                unanswered.Add(q);
                            }
                        }
                        else if (keep)
                        {
                            // Keeping the CPU's users only for a spec that
                            // keeps access control; a spec that sets it
                            // would leave them disagreeing.
                            if (!accessControlKept)
                            {
                                q.Message = q.Message + " (tiad keeps the CPU's online user management only for a keep spec on a session that never set access control: an apply of fullAccessUser on this session needs a fresh session that applies keep)";
                                unanswered.Add(q);
                            }
                        }
                        else if (d.UserManagementKeepOnline == false || d.UserManagementResetToProject == false) declined.Add(q);
                        else unanswered.Add(q);
                        break;
                    case QuestionIds.IdentificationData:
                        Sort(q, d.IdentificationDataLoadNothing, unanswered, declined);
                        break;
                    case QuestionIds.StartAfter:
                        // Either answer is a decision: no means load and
                        // stay in STOP, a legitimate choice.
                        if (!d.StartAfter.HasValue) unanswered.Add(q);
                        break;
                    case QuestionIds.Reinitialize:
                        // A reset nobody named cannot be agreed to.
                        List<string> missing = NotCovered(q.Objects, d.Reinitialize);
                        if (d.Reinitialize.Count == 0 || q.Objects.Count == 0)
                        {
                            unanswered.Add(q);
                        }
                        else if (missing.Count > 0)
                        {
                            q.Message = q.Message + " (not agreed: " + string.Join(", ", missing) + ")";
                            unanswered.Add(q);
                        }
                        break;
                    case QuestionIds.Certificate:
                        if (d.TrustCertificate == "") unanswered.Add(q);
                        else if (!string.Equals(d.TrustCertificate, q.Detail, StringComparison.OrdinalIgnoreCase)) declined.Add(q);
                        break;
                    case QuestionIds.Password:
                        if (!haveProtection) unanswered.Add(q);
                        break;
                    case QuestionIds.ConfigurationPassword:
                        if (!haveConfigurationPassword) unanswered.Add(q);
                        break;
                    default:
                        // A check tiad has never seen cannot be answered
                        // by accident.
                        unanswered.Add(q);
                        break;
                }
            }
        }

        static void Sort(Question q, bool? a, List<Question> unanswered, List<Question> declined)
        {
            if (!a.HasValue) unanswered.Add(q);
            else if (!a.Value) declined.Add(q);
        }

        // NotCovered returns the names in want that agreed lacks, compared
        // case-insensitively (TIA block names are), sorted.
        static List<string> NotCovered(List<string> want, List<string> agreed)
        {
            var ok = new HashSet<string>(StringComparer.Ordinal);
            foreach (var a in agreed) ok.Add(a.ToLowerInvariant());
            var outp = new List<string>();
            foreach (var w in want)
            {
                if (!ok.Contains(w.ToLowerInvariant())) outp.Add(w);
            }
            outp.Sort(StringComparer.Ordinal);
            return outp;
        }
    }

    // DownloadOutcome is what ITia.Download did.
    public sealed class DownloadOutcome
    {
        // Questions are TIA's pre-download checks, as asked.
        public List<Question> Questions = new List<Question>();
        // Loaded is true when TIA loaded the CPU (or ran its download
        // without asking anything, which counts as loaded).
        public bool Loaded;
        // State is the CPU's mode afterwards ("RUN", "STOP"), or "" when
        // nothing says, and StateSource what says it: "tia-message" (TIA's
        // download messages, TiaNames.ModeFromMessages), or "".
        public string State = "";
        public string StateSource = "";
        // Messages is TIA's download result's messages, joined, or "".
        public string Messages = "";
    }

    public sealed class Difference
    {
        public string Object = "", State = "";
    }

    // CompareNode is one element of TIA's online compare tree, as TIA
    // reports it: the object's name, TIA's comparison state for it
    // (ComparisonResult's name, e.g. "ObjectsIdentical") and its
    // children. TiaNames.CompareEqual decides what the tree proves.
    public sealed class CompareNode
    {
        public string Name = "", State = "";
        public List<CompareNode> Children = new List<CompareNode>();
    }

    // ProjectBlock is one program block the project holds, as Openness
    // reports it: its name, its Openness type name ("OB", "FB", "FC",
    // "GlobalDB", "InstanceDB", "ArrayDB", ...) and its number. The glue
    // reads them and decides nothing; TiaNames.BlockLabel names the block
    // as TIA's compare tree does.
    public sealed class ProjectBlock
    {
        public string Name = "", Type = "";
        public int Number;
    }

    // OnlineCompare is one online compare: TIA's compare tree, and every
    // program block the project held when it was compared, read in the
    // same call. TiaNames.CompareEqual needs both: an equal tree proves
    // the CPU holds the project only when every one of those blocks is in
    // it, compared and identical (#789 stage 3 gate).
    public sealed class OnlineCompare
    {
        public CompareNode Tree;
        public List<ProjectBlock> Blocks;
    }

    public sealed class ExportOptions
    {
        public string Blocks = ""; // "scl", "xml" or ""
        public bool TagTables, Hardware, Gsdml;
    }
}
