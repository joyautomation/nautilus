// TiaRules.cs: every decision tiad.Openness makes, as pure code over
// names and plain values. tiad.Openness is glue: it reads TIA's objects
// into these, and writes back what they say. CI has no Siemens assemblies,
// so the decisions live here, where tools/tiad/test checks them under
// Mono.

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

namespace Tiad
{
    // LicenceLatch is the licence gate. TIA's licence is not readable
    // before TIA's first start (on a fresh machine that start begins the
    // trial), so the gate is unknown, which blocks nothing, until TIA
    // starts. Then it latches: ok when TIA started, failed when the start
    // failed on a licence error. A failed latch blocks every TIA call until
    // the agent restarts.
    public sealed class LicenceLatch
    {
        public const string StartedDetail = "TIA started, so its licence lets Openness run";
        public const string FailedRemedy = "install or activate a TIA Portal licence (Automation License Manager), then restart tiad";

        readonly object mu = new object();
        string state = Gate.StateUnknown;
        string detail = MachineGates.LicenceUnknown;

        // Started records how a TIA start went: error is null when TIA
        // started. An error that is not a licence error says nothing about
        // the licence, and leaves the gate as it was.
        public void Started(Exception error)
        {
            lock (mu)
            {
                if (state == Gate.StateFailed) return;
                if (error == null)
                {
                    state = Gate.StateOk;
                    detail = StartedDetail;
                }
                else if (IsLicenceError(error))
                {
                    state = Gate.StateFailed;
                    detail = "TIA did not start: " + error.Message;
                }
            }
        }

        public Gate Current()
        {
            lock (mu)
            {
                var g = new Gate { Name = "licence", State = state, Detail = detail };
                if (state == Gate.StateFailed) g.Remedy = FailedRemedy;
                return g;
            }
        }

        // IsLicenceError reports whether e, or an exception inside it,
        // names a licence. Provisional: which exception TIA throws without
        // a licence is first seen at S2a.
        public static bool IsLicenceError(Exception e)
        {
            for (; e != null; e = e.InnerException)
            {
                string m = (e.Message ?? "").ToLowerInvariant();
                if (m.Contains("licen") || m.Contains("lizenz")) return true;
            }
            return false;
        }
    }

    // Setting is what tiad.Openness writes into one TIA configuration:
    // a selection by its enum name, or the protection password.
    public sealed class Setting
    {
        public const string Select = "select", Password = "password";

        public string Kind = "";
        public string Value = ""; // the selection's enum name

        public static Setting Choose(string value)
        {
            return new Setting { Kind = Select, Value = value };
        }

        public override string ToString()
        {
            return Kind == Select ? Value : Kind;
        }
    }

    // DownloadChecks maps TIA's pre-download configurations, by their
    // Openness type name, to the agent's questions and back.
    public static class DownloadChecks
    {
        // QuestionId is the question a configuration asks. A type tiad has
        // never seen is other, which nothing answers.
        public static string QuestionId(string type)
        {
            switch (type)
            {
                case "StopModules":
                    return QuestionIds.StopCpu;
                case "StartModules":
                    return QuestionIds.StartAfter;
                case "OverwriteSystemData":
                    return QuestionIds.Overwrite;
                case "DataBlockReinitialization":
                case "DataBlockReinitializationOrKeepActualValues":
                    return QuestionIds.Reinitialize;
                case "ModuleReadAccessPassword":
                case "ModuleWriteAccessPassword":
                    return QuestionIds.Password;
                case "TlsVerificationConfiguration":
                    return QuestionIds.Certificate;
                default:
                    return QuestionIds.Other;
            }
        }

        // For is the setting that applies answer yes or no to a
        // configuration of type, or null when that answer cannot be
        // applied: then the download is cancelled, never left to TIA's
        // default.
        public static Setting For(string type, bool yes)
        {
            switch (type)
            {
                case "StopModules":
                    return yes ? Setting.Choose("StopAll") : null;
                case "StartModules":
                    return Setting.Choose(yes ? "StartModule" : "NoAction");
                case "OverwriteSystemData":
                    return yes ? Setting.Choose("Overwrite") : null;
                case "DataBlockReinitialization":
                case "DataBlockReinitializationOrKeepActualValues":
                    return yes ? Setting.Choose("StopPlcAndReinitialize") : null;
                case "ModuleReadAccessPassword":
                case "ModuleWriteAccessPassword":
                    return yes ? new Setting { Kind = Setting.Password } : null;
                case "TlsVerificationConfiguration":
                    return yes ? Setting.Choose("Trusted") : null;
                default:
                    return null;
            }
        }
    }

