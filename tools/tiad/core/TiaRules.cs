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
                case "ConsistentBlocksDownload":
                    return QuestionIds.ConsistentDownload;
                case "AlarmTextLibrariesDownload":
                    return QuestionIds.AlarmTexts;
                case "AllBlocksDownload":
                    return QuestionIds.AllBlocks;
                case "UserManagementDownload":
                    return QuestionIds.UserManagement;
                case "LoadIdentificationData":
                    return QuestionIds.IdentificationData;
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
        // default. choice picks between yeses (Answer.Choice): only
        // user-management has more than one, and it must name one; any
        // other type with a choice cannot be applied.
        public static Setting For(string type, bool yes, string choice)
        {
            if (type == "UserManagementDownload")
            {
                // Keep, or reset to the project's, never TIA's preselection
                // and never an update: which one is the request's, under
                // UserManagementRule.
                if (!yes) return null;
                if (choice == Answer.ChoiceKeep) return Setting.Choose(UserManagementKeep);
                if (choice == Answer.ChoiceReset) return Setting.Choose(UserManagementReset);
                return null;
            }
            if (choice != "") return null;
            switch (type)
            {
                case "StopModules":
                    return yes ? Setting.Choose("StopAll") : null;
                case "StartModules":
                    return Setting.Choose(yes ? "StartModule" : "NoAction");
                case "OverwriteSystemData":
                    return yes ? Setting.Choose("Overwrite") : null;
                case "ConsistentBlocksDownload":
                    // V21's only selection: no cannot be written, so it
                    // cancels.
                    return yes ? Setting.Choose("ConsistentDownload") : null;
                case "AlarmTextLibrariesDownload":
                    // NoAction (skip the texts) is never written: texts
                    // that disagree with the program are not a load.
                    return yes ? Setting.Choose("ConsistentDownload") : null;
                case "AllBlocksDownload":
                    return yes ? Setting.Choose("DownloadAllBlocks") : null;
                case "LoadIdentificationData":
                    return yes ? Setting.Choose("LoadNothing") : null;
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

        // The user-management selections tiad may write, as V21 names them
        // (#789 7065368046).
        public const string UserManagementKeep = "KeepOnlineUserManagementData";
        public const string UserManagementReset = "DownloadAllUserManagementDataResetToProject";

        // Answered are the configuration types For can answer.
        public static readonly string[] Answered =
        {
            "StopModules", "StartModules", "OverwriteSystemData", "ConsistentBlocksDownload", "AlarmTextLibrariesDownload",
            "AllBlocksDownload", "UserManagementDownload", "LoadIdentificationData", "DataBlockReinitialization",
            "DataBlockReinitializationOrKeepActualValues", "ModuleReadAccessPassword",
            "ModuleWriteAccessPassword", "PlcMasterSecretPassword", "TlsVerificationConfiguration",
        };

        // Acting is whether answering the question id can change the CPU
        // itself (stop it, start it, overwrite or reset its data), as
        // opposed to a password or a certificate, which only lets the
        // download go on. TIA asks its checks one at a time and may act on
        // an answer before it asks the next (#789 S2b), so an acting answer
        // written before a later cancel leaves the CPU in doubt. A question
        // tiad does not know is acting. An answer is classified by
        // ActingAnswer, by what it writes.
        public static bool Acting(string questionId)
        {
            switch (questionId)
            {
                case QuestionIds.Password:
                case QuestionIds.ConfigurationPassword:
                case QuestionIds.Certificate:
                case QuestionIds.UserManagement:
                case QuestionIds.IdentificationData:
                    return false;
                default:
                    return true;
            }
        }

        // ActingAnswer is whether the answer yes/choice to questionId acts
        // on the CPU, by what it writes: start-after answered no
        // (NoAction) changes nothing, user-management keep changes
        // nothing, its reset deletes the CPU's users and sets its access
        // control. The agent's record and the decider's both use it.
        public static bool ActingAnswer(string questionId, bool yes, string choice)
        {
            if (questionId == QuestionIds.StartAfter) return yes;
            if (questionId == QuestionIds.UserManagement) return choice == Answer.ChoiceReset;
            return Acting(questionId);
        }

        // Consequence says plainly what acting answers (question ids)
        // given before the download ended may already have done to the
        // CPU, and what to do about it, or "" when there were none.
        public static string Consequence(List<string> acting)
        {
            if (acting == null || acting.Count == 0) return "";
            bool stop = false, start = false, load = false, users = false, other = false;
            foreach (string id in acting)
            {
                if (id == QuestionIds.StopCpu || id == QuestionIds.Reinitialize) stop = true;
                else if (id == QuestionIds.StartAfter) start = true;
                else if (id == QuestionIds.ConsistentDownload || id == QuestionIds.AlarmTexts || id == QuestionIds.AllBlocks) load = true;
                else if (id == QuestionIds.UserManagement) users = true;
                else other = true;
            }
            var l = new List<string>();
            if (stop) l.Add("TIA may already have stopped the CPU: check its mode and start it");
            if (start) l.Add("TIA may already have started the CPU: check its mode");
            if (load) l.Add("TIA may already have loaded the software: compare");
            if (users) l.Add("TIA may already have reset the CPU's user management to the project's: check its users and access control");
            if (other) l.Add("TIA may already have acted on them: check the CPU's mode and compare");
            return "tiad had answered " + string.Join(", ", acting.ToArray()) + " before the download ended, so " + string.Join("; ", l.ToArray());
        }

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
                    foreach (string choice in new[] { "", Answer.ChoiceKeep, Answer.ChoiceReset })
                    {
                        Setting s = For(t, yes, choice);
                        if (s == null || s.Kind != Setting.Select) continue;
                        l.Add(new KeyValuePair<string, string>(t, s.Value));
                    }
                }
            }
            return l;
        }
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

    // Step is one answer to write into one TIA configuration of Type. The
    // glue writes Setting, then reports Wrote; a write that fails is
    // reported with Fail.
    public sealed class Step
    {
        public Setting Setting;
        public string Type = "";
        // Acting is whether writing Setting acts on the CPU
        // (DownloadChecks.ActingAnswer). It defaults to acting, the safe
        // side: a step that forgets to say records its write.
        public bool Acting = true;
    }

    // DownloadDecider keeps ITia.Download's promise over Openness's
    // pre-download callback. TIA calls it once per configuration that
    // needs an answer, one at a time, and lists them nowhere: no
    // configuration has a parent, and no Openness type holds them (#789
    // S2b, offline reflection on V21). So each callback is answered on its
    // own, only from what the request decided (answer), before its answer
    // is written; the first one the request does not answer yes cancels
    // there. A configuration asked twice, an answer that cannot be applied,
    // or anything that would leave a configuration to TIA's default
    // cancels too.
    //
    // TIA may act on an answer before it asks the next configuration (its
    // own sample: StopModules StopAll "will set PLC into Stop mode"), so
    // the decider keeps the acting answers it wrote (Acting): a cancel
    // after one is never a clean decline, the CPU is in doubt.
    //
    // Whether the CPU starts after loading is the request's startAfter,
    // decided before TIA is called; TIA's after-download callback (After)
    // gets it.
    public sealed class DownloadDecider
    {
        // StartAfterType is the configuration that starts the CPU after a
        // download.
        public const string StartAfterType = "StartModules";

        readonly Func<Question, Answer> answer;
        readonly bool? startAfter;
        readonly List<object> seen = new List<object>();

        // Asked is the questions TIA asked, in order, each as answer got
        // it.
        public readonly List<Question> Asked = new List<Question>();

        // Approved is how many of them answer approved.
        public int Approved;

        // Acting is every acting answer written into TIA before the
        // download ended, as "Type = setting", in order
        // (DownloadChecks.Acting).
        public readonly List<string> Acting = new List<string>();

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

        // answer is called once per configuration TIA asks, with its
        // question; it returns the answer to write, or null to cancel the
        // download there. startAfter is the request's decision whether the
        // CPU starts after loading (null: it stays as it is).
        public DownloadDecider(Func<Question, Answer> answer, bool? startAfter)
        {
            this.answer = answer;
            this.startAfter = startAfter;
        }

        static string NewToken()
        {
            var b = new byte[8];
            using (var rng = new System.Security.Cryptography.RNGCryptoServiceProvider()) rng.GetBytes(b);
            var t = new StringBuilder("tiad-decline:");
            foreach (byte x in b) t.Append(x.ToString("x2", CultureInfo.InvariantCulture));
            return t.ToString();
        }

        DownloadCancel Cancel(string why, bool declined)
        {
            Cancelled = new DownloadCancel(why, declined);
            return Cancelled;
        }

        static string Name(string type, string message)
        {
            return message == "" ? type : type + " (" + message + ")";
        }

        // Next answers TIA's callback for the configuration key, of type,
        // with TIA's message. extra, when TIA's side read more, carries it:
        // the blocks a reinitialize names (Objects) and the certificate a
        // trust decision must match (Detail); null otherwise. It returns
        // the step to write, or throws a DownloadCancel.
        public Step Next(object key, string type, string message, Question extra)
        {
            string what = Name(type, message);
            if (Cancelled != null)
            {
                // A callback after a decline: TIA went on past the cancel,
                // so the download can no longer end declined.
                if (Cancelled.Declined) Fail("TIA asked " + what + " after tiad declined the download (" + Cancelled.Message + "): TIA went on");
                throw Cancelled;
            }
            foreach (object k in seen)
            {
                if (object.Equals(k, key)) throw Cancel("TIA asked " + what + " twice", false);
            }
            seen.Add(key);
            // Detail names TIA's configuration, except for a certificate,
            // whose detail is what a trust decision must match: none unless
            // TIA's side read one.
            string id = DownloadChecks.QuestionId(type);
            var q = new Question { Id = id, Message = message, Detail = id == QuestionIds.Certificate ? "" : type };
            if (extra != null)
            {
                if (extra.Objects != null) q.Objects.AddRange(extra.Objects);
                if (id == QuestionIds.Certificate) q.Detail = extra.Detail ?? "";
            }
            Asked.Add(q);
            Answer a = answer(q.Copy());
            if (a == null) throw Cancel("tiad cancels the download at TIA's " + what + ": the request does not answer it yes (" + Token + ")", true);
            if (a.Question == null || a.Question.Id != id) throw Cancel("the answer to " + what + " is not to " + id, false);
            string choice = a.Choice ?? "";
            Setting s = DownloadChecks.For(type, a.Yes, choice);
            if (s == null) throw Cancel("tiad cannot apply " + (a.Yes ? "yes" : "no") + (choice == "" ? "" : " (" + choice + ")") + " to " + what, false);
            Approved++;
            return new Step { Setting = s, Type = type, Acting = DownloadChecks.ActingAnswer(id, a.Yes, choice) };
        }

        // Wrote reports that step's setting is written into TIA's
        // configuration; an acting one is recorded (Step.Acting).
        public void Wrote(Step step)
        {
            if (step.Acting) Acting.Add(step.Type + " = " + step.Setting);
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
        // anything else (an answer that threw, a log line) as a failure the
        // decider keeps, so the download can never end declined after it.
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

        // Seen logs one callback as TIA hands it over, before anything is
        // decided from it: its type and its parent's, each read on its own
        // and dropped if reading it fails, so a diagnostic never changes
        // how the download ends. Type names only: a password configuration
        // passes here too.
        public static void Seen(int n, Func<string> type, Func<string> parent)
        {
            try
            {
                Log.Line("download: TIA asks check {0}: {1}; its parent: {2}", n, TiaNames.LogSafe(Read(type)), TiaNames.LogSafe(Read(parent)));
            }
            catch (Exception)
            {
            }
        }

        static string Read(Func<string> f)
        {
            try
            {
                return f() ?? "null";
            }
            catch (Exception e)
            {
                return "unreadable (" + e.GetType().Name + ")";
            }
        }

        // loadSeen is set by TIA's after-download callback: TIA loaded.
        bool loadSeen;

        // After answers TIA's after-download callback for a configuration
        // of type. Starting the CPU is never left to TIA's default: start-
        // after gets the request's startAfter, or NoAction (the CPU stays
        // as it is) when it said no, said nothing, or tiad cancelled (a
        // load nobody approved). For any other configuration, null: TIA's
        // own setting stands, since it cannot change what was loaded. Any
        // after-download callback means TIA loaded.
        public Step After(string type)
        {
            loadSeen = true;
            if (type == "UserManagementDownload")
            {
                // tiad answers user management only before loading. One
                // asked after it is left to TIA, whose preselection on a
                // hardware load is the reset (#789 stage 4): say so loudly.
                Log.Line("download: WARNING TIA asked UserManagementDownload after loading; tiad wrote nothing, so TIA's own selection stands (on a hardware load TIA preselects the reset to the project's): check the CPU's users and access control");
            }
            if (type != StartAfterType) return null;
            bool start = Cancelled == null && startAfter.HasValue && startAfter.Value;
            return new Step { Setting = Setting.Choose(start ? "StartModule" : "NoAction"), Type = type, Acting = start };
        }

        // Finish reads how TIA's download call ended: entered is whether
        // TIA's download call was made at all, state its result's state
        // when it returned (null when it threw), messages its result's
        // messages, error what was thrown. A cancel TIA swallowed, a
        // failed state, or a cancel after an acting answer is a failure,
        // and any failure once the call was entered is in doubt. A
        // download TIA ran without one callback is loaded when its result
        // says so: the agent decides what that means (a dry run cannot
        // cancel it).
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

        string ActingNote()
        {
            return Acting.Count == 0 ? "" : " (after tiad wrote " + string.Join(", ", Acting.ToArray()) + ")";
        }

        DownloadEnd Ended(string state, string messages, Exception error)
        {
            var end = new DownloadEnd();
            if (error != null)
            {
                if (Cancelled != null)
                {
                    // A decline only when what TIA threw carries that
                    // cancel, nothing shows it loaded anyway, and no
                    // acting answer went in before it.
                    if (Cancelled.Declined && !loadSeen && Carries(error, Token))
                    {
                        if (Acting.Count == 0) end.Declined = true;
                        else end.Failure = Cancelled.Message + ActingNote();
                    }
                    else if (Cancelled.Declined) end.Failure = "TIA went on after tiad cancelled the download (" + Cancelled.Message + ")" + ActingNote() + ": " + TiaNames.Message(error);
                    else end.Failure = Cancelled.Message + ActingNote();
                    return end;
                }
                end.Failure = TiaNames.Message(error) + ActingNote();
                return end;
            }
            string ended = "TIA's download ended " + (state ?? "without a state") + (string.IsNullOrEmpty(messages) ? "" : ": " + messages);
            if (Cancelled != null)
            {
                end.Failure = "TIA went on after tiad cancelled the download (" + Cancelled.Message + ")" + ActingNote() + "; " + ended;
                return end;
            }
            // TIA's result state decides, asked or not: a download TIA ran
            // without one callback is loaded only when its result says so
            // (#1154 review), and in doubt otherwise.
            if (!TiaNames.DownloadLoaded(state))
            {
                end.Failure = ended + ActingNote();
                return end;
            }
            end.Loaded = true;
            return end;
        }
    }

    // OnlineChecks decides how tiad answers what TIA asks when it goes
    // online to a CPU (ConnectionConfiguration.OnlineLegitimation), by the
    // question's Openness type name. By default tiad trusts no CPU
    // certificate: a certificate is answered not trusted. The exception is
    // an upload or fingerprint read whose request opts in
    // (trustUnverifiedCertificate): then a certificate question is answered
    // Trusted, blindly, since TIA shows no certificate to check. A project
    // user's legitimation (OnlineAuthenticationConfiguration) is answered
    // Login: LoginUse decides whether tiad logs on as the CPU's user
    // "nautilus" or refuses, and says why. Anything else cancels: a legacy
    // access-level password, a question tiad does not know. Any question
    // answered no fails the operation, even one TIA then carried on with.
    public static class OnlineChecks
    {
        public const string Certificate = "certificate", Credentials = "credentials", Unknown = "unknown";
        public const string NotTrusted = "not-trusted", Trusted = "trusted", Cancel = "cancel", Login = "login";
        // Authentication is the one credentials question tiad answers.
        public const string Authentication = "OnlineAuthenticationConfiguration";

        public static string Kind(string type)
        {
            switch (type)
            {
                case "TlsVerificationConfiguration":
                    return Certificate;
                case Authentication:
                case "OnlinePasswordConfiguration":
                case "OnlineReadAccessPassword":
                    return Credentials;
                default:
                    return Unknown;
            }
        }

        // Answer is how a question of type is answered: NotTrusted (the
        // selection NonTrusted) for a certificate, Login for a project
        // user's legitimation, Cancel for the rest.
        public static string Answer(string type)
        {
            return Answer(type, false);
        }

        // Answer with trustUnverifiedCertificate, the upload's explicit
        // opt-in: a certificate is answered Trusted; nothing else changes.
        public static string Answer(string type, bool trustUnverifiedCertificate)
        {
            if (type == Authentication) return Login;
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
        // A stopped handler answers no: NotTrusted, never Trusted, and
        // Cancel, never Login.
        public string Answer(string type)
        {
            string a = OnlineChecks.Answer(type, trustUnverifiedCertificate && !stopped);
            return a == OnlineChecks.Login && stopped ? OnlineChecks.Cancel : a;
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
        // LoggedIn are the credentials questions answered as the CPU's
        // user "nautilus" (LoginUse): a failure after one names it.
        public readonly List<string> LoggedIn = new List<string>();
        // Use, when set, is the operation's LoginUse: every question this
        // record answers no tells it (LoginUse.Refused), so no password
        // follows a no in the same operation.
        public LoginUse Use;

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
                if (r.Use != null) r.Use.Refused();
                lock (r.mu) r.Asked.Add(question);
                Refused = question;
            }

            public void Trust(string question)
            {
                lock (r.mu) r.TrustedBlindly.Add(question);
            }

            public void Login(string question)
            {
                lock (r.mu) r.LoggedIn.Add(question);
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

    // OnlineLogin is what tiad answers when TIA asks credentials going
    // online to a CPU (#789 stage 5 (A)): a legitimation as the project
    // user "nautilus", the user cpu.accessControl fullAccessUser makes,
    // with the password the request carries in secrets.fullAccessPassword.
    // tiad stores it nowhere: it lives for one call. Blocked, when not
    // null, is why it is never sent: the TIA instance trusts a CPU
    // certificate blindly, so the connection may reach a CPU tiad cannot
    // verify. A password is never logged; ToString names the user only.
    public sealed class OnlineLogin
    {
        public const string User = AccessSteps.UserName;
        public readonly string Password;
        public readonly string Blocked;

        OnlineLogin(string password, string blocked)
        {
            Password = password;
            Blocked = blocked;
        }

        // For is the login a call answers with: null without a password;
        // blocked while the instance holds a blind trust (bt, read on the
        // TIA thread just before the call).
        public static OnlineLogin For(string password, BlindTrust bt)
        {
            if (string.IsNullOrEmpty(password)) return null;
            return new OnlineLogin(password, bt == null ? null : BlockedBy(bt));
        }

        // BlockedBy is the refusal while the instance trusts blindly.
        public static string BlockedBy(BlindTrust bt)
        {
            return "password not sent: this agent's TIA instance trusted an unverified CPU certificate (" + bt.Describe() +
                "), and tiad sends no password over a connection that may reach a CPU it cannot verify: close every session (the agent then retires the instance) or restart the agent, then call again";
        }

        public override string ToString()
        {
            return "OnlineLogin(" + User + ")";
        }
    }

    // LoginUse is one operation's use of its OnlineLogin, shared by every
    // online handler of that operation (a fingerprint read has two). Decide
    // says whether TIA's credentials question may be answered with it, and
    // why not: tiad answers as the project user "nautilus" only, only with
    // the request's password, only over secure communication, never after
    // the operation trusted a certificate blindly or answered any question
    // no, and at most once per operation. TIA asking again, on any of the
    // operation's connections, may mean the CPU refused the password: tiad
    // never tries it twice (a CPU may lock a user out after failed logons).
    // Whether a second connection asks after a logon that worked (TIA's
    // fingerprint service) is for the bench to show (#789 stage 5).
    public sealed class LoginUse
    {
        public const string ProjectUser = "ProjectUser";
        public const string NoPassword = "the request carries no secrets.fullAccessPassword (the password of the CPU's user \"nautilus\", which cpu.accessControl fullAccessUser makes), so tiad has no credentials to answer";
        public const string TrustedHere = "password not sent: this operation trusted the CPU's unverified certificate, and tiad sends no password to a CPU it cannot verify";
        public const string AskedAgain = "password not sent again: TIA asked credentials again in this operation after tiad answered them as the user \"nautilus\" (the CPU may have refused secrets.fullAccessPassword), and tiad never sends a password twice";
        public const string RefusedBefore = "password not sent: tiad answered another question of this operation no (a certificate it does not trust, credentials, or a question it does not know), and sends no password after a no";
        public const string NotSecure = "the connection is not secure communication, and tiad sends no password over it";
        public const string SecureUnread = "tiad could not read whether the connection is secure communication, so it sends no password over it";
        public const string TypesUnread = "tiad could not read which legitimations the CPU offers, so it sends no password";
        public const string DownloadNeedsPassword = "the session's station has cpu.accessControl fullAccessUser, so its CPU asks credentials going online once a hardware load put them there, and the compare after this download would: secrets.fullAccessPassword (the password of the CPU's user \"nautilus\") is required; nothing was sent to the CPU";

        readonly object mu = new object();
        readonly OnlineLogin login;
        readonly string call, address;
        bool answered, refused, trusted;

        public LoginUse(OnlineLogin login, string call, string address)
        {
            this.login = login;
            this.call = call ?? "";
            this.address = address ?? "";
        }

        // Password is the password to write, once Decide returned null.
        public string Password
        {
            get { return login == null ? "" : login.Password; }
        }

        // Answered is true once the operation answered credentials.
        public bool Answered
        {
            get { lock (mu) return answered; }
        }

        // LoggedOn is true once the operation answered credentials and
        // answered nothing no since (TIA asking again is answered no, and
        // a write that failed cancels the operation): read when TIA asks a
        // later check, the CPU took the logon (UserManagementRule's logon
        // route).
        public bool LoggedOn
        {
            get { lock (mu) return answered && !refused; }
        }

        // Trusted records that a handler of this operation answered a
        // certificate question Trusted, blindly. Call it as soon as the
        // trust is written, before anything that may fail.
        public void Trusted()
        {
            lock (mu) trusted = true;
        }

        // Refused records that a handler of this operation answered a
        // question no (OnlineRecord calls it on every Refuse): no password
        // follows a no, since TIA may go on after one.
        public void Refused()
        {
            lock (mu) refused = true;
        }

        // Decide is null when TIA's credentials question is answered with
        // the login, or why it is answered no. secure is the connection's
        // IsSecureCommunication (null: unreadable); userTypes the user
        // types the CPU offers, by name (null: unreadable).
        public string Decide(bool? secure, List<string> userTypes)
        {
            string why = DecideLocked(secure, userTypes);
            if (why == null)
            {
                Log.Line("audit online-login call={0} address={1} user={2}: answering TIA's credentials question", call, Json.Quote(address), Json.Quote(OnlineLogin.User));
            }
            else
            {
                Log.Line("audit online-login call={0} address={1} user={2} refused: {3}", call, Json.Quote(address), Json.Quote(OnlineLogin.User), Json.Quote(why));
            }
            return why;
        }

        // Answer answers one credentials question of type, as tiad.Openness
        // does inside OnlineRecord.Handle: Decide, then write the login
        // (write gets the password; the caller sets the user "nautilus" as
        // a project user) and record it, or record the refusal and throw
        // to cancel.
        public void Answer(OnlineRecord.Call call, string type, bool? secure, List<string> userTypes, Action<string> write)
        {
            string ask = OnlineChecks.Ask(type, "", "", new List<string>());
            string why = Decide(secure, userTypes);
            if (why != null)
            {
                call.Refuse(ask + ": " + why);
                throw new TiaException("tiad cancels going online: TIA asked " + ask + ": " + why);
            }
            write(Password);
            call.Login(ask);
        }

        string DecideLocked(bool? secure, List<string> userTypes)
        {
            lock (mu)
            {
                if (answered) return AskedAgain;
                if (refused) return RefusedBefore;
                if (login == null) return NoPassword;
                if (login.Blocked != null) return login.Blocked;
                if (trusted) return TrustedHere;
                if (secure == null) return SecureUnread;
                if (secure != true) return NotSecure;
                if (userTypes == null) return TypesUnread;
                if (!userTypes.Contains(ProjectUser))
                {
                    return "the CPU offers no legitimation as a project user (it offers " + (userTypes.Count == 0 ? "none" : string.Join(", ", userTypes.ToArray())) + "), and tiad logs on only as the project user \"nautilus\"";
                }
                answered = true;
                return null;
            }
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
    // in doubt too). MessageList is the same messages one per element, as
    // TIA gave them (Messages joins them with "; "), and Cpu the name of
    // the CPU loaded: TiaNames.ModeFromMessages reads the CPU's mode from
    // them.
    public sealed class DownloadCall
    {
        public string State;
        public string Messages = "";
        public List<string> MessageList = new List<string>();
        public string Cpu = "";
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
            return Run(decider, address, prepare, download, stop, asked, null);
        }

        // LoginNote is added to a download TIA failed after tiad answered
        // its credentials question.
        public const string LoginNote = "tiad had answered TIA's credentials question as the CPU's user \"nautilus\": the CPU may have refused the password";

        // Run with loggedIn, whether tiad answered TIA's credentials
        // question going online (LoginUse.Answered): a download TIA failed
        // after it says so (LoginNote).
        public static DownloadOutcome Run(DownloadDecider decider, string address, Action prepare,
            Func<DownloadCall> download, Action stop, Func<List<string>> asked, Func<bool> loggedIn)
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
            if (end.Failure != null)
            {
                string failure = end.Failure;
                if (error != null && loggedIn != null && loggedIn()) failure += " (" + LoginNote + ")";
                throw new TiaException(failure, error, end.InDoubt);
            }
            // The CPU's mode afterwards is only what TIA's own messages
            // say (#789): evidence, labelled as such, never a proof.
            string mode = call == null ? "" : TiaNames.ModeFromMessages(call.Cpu, call.MessageList);
            return new DownloadOutcome
            {
                Questions = decider.Asked,
                Loaded = end.Loaded,
                Messages = call == null ? "" : (call.Messages ?? ""),
                State = mode,
                StateSource = mode == "" ? "" : TiaNames.StateFromTiaMessage,
            };
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

        // Members describes an object TIA handed over for one log line: each
        // public instance property's name and value, a list as its elements
        // (at most MaxElements), an enum's type as its value names too, and
        // each read that fails as its exception's type, and a property
        // whose name says secret (password, secret, credential, token,
        // key) as not read. Every value is LogSafe. It never throws. Only for objects that carry no secret:
        // the glue never passes a password configuration (#789 stage 4: a
        // selection TIA refused to take, with what it offers instead).
        public const int MaxElements = 20;

        public static string Members(object o)
        {
            if (o == null) return "null";
            var parts = new List<string>();
            System.Reflection.PropertyInfo[] props;
            try
            {
                props = o.GetType().GetProperties(System.Reflection.BindingFlags.Public | System.Reflection.BindingFlags.Instance);
            }
            catch (Exception e)
            {
                return "unreadable (" + e.GetType().Name + ")";
            }
            Array.Sort(props, delegate(System.Reflection.PropertyInfo a, System.Reflection.PropertyInfo b) { return string.CompareOrdinal(a.Name, b.Name); });
            foreach (var p in props)
            {
                if (!p.CanRead || p.GetIndexParameters().Length > 0) continue;
                string v;
                if (SecretName(p.Name))
                {
                    parts.Add(p.Name + "=<not read>");
                    continue;
                }
                try
                {
                    v = Value(p.GetValue(o, null));
                    if (p.PropertyType.IsEnum) v += " of [" + string.Join(", ", Enum.GetNames(p.PropertyType)) + "]";
                }
                catch (Exception e)
                {
                    Exception x = e is System.Reflection.TargetInvocationException && e.InnerException != null ? e.InnerException : e;
                    v = "unreadable (" + x.GetType().Name + ")";
                }
                parts.Add(p.Name + "=" + v);
            }
            return string.Join(" ", parts.ToArray());
        }

        static string Value(object v)
        {
            if (v == null) return "null";
            if (v is string) return LogSafe((string)v);
            var seq = v as System.Collections.IEnumerable;
            if (seq == null) return LogSafe(v.ToString());
            var l = new List<string>();
            bool more = false;
            // Stops past the cap, so a list without end cannot hold up the
            // cancel this is logged before (#1167 review).
            foreach (object x in seq)
            {
                if (l.Count == MaxElements)
                {
                    more = true;
                    break;
                }
                l.Add(LogSafe(x == null ? "null" : x.ToString()));
            }
            return "[" + string.Join(", ", l.ToArray()) + (more ? ", more" : "") + "]";
        }

        // SecretName is a property whose name says it may hold a secret:
        // never read, whatever type carries it.
        static bool SecretName(string name)
        {
            string n = name.ToLowerInvariant();
            foreach (string w in new[] { "password", "secret", "credential", "token", "key" })
            {
                if (n.Contains(w)) return true;
            }
            return false;
        }

        // LoginFailed is the failure when TIA failed going online after
        // tiad answered its credentials question (and answered nothing no):
        // the CPU may have refused the password without asking again.
        public static string LoginFailed(string address, string tiaMessage)
        {
            return "going online to " + address + ", tiad answered TIA's credentials question as the CPU's user \"nautilus\" with the request's secrets.fullAccessPassword, and TIA failed (TIA: " + tiaMessage +
                "): the CPU may have refused the password";
        }

        // OnlineRefused is the failure when TIA asked questions going online
        // that tiad answered no (OnlineChecks, LoginUse), and the operation
        // fails even if TIA went on.
        public static string OnlineRefused(string address, List<string> asked, string tiaMessage)
        {
            return "going online to " + address + ", TIA asked: " + string.Join("; ", asked.ToArray()) +
                ". tiad trusts no CPU certificate unless the request opts in (trustUnverifiedCertificate), and answers credentials only as the CPU's user \"nautilus\" with the request's secrets.fullAccessPassword, so it answered no (TIA: " + tiaMessage + ")";
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

        // CompareEqual decides whether an online compare proves the CPU
        // holds exactly the project's PLC software (#789), and adds to
        // into what differs, or why the compare proves nothing. Equal
        // needs all four:
        //   - the root says identical (FolderContentsIdentical or
        //     ObjectsIdentical);
        //   - at least one object below the root was compared and found
        //     identical (ObjectsIdentical): an empty tree, or one of empty
        //     folders, compared nothing and proves nothing;
        //   - no element anywhere in the tree is in a non-identical state,
        //     so a differing child under an identical-looking root fails;
        //   - every program block the project holds is in the tree as an
        //     element of its label (BlockLabel) that says ObjectsIdentical,
        //     wherever it sits: a block the tree leaves out was not
        //     compared, so nothing says the CPU holds it (#789 stage 3
        //     gate: an empty identical block folder beside one identical
        //     tag table must not pass). A block with no label is not
        //     compared either, and a project with no blocks read proves
        //     nothing about blocks.
        // CompareIrrelevant anywhere is "not compared": TIA left that
        // object out, so nothing says it matches (#1156 review: a skipped
        // block folder beside one identical tag table must not pass). A
        // folder that says its contents differ without a differing child to
        // show is named itself. A null tree is not equal. Blocks are
        // matched by label anywhere in the tree, not by folder: TIA names
        // its folders in the UI language, and imported blocks sit in a
        // user group one level deeper. Only the project's user program
        // blocks (its block group and user groups) are required by name:
        // system blocks, software units, data types and tag tables are
        // not, and fall under the rule that nothing in the tree may
        // differ. Residual: a label is matched against every identical
        // element, so a tag table named exactly like a block the tree
        // leaves out ("Pump [FB1]") would stand in for it; tiad names no
        // object so, and it takes a name made to collide.
        public static bool CompareEqual(CompareNode root, List<ProjectBlock> blocks, List<Difference> into)
        {
            if (root == null)
            {
                into.Add(new Difference { Object = "", State = "nothing compared" });
                return false;
            }
            int before = into.Count;
            int compared = 0;
            var identical = new HashSet<string>(StringComparer.Ordinal);
            Tally(root, into, ref compared, identical, true);
            if (root.State != "FolderContentsIdentical" && root.State != "ObjectsIdentical" && into.Count == before)
            {
                into.Add(new Difference { Object = root.Name, State = "not compared" });
            }
            if (compared == 0 && into.Count == before)
            {
                into.Add(new Difference { Object = root.Name, State = "nothing compared" });
            }
            if (blocks == null || blocks.Count == 0)
            {
                into.Add(new Difference { Object = "", State = "no program blocks in the project (or none read)" });
                return false;
            }
            var named = new HashSet<string>(StringComparer.Ordinal);
            for (int i = before; i < into.Count; i++) named.Add(into[i].Object);
            foreach (ProjectBlock b in blocks)
            {
                string label = BlockLabel(b);
                if (label == null)
                {
                    into.Add(new Difference { Object = b.Name + " (" + b.Type + ")", State = "not compared" });
                }
                else if (!identical.Contains(label) && !named.Contains(label))
                {
                    into.Add(new Difference { Object = label, State = "not compared" });
                }
            }
            return into.Count == before;
        }

        // Labels is every block's label, or its name and type when it has
        // none: the blocks a compare checked, as a response lists them.
        public static List<string> Labels(List<ProjectBlock> blocks)
        {
            var l = new List<string>();
            if (blocks == null) return l;
            foreach (ProjectBlock b in blocks) l.Add(BlockLabel(b) ?? b.Name + " (" + b.Type + ")");
            return l;
        }

        // StateFromTiaMessage labels a CPU mode read from TIA's download
        // messages: evidence of what TIA did, not a read of the CPU.
        public const string StateFromTiaMessage = "tia-message";

        // ModeFromMessages is the CPU's mode after a download as TIA's
        // download messages say it, one message per element: TIA reports
        // each step ("PLC_1 stopped.", "'Main' was loaded successfully.",
        // "PLC_1 started.", #789 stages 2 and 3). The last message that is
        // exactly "<cpu> started." or "<cpu> stopped." decides: "RUN" or
        // "STOP". No such message (TIA did not stop the CPU, another
        // language, another CPU's name) is "": unknown, never guessed.
        public static string ModeFromMessages(string cpu, List<string> messages)
        {
            if (string.IsNullOrEmpty(cpu) || messages == null) return "";
            string mode = "";
            foreach (string m in messages)
            {
                string t = (m ?? "").Trim();
                if (t == cpu + " started.") mode = "RUN";
                else if (t == cpu + " stopped.") mode = "STOP";
            }
            return mode;
        }

        // ElementName names one element of TIA's compare tree from its
        // offline (left) and online (right) names, by the side that holds
        // it: an element only the CPU has (LeftMissing) by its online
        // name, since TIA fills the missing side with a placeholder ("Does
        // not exist", #789 stage 3 window); one only the project has
        // (RightMissing) by its offline name; any other by its offline
        // name, or the online one when that is all TIA gave.
        public static string ElementName(string state, string left, string right)
        {
            left = left ?? "";
            right = right ?? "";
            if (state == "LeftMissing") return right != "" ? right : left;
            return left != "" ? left : right;
        }

        // BlockLabels lists blocks' labels for the journal, at most max of
        // them and then how many more ("Main [OB1], Pump [FB1], +3 more").
        public static string BlockLabels(List<ProjectBlock> blocks, int max)
        {
            var l = new List<string>();
            foreach (ProjectBlock b in blocks)
            {
                if (l.Count == max)
                {
                    l.Add("+" + (blocks.Count - max).ToString(CultureInfo.InvariantCulture) + " more");
                    break;
                }
                l.Add(BlockLabel(b) ?? b.Name);
            }
            return string.Join(", ", l.ToArray());
        }

        // BlockLabel is the name TIA's compare tree gives a program block:
        // its name, then its kind and number in brackets ("Main [OB1]",
        // the shape the bench's equal tree showed, #789), or null for an
        // Openness type that is no program block tiad can label.
        public static string BlockLabel(ProjectBlock b)
        {
            string kind = BlockKind(b.Type);
            if (kind == "" || kind == "UDT") return null;
            return b.Name + " [" + kind + b.Number.ToString(CultureInfo.InvariantCulture) + "]";
        }

        static void Tally(CompareNode n, List<Difference> into, ref int compared, HashSet<string> identical, bool root)
        {
            int at = into.Count;
            string own = Difference(n.State);
            if (own != "") into.Add(new Difference { Object = n.Name, State = own });
            else if (n.State == "CompareIrrelevant" && !root) into.Add(new Difference { Object = n.Name, State = "not compared" });
            else if (n.State == "ObjectsIdentical" && !root)
            {
                compared++;
                identical.Add(n.Name);
            }
            foreach (CompareNode c in n.Children) Tally(c, into, ref compared, identical, false);
            if (n.State == "FolderContentsDifferent" && into.Count == at)
            {
                into.Add(new Difference { Object = n.Name, State = "contents differ" });
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
