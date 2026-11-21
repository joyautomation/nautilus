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
    // before TIA starts, so the gate is unknown, which blocks nothing,
    // until TIA starts. Then it latches: ok when TIA started, failed when
    // the start or any later TIA call failed on a licence error (TIA starts
    // headless without one, and refuses at the first call that needs it). A
    // failed latch blocks every TIA call until the agent restarts.
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
        // Failed records a TIA call that failed after the start: a licence
        // error fails the gate; any other error says nothing about it.
        public void Failed(Exception error)
        {
            lock (mu)
            {
                if (state == Gate.StateFailed || !IsLicenceError(error)) return;
                state = Gate.StateFailed;
                detail = "TIA refused a call: " + error.Message;
            }
        }

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
    // a selection by its enum name, the protection password, or the
    // configuration password.
    public sealed class Setting
    {
        public const string Select = "select", Password = "password", ConfigurationPassword = "configuration-password";

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

        // Secret is the one secret a password setting writes, or null for
        // a selection: the protection password for Password, the
        // configuration password for ConfigurationPassword, never the
        // other. An empty secret is still returned: the glue refuses it.
        public static string Secret(Setting s, Secrets secrets)
        {
            if (s.Kind == Password) return secrets.Protection;
            if (s.Kind == ConfigurationPassword) return secrets.ConfigurationPassword;
            return null;
        }
    }

    // ConfigProtection decides what tiad.Openness does to a CPU's FW 3.1
    // protection of confidential configuration data
    // (cpu.configurationProtection, section 3.3). TIA's
    // PlcMasterSecretConfigurator holds it; the decision comes from the
    // state read from the project after the station step, never from the
    // spec last applied. After any write the glue reads the state again,
    // and Check refuses unless it is the state the action promises.
    public static class ConfigProtection
    {
        // States are MasterSecretConfiguration's names. Absent is a CPU
        // with no configurator (firmware before 3.1).
        public const string None = "None", WithoutPassword = "WithoutPassword", WithPassword = "WithPassword", Absent = "";

        // Actions. ProtectThenUnprotect turns off a CPU that was never set
        // up by Protect with a throwaway password and Unprotect with it:
        // both steps are proven on the TIA machine, where a bare
        // Unprotect of a new CPU is not.
        public const string Nothing = "", Protect = "protect", UnprotectWithPassword = "unprotect-with-password", ProtectThenUnprotect = "protect-then-unprotect";

        // Throwaway is a one-use password for ProtectThenUnprotect that
        // meets TIA's rules, from a cryptographic random source. It is
        // never logged and never kept.
        public static string Throwaway()
        {
            var b = new byte[16];
            using (var rng = new System.Security.Cryptography.RNGCryptoServiceProvider()) rng.GetBytes(b);
            var sb = new StringBuilder("Tz");
            foreach (byte x in b) sb.Append(x.ToString("x2", CultureInfo.InvariantCulture));
            return sb.Append('9').ToString();
        }

        // Stranded is the refusal when turning off a CPU never set up
        // failed after Protect with a throwaway password: the CPU may be
        // protected by a password nobody holds, so the glue removes it
        // from the project. removeFailed is why removing it failed, or
        // null when it was removed.
        public static string Stranded(string why, string removeFailed)
        {
            string head = "turning the configuration protection off failed part-way (" + why + "), which could leave the CPU protected by a throwaway password nobody holds";
            if (removeFailed == null) return head + ", so tiad removed the CPU from the project: close this session without saving";
            return head + ", and removing the CPU from the project failed too (" + removeFailed + "): close this session without saving";
        }

        // Note is what an apply says about the protection besides its
        // changes, or null: password on a CPU already protected leaves it
        // as it is, so a changed secret does not reach the project.
        public static string Note(string current, string want)
        {
            if (current != WithPassword || want != StationRules.ProtectPassword) return null;
            return "the CPU's configuration protection was already on and is left as it is: TIA cannot check its password offline, so a changed secrets.configurationPassword does not reach the project, and a download asks for the password the project holds; to change it, apply none with the old password, then password with the new one";
        }

        // Plan returns the action that takes current to what want asks,
        // or throws a 400 that names the fix. keep never acts, and refuses
        // only TIA's unconfigured default, which never compiles. password
        // on an already protected CPU leaves it: TIA cannot check a
        // password offline, and a second Protect throws.
        public static string Plan(string current, string want, bool havePassword)
        {
            if (want == StationRules.ProtectKeep)
            {
                if (current == WithoutPassword)
                {
                    throw TiadException.BadRequest("{0}", "the project's CPU has its configuration protection not set up (TIA's default for a new CPU, which does not compile), so keep has nothing to keep: set cpu.configurationProtection to password or none");
                }
                return Nothing;
            }
            if (current == Absent)
            {
                throw TiadException.BadRequest("cpu.configurationProtection is {0}, and the project's CPU has no configuration protection (firmware 3.1 and later have it): set it to keep", want);
            }
            if (current != None && current != WithoutPassword && current != WithPassword)
            {
                throw TiadException.BadRequest("the project's CPU has its configuration protection in a state tiad does not know ({0}); nothing was changed", Json.Quote(current));
            }
            if (want == StationRules.ProtectPassword)
            {
                if (!havePassword) throw TiadException.BadRequest("{0}", "cpu.configurationProtection is password, and secrets.configurationPassword is empty (it comes from the secret store, never the spec)");
                return current == WithPassword ? Nothing : Protect;
            }
            if (want == StationRules.ProtectNone)
            {
                if (current == None) return Nothing;
                if (current == WithoutPassword) return ProtectThenUnprotect;
                if (!havePassword)
                {
                    throw TiadException.BadRequest("{0}", "the project's CPU has its configuration protected by a password; turning that off needs it in secrets.configurationPassword");
                }
                return UnprotectWithPassword;
            }
            throw TiadException.BadRequest("cpu.configurationProtection: {0}", StationRules.CheckConfigurationProtection(want) ?? Json.Quote(want));
        }

        // Target is the state an action promises.
        public static string Target(string action)
        {
            return action == Protect ? WithPassword : None;
        }

        // Check returns why the state read after action is not the one it
        // promises, or null.
        public static string Check(string action, string after)
        {
            if (action == Nothing || after == Target(action)) return null;
            return "after " + action + " the CPU's configuration protection reads " + (after == Absent ? "absent" : after) + ", not " + Target(action);
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
                case "PlcMasterSecretPassword":
                    return QuestionIds.ConfigurationPassword;
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
                case "PlcMasterSecretPassword":
                    return yes ? new Setting { Kind = Setting.ConfigurationPassword } : null;
                case "TlsVerificationConfiguration":
                    return yes ? Setting.Choose("Trusted") : null;
                default:
                    return null;
            }
        }

        // Answered are the configuration types For can answer.
        public static readonly string[] Answered =
        {
            "StopModules", "StartModules", "OverwriteSystemData", "DataBlockReinitialization",
            "DataBlockReinitializationOrKeepActualValues", "ModuleReadAccessPassword",
            "ModuleWriteAccessPassword", "PlcMasterSecretPassword", "TlsVerificationConfiguration",
        };

        // Selections are every (configuration type, selection) tiad may
        // write: For's selections, and start-after's NoAction, which an
        // after-download callback gets when nothing decided it.
        public static List<KeyValuePair<string, string>> Selections()
        {
            var l = new List<KeyValuePair<string, string>>();
            foreach (string t in Answered)
            {
                foreach (bool yes in new[] { true, false })
                {
                    Setting s = For(t, yes);
                    if (s == null || s.Kind != Setting.Select) continue;
                    l.Add(new KeyValuePair<string, string>(t, s.Value));
                }
            }
            return l;
        }
    }

    // Pending is one configuration TIA has pending before a download: Key
    // is TIA's object (compared with Equals), Type its Openness type name.
    public sealed class Pending
    {
        public object Key;
        public string Type = "", Message = "";
    }

    // DownloadCancel is how the decider stops a download, inside tiad:
    // Declined is true when decide said no (a clean cancel, nothing
    // approved); any other cancel is a failure. It never reaches TIA: the
    // glue's callbacks run through Boundary.Call with Keep, which throws
    // TIA only Boundary.Fail's exception, carrying the cancel's message.
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
        // InDoubt marks a failure once TIA's download call was entered:
        // the CPU may or may not be loaded (outcome_unknown).
        public bool InDoubt;
    }

    // Step is one answer to write into one TIA configuration. The glue
    // writes Setting, then reports Wrote; a write that fails is reported
    // with Fail. Index is the decided configuration's, or -1 for a write
    // no decision covers (start-after's NoAction after an undecided load).
    public sealed class Step
    {
        public Setting Setting;
        public int Index = -1;
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

        // Cancelled is the cancel this decider made, if any: the state how
        // the download ended is read from. What comes back out of TIA's
        // download is at best a copy of what crossed (Boundary), so it is
        // never matched by reference.
        public DownloadCancel Cancelled;

        // Token marks this decider's decline: "tiad-decline:" and 16
        // random hex digits, new for every download. A decline counts only
        // when what TIA throws back carries it (Ended): TIA's own text,
        // however close in words, never does.
        public readonly string Token = NewToken();

        public DownloadDecider(Func<List<Question>, List<Answer>> decide)
        {
            this.decide = decide;
        }

        static string NewToken()
        {
            var b = new byte[8];
            using (var rng = new System.Security.Cryptography.RNGCryptoServiceProvider()) rng.GetBytes(b);
            var t = new StringBuilder("tiad-decline:");
            foreach (byte x in b) t.Append(x.ToString("x2", CultureInfo.InvariantCulture));
            return t.ToString();
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
        // the step to write, or throws a DownloadCancel. A configuration
        // counts as answered only once its write is reported (Wrote).
        public Step Next(object key, string type, string message, Func<List<Pending>> list)
        {
            if (Cancelled != null)
            {
                // A callback after a decline: TIA went on past the cancel,
                // so the download can no longer end declined.
                if (Cancelled.Declined) Fail("TIA asked " + Name(new Pending { Type = type, Message = message }) + " after tiad declined the download (" + Cancelled.Message + "): TIA went on");
                throw Cancelled;
            }
            if (pending == null) Decide(key, type, message, list);
            int i = IndexOf(key);
            if (i < 0)
            {
                throw Cancel("TIA asked " + Name(new Pending { Type = type, Message = message }) + " after the decision, which did not cover it", false);
            }
            if (applied[i]) throw Cancel("TIA asked " + Name(pending[i]) + " twice", false);
            return new Step { Setting = settings[i], Index = i };
        }

        // Wrote reports that step's setting is written into TIA's
        // configuration.
        public void Wrote(Step step)
        {
            if (step.Index >= 0) applied[step.Index] = true;
        }

        // Fail reports that writing an answer failed: the download is
        // cancelled, so a configuration never keeps TIA's default unseen.
        // It returns the cancel to throw.
        public DownloadCancel Fail(string why)
        {
            // A failure after a decline is no decline: TIA went on.
            if (Cancelled == null || Cancelled.Declined) Cancelled = new DownloadCancel(why, false);
            return Cancelled;
        }

        // Keep records why a callback failed with e and returns the reason
        // to send TIA (Boundary.Call): the decider's own cancel as it is,
        // anything else (a list that threw, a decide that threw, a log
        // line) as a failure the decider keeps, so the download can never
        // end declined after it.
        public string Keep(Exception e)
        {
            var c = e as DownloadCancel;
            if (c != null && object.ReferenceEquals(c, Cancelled)) return Cancelled.Message;
            string text;
            try
            {
                text = TiaNames.Message(e);
            }
            catch (Exception)
            {
                text = "(" + e.GetType().Name + ", its message unreadable)";
            }
            return Fail("tiad's download callback failed: " + text).Message;
        }

        // loadSeen is set by TIA's after-download callback: TIA loaded.
        bool loadSeen;

        void Decide(object key, string type, string message, Func<List<Pending>> list)
        {
            string what = Name(new Pending { Type = type, Message = message });
            List<Pending> all;
            try
            {
                all = list();
            }
            catch (Exception e)
            {
                Listed(what, null);
                throw Cancel("tiad cannot list TIA's pre-download checks (TIA asked " + what + "; reading them failed: " + TiaNames.Message(e) + "), so it asked nothing and cancelled", false);
            }
            Listed(what, all);
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
            if (answers == null) throw Cancel("the download was not decided: nothing was loaded (" + Token + ")", true);
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

        // Listed logs TIA's pending configurations as read, before anything
        // is decided from them: why a download was cancelled must never
        // depend on the cancel coming back out of TIA (stage 2 lost it).
        static void Listed(string what, List<Pending> all)
        {
            try
            {
                if (all == null)
                {
                    Log.Line("download: TIA asked {0}; its list of pending checks could not be read", TiaNames.LogSafe(what));
                    return;
                }
                var names = new List<string>();
                foreach (var p in all) names.Add(p == null ? "null" : Name(p));
                Log.Line("download: TIA asked {0}; it lists {1} pending check(s): {2}", TiaNames.LogSafe(what), all.Count, TiaNames.LogSafe(string.Join("; ", names.ToArray())));
            }
            catch (Exception)
            {
            }
        }

        static List<Question> Copy(List<Question> qs)
        {
            var l = new List<Question>();
            foreach (var q in qs) l.Add(q.Copy());
            return l;
        }

        // After answers TIA's after-download callback for a configuration
        // of type: the decided answer to the same configuration when it is
        // still unapplied, and start-after's again if TIA asks it again
        // after loading. Starting the CPU is never left to TIA's
        // default: with nothing decided (a load nobody approved),
        // start-after is written NoAction, so the CPU stays as it is. For
        // any other configuration, null: TIA's own setting stands, since it
        // cannot change what was loaded. Any after-download callback means
        // TIA loaded.
        public Step After(string type)
        {
            loadSeen = true;
            if (Approved)
            {
                // The decided answer, written again if TIA asks again.
                for (int i = 0; i < pending.Count; i++)
                {
                    if (pending[i].Type == type && (!applied[i] || type == StartAfterType)) return new Step { Setting = settings[i], Index = i };
                }
            }
            if (type == StartAfterType) return new Step { Setting = Setting.Choose("NoAction") };
            return null;
        }

        // Finish reads how TIA's download call ended: entered is whether
        // TIA's download call was made at all, state its result's state
        // when it returned (null when it threw), messages its result's
        // messages, error what was thrown. A cancel TIA swallowed, a
        // failed state, or a decided configuration TIA never asked about
        // is a failure, and any failure once the call was entered is in
        // doubt. A download TIA ran without one callback is loaded
        // undecided: the agent reports that as outcome_unknown (decide ran
        // zero times), never as a refusal.
        public DownloadEnd Finish(bool entered, string state, string messages, Exception error)
        {
            DownloadEnd end = Ended(state, messages, error);
            end.InDoubt = entered && end.Failure != null;
            return end;
        }

        // Carries is whether TIA's exception, or one inside it, is the
        // decline that crossed: by its token (Token), since TIA hands back a
        // deserialized copy at best, maybe inside its own exception or with
        // the message embedded in its own text. Anything without the token
        // is not that decline, and the download is in doubt.
        static bool Carries(Exception error, string token)
        {
            if (string.IsNullOrEmpty(token)) return false;
            int n = 0;
            for (Exception i = error; i != null && n < 16; i = i.InnerException, n++)
            {
                string m;
                try
                {
                    m = i.Message;
                }
                catch (Exception)
                {
                    m = null;
                }
                if (m != null && m.IndexOf(token, StringComparison.Ordinal) >= 0) return true;
            }
            return false;
        }

        DownloadEnd Ended(string state, string messages, Exception error)
        {
            var end = new DownloadEnd();
            if (error != null)
            {
                if (Cancelled != null)
                {
                    // A decline only when what TIA threw carries that
                    // cancel and nothing shows it loaded anyway.
                    if (Cancelled.Declined && !loadSeen && Carries(error, Token)) end.Declined = true;
                    else if (Cancelled.Declined) end.Failure = "TIA went on after tiad cancelled the download (" + Cancelled.Message + "): " + TiaNames.Message(error);
                    else end.Failure = Cancelled.Message;
                    return end;
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

    // OnlineChecks decides how tiad answers what TIA asks when it goes
    // online to a CPU (ConnectionConfiguration.OnlineLegitimation), by the
    // question's Openness type name. tiad holds no CPU credentials and, by
    // default, trusts no CPU certificate: a certificate is answered not
    // trusted, and anything else cancels. The one exception is an upload
    // whose request opts in (trustUnverifiedCertificate): then a certificate
    // question is answered Trusted, blindly, since TIA shows no certificate
    // to check. Any question not answered Trusted fails the operation, even
    // one TIA then carried on with.
    public static class OnlineChecks
    {
        public const string Certificate = "certificate", Credentials = "credentials", Unknown = "unknown";
        public const string NotTrusted = "not-trusted", Trusted = "trusted", Cancel = "cancel";

        public static string Kind(string type)
        {
            switch (type)
            {
                case "TlsVerificationConfiguration":
                    return Certificate;
                case "OnlineAuthenticationConfiguration":
                case "OnlinePasswordConfiguration":
                case "OnlineReadAccessPassword":
                case "OnlineWriteAccessPassword":
                    return Credentials;
                default:
                    return Unknown;
            }
        }

        // Answer is how a question of type is answered: NotTrusted (the
        // selection NonTrusted) for a certificate, Cancel for the rest.
        public static string Answer(string type)
        {
            return Answer(type, false);
        }

        // Answer with trustUnverifiedCertificate, the upload's explicit
        // opt-in: a certificate is answered Trusted; nothing else changes.
        public static string Answer(string type, bool trustUnverifiedCertificate)
        {
            if (Kind(type) != Certificate) return Cancel;
            return trustUnverifiedCertificate ? Trusted : NotTrusted;
        }

        // Ask describes one question: a certificate to trust (with the PLC's
        // name, TIA's text and the certificates' fingerprints), credentials
        // of a kind, or a question tiad does not know.
        public static string Ask(string type, string plc, string info, List<string> fingerprints)
        {
            switch (Kind(type))
            {
                case Certificate:
                    string s = "trust the TLS certificate of " + (plc == "" ? "the PLC" : plc);
                    if (fingerprints.Count > 0) s += " (SHA-256 " + string.Join(", ", fingerprints.ToArray()) + ")";
                    if (info != "") s += ": " + info;
                    return s;
                case Credentials:
                    return "credentials (" + type + ")";
                default:
                    return "a question tiad does not know (" + type + ")";
            }
        }
    }

    // OnlineAnswers is how one online handler answers: the upload's
    // trustUnverifiedCertificate opt-in holds only until the handler is
    // stopped. Stopping marks it stopped before detaching it, so a handler
    // that could not be detached from TIA answers no from then on, never
    // Trusted.
    public sealed class OnlineAnswers
    {
        readonly bool trustUnverifiedCertificate;
        volatile bool stopped;

        public OnlineAnswers(bool trustUnverifiedCertificate)
        {
            this.trustUnverifiedCertificate = trustUnverifiedCertificate;
        }

        // Answer is how a question of type is answered now (OnlineChecks).
        public string Answer(string type)
        {
            return OnlineChecks.Answer(type, trustUnverifiedCertificate && !stopped);
        }

        // Stop marks the handler stopped, then detaches it with detach,
        // never throwing; it reports whether the detach worked.
        public bool Stop(Action detach, string what)
        {
            stopped = true;
            return DownloadSteps.Quietly(detach, what);
        }
    }

    // OnlineRecord is what TIA asked going online for one operation: the
    // questions answered no (Asked: any one fails the operation) and the
    // certificate questions trusted blindly on an upload's opt-in. Handle
    // runs one of TIA's online questions (OnlineLegitimation) through
    // Boundary.Call: if answering it fails anywhere, that call records a
    // question answered no before TIA gets the cancel, so the operation
    // fails, even if the call had recorded a trust first.
    public sealed class OnlineRecord
    {
        readonly object mu = new object();
        public readonly List<string> Asked = new List<string>();
        public readonly List<string> TrustedBlindly = new List<string>();

        // Call is one question being answered: answer records it with
        // Refuse or Trust.
        public sealed class Call
        {
            readonly OnlineRecord r;
            internal string Refused;

            internal Call(OnlineRecord r)
            {
                this.r = r;
            }

            public void Refuse(string question)
            {
                lock (r.mu) r.Asked.Add(question);
                Refused = question;
            }

            public void Trust(string question)
            {
                lock (r.mu) r.TrustedBlindly.Add(question);
            }
        }

        // Handle answers one question: type reads its Openness type name,
        // answer(type, call) answers and records it, throwing to cancel.
        public void Handle(Func<string> type, Action<string, Call> answer)
        {
            var call = new Call(this);
            string t = "";
            Boundary.Call("online: answering TIA", delegate
            {
                t = type();
                answer(t, call);
            }, delegate(Exception e)
            {
                if (call.Refused == null) call.Refuse(OnlineChecks.Ask(t == "" ? "an unreadable question" : t, "", "", new List<string>()));
                return "tiad cancels going online: TIA asked " + call.Refused;
            });
        }

    }

    // UploadAsks answers the upload's callback (StationUpload), which tiad
    // never answers: Handle records what TIA asked before anything else,
    // then refuses it through Boundary.Call. Asked is set (to a plain
    // description when the configuration cannot be read) once TIA asked.
    public sealed class UploadAsks
    {
        public volatile string Asked;

        // Handle refuses one question; describe reads "type (message)".
        public void Handle(Func<string> describe)
        {
            Boundary.Call("upload: answering TIA", delegate
            {
                Asked = describe();
                throw new TiaException("upload asked " + Asked + ", which tiad does not answer");
            }, delegate(Exception e)
            {
                if (Asked == null) Asked = "an upload configuration";
                return "upload asked " + Asked + ", which tiad does not answer";
            });
        }
    }

    // DownloadCall is what TIA's download call returned: its result's
    // state and messages, read inside the call (a failure reading them is
    // in doubt too).
    public sealed class DownloadCall
    {
        public string State;
        public string Messages = "";
    }

    // DownloadSteps runs one download through TIA in three steps:
    // prepare (finding the CPU, attaching tiad's handlers, the address: a
    // failure there loads nothing), TIA's download call (from its start, a
    // failure of any kind is in doubt), and stop (detaching the handlers).
    // TIA may be gone by the time stop runs (in #789 stage 2 it died inside
    // the download call and took its objects with it), so whatever stop
    // throws is logged and dropped: it never replaces how the download
    // ended. Everything after the call is read inside one catch, so the
    // agent answers outcome_unknown, never operation_failed, once the call
    // was entered.
    public static class DownloadSteps
    {
        // Run runs the steps and returns the outcome, or throws a
        // TiaException that is InDoubt once the download call was entered.
        // asked lists the questions tiad answered no going online (empty
        // when none): one fails the download even when TIA went on.
        public static DownloadOutcome Run(DownloadDecider decider, string address, Action prepare,
            Func<DownloadCall> download, Action stop, Func<List<string>> asked)
        {
            bool entered = false;
            DownloadCall call = null;
            Exception error = null;
            try
            {
                prepare();
                entered = true;
                call = download();
                if (call == null) throw new TiaException("TIA's download call returned nothing");
            }
            catch (Exception e)
            {
                call = null;
                error = e;
                // What TIA threw back, level by level: whether a cancel
                // came back as tiad's own (a decline) is read from it.
                try
                {
                    Log.Line("download: {0} threw: {1}", entered ? "TIA's download call" : "preparing the download", TiaNames.LogSafe(Boundary.Chain(e)));
                }
                catch (Exception)
                {
                }
            }
            finally
            {
                Quietly(stop, "download: detaching tiad's handlers from TIA");
            }
            DownloadEnd end;
            List<string> refused;
            try
            {
                // How it ended is decided in DownloadDecider.Finish.
                end = decider.Finish(entered, call == null ? null : call.State, call == null ? "" : (call.Messages ?? ""), error);
                refused = asked() ?? new List<string>();
            }
            catch (Exception e)
            {
                throw new TiaException("reading how TIA's download ended failed (" + TiaNames.Message(e) + ")" +
                    (error == null ? "" : " after " + TiaNames.Message(error)), error ?? e, entered);
            }
            // A question going online fails the download, even one TIA
            // went on with: once TIA's download call was entered, in doubt.
            if (refused.Count > 0)
            {
                throw new TiaException(TiaNames.OnlineRefused(address, refused, end.Failure ?? "TIA went on with the download"), error, entered);
            }
            if (end.Failure != null) throw new TiaException(end.Failure, error, end.InDoubt);
            return new DownloadOutcome { Questions = decider.Asked, Loaded = end.Loaded };
        }

        // Quietly runs f, a clean-up step described by what, logging and
        // dropping whatever it throws (the log line included): a handler
        // that cannot be detached from a TIA that is gone must not replace
        // the failure that matters. It reports whether f worked.
        public static bool Quietly(Action f, string what)
        {
            try
            {
                f();
                return true;
            }
            catch (Exception e)
            {
                try
                {
                    Log.Line("{0} failed ({1}: {2}); TIA may be gone", what, e.GetType().Name, TiaNames.LogSafe(TiaNames.Message(e)));
                }
                catch (Exception)
                {
                }
                return false;
            }
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

        // Fingerprint is a certificate's SHA-256 over its DER bytes, as
        // lower-case hex: what a caller compares to trust one.
        public static string Fingerprint(byte[] der)
        {
            using (var h = System.Security.Cryptography.SHA256.Create())
            {
                var b = new StringBuilder();
                foreach (byte x in h.ComputeHash(der)) b.Append(x.ToString("x2", CultureInfo.InvariantCulture));
                return b.ToString();
            }
        }

        // LogSafe is text from TIA or a CPU made safe for one log line:
        // quoted, with every control character escaped (Json.Quote), so it
        // can neither end the line nor forge another.
        public static string LogSafe(string s)
        {
            return Json.Quote(s ?? "");
        }

        // OnlineRefused is the failure when TIA asked questions going online:
        // tiad answered no (it trusts no certificate and holds no CPU
        // credentials yet), and the operation fails even if TIA went on.
        public static string OnlineRefused(string address, List<string> asked, string tiaMessage)
        {
            return "going online to " + address + ", TIA asked: " + string.Join("; ", asked.ToArray()) +
                ". tiad trusts no CPU certificate and holds no CPU credentials yet, so it answered no (TIA: " + tiaMessage + ")";
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