    // Pending is one configuration TIA has pending before a download: Key
    // is TIA's object (compared with Equals), Type its Openness type name.
    public sealed class Pending
    {
        public object Key;
        public string Type = "", Message = "";
    }

    // DownloadCancel is thrown out of TIA's pre-download callback to stop
    // the download. Declined is true when decide said no: a clean cancel,
    // nothing approved. Any other cancel is a failure.
    public sealed class DownloadCancel : Exception
    {
        public readonly bool Declined;

        public DownloadCancel(string message, bool declined) : base(message)
        {
            Declined = declined;
        }
    }

    // DownloadEnd is how a download ended, as ITia.Download reports it:
    // loaded, declined (decide said no, nothing approved), or a failure,
    // which the agent reports as outcome_unknown once decide has run.
    public sealed class DownloadEnd
    {
        public bool Loaded, Declined;
        public string Failure; // null unless the download failed
    }

    // DownloadDecider keeps ITia.Download's promise over Openness's
    // pre-download callback, which TIA calls once per configuration:
    // decide runs exactly once, at the first callback, with every pending
    // configuration as a question, before anything loads. Each callback
    // then gets that configuration's resolved answer, written explicitly.
    // Anything that would leave a configuration to TIA's default cancels:
    // a list that cannot be read, a configuration missing from it or
    // asked twice, an answer that cannot be applied. Provisional until S2b
    // shows TIA lists its pending configurations and a cancel loads nothing.
    //
    // Whether the CPU starts after loading is decided before loading too:
    // when no pending configuration asks it, start-after is asked anyway,
    // and TIA's after-download callback (After) gets that answer.
    public sealed class DownloadDecider
    {
        // StartAfterType is the configuration that starts the CPU after a
        // download.
        public const string StartAfterType = "StartModules";

        // AfterKey stands for a configuration TIA asks only after loading.
        static readonly object AfterKey = new object();

        readonly Func<List<Question>, List<Answer>> decide;
        List<Pending> pending;
        List<Setting> settings;
        bool[] applied;

        // Asked is the questions decide was asked, in order.
        public readonly List<Question> Asked = new List<Question>();

        // Approved is true once decide answered every question.
        public bool Approved;

        // Cancelled is the cancel this decider threw, if any: TIA may wrap
        // it before it comes back out of the download.
        public DownloadCancel Cancelled;

        public DownloadDecider(Func<List<Question>, List<Answer>> decide)
        {
            this.decide = decide;
        }

        public bool Decided
        {
            get { return pending != null; }
        }

        DownloadCancel Cancel(string why, bool declined)
        {
            Cancelled = new DownloadCancel(why, declined);
            return Cancelled;
        }

        static string Name(Pending p)
        {
            return p.Message == "" ? p.Type : p.Type + " (" + p.Message + ")";
        }

        int IndexOf(object key)
        {
            for (int i = 0; i < pending.Count; i++)
            {
                if (object.Equals(pending[i].Key, key)) return i;
            }
            return -1;
        }

        // Next answers TIA's callback for the configuration key. list reads
        // every configuration pending: it runs once, at the first callback,
        // and returns null when TIA's side cannot list them. Next returns
        // the setting to write, or throws a DownloadCancel.
        public Setting Next(object key, string type, string message, Func<List<Pending>> list)
        {
            if (Cancelled != null) throw Cancelled;
            if (pending == null) Decide(key, type, message, list);
            int i = IndexOf(key);
            if (i < 0)
            {
                throw Cancel("TIA asked " + Name(new Pending { Type = type, Message = message }) + " after the decision, which did not cover it", false);
            }
            if (applied[i]) throw Cancel("TIA asked " + Name(pending[i]) + " twice", false);
            applied[i] = true;
            return settings[i];
        }

