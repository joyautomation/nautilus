// RulesTest.cs: tiad-test.exe, the unit tests of the decisions
// tiad.Openness makes (core/TiaRules.cs). tiad.Openness itself needs TIA
// and its assemblies, which CI has not, so everything it decides is here,
// as pure code, and checked under Mono. build-fake.sh builds and runs it;
// it exits 1 when a test fails.

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        static int failed, passed;
        static string current = "";

        static void Fail(string what)
        {
            failed++;
            Console.Error.WriteLine("FAIL " + current + ": " + what);
        }

        static void Eq(object got, object want, string what)
        {
            if (!object.Equals(got, want)) Fail(what + ": got " + Show(got) + ", want " + Show(want));
        }

        static void True(bool ok, string what)
        {
            if (!ok) Fail(what);
        }

        static string Show(object o)
        {
            if (o == null) return "null";
            if (o is string) return Json.Quote((string)o);
            return o.ToString();
        }

        static T Throws<T>(Action f, string what) where T : Exception
        {
            try
            {
                f();
            }
            catch (T e)
            {
                return e;
            }
            catch (Exception e)
            {
                Fail(what + ": threw " + e.GetType().Name + " (" + e.Message + "), want " + typeof(T).Name);
                return null;
            }
            Fail(what + ": did not throw, want " + typeof(T).Name);
            return null;
        }

        static void Contains(string s, string sub, string what)
        {
            if (s == null || s.IndexOf(sub, StringComparison.Ordinal) < 0) Fail(what + ": " + Show(s) + " lacks " + Show(sub));
        }

        static int Main()
        {
            var tests = new List<KeyValuePair<string, Action>>
            {
                T("LicenceStartsUnknown", LicenceStartsUnknown),
                T("LicenceOkOnceStarted", LicenceOkOnceStarted),
                T("LicenceFailsOnALicenceError", LicenceFailsOnALicenceError),
                T("LicenceIgnoresOtherErrors", LicenceIgnoresOtherErrors),
                T("LicenceFailedStaysFailed", LicenceFailedStaysFailed),
                T("LicenceFailsOnALaterCall", LicenceFailsOnALaterCall),
                T("QuestionIds", QuestionIdsOfConfigurations),
                T("Settings", SettingsForAnswers),
                T("PasswordsNeverCross", PasswordsNeverCross),
                T("ProtectionPlan", ProtectionPlan),
                T("ProtectionReadBack", ProtectionReadBack),
                T("ReuseDecides", ReuseDecides),
                T("ReuseRefusesAMismatch", ReuseRefusesAMismatch),
                T("ReuseNeverAfterAFailure", ReuseNeverAfterAFailure),
                T("ReuseFirmwareIsExact", ReuseFirmwareIsExact),
                T("KeptDevicesCollide", KeptDevicesCollide),
                T("KeptNoteAndChanges", KeptNoteAndChanges),
                T("KeptDevicesNeedANamedYes", KeptDevicesNeedANamedYes),
                T("ProtectionStepsHappyPaths", ProtectionStepsHappyPaths),
                T("ProtectionStepsStranded", ProtectionStepsStranded),
                T("ProtectionStepsReadBack", ProtectionStepsReadBack),
                T("AccessPlan", AccessPlan),
                T("AccessStepsNewCpu", AccessStepsNewCpu),
                T("AccessStepsKeptCpu", AccessStepsKeptCpu),
                T("AccessStepsReadBack", AccessStepsReadBack),
                T("AccessPrepare", AccessPrepare),
                T("AccessRecordsKnownAnswer", AccessRecordsKnownAnswer),
                T("AccessPasswordChecks", AccessPasswordChecks),
                T("AnswersEachCallbackOnItsOwn", AnswersEachCallbackOnItsOwn),
                T("ActingAndPassive", ActingAndPassive),
                T("ConsequenceNamesWhatToDo", ConsequenceNamesWhatToDo),
                T("AfterGetsTheRequestsStartAfter", AfterGetsTheRequestsStartAfter),
                T("DeclineIsClean", DeclineIsClean),
                T("CancelOnABrokenAnswer", CancelOnABrokenAnswer),
                T("CancelOnAnUnappliableAnswer", CancelOnAnUnappliableAnswer),
                T("CancelOnARepeatedCallback", CancelOnARepeatedCallback),
                T("CertificateDetail", CertificateDetail),
                T("SeenNeverThrows", SeenNeverThrows),
                T("FinishLoaded", FinishLoaded),
                T("FinishDeclined", FinishDeclined),
                T("FinishDeclineAfterActing", FinishDeclineAfterActing),
                T("FinishFailures", FinishFailures),
                T("FinishSwallowedCancel", FinishSwallowedCancel),
                T("FinishWithoutACallback", FinishWithoutACallback),
                T("Messages", Messages),
                T("OnlineAsks", OnlineAsks),
                T("AFailedWriteCancels", AFailedWriteCancels),
                T("InDoubtOnceEntered", InDoubtOnceEntered),
                T("DownloadStepsTiaDies", DownloadStepsTiaDies),
                T("DownloadStepsAfterTheCall", DownloadStepsAfterTheCall),
                T("ATiadTypeKillsTia", ATiadTypeKillsTia),
                T("BoundarySendsOnlyACancel", BoundarySendsOnlyACancel),
                T("DeclineSurvivesTiasCopy", DeclineSurvivesTiasCopy),
                T("DeclineNeedsItsOwnCancelBack", DeclineNeedsItsOwnCancelBack),
                T("EveryCancelCrossesAndIsKept", EveryCancelCrossesAndIsKept),
                T("DiagnosticsNeverFlipADecline", DiagnosticsNeverFlipADecline),
                T("DownloadStepsDeclineThroughTia", DownloadStepsDeclineThroughTia),
                T("ASerializableTiadTypeStillKillsTia", ASerializableTiadTypeStillKillsTia),
                T("DeclineNeedsItsToken", DeclineNeedsItsToken),
                T("DeclineThenTiaGoesOn", DeclineThenTiaGoesOn),
                T("KeepRecordsWhenTheMessageThrows", KeepRecordsWhenTheMessageThrows),
                T("OnlineRecordFailsClosed", OnlineRecordFailsClosed),
                T("UploadAsksRecordsFirst", UploadAsksRecordsFirst),
                T("OnlineAnswersAfterStop", OnlineAnswersAfterStop),
                T("QuietlyEvenWhenTheLogFails", QuietlyEvenWhenTheLogFails),
                T("Selections", SelectionsWritten),
                T("ALoadAfterADeclineIsInDoubt", ALoadAfterADeclineIsInDoubt),
                T("TypeIdentifiers", TypeIdentifiers),
                T("PcInterfaces", PcInterfaces),
                T("Only", OnlyOne),
                T("CheckAttribute", CheckAttribute),
                T("ResultStates", ResultStates),
                T("Differences", Differences),
                T("CompareEqual", CompareEqualTests),
                T("FileNames", FileNames),
                T("BlockKinds", BlockKinds),
                T("OpennessAssemblies", OpennessAssemblies),
                T("PublicApiDir", PublicApiDir),
            };
            foreach (var t in tests)
            {
                current = t.Key;
                int before = failed;
                try
                {
                    t.Value();
                }
                catch (Exception e)
                {
                    Fail("panicked: " + e);
                }
                if (failed == before) passed++;
            }
            if (failed > 0)
            {
                Console.Error.WriteLine("tiad-test: " + failed + " failure(s)");
                return 1;
            }
            Console.WriteLine("tiad-test: " + passed + " tests passed");
            return 0;
        }

        static KeyValuePair<string, Action> T(string name, Action f)
        {
            return new KeyValuePair<string, Action>(name, f);
        }

        // ---- the licence latch ----

        static void LicenceStartsUnknown()
        {
            Gate g = new LicenceLatch().Current();
            Eq(g.Name, "licence", "name");
            Eq(g.State, Gate.StateUnknown, "state");
            True(!g.Blocks, "an unknown licence blocks");
            Eq(g.Detail, MachineGates.LicenceUnknown, "detail");
        }

        static void LicenceOkOnceStarted()
        {
            var l = new LicenceLatch();
            l.Started(null);
            Gate g = l.Current();
            Eq(g.State, Gate.StateOk, "state");
            Eq(g.Detail, LicenceLatch.StartedDetail, "detail");
            Eq(g.Remedy, "", "remedy");
        }

        static void LicenceFailsOnALicenceError()
        {
            foreach (var e in new Exception[]
            {
                new TiaException("No valid license found for STEP 7"),
                new TiaException("TIA Portal could not start", new InvalidOperationException("Licence missing")),
                new TiaException("Keine gueltige Lizenz"),
            })
            {
                var l = new LicenceLatch();
                l.Started(e);
                Gate g = l.Current();
                Eq(g.State, Gate.StateFailed, e.Message + ": state");
                True(g.Blocks, e.Message + ": a failed licence does not block");
                Eq(g.Detail, "TIA did not start: " + e.Message, e.Message + ": detail");
                Eq(g.Remedy, LicenceLatch.FailedRemedy, e.Message + ": remedy");
            }
        }

        static void LicenceIgnoresOtherErrors()
        {
            var l = new LicenceLatch();
            l.Started(new TiaException("tiad.Openness.dll is not installed beside tiad.exe"));
            Eq(l.Current().State, Gate.StateUnknown, "after a start that failed for another reason");
            l.Started(null);
            l.Started(new TiaException("the process ended"));
            Eq(l.Current().State, Gate.StateOk, "an ok latch after another error");
        }

        static void LicenceFailedStaysFailed()
        {
            var l = new LicenceLatch();
            l.Started(new TiaException("license expired"));
            l.Started(null);
            Eq(l.Current().State, Gate.StateFailed, "a start after a licence failure");
        }

        static void LicenceFailsOnALaterCall()
        {
            // TIA starts headless without a licence, then refuses the first
            // call that needs one.
            var l = new LicenceLatch();
            l.Started(null);
            l.Failed(new TiaException("Error when calling method 'CreateWithItem'.\r\n\r\nNecessary license 'STEP 7 Professional' is missing."));
            Gate g = l.Current();
            Eq(g.State, Gate.StateFailed, "state");
            Contains(g.Detail, "TIA refused a call: ", "detail");
            Contains(g.Detail, "Necessary license 'STEP 7 Professional' is missing.", "detail");
            Eq(g.Remedy, LicenceLatch.FailedRemedy, "remedy");
            // Another failure says nothing about the licence.
            var o = new LicenceLatch();
            o.Started(null);
            o.Failed(new TiaException("The connection to the CPU was lost"));
            Eq(o.Current().State, Gate.StateOk, "after another failure");
            // A failed latch keeps its first reason.
            l.Failed(new TiaException("license expired"));
            Contains(l.Current().Detail, "STEP 7 Professional", "a second licence error");
            var u = new LicenceLatch();
            u.Failed(new TiaException("licence missing"));
            Eq(u.Current().State, Gate.StateFailed, "a licence error before any start was recorded");
        }

        // ---- download configurations ----

        static void QuestionIdsOfConfigurations()
        {
            var want = new Dictionary<string, string>
            {
                { "StopModules", QuestionIds.StopCpu },
                { "StartModules", QuestionIds.StartAfter },
                { "OverwriteSystemData", QuestionIds.Overwrite },
                { "DataBlockReinitialization", QuestionIds.Reinitialize },
                { "DataBlockReinitializationOrKeepActualValues", QuestionIds.Reinitialize },
                { "ModuleReadAccessPassword", QuestionIds.Password },
                { "ModuleWriteAccessPassword", QuestionIds.Password },
                { "PlcMasterSecretPassword", QuestionIds.ConfigurationPassword },
                { "TlsVerificationConfiguration", QuestionIds.Certificate },
                { "ResetModule", QuestionIds.Other },
                { "UpgradeTargetDevice", QuestionIds.Other },
                { "", QuestionIds.Other },
                { "stopmodules", QuestionIds.Other },
            };
            foreach (var kv in want) Eq(DownloadChecks.QuestionId(kv.Key), kv.Value, kv.Key);
        }

        static string S(Setting s)
        {
            return s == null ? null : s.ToString();
        }

        static string S(Step st)
        {
            return st == null ? null : st.Setting.ToString();
        }

        // N is one callback the glue answers: the step, then its write.
        static string N(Run r, object key, string type, string message)
        {
            Step st = r.D.Next(key, type, message, null);
            r.D.Wrote(st);
            return S(st);
        }

        // A is one after-download callback, written.
        static string A(DownloadDecider d, string type)
        {
            Step st = d.After(type);
            if (st != null) d.Wrote(st);
            return S(st);
        }

        static void SettingsForAnswers()
        {
            Eq(S(DownloadChecks.For("StopModules", true)), "StopAll", "stop yes");
            Eq(S(DownloadChecks.For("StopModules", false)), null, "stop no");
            Eq(S(DownloadChecks.For("StartModules", true)), "StartModule", "start yes");
            Eq(S(DownloadChecks.For("StartModules", false)), "NoAction", "start no");
            Eq(S(DownloadChecks.For("OverwriteSystemData", true)), "Overwrite", "overwrite yes");
            Eq(S(DownloadChecks.For("OverwriteSystemData", false)), null, "overwrite no");
            Eq(S(DownloadChecks.For("DataBlockReinitialization", true)), "StopPlcAndReinitialize", "reinit yes");
            Eq(S(DownloadChecks.For("DataBlockReinitializationOrKeepActualValues", true)), "StopPlcAndReinitialize", "reinit-or-keep yes");
            Eq(S(DownloadChecks.For("DataBlockReinitialization", false)), null, "reinit no");
            Eq(S(DownloadChecks.For("ModuleWriteAccessPassword", true)), Setting.Password, "password yes");
            Eq(S(DownloadChecks.For("ModuleReadAccessPassword", true)), Setting.Password, "read password yes");
            Eq(S(DownloadChecks.For("ModuleWriteAccessPassword", false)), null, "password no");
            Eq(S(DownloadChecks.For("PlcMasterSecretPassword", true)), Setting.ConfigurationPassword, "configuration password yes");
            Eq(S(DownloadChecks.For("PlcMasterSecretPassword", false)), null, "configuration password no");
            Eq(S(DownloadChecks.For("TlsVerificationConfiguration", true)), "Trusted", "certificate yes");
            Eq(S(DownloadChecks.For("TlsVerificationConfiguration", false)), null, "certificate no");
            Eq(S(DownloadChecks.For("ResetModule", true)), null, "an unknown configuration, yes");
            Eq(S(DownloadChecks.For("ResetModule", false)), null, "an unknown configuration, no");
            Eq(DownloadChecks.For("StopModules", true).Kind, Setting.Select, "a selection's kind");
        }

        // PasswordsNeverCross: each password setting writes its own secret,
        // never the other's, even when only the other is set.
        static void PasswordsNeverCross()
        {
            var both = new Secrets { Protection = "protection-pw", ConfigurationPassword = "configuration-pw" };
            var onlyProtection = new Secrets { Protection = "protection-pw" };
            var onlyConfiguration = new Secrets { ConfigurationPassword = "configuration-pw" };
            Setting module = DownloadChecks.For("ModuleWriteAccessPassword", true);
            Setting master = DownloadChecks.For("PlcMasterSecretPassword", true);
            Eq(Setting.Secret(module, both), "protection-pw", "module password, both set");
            Eq(Setting.Secret(master, both), "configuration-pw", "master secret, both set");
            Eq(Setting.Secret(module, onlyConfiguration), "", "module password never takes the configuration password");
            Eq(Setting.Secret(master, onlyProtection), "", "master secret never takes the protection password");
            Eq(Setting.Secret(DownloadChecks.For("StopModules", true), both), null, "a selection has no secret");
            // The Full access user's password answers no download question
            // (UserManagementDownload waits for S2b): no setting takes it.
            var onlyFull = new Secrets { FullAccessPassword = "full-access-pw" };
            foreach (string type in DownloadChecks.Answered)
            {
                Setting st = DownloadChecks.For(type, true);
                if (st == null) continue;
                string got = Setting.Secret(st, onlyFull);
                True(got == null || got == "", type + " never takes the Full access password");
            }
            Eq(DownloadChecks.QuestionId("UserManagementDownload"), QuestionIds.Other, "user management is a question nothing answers yet");
            Eq(DownloadChecks.For("UserManagementDownload", true), null, "user management is never answered yet");
        }

        // ProtectionPlan is ConfigProtection.Plan over every state and
        // mode: an action, or a refusal naming the fix.
        static void ProtectionPlan()
        {
            const string W = ConfigProtection.WithoutPassword, P = ConfigProtection.WithPassword, N = ConfigProtection.None, A = ConfigProtection.Absent;
            const string pw = StationRules.ProtectPassword, none = StationRules.ProtectNone, keep = StationRules.ProtectKeep;
            var acts = new[]
            {
                // current, want, have password, action
                new object[] { N, keep, false, ConfigProtection.Nothing },
                // A password sent with keep changes nothing either.
                new object[] { P, keep, true, ConfigProtection.Nothing },
                new object[] { N, keep, true, ConfigProtection.Nothing },
                new object[] { P, keep, false, ConfigProtection.Nothing },
                new object[] { A, keep, false, ConfigProtection.Nothing },
                new object[] { "Bogus", keep, false, ConfigProtection.Nothing },
                new object[] { W, pw, true, ConfigProtection.Protect },
                new object[] { N, pw, true, ConfigProtection.Protect },
                new object[] { P, pw, true, ConfigProtection.Nothing },
                new object[] { N, none, false, ConfigProtection.Nothing },
                new object[] { W, none, false, ConfigProtection.ProtectThenUnprotect },
                new object[] { P, none, true, ConfigProtection.UnprotectWithPassword },
            };
            foreach (var c in acts)
            {
                string what = c[0] + "/" + c[1] + "/" + c[2];
                Eq(ConfigProtection.Plan((string)c[0], (string)c[1], (bool)c[2]), (string)c[3], what);
            }
            var refusals = new[]
            {
                new object[] { W, keep, true, "keep has nothing to keep: set cpu.configurationProtection to password or none" },
                new object[] { A, pw, true, "has no configuration protection (firmware 3.1 and later have it): set it to keep" },
                new object[] { A, none, true, "set it to keep" },
                new object[] { "Bogus", pw, true, "a state tiad does not know (\"Bogus\"); nothing was changed" },
                new object[] { "Bogus", none, true, "a state tiad does not know" },
                new object[] { W, pw, false, "secrets.configurationPassword is empty" },
                new object[] { P, none, false, "turning that off needs it in secrets.configurationPassword" },
                new object[] { N, "Password", true, "\"Password\" is not one of password, none, keep" },
            };
            foreach (var c in refusals)
            {
                string what = c[0] + "/" + c[1] + "/" + c[2];
                var e = Throws<TiadException>(delegate { ConfigProtection.Plan((string)c[0], (string)c[1], (bool)c[2]); }, what);
                if (e == null) continue;
                Eq(e.Kind, Kinds.BadRequest, what + " kind");
                Contains(e.Message, (string)c[3], what);
            }
        }

        // ProtectionReadBack: after a write, only the promised state passes.
        static void ProtectionReadBack()
        {
            Eq(ConfigProtection.Check(ConfigProtection.Nothing, ConfigProtection.WithoutPassword), null, "nothing written, nothing checked");
            Eq(ConfigProtection.Check(ConfigProtection.Protect, ConfigProtection.WithPassword), null, "protect reached");
            Eq(ConfigProtection.Check(ConfigProtection.ProtectThenUnprotect, ConfigProtection.None), null, "protect-then-unprotect reached");
            Eq(ConfigProtection.Check(ConfigProtection.UnprotectWithPassword, ConfigProtection.None), null, "unprotect with password reached");
            Contains(ConfigProtection.Check(ConfigProtection.ProtectThenUnprotect, ConfigProtection.WithPassword), "reads WithPassword, not None", "an unprotect TIA ignored");
            string a = ConfigProtection.Throwaway(), b = ConfigProtection.Throwaway();
            Eq(StationRules.CheckTiaPassword(a), null, "a throwaway meets TIA's rules");
            True(a != b, "each throwaway is new");
            Eq(a.Length, 35, "a throwaway carries 16 random bytes");
            Contains(ConfigProtection.Stranded("TIA said no", null), "tiad removed the CPU from the project: close this session without saving", "stranded, removed");
            Contains(ConfigProtection.Stranded("TIA said no", "locked"), "removing the CPU from the project failed too (locked): close this session without saving", "stranded, not removed");
            Contains(ConfigProtection.Stranded("TIA said no", null), "a throwaway password nobody holds", "stranded names the risk");
            Contains(ConfigProtection.Note(ConfigProtection.WithPassword, StationRules.ProtectPassword), "apply none with the old password, then password with the new one", "a kept password names how to change it");
            foreach (string cur in new[] { ConfigProtection.None, ConfigProtection.WithoutPassword, ConfigProtection.Absent })
            {
                Eq(ConfigProtection.Note(cur, StationRules.ProtectPassword), null, "no note on " + cur);
            }
            Eq(ConfigProtection.Note(ConfigProtection.WithPassword, StationRules.ProtectKeep), null, "no note on keep");
            Contains(ConfigProtection.Check(ConfigProtection.Protect, ConfigProtection.WithoutPassword), "not WithPassword", "a protect TIA ignored");
            Contains(ConfigProtection.Check(ConfigProtection.UnprotectWithPassword, ConfigProtection.WithPassword), "not None", "a wrong password TIA swallowed");
            Contains(ConfigProtection.Check(ConfigProtection.Protect, ConfigProtection.Absent), "reads absent", "a configurator gone");
        }

        // A fake TIA side for the decider: configurations are plain objects,
        // asked one at a time, as Openness does (#789 S2b).
        sealed class Run
        {
            public int Answers;
            public readonly List<Question> Seen = new List<Question>();
            public Func<Question, Answer> Reply;
            public DownloadDecider D;

            public Run(Func<Question, Answer> reply) : this(reply, false)
            {
            }

            public Run(Func<Question, Answer> reply, bool? startAfter)
            {
                Reply = reply;
                D = new DownloadDecider(delegate(Question q)
                {
                    Answers++;
                    Seen.Add(q);
                    return Reply(q);
                }, startAfter);
            }
        }

        static Answer AllYes(Question q)
        {
            return new Answer { Question = q, Yes = true };
        }

        static DownloadDecider Decider()
        {
            return new DownloadDecider(AllYes, false);
        }

        static void AnswersEachCallbackOnItsOwn()
        {
            // #789 stage-2 retry: TIA's first callback had no parent and
            // nothing listed its checks; each is answered as it comes.
            object stop = new object(), over = new object(), start = new object();
            var r = new Run(AllYes);
            Eq(N(r, stop, "StopModules", "stop the CPU"), "StopAll", "first callback");
            Eq(r.Answers, 1, "answered after the first callback: one, never a look ahead");
            Eq(r.Seen[0].Id, QuestionIds.StopCpu, "question 1");
            Eq(r.Seen[0].Message, "stop the CPU", "question 1 message");
            Eq(r.Seen[0].Detail, "StopModules", "question 1 detail");
            Eq(N(r, over, "OverwriteSystemData", "overwrite"), "Overwrite", "second callback");
            Eq(N(r, start, "StartModules", "start"), "StartModule", "third callback");
            Eq(r.Answers, 3, "answered in all");
            Eq(r.D.Approved, 3, "approved");
            Eq(r.D.Asked.Count, 3, "asked");
            Eq(string.Join("; ", r.D.Acting.ToArray()), "StopModules = StopAll; OverwriteSystemData = Overwrite; StartModules = StartModule", "acting answers written");
            True(r.D.Cancelled == null, "cancelled");
        }

        static void ActingAndPassive()
        {
            foreach (string id in new[] { QuestionIds.StopCpu, QuestionIds.StartAfter, QuestionIds.Overwrite, QuestionIds.Reinitialize, QuestionIds.Other, "new-in-v22" })
            {
                True(DownloadChecks.Acting(id), id + " is acting");
            }
            foreach (string id in new[] { QuestionIds.Password, QuestionIds.ConfigurationPassword, QuestionIds.Certificate })
            {
                True(!DownloadChecks.Acting(id), id + " is passive");
            }
            // An answer counts as acting only once written, and a passive
            // one never does.
            object pw = new object(), stop = new object();
            var r = new Run(AllYes);
            N(r, pw, "ModuleWriteAccessPassword", "pw");
            Eq(r.D.Acting.Count, 0, "a password written");
            Step st = r.D.Next(stop, "StopModules", "stop", null);
            Eq(r.D.Acting.Count, 0, "stop answered, not yet written");
            r.D.Wrote(st);
            Eq(r.D.Acting.Count, 1, "stop written");
            // Start-after answered no writes NoAction, which changes
            // nothing: not acting.
            var n = new Run(delegate(Question q) { return new Answer { Question = q, Yes = false }; });
            N(n, new object(), "StartModules", "start");
            Eq(n.D.Acting.Count, 0, "start-after no");
        }

        static void ConsequenceNamesWhatToDo()
        {
            Eq(DownloadChecks.Consequence(new List<string>()), "", "none");
            Eq(DownloadChecks.Consequence(null), "", "null");
            Contains(DownloadChecks.Consequence(new List<string> { QuestionIds.StopCpu }), "TIA may already have stopped the CPU: check its mode and start it", "stop");
            Contains(DownloadChecks.Consequence(new List<string> { QuestionIds.Reinitialize }), "stopped the CPU", "reinitialize stops it");
            Contains(DownloadChecks.Consequence(new List<string> { QuestionIds.StartAfter }), "started the CPU", "start");
            Contains(DownloadChecks.Consequence(new List<string> { QuestionIds.Overwrite }), "acted on them", "overwrite");
            Contains(DownloadChecks.Consequence(new List<string> { QuestionIds.StopCpu, QuestionIds.Overwrite }), "tiad had answered stop-cpu, overwrite before the download ended", "names them");
        }

        static void AfterGetsTheRequestsStartAfter()
        {
            object stop = new object();
            foreach (var c in new[] { new KeyValuePair<bool?, string>(true, "StartModule"), new KeyValuePair<bool?, string>(false, "NoAction"), new KeyValuePair<bool?, string>(null, "NoAction") })
            {
                var r = new Run(AllYes, c.Key);
                N(r, stop, "StopModules", "stop");
                Eq(A(r.D, "StartModules"), c.Value, "startAfter " + c.Key);
                Eq(A(r.D, "StartModules"), c.Value, "asked again: never left to TIA");
                Eq(A(r.D, "StopModules"), null, "another configuration after loading");
            }
            // A load nobody approved never starts the CPU.
            var d = new Run(delegate { return null; }, true);
            Throws<DownloadCancel>(delegate { d.D.Next(stop, "StopModules", "stop", null); }, "declined");
            Eq(A(d.D, "StartModules"), "NoAction", "after a decline, start-after yes in the request");
            Eq(A(new DownloadDecider(AllYes, true), "StartModules"), "StartModule", "no check asked: the request's start-after");
        }

        static void Cancelled(Run r, Action f, string sub, bool declined, string what)
        {
            DownloadCancel c = Throws<DownloadCancel>(f, what);
            if (c == null) return;
            Contains(c.Message, sub, what + ": message");
            Eq(c.Declined, declined, what + ": declined");
            True(object.ReferenceEquals(r.D.Cancelled, c), what + ": the decider keeps the cancel");
        }

        static void DeclineIsClean()
        {
            var r = new Run(delegate { return null; });
            object k = new object();
            Cancelled(r, delegate { r.D.Next(k, "StopModules", "stop", null); }, "at TIA's StopModules (stop)", true, "the request says no");
            Contains(r.D.Cancelled.Message, r.D.Token, "the decline carries the token");
            Eq(r.Answers, 1, "answered");
            Eq(r.D.Approved, 0, "approved");
            Eq(r.D.Asked.Count, 1, "asked: the one configuration");
            // A later callback means TIA went on past the decline: a
            // failure from then on, never a decline (#1145 review), and
            // nothing more is asked.
            Cancelled(r, delegate { r.D.Next(new object(), "OverwriteSystemData", "over", null); }, "TIA went on", false, "a later callback");
            Eq(r.Answers, 1, "answered after a later callback");
            Eq(A(r.D, "StartModules"), "NoAction", "after a load nobody approved");
            Eq(A(r.D, "StopModules"), null, "another configuration after a load nobody approved");
        }

        static void CancelOnABrokenAnswer()
        {
            object k = new object();
            var cases = new Dictionary<string, Func<Question, Answer>>
            {
                { "is not to stop-cpu", delegate(Question q) { return new Answer { Question = new Question { Id = QuestionIds.Overwrite }, Yes = true }; } },
                { "is not to", delegate(Question q) { return new Answer { Question = null, Yes = true }; } },
            };
            foreach (var c in cases)
            {
                var r = new Run(c.Value);
                Cancelled(r, delegate { r.D.Next(k, "StopModules", "stop", null); }, c.Key, false, c.Key);
                Eq(r.D.Approved, 0, c.Key + ": approved");
            }
        }

        static void CancelOnAnUnappliableAnswer()
        {
            object k = new object();
            var r = new Run(delegate(Question q) { return new Answer { Question = q, Yes = false }; });
            Cancelled(r, delegate { r.D.Next(k, "StopModules", "stop", null); }, "cannot apply no to StopModules", false, "no to stop");
            Eq(r.D.Approved, 0, "no to stop: approved");
            object u = new object();
            r = new Run(AllYes);
            Cancelled(r, delegate { r.D.Next(u, "ResetModule", "reset", null); }, "cannot apply yes to ResetModule (reset)", false, "yes to an unknown configuration");
            Eq(r.Seen[0].Id, QuestionIds.Other, "an unknown configuration is other");
        }

        static void CancelOnARepeatedCallback()
        {
            object a = new object();
            var r = new Run(AllYes);
            N(r, a, "StopModules", "stop");
            Cancelled(r, delegate { r.D.Next(a, "StopModules", "stop", null); }, "twice", false, "a repeated configuration");
            Eq(r.Answers, 1, "the repeat is never answered");
        }

        static void CertificateDetail()
        {
            object c = new object();
            var r = new Run(AllYes);
            r.D.Next(c, "TlsVerificationConfiguration", "trust?", null);
            Eq(r.Seen[0].Id, QuestionIds.Certificate, "id");
            Eq(r.Seen[0].Detail, "", "detail, nothing read");
            r = new Run(AllYes);
            r.D.Next(c, "TlsVerificationConfiguration", "trust?", new Question { Detail = "AB12" });
            Eq(r.Seen[0].Detail, "AB12", "detail, as read");
            r = new Run(AllYes);
            r.D.Next(c, "DataBlockReinitialization", "reinit", new Question { Detail = "AB12", Objects = new List<string> { "Tags" } });
            Eq(r.Seen[0].Detail, "DataBlockReinitialization", "another configuration's detail is its type");
            Eq(string.Join(",", r.Seen[0].Objects.ToArray()), "Tags", "its objects");
        }

        static void SeenNeverThrows()
        {
            // The glue's diagnostics before the decision: reading TIA's
            // object may fail; it must never change how the download ends.
            Func<string> broken = delegate { throw new ObjectDisposedException("DownloadConfiguration"); };
            True(Caught(delegate { DownloadDecider.Seen(1, broken, broken); }) == null, "every read fails");
            True(Caught(delegate { DownloadDecider.Seen(2, delegate { return "StopModules"; }, delegate { return null; }); }) == null, "nulls");
        }

        // ---- how a download ended ----

        static void End(DownloadEnd e, bool loaded, bool declined, string failure, string what)
        {
            Eq(e.Loaded, loaded, what + ": loaded");
            Eq(e.Declined, declined, what + ": declined");
            if (failure == null) Eq(e.Failure, null, what + ": failure");
            else Contains(e.Failure, failure, what + ": failure");
        }

        // Approved runs a decider through an approved, passive first
        // callback (a password: nothing acting written).
        static Run Approved(object key)
        {
            var r = new Run(AllYes);
            N(r, key, "ModuleWriteAccessPassword", "pw");
            return r;
        }

        static void FinishLoaded()
        {
            foreach (string st in new[] { "Success", "Information", "Warning" })
            {
                End(Approved(new object()).D.Finish(true, st, "", null), true, false, null, st);
            }
        }

        static void FinishDeclined()
        {
            var r = new Run(delegate { return null; });
            object k = new object();
            Exception thrown = null;
            try
            {
                r.D.Next(k, "StopModules", "stop", null);
            }
            catch (DownloadCancel c)
            {
                thrown = new InvalidOperationException("wrapped by TIA", c);
            }
            End(r.D.Finish(true, null, "", thrown), false, true, null, "declined, as TIA rethrows it");
        }

        static void FinishDeclineAfterActing()
        {
            // TIA asked stop (answered yes, written), then overwrite, which
            // the request does not answer: TIA may have stopped the CPU
            // already, so the clean decline is in doubt, naming the answer.
            var r = new Run(delegate(Question q) { return q.Id == QuestionIds.StopCpu ? AllYes(q) : null; });
            N(r, new object(), "StopModules", "stop");
            Exception thrown = null;
            try
            {
                r.D.Next(new object(), "OverwriteSystemData", "over", null);
            }
            catch (DownloadCancel c)
            {
                thrown = new InvalidOperationException("wrapped by TIA", c);
            }
            True(r.D.Cancelled != null && r.D.Cancelled.Declined, "the decline is kept as a decline");
            DownloadEnd e = r.D.Finish(true, null, "", thrown);
            End(e, false, false, "after tiad wrote StopModules = StopAll", "a decline after an acting answer");
            True(e.InDoubt, "in doubt");
            // A passive answer before it leaves the decline clean.
            var p = new Run(delegate(Question q) { return q.Id == QuestionIds.Password ? AllYes(q) : null; });
            N(p, new object(), "ModuleWriteAccessPassword", "pw");
            try
            {
                p.D.Next(new object(), "StopModules", "stop", null);
            }
            catch (DownloadCancel c)
            {
                thrown = new InvalidOperationException("wrapped by TIA", c);
            }
            End(p.D.Finish(true, null, "", thrown), false, true, null, "a decline after a passive answer");
        }

        static void FinishFailures()
        {
            // A load that ended in an error.
            End(Approved(new object()).D.Finish(true, "Error", "the CPU refused", null), false, false, "TIA's download ended Error: the CPU refused", "error state");
            End(Approved(new object()).D.Finish(true, "Bogus", "", null), false, false, "TIA's download ended Bogus", "unknown state");
            // TIA failed while loading.
            End(Approved(new object()).D.Finish(true, null, "", new TiaException("connection lost", new Exception("timeout"))), false, false, "connection lost: timeout", "TIA threw");
            // A cancel after approval (an answer tiad cannot apply), wrapped
            // by TIA.
            var r = Approved(new object());
            Exception thrown = null;
            try
            {
                r.D.Next(new object(), "ResetModule", "reset", null);
            }
            catch (DownloadCancel c)
            {
                thrown = new InvalidOperationException("wrapped", c);
            }
            End(r.D.Finish(true, null, "", thrown), false, false, "cannot apply yes to ResetModule", "a cancel after approval");
            // A cancel the glue made itself (Write's Fail): its kept reason,
            // whatever TIA's exception says.
            var g = Approved(new object());
            g.D.Fail("no protection password");
            DownloadEnd own = g.D.Finish(true, null, "", new InvalidOperationException("wrapped", new OperationCanceledException("no protection password")));
            End(own, false, false, "no protection password", "the glue's own cancel");
            Eq(own.Failure, "no protection password", "the glue's own cancel, without TIA's wrapping");
            // With an acting answer written before it, the failure names it.
            var s = new Run(AllYes);
            N(s, new object(), "StopModules", "stop");
            s.D.Fail("writing Overwrite into OverwriteSystemData failed: denied");
            End(s.D.Finish(true, null, "", new InvalidOperationException("wrapped")), false, false, "denied (after tiad wrote StopModules = StopAll)", "a failure after an acting answer");
            End(s.D.Finish(true, "Error", "", null), false, false, "after tiad wrote StopModules = StopAll", "TIA went on after the failure");
            // Before any callback: TIA refused, nothing decided.
            End(Decider().Finish(true, null, "", new TiaException("no route")), false, false, "no route", "before any callback");
        }

        static void FinishSwallowedCancel()
        {
            foreach (bool declined in new[] { true, false })
            {
                var r = declined ? new Run(delegate { return null; }) : Approved(new object());
                object k = new object();
                try
                {
                    r.D.Next(k, declined ? "StopModules" : "ResetModule", "x", null);
                }
                catch (DownloadCancel)
                {
                }
                // TIA caught the cancel and returned instead.
                foreach (string st in new[] { "Success", "Error" })
                {
                    End(r.D.Finish(true, st, "", null), false, false, "TIA went on after tiad cancelled", (declined ? "declined" : "cancelled") + ", then " + st);
                }
            }
        }

        static void FinishWithoutACallback()
        {
            // TIA ran its download without asking anything: loaded when its
            // result says so (which the agent refuses to call a dry run),
            // in doubt when it does not (#1154 review: never loaded on an
            // Error state just because nothing was asked).
            End(Decider().Finish(true, "Success", "", null), true, false, null, "Success");
            foreach (string st in new[] { "Error", "Bogus", null })
            {
                DownloadEnd e = Decider().Finish(true, st, "", null);
                End(e, false, false, "TIA's download ended", st ?? "no state");
                True(e.InDoubt, (st ?? "no state") + ": in doubt");
            }
        }

        static void AFailedWriteCancels()
        {
            object a = new object(), b = new object();
            var r = new Run(AllYes);
            r.D.Next(a, "StopModules", "stop", null);
            DownloadCancel c = r.D.Fail("writing StopAll into StopModules failed: no such value");
            True(object.ReferenceEquals(r.D.Cancelled, c), "the decider keeps the cancel");
            Eq(c.Declined, false, "declined");
            Eq(r.D.Acting.Count, 0, "a write that failed is not written");
            // TIA swallowed the exception and went on.
            Cancelled(r, delegate { r.D.Next(b, "OverwriteSystemData", "over", null); }, "no such value", false, "the next callback");
            End(r.D.Finish(true, "Success", "", null), false, false, "TIA went on after tiad cancelled the download (writing StopAll", "TIA reported success");
            // A second failure keeps the first cancel.
            True(object.ReferenceEquals(r.D.Fail("other"), c), "a second failure");
        }

        // DownloadStepsRun runs DownloadSteps.Run and returns what it threw
        // (null when it returned).
        static TiaException DownloadStepsRun(DownloadDecider d, Action prepare, Func<DownloadCall> download, Action stop, Func<List<string>> asked, out DownloadOutcome o)
        {
            o = null;
            try
            {
                o = DownloadSteps.Run(d, "192.0.2.10", prepare, download, stop, asked);
                return null;
            }
            catch (TiaException e)
            {
                return e;
            }
        }

        static List<string> NoneAsked()
        {
            return new List<string>();
        }

        // #789 stage 2: TIA died inside its download call, and detaching
        // the handlers from its disposed objects threw too.
        static void DownloadStepsTiaDies()
        {
            DownloadOutcome o;
            Action gone = delegate { throw new ObjectDisposedException("ConnectionConfiguration", "detach failed"); };
            TiaException e = DownloadStepsRun(Decider(), delegate { },
                delegate { throw new ObjectDisposedException("DownloadProvider", "TIA exited"); }, gone, NoneAsked, out o);
            True(e != null && e.InDoubt, "TIA dies inside its download call: in doubt");
            Contains(e.Message, "TIA exited", "the download's own failure");
            True(e.Message.IndexOf("detach failed", StringComparison.Ordinal) < 0, "not the detach's");
            // Any failure inside the call, not only TIA's own.
            e = DownloadStepsRun(Decider(), delegate { },
                delegate { throw new InvalidOperationException("reading the result failed"); }, delegate { }, NoneAsked, out o);
            True(e != null && e.InDoubt, "any failure inside the call: in doubt");
            // Before the call: nothing loaded, the failure itself named.
            e = DownloadStepsRun(Decider(), delegate { throw new ObjectDisposedException("DownloadProvider", "TIA exited"); },
                delegate { throw new InvalidOperationException("the call was made"); }, gone, NoneAsked, out o);
            True(e != null && !e.InDoubt, "TIA dies before its download call: not in doubt");
            Contains(e.Message, "TIA exited", "the prepare's own failure");
            True(e.Message.IndexOf("detach failed", StringComparison.Ordinal) < 0, "not the detach's, before the call");
            // A download that loaded stays loaded when detaching fails.
            e = DownloadStepsRun(Approved(new object()).D, delegate { }, delegate { return new DownloadCall { State = "Success" }; }, gone, NoneAsked, out o);
            True(e == null && o != null && o.Loaded, "loaded, then detaching fails: loaded");
            // stop runs every time, also when the call throws.
            int stops = 0;
            DownloadStepsRun(Decider(), delegate { }, delegate { throw new InvalidOperationException("x"); }, delegate { stops++; }, NoneAsked, out o);
            DownloadStepsRun(Decider(), delegate { throw new InvalidOperationException("x"); }, delegate { return null; }, delegate { stops++; }, NoneAsked, out o);
            Eq(stops, 2, "stop runs after a failure, before and inside the call");
        }

        // After the call, nothing escapes as a plain exception.
        static void DownloadStepsAfterTheCall()
        {
            DownloadOutcome o;
            Func<List<string>> broken = delegate { throw new InvalidOperationException("Asked is disposed"); };
            TiaException e = DownloadStepsRun(Approved(new object()).D, delegate { }, delegate { return new DownloadCall { State = "Success" }; }, delegate { }, broken, out o);
            True(e != null && e.InDoubt, "reading the questions fails after the call: in doubt");
            e = DownloadStepsRun(Decider(), delegate { }, delegate { return null; }, delegate { }, NoneAsked, out o);
            True(e != null && e.InDoubt, "the call returned nothing: in doubt");
            // A question going online fails it, even when TIA went on.
            e = DownloadStepsRun(Approved(new object()).D, delegate { }, delegate { return new DownloadCall { State = "Success" }; }, delegate { },
                delegate { return new List<string> { "trust the TLS certificate of PLC_1" }; }, out o);
            True(e != null && e.InDoubt, "asked going online, TIA went on: in doubt");
            Contains(e.Message, "TLS certificate of PLC_1", "the question");
        }

        // A handler that could not be detached answers no from then on,
        // never Trusted, even on the upload's opt-in.
        static void OnlineAnswersAfterStop()
        {
            const string tls = "TlsVerificationConfiguration", creds = "OnlinePasswordConfiguration";
            var a = new OnlineAnswers(true);
            Eq(a.Answer(tls), OnlineChecks.Trusted, "opted in, before stop");
            Eq(a.Answer(creds), OnlineChecks.Cancel, "credentials, before stop");
            bool ok = a.Stop(delegate { throw new ObjectDisposedException("ConnectionConfiguration"); }, "test: detaching");
            True(!ok, "a detach that throws reports failure");
            Eq(a.Answer(tls), OnlineChecks.NotTrusted, "after a failed detach");
            Eq(a.Answer(creds), OnlineChecks.Cancel, "credentials, after a failed detach");
            var b = new OnlineAnswers(true);
            True(b.Stop(delegate { }, "test: detaching"), "a detach that works");
            Eq(b.Answer(tls), OnlineChecks.NotTrusted, "after stop");
            Eq(new OnlineAnswers(false).Answer(tls), OnlineChecks.NotTrusted, "not opted in");
        }

        sealed class BrokenWriter : System.IO.TextWriter
        {
            public override System.Text.Encoding Encoding
            {
                get { return System.Text.Encoding.UTF8; }
            }

            public override void Write(char value)
            {
                throw new System.IO.IOException("stderr is gone");
            }

            public override void WriteLine(string value)
            {
                throw new System.IO.IOException("stderr is gone");
            }
        }

        // Quietly never throws, even when logging what it dropped fails.
        static void QuietlyEvenWhenTheLogFails()
        {
            System.IO.TextWriter was = Console.Error;
            bool ok = true;
            Exception escaped = null;
            Console.SetError(new BrokenWriter());
            try
            {
                ok = DownloadSteps.Quietly(delegate { throw new ObjectDisposedException("ConnectionConfiguration"); }, "test: detaching");
            }
            catch (Exception e)
            {
                escaped = e;
            }
            finally
            {
                Console.SetError(was);
            }
            True(escaped == null, "nothing escapes: " + (escaped == null ? "" : escaped.Message));
            True(!ok, "reports the failure");
        }

        static void InDoubtOnceEntered()
        {
            DownloadEnd e = Approved(new object()).D.Finish(true, "Error", "", null);
            True(e.InDoubt, "a failed state once entered");
            e = Decider().Finish(true, null, "", new TiaException("connection lost"));
            True(e.InDoubt, "TIA threw once entered, before any callback");
            e = Decider().Finish(false, null, "", new TiaException("no PC interface"));
            True(!e.InDoubt, "a failure before TIA's download call");
            e = Approved(new object()).D.Finish(true, "Success", "", null);
            True(!e.InDoubt && e.Loaded, "loaded");
            var r = new Run(delegate { return null; });
            object k = new object();
            Exception thrown = null;
            try
            {
                r.D.Next(k, "StopModules", "stop", null);
            }
            catch (DownloadCancel x)
            {
                thrown = x;
            }
            e = r.D.Finish(true, null, "", thrown);
            True(e.Declined && !e.InDoubt, "declined is not in doubt");
            // A cancel (an answer tiad cannot apply) that TIA swallowed: in
            // doubt.
            r = new Run(AllYes);
            try
            {
                r.D.Next(k, "ResetModule", "reset", null);
            }
            catch (DownloadCancel)
            {
            }
            e = r.D.Finish(true, "Success", "", null);
            True(e.InDoubt && e.Failure != null, "a swallowed cancel");
        }

        // Declined runs a decider whose decide says no, and returns the
        // cancel its first callback threw.
        static DownloadCancel Declined(out Run r)
        {
            r = new Run(delegate { return null; });
            object k = new object();
            try
            {
                r.D.Next(k, "StopModules", "stop", null);
            }
            catch (DownloadCancel c)
            {
                return c;
            }
            Fail("decide said no, but the callback did not cancel");
            return null;
        }

        static void ALoadAfterADeclineIsInDoubt()
        {
            Run r;
            // TIA swallowed the decline and loaded; then writing start-after
            // failed, and TIA threw that failure.
            DownloadCancel declined = Declined(out r);
            Step st = r.D.After(DownloadDecider.StartAfterType);
            Eq(S(st), "NoAction", "start-after after an undecided load");
            DownloadCancel failed = r.D.Fail("writing NoAction into StartModules failed: lost");
            True(!object.ReferenceEquals(failed, declined) && !failed.Declined, "a failure after a decline is a new, undeclined cancel");
            DownloadEnd e = r.D.Finish(true, null, "", new InvalidOperationException("wrapped", failed));
            True(!e.Declined && e.Failure != null && e.InDoubt, "write failed after a swallowed decline: in doubt, not declined (" + e.Failure + ")");

            // TIA swallowed the decline, loaded, wrote start-after, then
            // threw the decline after all.
            declined = Declined(out r);
            r.D.Wrote(r.D.After(DownloadDecider.StartAfterType));
            e = r.D.Finish(true, null, "", new InvalidOperationException("wrapped", declined));
            True(!e.Declined && e.InDoubt, "the decline thrown after a load");
            Contains(e.Failure, "TIA went on after tiad cancelled the download", "the decline thrown after a load");

            // TIA swallowed the decline, then failed for its own reason.
            Declined(out r);
            e = r.D.Finish(true, null, "", new TiaException("The connection to the CPU was lost"));
            True(!e.Declined && e.InDoubt, "another failure after a decline");
            Contains(e.Failure, "The connection to the CPU was lost", "another failure after a decline");

            // The decline itself, thrown back by TIA: declined, not in doubt.
            declined = Declined(out r);
            e = r.D.Finish(true, null, "", new InvalidOperationException("wrapped", declined));
            True(e.Declined && e.Failure == null && !e.InDoubt, "a plain decline");
        }

        static void SelectionsWritten()
        {
            var want = new List<string>
            {
                "StopModules.StopAll", "StartModules.StartModule", "StartModules.NoAction", "OverwriteSystemData.Overwrite",
                "DataBlockReinitialization.StopPlcAndReinitialize", "DataBlockReinitializationOrKeepActualValues.StopPlcAndReinitialize",
                "TlsVerificationConfiguration.Trusted",
            };
            var got = new List<string>();
            foreach (var kv in DownloadChecks.Selections()) got.Add(kv.Key + "." + kv.Value);
            Eq(string.Join(" ", got.ToArray()), string.Join(" ", want.ToArray()), "selections");
            // Every type For answers is listed, and only those.
            foreach (string t in new[] { "ResetModule", "UpgradeTargetDevice" })
            {
                True(Array.IndexOf(DownloadChecks.Answered, t) < 0, t + " is answered");
            }
            foreach (string t in DownloadChecks.Answered)
            {
                True(DownloadChecks.For(t, true) != null, t + ": yes has no setting");
            }
            // After's NoAction for start-after is among them.
            True(got.Contains("StartModules." + Decider().After(DownloadDecider.StartAfterType).Setting.Value), "after-download NoAction");
        }

        static void OnlineAsks()
        {
            Eq(TiaNames.Fingerprint(new byte[0]), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "the SHA-256 of nothing");
            Eq(TiaNames.Fingerprint(System.Text.Encoding.ASCII.GetBytes("abc")), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "the SHA-256 of abc");
            Eq(OnlineChecks.Ask("TlsVerificationConfiguration", "PLC_1", "The certificate is not trusted.", new List<string> { "aa", "bb" }),
                "trust the TLS certificate of PLC_1 (SHA-256 aa, bb): The certificate is not trusted.", "a certificate");
            Eq(OnlineChecks.Ask("TlsVerificationConfiguration", "", "", new List<string>()), "trust the TLS certificate of the PLC", "a certificate, nothing known");
            Eq(OnlineChecks.Ask("OnlineAuthenticationConfiguration", "PLC_1", "x", new List<string> { "aa" }),
                "credentials (OnlineAuthenticationConfiguration)", "credentials");
            Eq(OnlineChecks.Ask("OnlineFancyNewThing", "", "", new List<string>()), "a question tiad does not know (OnlineFancyNewThing)", "unknown");
            // Never trusted: a certificate is answered not trusted, the rest
            // cancel.
            var kinds = new Dictionary<string, string>
            {
                { "TlsVerificationConfiguration", OnlineChecks.Certificate },
                { "OnlineAuthenticationConfiguration", OnlineChecks.Credentials },
                { "OnlinePasswordConfiguration", OnlineChecks.Credentials },
                { "OnlineReadAccessPassword", OnlineChecks.Credentials },
                { "OnlineWriteAccessPassword", OnlineChecks.Credentials },
                { "OnlineFancyNewThing", OnlineChecks.Unknown },
                { "", OnlineChecks.Unknown },
                { "tlsverificationconfiguration", OnlineChecks.Unknown },
            };
            foreach (var kv in kinds)
            {
                Eq(OnlineChecks.Kind(kv.Key), kv.Value, kv.Key + ": kind");
                Eq(OnlineChecks.Answer(kv.Key), kv.Value == OnlineChecks.Certificate ? OnlineChecks.NotTrusted : OnlineChecks.Cancel, kv.Key + ": answer");
            }
            // The upload's opt-in trusts a certificate, and nothing else.
            Eq(OnlineChecks.Answer("TlsVerificationConfiguration"), OnlineChecks.NotTrusted, "the default");
            Eq(OnlineChecks.Answer("TlsVerificationConfiguration", false), OnlineChecks.NotTrusted, "no opt-in");
            Eq(OnlineChecks.Answer("TlsVerificationConfiguration", true), OnlineChecks.Trusted, "the opt-in");
            foreach (string t in new[] { "OnlineAuthenticationConfiguration", "OnlineReadAccessPassword", "OnlineFancyNewThing", "" })
            {
                Eq(OnlineChecks.Answer(t, true), OnlineChecks.Cancel, t + ": with the opt-in");
            }
            // Text from a CPU cannot end or forge a log line.
            Eq(TiaNames.LogSafe("PLC_1\n2026 audit download forged\r\u2028x"), "\"PLC_1\\n2026 audit download forged\\r\\u2028x\"", "log escaping");
            Eq(TiaNames.LogSafe(null), "\"\"", "log escaping, null");
            Eq(TiaNames.OnlineRefused("192.0.2.10", new List<string> { "one", "two" }, "Online connection failed."),
                "going online to 192.0.2.10, TIA asked: one; two. tiad trusts no CPU certificate and holds no CPU credentials yet, so it answered no (TIA: Online connection failed.)",
                "the refusal");
        }

        static void Messages()
        {
            Eq(TiaNames.Message(new Exception("a", new Exception("b", new Exception("a")))), "a: b", "nested, repeats once");
            Eq(TiaNames.Message(new Exception("outer: inner", new Exception("inner"))), "outer: inner", "already included");
            Eq(TiaNames.Message(new Exception("a", new Exception(""))), "a", "empty inner");
        }

        // ---- names ----

        static void TypeIdentifiers()
        {
            Eq(TiaNames.TypeIdentifier("6ES7 510-1DK03-0AB0", "V3.1"), "OrderNumber:6ES7 510-1DK03-0AB0/V3.1", "with V");
            Eq(TiaNames.TypeIdentifier("6ES7 510-1DK03-0AB0", "3.1"), "OrderNumber:6ES7 510-1DK03-0AB0/V3.1", "without V");
        }

        static void PcInterfaces()
        {
            var one = new List<string> { "Ethernet A" };
            var two = new List<string> { "Ethernet B", "Ethernet A" };
            Eq(TiaNames.PcInterface(one, ""), "Ethernet A", "the only one");
            Eq(TiaNames.PcInterface(two, "Ethernet B"), "Ethernet B", "the configured one");
            Contains(Throws<TiaException>(delegate { TiaNames.PcInterface(two, ""); }, "two, none configured").Message,
                "several PN/IE PC interfaces (Ethernet A, Ethernet B): set TIAD_PC_INTERFACE", "two, none configured");
            Contains(Throws<TiaException>(delegate { TiaNames.PcInterface(two, "ethernet b"); }, "configured, case differs").Message,
                "TIAD_PC_INTERFACE \"ethernet b\" is not a PN/IE PC interface TIA lists (Ethernet A, Ethernet B)", "configured, case differs");
            Contains(Throws<TiaException>(delegate { TiaNames.PcInterface(new List<string>(), ""); }, "none").Message, "no PN/IE PC interface", "none");
            Contains(Throws<TiaException>(delegate { TiaNames.PcInterface(new List<string>(), "x"); }, "none, configured").Message, "(none)", "none, configured");
        }

        static void OnlyOne()
        {
            Eq(TiaNames.Only("target interface", new List<string> { "1 X1" }), "1 X1", "one");
            Contains(Throws<TiaException>(delegate { TiaNames.Only("target interface", new List<string> { "b", "a" }); }, "two").Message,
                "TIA lists 2 target interfaces (a, b), not one", "two");
            Contains(Throws<TiaException>(delegate { TiaNames.Only("target interface", new List<string>()); }, "none").Message,
                "TIA lists 0 target interfaces, not one", "none");
        }

        static void CheckAttribute()
        {
            var names = new List<string> { "SubnetMask", "Address" };
            TiaNames.CheckAttribute("Node", names, "Address");
            Contains(Throws<TiaException>(delegate { TiaNames.CheckAttribute("Node", names, "address"); }, "case differs").Message,
                "Node has no attribute address; it has: Address, SubnetMask", "case differs");
        }

        static void ResultStates()
        {
            Eq(TiaNames.CompileResult("Success"), "success", "compile success");
            Eq(TiaNames.CompileResult("Information"), "success", "compile information");
            Eq(TiaNames.CompileResult("Warning"), "warning", "compile warning");
            Eq(TiaNames.CompileResult("Error"), "error", "compile error");
            Eq(TiaNames.CompileResult("Bogus"), "error", "compile unknown");
            Eq(TiaNames.Severity("Success"), "info", "severity success");
            Eq(TiaNames.Severity("Information"), "info", "severity information");
            Eq(TiaNames.Severity("Warning"), "warning", "severity warning");
            Eq(TiaNames.Severity("Error"), "error", "severity error");
            Eq(TiaNames.Severity("Bogus"), "error", "severity unknown");
            True(TiaNames.DownloadLoaded("Success"), "download success");
            True(TiaNames.DownloadLoaded("Information"), "download information");
            True(TiaNames.DownloadLoaded("Warning"), "download warning");
            True(!TiaNames.DownloadLoaded("Error"), "download error");
            True(!TiaNames.DownloadLoaded("Bogus"), "download unknown");
        }

        static void Differences()
        {
            foreach (string same in new[] { "ObjectsIdentical", "CompareIrrelevant", "FolderContentsIdentical", "FolderContentsDifferent" })
            {
                Eq(TiaNames.Difference(same), "", same);
            }
            Eq(TiaNames.Difference("LeftMissing"), "only online", "left missing");
            Eq(TiaNames.Difference("RightMissing"), "only offline", "right missing");
            Eq(TiaNames.Difference("ObjectsDifferent"), "different", "different");
            Eq(TiaNames.Difference("FolderContentEqualOwnStateDifferent"), "different", "folder's own state");
            Eq(TiaNames.Difference("FolderContainsDifferencesOwnStateDifferent"), "different", "folder's own state and contents");
            Eq(TiaNames.Difference("Bogus"), "different", "unknown");
        }

        static CompareNode Cmp(string name, string state, params CompareNode[] children)
        {
            var n = new CompareNode { Name = name, State = state };
            n.Children.AddRange(children);
            return n;
        }

        static string Diffs(List<Difference> l)
        {
            var s = new List<string>();
            foreach (var d in l) s.Add(d.Object + ": " + d.State);
            return string.Join("; ", s.ToArray());
        }

        // BenchTree is the shape TIA reported for an equal CPU in the #789
        // dry-run window (names here are TIA's defaults): the CPU's folder
        // over five children, three identical objects among them, and three
        // empty folders.
        static CompareNode BenchTree()
        {
            return Cmp("PLC_1", "FolderContentsIdentical",
                Cmp("Program blocks", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical")),
                Cmp("PLC tags", "FolderContentsIdentical", Cmp("Default tag table", "ObjectsIdentical"), Cmp("Tag table_1", "ObjectsIdentical")),
                Cmp("Software units", "FolderContentsIdentical"),
                Cmp("Technology objects", "FolderContentsIdentical"),
                Cmp("PLC data types", "FolderContentsIdentical"));
        }

        static void CompareIs(CompareNode tree, bool equal, string diffs, string what)
        {
            var l = new List<Difference>();
            Eq(TiaNames.CompareEqual(tree, l), equal, what + " equal");
            Eq(Diffs(l), diffs, what + " differences");
        }

        // CompareEqualTests: #789. A compare is equal only when the root
        // says identical, at least one object below it was compared, and
        // nothing anywhere is in a non-identical state; the bench tree
        // passes, and every tree that compares nothing or hides a
        // difference fails, naming why.
        static void CompareEqualTests()
        {
            CompareIs(BenchTree(), true, "", "the bench's equal tree");
            CompareIs(Cmp("PLC_1", "ObjectsIdentical", Cmp("Main [OB1]", "ObjectsIdentical")), true, "", "an ObjectsIdentical root");
            // CompareIrrelevant below the root is neutral.
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("Info", "CompareIrrelevant")), true, "",
                "an irrelevant object beside an identical one");

            // Nothing compared.
            CompareIs(null, false, ": nothing compared", "no tree");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical"), false, "PLC_1: nothing compared", "an identical root with no children");
            CompareIs(Cmp("PLC_1", "ObjectsIdentical"), false, "PLC_1: nothing compared", "an ObjectsIdentical root alone");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Software units", "FolderContentsIdentical"), Cmp("PLC data types", "FolderContentsIdentical")),
                false, "PLC_1: nothing compared", "an identical root over empty folders");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Info", "CompareIrrelevant")), false, "PLC_1: nothing compared",
                "an identical root over an irrelevant object only");

            // The root must say identical.
            CompareIs(Cmp("PLC_1", "CompareIrrelevant", Cmp("Main [OB1]", "ObjectsIdentical")), false, "PLC_1: not compared",
                "an irrelevant root over an identical object");
            CompareIs(Cmp("PLC_1", "Bogus", Cmp("Main [OB1]", "ObjectsIdentical")), false, "PLC_1: different", "an unknown root state");
            CompareIs(Cmp("PLC_1", "ObjectsDifferent", Cmp("Main [OB1]", "ObjectsIdentical")), false, "PLC_1: different", "a different root");

            // No non-identical state anywhere.
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical",
                Cmp("Program blocks", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("FC1", "ObjectsDifferent"))),
                false, "FC1: different", "a differing block under an identical root");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical",
                Cmp("Program blocks", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical")), Cmp("PLC tags", "FolderContentsDifferent")),
                false, "PLC tags: contents differ", "a folder whose contents differ, showing no differing child");
            CompareIs(Cmp("PLC_1", "FolderContentsDifferent", Cmp("Main [OB1]", "ObjectsIdentical")), false, "PLC_1: contents differ",
                "a root whose contents differ, showing no differing child");
            CompareIs(Cmp("PLC_1", "FolderContentsDifferent", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("FB1", "RightMissing")), false, "FB1: only offline",
                "a root whose contents differ names the differing child, not itself");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("FB9", "LeftMissing")), false, "FB9: only online",
                "an object only the CPU has");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical", Cmp("Network 1", "ObjectsDifferent"))), false,
                "Network 1: different", "a differing part inside an identical object");

            // The decision adds to what is already there and reads only that.
            var l = new List<Difference> { new Difference { Object = "earlier", State = "different" } };
            Eq(TiaNames.CompareEqual(BenchTree(), l), true, "an equal tree with earlier entries");
            Eq(l.Count, 1, "an equal tree adds nothing");
        }

        static void FileNames()
        {
            Eq(TiaNames.FileName("Main_1-a", ".scl"), "Main_1-a.scl", "plain");
            Eq(TiaNames.FileName("a b.c/d:e", ".xml"), "a%20b%2Ec%2Fd%3Ae.xml", "escaped");
            Eq(TiaNames.FileName("100%", ""), "100%25", "percent");
            Eq(TiaNames.FileName("\u00b5s", ".xml"), "%C2%B5s.xml", "UTF-8");
            Eq(TiaNames.FileName("", ".xml"), "%.xml", "empty");
            Eq(TiaNames.FileName("CON", ".scl"), "%43ON.scl", "device name");
            Eq(TiaNames.FileName("lpt1", ".scl"), "%6Cpt1.scl", "device name, lower case");
            Eq(TiaNames.FileName("com0", ".scl"), "%63om0.scl", "COM0, a device name");
            Eq(TiaNames.FileName("LPT9", ".scl"), "%4CPT9.scl", "LPT9, a device name");
            Eq(TiaNames.FileName("COMX", ".scl"), "COMX.scl", "not a device name: no digit");
            Eq(TiaNames.FileName("COM10", ".scl"), "COM10.scl", "not a device name");
            Eq(TiaNames.FileName("CONSOLE", ".scl"), "CONSOLE.scl", "longer than a device name");
            Eq(TiaNames.FileName("Con.x", ".scl"), "Con%2Ex.scl", "a device name with a dot is escaped anyway");
            foreach (string n in new[] { "CON", "nul", "a.", "a ", "..", "x/../y", "COM\u00b9", "PRN" })
            {
                string f = TiaNames.FileName(n, ".xml");
                try
                {
                    Eq(Paths.CleanPath(f), f, n + ": the sandbox rule accepts it as it is");
                }
                catch (TiadException e)
                {
                    Fail(n + ": " + f + " is refused by the sandbox rule: " + e.Message);
                }
            }
        }

        static void BlockKinds()
        {
            var want = new Dictionary<string, string>
            {
                { "OB", "OB" }, { "FB", "FB" }, { "FC", "FC" },
                { "GlobalDB", "DB" }, { "InstanceDB", "DB" }, { "ArrayDB", "DB" },
                { "PlcStruct", "UDT" }, { "PlcType", "UDT" },
                { "PlcExternalSource", "" }, { "", "" },
            };
            foreach (var kv in want) Eq(TiaNames.BlockKind(kv.Key), kv.Value, kv.Key);
            True(TiaNames.ExportsAsScl("SCL"), "SCL");
            True(TiaNames.ExportsAsScl("DB"), "DB");
            True(!TiaNames.ExportsAsScl("LAD"), "LAD");
            True(!TiaNames.ExportsAsScl("STL"), "STL");
        }

        static void OpennessAssemblies()
        {
            const string dir = @"C:\api";
            Eq(TiaNames.OpennessAssembly(dir, "Siemens.Engineering.Step7"), @"C:\api\Siemens.Engineering.Step7.dll", "Step7");
            Eq(TiaNames.OpennessAssembly(dir, "Siemens.Engineering"), @"C:\api\Siemens.Engineering.dll", "the root name");
            Eq(TiaNames.OpennessAssembly(dir, "Siemens.EngineeringX"), null, "a lookalike");
            Eq(TiaNames.OpennessAssembly(dir, "System.Xml"), null, "another assembly");
            Eq(TiaNames.OpennessAssembly(dir, "Siemens.Engineering.\\..\\evil"), null, "a path");
            Eq(TiaNames.OpennessAssembly(dir, "Siemens.Engineering...evil"), null, "dots");
            Eq(TiaNames.OpennessAssembly(dir, "Siemens.Engineering.a/b"), null, "a slash");
            Eq(TiaNames.OpennessAssembly(dir, "Siemens.Engineering.C:x"), null, "a colon");
            Eq(TiaNames.OpennessAssembly("", "Siemens.Engineering.Step7"), null, "no folder");
            Eq(TiaNames.OpennessAssembly(dir, null), null, "no name");
        }

        // A machine with Openness V21 registered, or not.
        sealed class Machine : IMachine
        {
            public Dictionary<string, string> Reg = new Dictionary<string, string>();
            public HashSet<string> Files = new HashSet<string>();

            public string RegString(string key, string name)
            {
                string v;
                return Reg.TryGetValue(key + "|" + name, out v) ? v : null;
            }

            public List<string> RegSubKeys(string key)
            {
                return null;
            }

            public bool FileExists(string path)
            {
                return Files.Contains(path);
            }

            public string ExePath
            {
                get { return @"C:\tiad\tiad.exe"; }
            }

            public DateTime ExeLastWriteUtc
            {
                get { return DateTime.UtcNow; }
            }

            public byte[] ExeSha256
            {
                get { return new byte[32]; }
            }

            public GroupState OpennessGroup()
            {
                return GroupState.Member;
            }
        }

        static void PublicApiDir()
        {
            const string api = @"C:\Program Files\Siemens\Automation\Portal V21\PublicAPI\V21\net48";
            var m = new Machine();
            Eq(MachineGates.PublicApiDir(m), "", "nothing registered");
            m.Reg[MachineGates.OpennessKey + "|PortalVersion"] = "V21";
            m.Reg[MachineGates.PublicApiKey + "|Siemens.Engineering.Base"] = api + @"\Siemens.Engineering.Base.dll";
            m.Reg[MachineGates.PublicApiKey + "|AssemblyVersion"] = "21.0.0.0";
            m.Files.Add(api + @"\Siemens.Engineering.Base.dll");
            m.Files.Add(api + @"\Siemens.Engineering.Step7.dll");
            Eq(MachineGates.PublicApiDir(m), api, "registered");
            m.Files.Clear();
            Eq(MachineGates.PublicApiDir(m), "", "registered, assemblies missing");
        }
    }
}
