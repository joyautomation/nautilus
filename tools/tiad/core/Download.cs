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

namespace Tiad
{
    public static class QuestionIds
    {
        public const string StopCpu = "stop-cpu";
        public const string Reinitialize = "reinitialize";
        public const string Overwrite = "overwrite";
        public const string StartAfter = "start-after";
        public const string Password = "password";
        public const string Certificate = "certificate";
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
        public bool? StopCpu, Overwrite, StartAfter;
        public List<string> Reinitialize = new List<string>();
        public string TrustCertificate = "";

        public static Decisions Read(Reader r)
        {
            var d = new Decisions();
            if (r == null) return d;
            d.StopCpu = r.OptBool("stopCpu");
            d.Overwrite = r.OptBool("overwrite");
            d.StartAfter = r.OptBool("startAfter");
            d.Reinitialize = r.Strs("reinitialize");
            d.TrustCertificate = r.Str("trustCertificate");
            return d;
        }
    }

    public sealed class DownloadRequest
    {
        public string Address = "";
        public bool Hardware, Software, DryRun, Announced;
        public Decisions Decisions = new Decisions();
        public Secrets Secrets = new Secrets();

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
            return d;
        }

        // Validate is tiad.DownloadRequest.Validate: the request's own
        // shape, with Go's messages.
        public string Validate()
        {
            if (!Ip.IsIPv4(Address)) return "download: address " + Json.Quote(Address) + " is not an IPv4 address";
            if (!Hardware && !Software) return "download: neither hardware nor software selected";
            if (!DryRun && !Announced) return "download: a real download must be announced first (announced: true)";
            return null;
        }
    }

    public static class DownloadRule
    {
        // Resolve applies the download rule: every question is answered
        // only from d. It fills the questions with no answer and the ones
        // answered no; a download goes ahead only when both are empty.
        public static void Resolve(List<Question> qs, Decisions d, bool haveProtection, List<Question> unanswered, List<Question> declined)
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
        // Loaded is true only when the decide callback approved and TIA
        // loaded the CPU.
        public bool Loaded;
        // State is the CPU's mode afterwards ("RUN", "STOP"), or "".
        public string State = "";
    }

    public sealed class Difference
    {
        public string Object = "", State = "";
    }

    public sealed class ExportOptions
    {
        public string Blocks = ""; // "scl", "xml" or ""
        public bool TagTables, Hardware, Gsdml;
    }
}
