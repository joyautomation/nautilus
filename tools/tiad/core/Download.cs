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
        // UserManagementDownload, answered only by keeping the CPU's
        // online user management, and only when the spec keeps access
        // control as found (passive: nothing on the CPU changes).
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
        public List<string> Reinitialize = new List<string>();
        public string TrustCertificate = "";
        // KeptDevices names every device the caller agrees to load as the
        // project keeps it; it must be exactly the kept set (KeptRule).
        public List<string> KeptDevices = new List<string>();
        // AcceptUnverifiedHardware answers UnverifiedHardware: load the
        // hardware configuration although tiad cannot verify it. Yes
        // needs the request's attestation too (HardwareRule).
        public bool? AcceptUnverifiedHardware;

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
            d.IdentificationDataLoadNothing = r.OptBool("identificationDataLoadNothing");
            d.StartAfter = r.OptBool("startAfter");
            d.Reinitialize = r.Strs("reinitialize");
            d.TrustCertificate = r.Str("trustCertificate");
            d.KeptDevices = r.Strs("keptDevices");
            d.AcceptUnverifiedHardware = r.OptBool("acceptUnverifiedHardware");
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
        public string By = "", Text = "", At = "";

        public static Attestation Read(Reader r)
        {
            var a = new Attestation();
            if (r == null) return a;
            a.By = r.Str("by");
            a.Text = r.Str("text");
            return a;
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
            if (By == "" && Text == "") return "unset";
            return "{by=" + Json.Quote(By) + " at=" + (At == "" ? "unset" : At) + " text=" + Json.Quote(Text) + "}";
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["by"] = By;
            o["at"] = At;
            o["text"] = Text;
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

    // Answer is the agent's resolved answer to one question: Yes means do
    // what the question asks (stop the CPU, reinitialize the DBs it names,
    // overwrite, trust the certificate, use the protection password, start
    // the CPU after loading). The only question that can be answered no
    // and still load is start-after: no means stay in STOP.
    public sealed class Answer
    {
        public Question Question;
        public bool Yes;
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
                if (q.Id == QuestionIds.StartAfter) yes = d.StartAfter.HasValue && d.StartAfter.Value;
                outp.Add(new Answer { Question = q.Copy(), Yes = yes });
            }
            return outp;
        }

        // Resolve applies the download rule: every question is answered
        // only from d. It fills the questions with no answer and the ones
        // answered no; a download goes ahead only when both are empty.
        // accessControlKept is whether the session's spec keeps the CPU's
        // access control as found: only then can user-management be
        // answered.
        public static void Resolve(List<Question> qs, Decisions d, bool haveProtection, bool haveConfigurationPassword, bool accessControlKept, List<Question> unanswered, List<Question> declined)
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
                        // Keeping the CPU's users is tiad's only answer, and
                        // only for a spec that keeps access control; a spec
                        // that sets it would leave them disagreeing.
                        if (d.UserManagementKeepOnline.HasValue && d.UserManagementKeepOnline.Value && !accessControlKept)
                        {
                            q.Message = q.Message + " (tiad keeps the CPU's online user management only for a keep spec on a session that never set access control: an apply of fullAccessUser on this session needs a fresh session that applies keep)";
                            unanswered.Add(q);
                        }
                        else Sort(q, d.UserManagementKeepOnline, unanswered, declined);
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