        void Decide(object key, string type, string message, Func<List<Pending>> list)
        {
            List<Pending> all = list();
            string what = Name(new Pending { Type = type, Message = message });
            if (all == null || all.Count == 0)
            {
                throw Cancel("tiad cannot list TIA's pre-download checks (TIA asked " + what + "), so it asked nothing and cancelled", false);
            }
            for (int i = 0; i < all.Count; i++)
            {
                for (int j = 0; j < i; j++)
                {
                    if (object.Equals(all[i].Key, all[j].Key)) throw Cancel("TIA listed " + Name(all[i]) + " twice", false);
                }
            }
            pending = all;
            if (IndexOf(key) < 0)
            {
                pending = null;
                throw Cancel("TIA asked " + what + ", which its list of pre-download checks lacks", false);
            }
            bool startAsked = false;
            foreach (var p in all)
            {
                if (DownloadChecks.QuestionId(p.Type) == QuestionIds.StartAfter) startAsked = true;
            }
            if (!startAsked)
            {
                all = new List<Pending>(all);
                all.Add(new Pending { Key = AfterKey, Type = StartAfterType, Message = "start the CPU after loading" });
                pending = all;
            }
            foreach (var p in all)
            {
                // Detail names TIA's configuration, except for a certificate,
                // whose detail is what a trust decision must match.
                string id = DownloadChecks.QuestionId(p.Type);
                Asked.Add(new Question { Id = id, Message = p.Message, Detail = id == QuestionIds.Certificate ? "" : p.Type });
            }
            applied = new bool[all.Count];
            List<Answer> answers = decide(Copy(Asked));
            if (answers == null) throw Cancel("the download was not decided: nothing was loaded", true);
            if (answers.Count != all.Count) throw Cancel("decide answered " + answers.Count + " of " + all.Count + " questions", false);
            settings = new List<Setting>();
            for (int i = 0; i < all.Count; i++)
            {
                if (answers[i] == null || answers[i].Question == null || answers[i].Question.Id != Asked[i].Id)
                {
                    throw Cancel("decide's answer " + (i + 1) + " is not to " + Asked[i].Id, false);
                }
                Setting s = DownloadChecks.For(all[i].Type, answers[i].Yes);
                if (s == null)
                {
                    throw Cancel("tiad cannot apply " + (answers[i].Yes ? "yes" : "no") + " to " + Name(all[i]), false);
                }
                settings.Add(s);
            }
            Approved = true;
        }

        static List<Question> Copy(List<Question> qs)
        {
            var l = new List<Question>();
            foreach (var q in qs) l.Add(q.Copy());
            return l;
        }

        // After answers TIA's after-download callback for a configuration
        // of type: the decided answer to the same configuration when it is
        // still unapplied, else null, and TIA's own setting stands (it
        // cannot change what was loaded). Only an approved download gets
        // here.
        public Setting After(string type)
        {
            if (!Approved) return null;
            for (int i = 0; i < pending.Count; i++)
            {
                if (!applied[i] && pending[i].Type == type)
                {
                    applied[i] = true;
                    return settings[i];
                }
            }
            return null;
        }

        // Finish reads how TIA's download call ended: state is its result's
        // state when it returned (null when it threw), messages its
        // result's messages, error what it threw. A cancel TIA swallowed,
        // a failed state, or a decided configuration TIA never asked about
        // is a failure. A download TIA ran without one callback is loaded
        // undecided: the agent reports that as outcome_unknown (decide ran
        // zero times), never as a refusal.
        public DownloadEnd Finish(string state, string messages, Exception error)
        {
            var end = new DownloadEnd();
            if (error != null)
            {
                if (Cancelled != null)
                {
                    if (Cancelled.Declined) end.Declined = true;
                    else end.Failure = Cancelled.Message;
                    return end;
                }
                for (Exception i = error; i != null; i = i.InnerException)
                {
                    if (i is DownloadCancel)
                    {
                        end.Failure = i.Message;
                        return end;
                    }
                }
                end.Failure = TiaNames.Message(error);
                return end;
            }
            string ended = "TIA's download ended " + (state ?? "without a state") + (string.IsNullOrEmpty(messages) ? "" : ": " + messages);
            if (Cancelled != null)
            {
                end.Failure = "TIA went on after tiad cancelled the download (" + Cancelled.Message + "); " + ended;
                return end;
            }
            if (!Decided)
            {
                end.Loaded = true;
                return end;
            }
            if (!TiaNames.DownloadLoaded(state))
            {
                end.Failure = ended;
                return end;
            }
            List<string> missed = Unapplied();
            if (missed.Count > 0)
            {
                end.Failure = "TIA loaded without asking " + string.Join(", ", missed.ToArray()) + " again, so its answer was never written";
                return end;
            }
            end.Loaded = true;
            return end;
        }

        // Unapplied names the decided pre-download configurations TIA
        // never asked about in a callback, so never got their answer. The
        // start-after question asked for the after-download callback is
        // not one: TIA asks it only when it stopped the CPU.
        public List<string> Unapplied()
        {
            var l = new List<string>();
            if (pending == null) return l;
            for (int i = 0; i < pending.Count; i++)
            {
                if (!applied[i] && pending[i].Key != AfterKey) l.Add(Name(pending[i]));
            }
            return l;
        }
    }

    // TiaNames are the small mappings from TIA's names to the agent's.
    public static class TiaNames
    {
        // ConnectionMode is the connection mode tiad reaches a CPU in.
        public const string ConnectionMode = "PN/IE";

        // TypeIdentifier is the hardware catalogue's identifier for an
        // order number at a firmware version: OrderNumber:<order>/V<x.y>.
        public static string TypeIdentifier(string order, string firmware)
        {
            string fw = firmware.StartsWith("V", StringComparison.Ordinal) ? firmware : "V" + firmware;
            return "OrderNumber:" + order + "/" + fw;
        }

        // PcInterface picks the PC interface to reach a CPU through, from
        // the names TIA lists for the connection mode: the configured one
        // (TIAD_PC_INTERFACE), or the only one there is.
        public static string PcInterface(List<string> names, string configured)
        {
            var sorted = new List<string>(names);
            sorted.Sort(StringComparer.Ordinal);
            string all = sorted.Count == 0 ? "none" : string.Join(", ", sorted.ToArray());
            if (configured != "")
            {
                foreach (string n in names)
                {
                    if (n == configured) return n;
                }
                throw new TiaException("TIAD_PC_INTERFACE " + Json.Quote(configured) + " is not a " + ConnectionMode + " PC interface TIA lists (" + all + ")");
            }
            if (names.Count == 1) return names[0];
            if (names.Count == 0) throw new TiaException("TIA lists no " + ConnectionMode + " PC interface");
            throw new TiaException("TIA lists several " + ConnectionMode + " PC interfaces (" + all + "): set TIAD_PC_INTERFACE to one");
        }

        // OpennessAssembly is the file an assembly of that simple name
        // resolves to: Siemens.Engineering's own assemblies, from the
        // registered public API folder; null for anything else, which the
        // runtime resolves as it always does.
        public static string OpennessAssembly(string apiDir, string name)
        {
            if (string.IsNullOrEmpty(apiDir) || name == null) return null;
            if (name != "Siemens.Engineering" && !name.StartsWith("Siemens.Engineering.", StringComparison.Ordinal)) return null;
            if (name.IndexOfAny(new[] { '/', '\\', ':' }) >= 0 || name.Contains("..")) return null;
            return apiDir + "\\" + name + ".dll";
        }

        // Message is an exception's text with the inner ones it wraps, each
        // once: Openness wraps the reason in its own exceptions.
        public static string Message(Exception e)
        {
            string m = e.Message ?? "";
            for (Exception i = e.InnerException; i != null; i = i.InnerException)
            {
                if (!string.IsNullOrEmpty(i.Message) && m.IndexOf(i.Message, StringComparison.Ordinal) < 0) m += ": " + i.Message;
            }
            return m;
        }

        // Only picks the one name in names, for what TIA lists more than one
        // of only on machines tiad was not built for.
        public static string Only(string what, List<string> names)
        {
            if (names.Count == 1) return names[0];
            var sorted = new List<string>(names);
            sorted.Sort(StringComparer.Ordinal);
            throw new TiaException("TIA lists " + names.Count + " " + what + "s" + (names.Count == 0 ? "" : " (" + string.Join(", ", sorted.ToArray()) + ")") + ", not one");
        }

        // BlockKind is the agent's kind for a block's Openness type name,
        // "" for a type that is no block tiad reports.
        public static string BlockKind(string type)
        {
            switch (type)
            {
                case "OB":
                    return "OB";
                case "FB":
                    return "FB";
                case "FC":
                    return "FC";
                case "GlobalDB":
                case "InstanceDB":
                case "ArrayDB":
                    return "DB";
                case "PlcStruct":
                case "PlcType":
                    return "UDT";
                default:
                    return "";
            }
        }

        // ExportsAsScl reports whether a block in this programming language
        // exports as an SCL source; the rest export as SimaticML.
        public static bool ExportsAsScl(string language)
        {
            return language == "SCL" || language == "DB";
        }

        // CheckAttribute refuses to set an attribute the object does not
        // have, naming the ones it has: TIA's names are checked, never
        // guessed.
        public static void CheckAttribute(string what, List<string> available, string name)
        {
            if (available.Contains(name)) return;
            var sorted = new List<string>(available);
            sorted.Sort(StringComparer.Ordinal);
            throw new TiaException(what + " has no attribute " + name + "; it has: " + string.Join(", ", sorted.ToArray()));
        }

        // CompileResult is the agent's result for TIA's compiler state.
        // A state tiad does not know is an error.
        public static string CompileResult(string state)
        {
            switch (state)
            {
                case "Success":
                case "Information":
                    return "success";
                case "Warning":
                    return "warning";
                default:
                    return "error";
            }
        }

        // Severity is the agent's severity for a compiler message's state.
        public static string Severity(string state)
        {
            switch (state)
            {
                case "Success":
                case "Information":
                    return "info";
                case "Warning":
                    return "warning";
                default:
                    return "error";
            }
        }

        // DownloadLoaded reports whether TIA's download result state means
        // the download went through. Anything else, including a state tiad
        // does not know, leaves the CPU in doubt.
        public static bool DownloadLoaded(string state)
        {
            return state == "Success" || state == "Information" || state == "Warning";
        }

        // Difference is the difference state for one compare element's
        // state, "" when the element itself does not differ (its children
        // may), or "different" for a state tiad does not know.
        public static string Difference(string state)
        {
            switch (state)
            {
                case "ObjectsIdentical":
                case "CompareIrrelevant":
                case "FolderContentsIdentical":
                case "FolderContentsDifferent":
                    return "";
                case "LeftMissing":
                    return "only online";
                case "RightMissing":
                    return "only offline";
                default:
                    return "different";
            }
        }

        // FileName makes a TIA object's name one safe file name segment:
        // every character but ASCII letters, digits, '_' and '-' becomes
        // %XX of its UTF-8 bytes, and a Windows device name gets its first
        // letter escaped, so the work dir's sandbox rule always accepts it.
        public static string FileName(string name, string ext)
        {
            var b = new StringBuilder();
            foreach (byte x in Encoding.UTF8.GetBytes(name))
            {
                char c = (char)x;
                if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-')
                {
                    b.Append(c);
                }
                else
                {
                    b.Append('%').Append(x.ToString("X2", CultureInfo.InvariantCulture));
                }
            }
            string s = b.Length == 0 ? "%" : b.ToString();
            if (s.Length >= 3 && IsDevice(s.ToUpperInvariant()))
            {
                s = "%" + ((int)s[0]).ToString("X2", CultureInfo.InvariantCulture) + s.Substring(1);
            }
            return s + ext;
        }

        static bool IsDevice(string upper)
        {
            if (upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL") return true;
            return upper.Length == 4 && (upper.StartsWith("COM", StringComparison.Ordinal) || upper.StartsWith("LPT", StringComparison.Ordinal)) &&
                upper[3] >= '0' && upper[3] <= '9';
        }
    }
}
