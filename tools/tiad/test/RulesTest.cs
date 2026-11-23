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

        enum Pick { KeepOnline, UpdateKeepPassword, ResetToProject }

        sealed class Offered
        {
            public Pick CurrentSelection { get { return Pick.KeepOnline; } set { } }
            public List<Pick> Selections { get { return new List<Pick> { Pick.UpdateKeepPassword, Pick.ResetToProject }; } }
            public string Message { get { return "line\n2026 audit download forged"; } }
            public int[] Many { get { var a = new int[25]; for (int i = 0; i < a.Length; i++) a[i] = i; return a; } }
            public string Broken { get { throw new InvalidOperationException("disposed"); } }
            public object Missing { get { return null; } }
            public string this[int i] { get { return "indexer"; } }
            public IEnumerable<int> Endless { get { return Count(); } }
            public IEnumerable<int> BreaksMidway { get { return Break(); } }
            public string ConfigurationPassword { get { throw new InvalidOperationException("must not be read"); } }
            public string PublicKey { get { return "must-not-be-logged"; } }
            string Hidden { get { return "private"; } }

            static IEnumerable<int> Count()
            {
                for (int i = 0; ; i++) yield return i;
            }

            static IEnumerable<int> Break()
            {
                yield return 1;
                throw new InvalidOperationException("enumerator broke");
            }
        }

        // Members: what a configuration TIA handed over offers, on one log
        // line that nothing in it can end or forge, and never throwing.
        static void MembersDescribesWhatTiaOffers()
        {
            string m = TiaNames.Members(new Offered());
            string twenty = "[\"0\", \"1\", \"2\", \"3\", \"4\", \"5\", \"6\", \"7\", \"8\", \"9\", \"10\", \"11\", \"12\", \"13\", \"14\", \"15\", \"16\", \"17\", \"18\", \"19\", more]";
            // A list without end stops at the cap; secret names are never read.
            Eq(m, "BreaksMidway=unreadable (InvalidOperationException) Broken=unreadable (InvalidOperationException) ConfigurationPassword=<not read> " +
                "CurrentSelection=\"KeepOnline\" of [KeepOnline, UpdateKeepPassword, ResetToProject] Endless=" + twenty + " " +
                "Many=" + twenty + " " +
                "Message=\"line\\n2026 audit download forged\" Missing=null PublicKey=<not read> Selections=[\"UpdateKeepPassword\", \"ResetToProject\"]", "members");
            Eq(TiaNames.Members(null), "null", "members of null");
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
                T("TrustFixOnlyForCertificates", TrustFixOnlyForCertificates),
                T("DeployStatusOf", DeployStatusOf),
                T("DeploySourceShape", DeploySourceShape),
                T("SessionsLeftCountsClosing", SessionsLeftCountsClosing),
                T("ReserveOneProject", ReserveOneProject),
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
                T("FoundSaysWhatTheFirstApplyChanges", FoundSaysWhatTheFirstApplyChanges),
                T("OwnershipReadsEveryHeader", OwnershipReadsEveryHeader),
                T("OwnershipRefusesABlockOutsideTheGroup", OwnershipRefusesABlockOutsideTheGroup),
                T("OwnershipSeesAChangeOutsideTheGroup", OwnershipSeesAChangeOutsideTheGroup),
                T("OwnershipRefusesADataTypeOutsideTheGroup", OwnershipRefusesADataTypeOutsideTheGroup),
                T("OwnershipPrunesOnlyPrefixedObjects", OwnershipPrunesOnlyPrefixedObjects),
                T("CyclicObsReadsAndRefuses", CyclicObsReadsAndRefuses),
                T("CyclicObsMatchSources", CyclicObsMatchSources),
                T("CyclicObsRefuseTheSystemConstantClash", CyclicObsRefuseTheSystemConstantClash),
                T("CyclicObsPlan", CyclicObsPlan),
                T("CyclicObsVerifyAndXml", CyclicObsVerifyAndXml),
                T("KeptDevicesNeedANamedYes", KeptDevicesNeedANamedYes),
                T("ProtectionStepsHappyPaths", ProtectionStepsHappyPaths),
                T("ProtectionStepsStranded", ProtectionStepsStranded),
                T("ProtectionStepsReadBack", ProtectionStepsReadBack),
                T("AccessPlan", AccessPlan),
                T("AccessStepsNewCpu", AccessStepsNewCpu),
                T("AccessStepsKeptCpu", AccessStepsKeptCpu),
                T("TurnOnCheck", TurnOnCheck),
                T("AccessStepsReadBack", AccessStepsReadBack),
                T("AccessPrepare", AccessPrepare),
                T("AccessRecordsKnownAnswer", AccessRecordsKnownAnswer),
                T("AccessPasswordChecks", AccessPasswordChecks),
                T("WebPlan", WebPlan),
                T("WebSteps", WebSteps_),
                T("WebStepsReadBack", WebStepsReadBack),
                T("WebUsersCheck", WebUsersCheck),
                T("WebUsersRunNew", WebUsersRunNew),
                T("WebUsersRunExact", WebUsersRunExact),
                T("WebUsersStale", WebUsersStale),
                T("WebUsersRunRefuses", WebUsersRunRefuses),
                T("WebUsersPasswords", WebUsersPasswords),
                T("WebKeptNotes", WebKeptNotes),
                T("WebUserNames", WebUserNames),
                T("WebUserSecrets", WebUserSecrets),
                T("AccessRemoveWeb", AccessRemoveWeb),
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
                T("MembersDescribesWhatTiaOffers", MembersDescribesWhatTiaOffers),
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
                T("LoginFor", LoginFor),
                T("LoginDecides", LoginDecides),
                T("LoginOncePerOperation", LoginOncePerOperation),
                T("DownloadNamesTheLogin", DownloadNamesTheLogin),
                T("LoginThroughTheRecord", LoginThroughTheRecord),
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
                T("CompareSeesEveryBlock", CompareSeesEveryBlock),
                T("BlockLabels", BlockLabels),
                T("ElementNames", ElementNames),
                T("ModeFromMessages", ModeFromMessages),
                T("UserManagementGate", UserManagementGate),
                T("UserManagementResetRule", UserManagementResetRule),
                T("UserManagementResetMatrix", UserManagementResetMatrix),
                T("UserManagementResetAt", UserManagementResetAt),
                T("InstanceLogonsHold", InstanceLogonsHold),
                T("LoginLoggedOn", LoginLoggedOn),
                T("UserManagementKeepOrReset", UserManagementKeepOrReset),
                T("UserManagementChoices", UserManagementChoices),
                T("AttestationAccess", AttestationAccess),
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

        // The fingerprint read names its trust opt-in only for a refusal of
        // certificate questions alone: the opt-in answers no other.
        // SessionsLeftCountsClosing: a session taken out of the table to be
        // closed still counts until its close ended, so the agent never
        // retires a TIA instance a project is still open on (#789 D1).
        static void SessionsLeftCountsClosing()
        {
            var t = new SessionTable();
            True(!t.Any, "an empty table");
            Session a = t.Reserve("a.ap21", "/w/a.ap21", "");
            True(t.Any, "a session still opening");
            a.Handle = new object();
            t.End(a);
            True(t.Remove(a), "remove a");
            True(t.Any, "a out of the table, its close not ended");
            t.Restore(a);
            True(t.Any, "a back, broken");
            True(t.Remove(a), "remove a again");
            t.Closed(a);
            True(!t.Any, "every close ended");
            True(!t.Remove(a), "remove a twice");
            True(!t.Any, "a second remove counts nothing");
            Session c = t.Reserve("c.ap21", "/w/c.ap21", "");
            c.Handle = new object();
            t.End(c);
            c.LastUsed = DateTime.UtcNow.AddHours(-1);
            Eq(t.TakeIdle(TimeSpan.FromMinutes(1)).Count, 1, "the reaper takes c");
            True(t.Any, "c taken by the reaper, its close not ended");
            t.Closed(c);
            True(!t.Any, "the reaper's close ended");
        }

        // ReserveOneProject: TIA opens one project at a time (#616), so the
        // table holds one session: a second is refused while one is open,
        // opening, closing, or back broken, naming the open one, and taken
        // once it is closed or dropped.
        static void ReserveOneProject()
        {
            var t = new SessionTable();
            Session a = t.Reserve("projects/a/a.ap21", "/w/projects/a/a.ap21", "projects/a/");
            var e = Throws<TiadException>(delegate { t.Reserve("projects/b/b.ap21", "/w/projects/b/b.ap21", "projects/b/"); }, "a second while the first opens");
            if (e != null)
            {
                Eq(e.Status, 409, "status");
                Eq(e.Kind, Kinds.Busy, "kind");
                Contains(e.Message, "TIA opens one project at a time, and session " + a.Id + " is still opening projects/a/a.ap21: try again once it is open, then close it first; nothing was touched", "message while opening");
                True(e.Message.IndexOf("DELETE", StringComparison.Ordinal) < 0, "no close offered for a session still opening");
                var d = e.Payload as Obj;
                True(d != null && (string)d["openSession"] == a.Id && (string)d["openProject"] == "projects/a/a.ap21", "data names the open session");
            }
            a.Handle = new object();
            t.End(a);
            e = Throws<TiadException>(delegate { t.Reserve("projects/a/a.ap21", "/w/projects/a/a.ap21", "projects/a/"); }, "the same project again");
            if (e != null) Contains(e.Message, "TIA opens one project at a time, and session " + a.Id + " has projects/a/a.ap21 open: close it first (DELETE /v1/sessions/" + a.Id + "); nothing was touched", "message while open");
            True(t.Remove(a), "remove a");
            e = Throws<TiadException>(delegate { t.Reserve("projects/b/b.ap21", "/w/projects/b/b.ap21", ""); }, "a second while the first closes");
            if (e != null) Contains(e.Message, "a session is still closing", "closing message");
            t.Restore(a);
            e = Throws<TiadException>(delegate { t.Reserve("projects/b/b.ap21", "/w/projects/b/b.ap21", ""); }, "a second while the first is back, broken");
            if (e != null) Contains(e.Message, "has projects/a/a.ap21 open, and closing it failed: close it again (DELETE /v1/sessions/" + a.Id + "), or restart the agent", "message while broken");
            True(t.Remove(a), "remove a again");
            t.Closed(a);
            Session b = t.Reserve("projects/b/b.ap21", "/w/projects/b/b.ap21", "");
            t.Drop(b);
            Session c = t.Reserve("projects/c/c.ap21", "/w/projects/c/c.ap21", "");
            True(c != null && c.Id != b.Id, "taken after a drop");
        }

        static void TrustFixOnlyForCertificates()
        {
            string cert = OnlineChecks.Ask("TlsVerificationConfiguration", "PLC_1", "certificate not matching", new List<string>());
            string cred = OnlineChecks.Ask("OnlineAuthenticationConfiguration", "", "", new List<string>());
            string other = OnlineChecks.Ask("SomethingNewConfiguration", "", "", new List<string>());
            True(FingerprintRule.CertificateOnly("TIA: " + TiaNames.OnlineRefused("192.0.2.10", new List<string> { cert }, "x")), "a certificate question alone");
            True(!FingerprintRule.CertificateOnly("TIA: " + TiaNames.OnlineRefused("192.0.2.10", new List<string> { cred }, "x")), "credentials: " + cred);
            True(!FingerprintRule.CertificateOnly("TIA: " + TiaNames.OnlineRefused("192.0.2.10", new List<string> { cert, cred }, "x")), "a certificate and credentials");
            True(!FingerprintRule.CertificateOnly("TIA: " + TiaNames.OnlineRefused("192.0.2.10", new List<string> { other }, "x")), "a question tiad does not know: " + other);
            True(!FingerprintRule.CertificateOnly("TIA: " + TiaNames.OnlineRefused("192.0.2.10", new List<string> { cert, other }, "x")), "a certificate and a question tiad does not know");
            True(!FingerprintRule.CertificateOnly("TIA: the fingerprint service failed"), "no question asked");
        }

        static void QuestionIdsOfConfigurations()
        {
            var want = new Dictionary<string, string>
            {
                { "StopModules", QuestionIds.StopCpu },
                { "StartModules", QuestionIds.StartAfter },
                { "OverwriteSystemData", QuestionIds.Overwrite },
                { "ConsistentBlocksDownload", QuestionIds.ConsistentDownload },
                { "AlarmTextLibrariesDownload", QuestionIds.AlarmTexts },
                { "AllBlocksDownload", QuestionIds.AllBlocks },
                { "UserManagementDownload", QuestionIds.UserManagement },
                { "LoadIdentificationData", QuestionIds.IdentificationData },
                { "OverwriteOnMemoryCard", QuestionIds.Other },
                { "ProtectionLevelChanged", QuestionIds.Other },
                { "InitializeMemory", QuestionIds.Other },
                { "SelectiveDeleteDownload", QuestionIds.Other },
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
            Eq(S(DownloadChecks.For("StopModules", true, "")), "StopAll", "stop yes");
            Eq(S(DownloadChecks.For("StopModules", false, "")), null, "stop no");
            Eq(S(DownloadChecks.For("StartModules", true, "")), "StartModule", "start yes");
            Eq(S(DownloadChecks.For("StartModules", false, "")), "NoAction", "start no");
            Eq(S(DownloadChecks.For("OverwriteSystemData", true, "")), "Overwrite", "overwrite yes");
            Eq(S(DownloadChecks.For("OverwriteSystemData", false, "")), null, "overwrite no");
            Eq(S(DownloadChecks.For("ConsistentBlocksDownload", true, "")), "ConsistentDownload", "consistent download yes");
            Eq(S(DownloadChecks.For("ConsistentBlocksDownload", false, "")), null, "consistent download no");
            Eq(S(DownloadChecks.For("AlarmTextLibrariesDownload", true, "")), "ConsistentDownload", "alarm texts yes");
            Eq(S(DownloadChecks.For("AlarmTextLibrariesDownload", false, "")), null, "alarm texts no never writes NoAction");
            Eq(S(DownloadChecks.For("AllBlocksDownload", true, "")), "DownloadAllBlocks", "all blocks yes");
            Eq(S(DownloadChecks.For("AllBlocksDownload", false, "")), null, "all blocks no");
            Eq(S(DownloadChecks.For("UserManagementDownload", false, "")), null, "user management no");
            Eq(S(DownloadChecks.For("LoadIdentificationData", true, "")), "LoadNothing", "identification data yes loads nothing");
            Eq(S(DownloadChecks.For("LoadIdentificationData", false, "")), null, "identification data no");
            Eq(S(DownloadChecks.For("DataBlockReinitialization", true, "")), "StopPlcAndReinitialize", "reinit yes");
            Eq(S(DownloadChecks.For("DataBlockReinitializationOrKeepActualValues", true, "")), "StopPlcAndReinitialize", "reinit-or-keep yes");
            Eq(S(DownloadChecks.For("DataBlockReinitialization", false, "")), null, "reinit no");
            Eq(S(DownloadChecks.For("ModuleWriteAccessPassword", true, "")), Setting.Password, "password yes");
            Eq(S(DownloadChecks.For("ModuleReadAccessPassword", true, "")), Setting.Password, "read password yes");
            Eq(S(DownloadChecks.For("ModuleWriteAccessPassword", false, "")), null, "password no");
            Eq(S(DownloadChecks.For("PlcMasterSecretPassword", true, "")), Setting.ConfigurationPassword, "configuration password yes");
            Eq(S(DownloadChecks.For("PlcMasterSecretPassword", false, "")), null, "configuration password no");
            Eq(S(DownloadChecks.For("TlsVerificationConfiguration", true, "")), "Trusted", "certificate yes");
            Eq(S(DownloadChecks.For("TlsVerificationConfiguration", false, "")), null, "certificate no");
            Eq(S(DownloadChecks.For("ResetModule", true, "")), null, "an unknown configuration, yes");
            Eq(S(DownloadChecks.For("ResetModule", false, "")), null, "an unknown configuration, no");
            Eq(DownloadChecks.For("StopModules", true, "").Kind, Setting.Select, "a selection's kind");
        }

        // PasswordsNeverCross: each password setting writes its own secret,
        // never the other's, even when only the other is set.
        static void PasswordsNeverCross()
        {
            var both = new Secrets { Protection = "protection-pw", ConfigurationPassword = "configuration-pw" };
            var onlyProtection = new Secrets { Protection = "protection-pw" };
            var onlyConfiguration = new Secrets { ConfigurationPassword = "configuration-pw" };
            Setting module = DownloadChecks.For("ModuleWriteAccessPassword", true, "");
            Setting master = DownloadChecks.For("PlcMasterSecretPassword", true, "");
            Eq(Setting.Secret(module, both), "protection-pw", "module password, both set");
            Eq(Setting.Secret(master, both), "configuration-pw", "master secret, both set");
            Eq(Setting.Secret(module, onlyConfiguration), "", "module password never takes the configuration password");
            Eq(Setting.Secret(master, onlyProtection), "", "master secret never takes the protection password");
            Eq(Setting.Secret(DownloadChecks.For("StopModules", true, ""), both), null, "a selection has no secret");
            // The Full access user's password answers no download question:
            // no setting takes it. UserManagementDownload is answered only by
            // keeping or resetting the CPU's users, a selection, never a password.
            var onlyFull = new Secrets { FullAccessPassword = "full-access-pw" };
            foreach (string type in DownloadChecks.Answered)
            {
                Setting st = DownloadChecks.For(type, true, "");
                if (st == null) continue;
                string got = Setting.Secret(st, onlyFull);
                True(got == null || got == "", type + " never takes the Full access password");
            }
            Eq(DownloadChecks.QuestionId("UserManagementDownload"), QuestionIds.UserManagement, "user management is its own question");
            Eq(S(DownloadChecks.For("UserManagementDownload", true, Answer.ChoiceKeep)), "KeepOnlineUserManagementData", "user management keep");
            Eq(S(DownloadChecks.For("UserManagementDownload", true, Answer.ChoiceReset)), "DownloadAllUserManagementDataResetToProject", "user management reset, as V21 names it");
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

        static List<string> Ids(List<Question> qs)
        {
            var l = new List<string>();
            foreach (var q in qs) l.Add(q.Id);
            return l;
        }

        // UserManagementGate: #1159. Keeping the CPU's online users answers
        // user-management only when access control was kept; every
        // software-load check is answered only by its own decision.
        static void UserManagementGate()
        {
            var keep = new Decisions { UserManagementKeepOnline = true };
            var um = new List<Question> { new Question { Id = QuestionIds.UserManagement } };
            var u = new List<Question>(); var d = new List<Question>();
            DownloadRule.Resolve(um, keep, false, false, true, null, u, d);
            Eq(u.Count + d.Count, 0, "keep with access control kept is answered");
            u.Clear(); d.Clear();
            DownloadRule.Resolve(um, keep, false, false, false, null, u, d);
            Eq(string.Join(",", Ids(u).ToArray()), QuestionIds.UserManagement, "keep with access control set is unanswered");
            Contains(u[0].Message, "a keep spec on a session that never set access control", "and names why");
            u.Clear(); d.Clear();
            DownloadRule.Resolve(um, new Decisions { UserManagementKeepOnline = false }, false, false, true, null, u, d);
            Eq(string.Join(",", Ids(d).ToArray()), QuestionIds.UserManagement, "no is declined");
            var others = new Decisions { StopCpu = true, Overwrite = true, ConsistentDownload = true, StartAfter = true };
            foreach (string id in new[] { QuestionIds.AlarmTexts, QuestionIds.AllBlocks, QuestionIds.UserManagement, QuestionIds.IdentificationData })
            {
                u.Clear(); d.Clear();
                DownloadRule.Resolve(new List<Question> { new Question { Id = id } }, others, true, true, true, null, u, d);
                Eq(string.Join(",", Ids(u).ToArray()), id, id + " is not answered by the other decisions");
            }
        }

        // ResetRequest is a request every conjunct of UserManagementRule
        // lets reset by the upload route.
        static DownloadRequest ResetRequest()
        {
            var r = new DownloadRequest { Address = "192.0.2.10", Hardware = true, Software = true, Announced = true };
            r.Decisions.UserManagementResetToProject = true;
            r.Attestation = new Attestation { By = "op", Text = "as found" };
            return r;
        }

        // UserManagementResetRule: #789 stage 4. From a request every
        // conjunct lets, each one flipped on its own refuses, and only
        // those (a security property: each is mutation-checked).
        static void UserManagementResetRule()
        {
            int reads = 0;
            string project = "Disabled";
            Func<string> read = delegate
            {
                reads++;
                return project;
            };
            const string addr = "192.0.2.10";
            UserManagementRule.Gate g = UserManagementRule.Check(ResetRequest(), true, addr, true, read);
            True(g.Allowed, "the baseline is let: " + g.Why);
            Eq(g.Route, UserManagementRule.RouteUpload, "by the upload route");
            Eq(reads, 1, "the project is read once");

            DownloadRequest r = ResetRequest();
            r.Decisions.UserManagementResetToProject = null;
            g = UserManagementRule.Check(r, true, addr, true, read);
            True(!g.Allowed, "not asked");
            r.Decisions.UserManagementResetToProject = false;
            True(!UserManagementRule.Check(r, true, addr, true, read).Allowed, "asked no");

            reads = 0;
            r = ResetRequest();
            r.Hardware = false;
            g = UserManagementRule.Check(r, true, addr, true, read);
            True(!g.Allowed, "a software-only download");
            Contains(g.Why, "loads no hardware", "names why");
            Contains(g.Why, "with attestation.cpuAccessControl \"disabled\" or an upload from this CPU in this session (to a Disabled project), or \"enabled\" with tiad logged on as \"nautilus\" in the download", "and every route");

            g = UserManagementRule.Check(ResetRequest(), false, addr, true, read);
            True(!g.Allowed, "access control not kept");
            Contains(g.Why, "does not keep access control", "names why");
            Eq(reads, 0, "the cheap conjuncts refuse before the project is read");

            // An attestation that says enabled takes the logon route, the
            // upload notwithstanding: it needs the password to log on.
            r = ResetRequest();
            r.Attestation.CpuAccessControl = Attestation.CpuAccessEnabled;
            g = UserManagementRule.Check(r, true, addr, true, read);
            True(!g.Allowed, "an attestation that says enabled, without a password");
            Eq(g.Why, "decisions.userManagementResetToProject (delete the CPU's user management data and set its access control as the project has it) is refused: " + UserManagementRule.NoPassword, "names why");
            r.Secrets.FullAccessPassword = "Bench-pass-2026";
            g = UserManagementRule.Check(r, true, addr, true, read);
            True(g.Allowed && g.Route == UserManagementRule.RouteLogon, "attested enabled with a password: the logon route, over the upload's");

            foreach (string st in new[] { AccessRule.Enabled, AccessRule.Absent, "disabled", "Disabled ", "EnabledWithAccessControlViaAccessLevel", null })
            {
                project = st;
                g = UserManagementRule.Check(ResetRequest(), true, addr, true, read);
                True(!g.Allowed, "project access control " + Show(st) + " refuses");
                Contains(g.Why, "not Disabled", "names why for " + Show(st));
            }
            project = "Disabled";
            g = UserManagementRule.Check(ResetRequest(), true, addr, true, null);
            True(!g.Allowed, "no reader refuses");
            g = UserManagementRule.Check(ResetRequest(), true, addr, true, delegate { throw new TiadException(500, Kinds.OperationFailed, "TIA said no"); });
            True(!g.Allowed, "a read that fails refuses");
            Contains(g.Why, "TIA said no", "and says why");
            bool fatal = false;
            try
            {
                UserManagementRule.Check(ResetRequest(), true, addr, true, delegate { throw new TiadException(500, Kinds.OperationFailed, "TIA died", null, true); });
            }
            catch (TiadException e)
            {
                fatal = e.Fatal;
            }
            True(fatal, "a fatal read is not swallowed");

            // The routes: no upload from this address and no attestation
            // refuses; the attestation saying disabled lets it, only with
            // who and what.
            foreach (string up in new[] { "", "192.0.2.11" })
            {
                g = UserManagementRule.Check(ResetRequest(), true, up, true, read);
                True(!g.Allowed, "uploaded from " + Show(up) + ", no attestation");
                Contains(g.Why, "nothing shows the CPU's access control as it is now: upload from 192.0.2.10 in this session first, or give attestation.cpuAccessControl \"disabled\" or \"enabled\"", "names every route");
                r = ResetRequest();
                r.Attestation.CpuAccessControl = Attestation.CpuAccessDisabled;
                g = UserManagementRule.Check(r, true, up, true, read);
                True(g.Allowed, "attested disabled, uploaded from " + Show(up));
                Eq(g.Route, UserManagementRule.RouteAttestation, "by the attestation route");
                r.Attestation.By = " ";
                True(!UserManagementRule.Check(r, true, up, true, read).Allowed, "attested disabled without who");
                r.Attestation.By = "op";
                r.Attestation.Text = "";
                True(!UserManagementRule.Check(r, true, up, true, read).Allowed, "attested disabled without what");
            }
            // Both routes: the upload is named.
            r = ResetRequest();
            r.Attestation.CpuAccessControl = Attestation.CpuAccessDisabled;
            Eq(UserManagementRule.Check(r, true, addr, true, read).Route, UserManagementRule.RouteUpload, "both routes");
            // A gate nobody filled in refuses.
            True(!new UserManagementRule.Gate().Allowed, "the zero gate refuses");
            // With the upload route off (UploadRouteEnabled), an upload
            // from this very CPU shows nothing: only the attestation lets
            // a reset, and the refusal says why.
            g = UserManagementRule.Check(ResetRequest(), true, addr, false, read);
            True(!g.Allowed, "upload route off");
            Contains(g.Why, "the upload route is not yet available", "says so");
            r = ResetRequest();
            r.Hardware = false;
            Contains(UserManagementRule.Check(r, true, addr, false, read).Why, "the upload route is not yet available", "every refusal says so");
            r = ResetRequest();
            r.Attestation.CpuAccessControl = Attestation.CpuAccessDisabled;
            g = UserManagementRule.Check(r, true, addr, false, read);
            True(g.Allowed && g.Route == UserManagementRule.RouteAttestation, "upload route off, attested disabled");
            // The conjuncts flipped one at a time from the baseline
            // production runs: the upload route off, the operator attesting
            // disabled, no upload.
            Func<DownloadRequest> att = delegate
            {
                DownloadRequest b = ResetRequest();
                b.Attestation.CpuAccessControl = Attestation.CpuAccessDisabled;
                return b;
            };
            project = "Disabled";
            g = UserManagementRule.Check(att(), true, "", false, read);
            True(g.Allowed && g.Route == UserManagementRule.RouteAttestation, "the attestation baseline: " + g.Why);
            r = att(); r.Hardware = false;
            True(!UserManagementRule.Check(r, true, "", false, read).Allowed, "attestation route, software only");
            True(!UserManagementRule.Check(att(), false, "", false, read).Allowed, "attestation route, access control set");
            True(!UserManagementRule.Check(att(), true, "", false, delegate { return AccessRule.Enabled; }).Allowed, "attestation route, project enabled");
            True(!UserManagementRule.Check(att(), true, "", false, delegate { return null; }).Allowed, "attestation route, project unreadable");
            r = att(); r.Attestation.By = "";
            True(!UserManagementRule.Check(r, true, "", false, read).Allowed, "attestation route, no who");
            r = att(); r.Attestation.Text = " ";
            True(!UserManagementRule.Check(r, true, "", false, read).Allowed, "attestation route, no what");
            r = att(); r.Attestation.CpuAccessControl = Attestation.CpuAccessEnabled;
            True(!UserManagementRule.Check(r, true, "", false, read).Allowed, "attestation route, attested enabled");
            r = att(); r.Attestation.CpuAccessControl = "";
            True(!UserManagementRule.Check(r, true, addr, false, read).Allowed, "attestation route, attested nothing, uploaded here");
            r = att(); r.Decisions.UserManagementResetToProject = null;
            True(!UserManagementRule.Check(r, true, "", false, read).Allowed, "attestation route, reset not asked");
            // Off until a bench upload of a CPU with access control on
            // reads back as not Disabled (naut-lead 2026-11-21).
            True(!UserManagementRule.UploadRouteEnabled, "UploadRouteEnabled is on: it needs a bench upload of a CPU with access control on that reads back as not Disabled");
        }

        // ResetFacts are a session's facts for the reset rule: keep (spec
        // keep, never set) or tiad's user (fullAccessUser set on the
        // session), the project's access control and whether its record
        // is of the request's password, each read counted.
        sealed class ResetFacts
        {
            public string Project = "Disabled";
            public bool? Recorded = true;
            public int ProjectReads, RecordReads;

            public UserManagementRule.Facts Make(bool keep, bool set)
            {
                return new UserManagementRule.Facts
                {
                    AccessControlKept = keep,
                    FullAccessUserSet = set,
                    ProjectAccessControl = delegate { ProjectReads++; return Project; },
                    PasswordRecorded = delegate { RecordReads++; return Recorded; },
                };
            }
        }

        static DownloadRequest Attested(string cpu, string password)
        {
            DownloadRequest r = ResetRequest();
            r.Attestation.CpuAccessControl = cpu;
            r.Secrets.FullAccessPassword = password;
            return r;
        }

        // UserManagementResetMatrix: the closed set of resets the rule lets
        // (UserManagementRule's table, #789 option 1), every case let by
        // its own route, and each conjunct flipped on its own refuses.
        static void UserManagementResetMatrix()
        {
            const string pw = "Bench-pass-2026";
            string pre = "decisions.userManagementResetToProject (delete the CPU's user management data and set its access control as the project has it) is refused: ";
            // The four cases: keep -> Disabled, attested disabled
            // (attestation) or enabled (logon, the restore); tiad's user
            // -> on, attested disabled (turning it on) or enabled (logon,
            // a reload).
            foreach (bool keep in new[] { true, false })
            {
                foreach (string cpu in new[] { Attestation.CpuAccessDisabled, Attestation.CpuAccessEnabled })
                {
                    string name = (keep ? "keep" : "tiad's user") + ", attested " + cpu;
                    var f = new ResetFacts { Project = keep ? "Disabled" : AccessRule.Enabled };
                    UserManagementRule.Gate g = UserManagementRule.Check(Attested(cpu, pw), f.Make(keep, !keep));
                    True(g.Allowed, name + " is let: " + g.Why);
                    Eq(g.Route, cpu == Attestation.CpuAccessEnabled ? UserManagementRule.RouteLogon : UserManagementRule.RouteAttestation, name + ": its route");
                    Eq(f.ProjectReads, 1, name + ": the project read once");
                    Eq(f.RecordReads, keep ? 0 : 1, name + ": the record read only for tiad's user");
                    // The project the other kind's: refused.
                    var other = new ResetFacts { Project = keep ? AccessRule.Enabled : "Disabled" };
                    g = UserManagementRule.Check(Attested(cpu, pw), other.Make(keep, !keep));
                    True(!g.Allowed, name + ": project " + other.Project + " refuses");
                    Contains(g.Why, "not " + (keep ? "Disabled" : AccessRule.Enabled), name + ": names why");
                    foreach (string st in new[] { "EnabledWithAccessControlViaAccessLevel", AccessRule.Absent, null, "enabledwithaccesscontrol" })
                    {
                        var odd = new ResetFacts { Project = st };
                        True(!UserManagementRule.Check(Attested(cpu, pw), odd.Make(keep, !keep)).Allowed, name + ": project " + Show(st) + " refuses");
                    }
                    // Without who or what: refused.
                    DownloadRequest r = Attested(cpu, pw);
                    r.Attestation.By = "";
                    True(!UserManagementRule.Check(r, new ResetFacts { Project = f.Project }.Make(keep, !keep)).Allowed, name + ": no who");
                    r = Attested(cpu, pw);
                    r.Attestation.Text = " ";
                    True(!UserManagementRule.Check(r, new ResetFacts { Project = f.Project }.Make(keep, !keep)).Allowed, name + ": no what");
                    // A software load: refused.
                    r = Attested(cpu, pw);
                    r.Hardware = false;
                    True(!UserManagementRule.Check(r, new ResetFacts { Project = f.Project }.Make(keep, !keep)).Allowed, name + ": software only");
                }
            }
            // Neither keep nor tiad's user (keep after fullAccessUser on the
            // same session, #1159): refused before any read.
            var n = new ResetFacts();
            UserManagementRule.Gate ng = UserManagementRule.Check(Attested(Attestation.CpuAccessEnabled, pw), n.Make(false, false));
            True(!ng.Allowed, "neither keep nor tiad's user");
            Contains(ng.Why, "does not keep access control as found", "names why");
            Eq(n.ProjectReads + n.RecordReads, 0, "before any read");
            // Nothing attested: refused, naming both words.
            foreach (bool keep in new[] { true, false })
            {
                var f = new ResetFacts { Project = keep ? "Disabled" : AccessRule.Enabled };
                UserManagementRule.Gate g = UserManagementRule.Check(Attested("", pw), f.Make(keep, !keep));
                True(!g.Allowed, "nothing attested");
                Contains(g.Why, "give attestation.cpuAccessControl \"disabled\" or \"enabled\"", "names both words");
            }
            // The logon route without a password: refused before any read.
            foreach (bool keep in new[] { true, false })
            {
                var f = new ResetFacts { Project = keep ? "Disabled" : AccessRule.Enabled };
                UserManagementRule.Gate g = UserManagementRule.Check(Attested(Attestation.CpuAccessEnabled, ""), f.Make(keep, !keep));
                Eq(g.Why, pre + UserManagementRule.NoPassword, "the logon route without a password");
                Eq(f.ProjectReads, 0, "before the project is read");
            }
            // tiad's user: the record must be of this password.
            foreach (string cpu in new[] { Attestation.CpuAccessDisabled, Attestation.CpuAccessEnabled })
            {
                var f = new ResetFacts { Project = AccessRule.Enabled, Recorded = false };
                UserManagementRule.Gate g = UserManagementRule.Check(Attested(cpu, pw), f.Make(false, true));
                Eq(g.Why, pre + UserManagementRule.PasswordNotRecorded, "attested " + cpu + ": another password");
                f = new ResetFacts { Project = AccessRule.Enabled, Recorded = null };
                g = UserManagementRule.Check(Attested(cpu, pw), f.Make(false, true));
                True(!g.Allowed, "attested " + cpu + ": the record unreadable");
                Contains(g.Why, "could not read the project's record", "names why");
                var facts = new ResetFacts { Project = AccessRule.Enabled }.Make(false, true);
                facts.PasswordRecorded = null;
                True(!UserManagementRule.Check(Attested(cpu, pw), facts).Allowed, "attested " + cpu + ": no record reader");
                facts.PasswordRecorded = delegate { throw new TiadException(500, Kinds.OperationFailed, "TIA said no"); };
                g = UserManagementRule.Check(Attested(cpu, pw), facts);
                True(!g.Allowed, "attested " + cpu + ": the record read fails");
                Contains(g.Why, "TIA said no", "and says why");
                bool fatal = false;
                facts.PasswordRecorded = delegate { throw new TiadException(500, Kinds.OperationFailed, "TIA died", null, true); };
                try
                {
                    UserManagementRule.Check(Attested(cpu, pw), facts);
                }
                catch (TiadException e)
                {
                    fatal = e.Fatal;
                }
                True(fatal, "a fatal record read is not swallowed");
            }
            // A session that applied fullAccessRecordOnly: refused for tiad's
            // user, before any read; keep is not concerned.
            foreach (string cpu in new[] { Attestation.CpuAccessDisabled, Attestation.CpuAccessEnabled })
            {
                var ro = new ResetFacts { Project = AccessRule.Enabled };
                UserManagementRule.Facts rf = ro.Make(false, true);
                rf.RecordOnly = true;
                Eq(UserManagementRule.Check(Attested(cpu, pw), rf).Why, pre + UserManagementRule.RecordOnlyRefused, "attested " + cpu + ": record only");
                Eq(ro.ProjectReads + ro.RecordReads, 0, "before any read");
                var rk = new ResetFacts();
                UserManagementRule.Facts kf = rk.Make(true, false);
                kf.RecordOnly = true;
                True(UserManagementRule.Check(Attested(cpu, pw), kf).Allowed, "attested " + cpu + ": keep, record only elsewhere");
            }
            // The upload route never lets a reset to tiad's user.
            var up = new ResetFacts { Project = AccessRule.Enabled }.Make(false, true);
            up.UploadRoute = true;
            up.UploadedFrom = "192.0.2.10";
            True(!UserManagementRule.Check(Attested("", pw), up).Allowed, "the upload route, tiad's user");
        }

        // UserManagementResetAt: the verdict when TIA asks. The logon route
        // needs the download logged on; the others a CPU that asked no
        // credentials; a refused gate stays refused.
        static void UserManagementResetAt()
        {
            string pre = "decisions.userManagementResetToProject (delete the CPU's user management data and set its access control as the project has it) is refused: ";
            var logon = new UserManagementRule.Gate { Allowed = true, Route = UserManagementRule.RouteLogon };
            True(logon.At(true, true).Allowed && logon.At(true, true).Route == UserManagementRule.RouteLogon, "logon, logged on");
            foreach (bool asked in new[] { false, true })
            {
                UserManagementRule.Gate g = logon.At(false, asked);
                True(!g.Allowed, "logon, not logged on, credentials asked " + asked);
                Eq(g.Why, pre + UserManagementRule.NoLogon, "names why");
                Contains(g.Why, "first, then download again", "and the fix");
                // The plan's old fix (close every session to retire the
                // instance) retires only an instance that trusted blindly.
                True(!g.Why.Contains("close every session"), "no fix that does nothing on an instance with no blind trust");
            }
            // The instance-logon route: the instance's evidence stands in
            // for the download's logon only when the download asked no
            // credentials (and refused nothing) before the check.
            UserManagementRule.Gate inst = logon.At(false, false, true);
            True(inst.Allowed && inst.Route == UserManagementRule.RouteInstanceLogon, "logon, not logged on, nothing asked, instance evidence: instance-logon");
            UserManagementRule.Gate contra = logon.At(false, true, true);
            True(!contra.Allowed, "instance evidence, but the download asked credentials and did not log on");
            Eq(contra.Why, pre + UserManagementRule.NoLogon, "names why");
            True(logon.At(true, true, true).Route == UserManagementRule.RouteLogon, "the download's own logon comes first");
            True(!logon.At(false, false, false).Allowed, "no evidence, nothing asked: refused");
            foreach (string route in new[] { UserManagementRule.RouteAttestation, UserManagementRule.RouteUpload })
            {
                var off = new UserManagementRule.Gate { Allowed = true, Route = route };
                True(off.At(false, false, true).Route == route, route + ": instance evidence changes nothing");
                True(!off.At(true, true, true).Allowed, route + ": instance evidence never lets a contradicted off route");
            }
            True(!new UserManagementRule.Gate { Why = "no" }.At(false, false, true).Allowed, "a refused gate stays refused, instance evidence too");
            // A logon earlier in the instance refuses the off routes: TIA
            // may ride on it and ask nothing.
            foreach (string route in new[] { UserManagementRule.RouteAttestation, UserManagementRule.RouteUpload })
            {
                var off = new UserManagementRule.Gate { Allowed = true, Route = route };
                UserManagementRule.Gate g = off.At(false, false, false, true);
                True(!g.Allowed, route + ": logged on earlier in the instance");
                Eq(g.Why, pre + UserManagementRule.LoggedOnBefore(route), "names why");
                True(off.At(false, false, false, false).Allowed, route + ": not logged on earlier");
            }
            True(logon.At(true, true, false, true).Allowed && logon.At(false, false, true, true).Route == UserManagementRule.RouteInstanceLogon, "the logon routes do not care");
            foreach (string route in new[] { UserManagementRule.RouteAttestation, UserManagementRule.RouteUpload })
            {
                var off = new UserManagementRule.Gate { Allowed = true, Route = route };
                True(off.At(false, false).Allowed && off.At(false, false).Route == route, route + ", no credentials asked");
                // Asked and answered, or asked and refused (TIA may go on
                // after a no): either shows the CPU's access control on.
                foreach (bool loggedOn in new[] { true, false })
                {
                    UserManagementRule.Gate g = off.At(loggedOn, true);
                    True(!g.Allowed, route + ", the CPU asked credentials, logged on " + loggedOn);
                    Contains(g.Why, "so its access control is on, but the " + route + " route rests on it being off", "names why");
                }
            }
            var refused = new UserManagementRule.Gate { Why = "no" };
            True(!refused.At(true, true).Allowed && !refused.At(false, false).Allowed, "a refused gate stays refused");
            True(!new UserManagementRule.Gate { Route = UserManagementRule.RouteLogon }.At(true, true).Allowed, "a gate not let stays refused, logged on too");
        }

        // InstanceLogonsHold: the evidence holds only for the instance,
        // address and password it was recorded with, and goes when
        // forgotten; a credentials question answered no runs the login's
        // Lost.
        static void InstanceLogonsHold()
        {
            var l = new InstanceLogons();
            object a = new object(), b = new object();
            True(!l.Holds(a, "192.0.2.10", "Full-access-1"), "nothing recorded");
            l.Record(a, "192.0.2.10", "Full-access-1", "compare", "s1");
            True(l.Holds(a, "192.0.2.10", "Full-access-1"), "same instance, address, password");
            True(!l.Holds(b, "192.0.2.10", "Full-access-1"), "another instance");
            True(!l.Holds(a, "192.0.2.11", "Full-access-1"), "another address");
            True(!l.Holds(a, "192.0.2.10", "Full-access-2"), "another password");
            True(!l.Holds(a, "192.0.2.10", "Full-access-1x"), "a longer password");
            True(!l.Holds(a, "192.0.2.10", ""), "no password");
            True(!l.Describe("192.0.2.10").Contains("Full-access-1"), "the description holds no password");
            l.Record(a, "192.0.2.11", "Full-access-1", "compare", "s1");
            l.Forget("192.0.2.10", "test");
            True(!l.Holds(a, "192.0.2.10", "Full-access-1"), "forgotten");
            True(l.Holds(a, "192.0.2.11", "Full-access-1"), "only that address");
            l.ForgetAll("test");
            True(!l.Holds(a, "192.0.2.11", "Full-access-1"), "all forgotten");
            l.Record(null, "192.0.2.10", "Full-access-1", "compare", "s1");
            True(!l.Holds(null, "192.0.2.10", "Full-access-1"), "no instance, no record");
            var offered = new List<string> { "AnonymousUser", LoginUse.ProjectUser };
            int lost = 0;
            var u = new LoginUse(OnlineLogin.For("Full-access-1", null, delegate { lost++; }, null), "compare", "192.0.2.10");
            Eq(u.Decide(true, offered), null, "answered");
            Eq(lost, 0, "an answer loses nothing");
            True(u.Decide(true, offered) != null, "asked again, refused");
            Eq(lost, 1, "asked again: lost");
            var v = new LoginUse(OnlineLogin.For("Full-access-1", null, delegate { lost++; }, null), "compare", "192.0.2.10");
            True(v.Decide(false, offered) != null, "not secure, refused");
            Eq(lost, 2, "refused: lost");
            int took = 0;
            var w = new LoginUse(OnlineLogin.For("Full-access-1", null, null, delegate { took++; }), "compare", "192.0.2.10");
            Eq(w.Decide(true, offered), null, "answered");
            Eq(took, 1, "an answer runs Took");
            True(w.Decide(true, offered) != null && took == 1, "a refusal does not");
            // MarkAnswered: the instance logged on to the address; a record
            // of another password, or from another instance, goes.
            var m = new InstanceLogons();
            True(!m.AnsweredBefore(a, "192.0.2.10"), "nothing answered");
            m.Record(a, "192.0.2.10", "Full-access-1", "compare", "s1");
            m.MarkAnswered(a, "192.0.2.10", "Full-access-1");
            True(m.AnsweredBefore(a, "192.0.2.10") && !m.AnsweredBefore(b, "192.0.2.10") && !m.AnsweredBefore(a, "192.0.2.11"), "answered, in that instance, for that address");
            True(m.Holds(a, "192.0.2.10", "Full-access-1"), "the same password keeps the record");
            m.MarkAnswered(a, "192.0.2.10", "Full-access-2");
            True(!m.Holds(a, "192.0.2.10", "Full-access-1"), "another password takes it away");
            m.Record(a, "192.0.2.10", "Full-access-1", "compare", "s1");
            m.ForgetAll("blind trust");
            True(m.AnsweredBefore(a, "192.0.2.10"), "a blind trust forgets the evidence, not that tiad logged on");
            m.NewInstance("retired");
            True(!m.AnsweredBefore(a, "192.0.2.10"), "a new instance holds no logon");
        }

        // LoginLoggedOn: logged on once credentials are answered, and no
        // longer once anything is answered no.
        static void LoginLoggedOn()
        {
            var offered = new List<string> { "AnonymousUser", LoginUse.ProjectUser };
            var u = new LoginUse(OnlineLogin.For("Full-access-1", null), "download", "192.0.2.10");
            True(!u.LoggedOn && !u.MayHaveAskedCredentials, "nothing asked");
            Eq(u.Decide(true, offered), null, "answered");
            True(u.LoggedOn && u.MayHaveAskedCredentials, "logged on");
            True(u.Decide(true, offered) != null, "asked again, refused");
            u.Refused();
            True(!u.LoggedOn, "a refusal since: no longer logged on");
            var v = new LoginUse(OnlineLogin.For("Full-access-1", null), "download", "192.0.2.10");
            v.Refused();
            True(!v.LoggedOn, "refused, never answered");
            var w = new LoginUse(null, "download", "192.0.2.10");
            True(w.Decide(true, offered) != null && !w.LoggedOn, "no password: never logged on");
            True(w.MayHaveAskedCredentials, "but asked");
            var x = new LoginUse(OnlineLogin.For("Full-access-1", null), "download", "192.0.2.10");
            x.Refused();
            True(x.MayHaveAskedCredentials, "any question refused: a credentials question may have been refused before Decide");
        }

        // UserManagementKeepOrReset: the keep x reset table, through
        // Resolve, with the rule's verdict let, refused and never made.
        static void UserManagementKeepOrReset()
        {
            var um = new List<Question> { new Question { Id = QuestionIds.UserManagement, Message = "m" } };
            var let = new UserManagementRule.Gate { Allowed = true, Route = UserManagementRule.RouteUpload };
            var refused = new UserManagementRule.Gate { Why = "refused because" };
            bool?[] vals = { null, false, true };
            foreach (bool? keep in vals)
            {
                foreach (bool? reset in vals)
                {
                    foreach (var gate in new[] { let, refused, null })
                    {
                        var d = new Decisions { UserManagementKeepOnline = keep, UserManagementResetToProject = reset };
                        var u = new List<Question>(); var dd = new List<Question>();
                        DownloadRule.Resolve(um, d, false, false, true, gate, u, dd);
                        string want;
                        if (keep == true && reset == true) want = "unanswered";
                        else if (reset == true) want = gate == let ? "answered" : "unanswered";
                        else if (keep == true) want = "answered";
                        else if (keep == false || reset == false) want = "declined";
                        else want = "unanswered";
                        string got = u.Count > 0 ? "unanswered" : dd.Count > 0 ? "declined" : "answered";
                        string what = "keep=" + Show(keep) + " reset=" + Show(reset) + " gate=" + (gate == let ? "let" : gate == refused ? "refused" : "null");
                        Eq(got, want, what);
                        if (got == "unanswered" && reset == true && keep != true && gate == refused) Contains(u[0].Message, "refused because", what + " names the rule's why");
                        if (got == "answered")
                        {
                            Answer a = DownloadRule.Answers(um, d)[0];
                            Eq(a.Choice, reset == true ? Answer.ChoiceReset : Answer.ChoiceKeep, what + " choice");
                        }
                    }
                }
            }
            var bad = new DownloadRequest { Address = "192.0.2.10", Software = true, Announced = true };
            bad.Decisions.UserManagementKeepOnline = true;
            bad.Decisions.UserManagementResetToProject = true;
            Contains(bad.Validate(), "both yes", "keep and reset both yes is a bad request");
            bad.Decisions.UserManagementKeepOnline = false;
            Eq(bad.Validate(), null, "keep no, reset yes is a request");
        }

        // UserManagementChoices: what each choice writes, and that acting
        // follows what is written.
        static void UserManagementChoices()
        {
            Eq(S(DownloadChecks.For("UserManagementDownload", true, "")), null, "user management yes without a choice cannot be applied");
            Eq(S(DownloadChecks.For("UserManagementDownload", true, "update")), null, "an unknown choice cannot be applied");
            Eq(S(DownloadChecks.For("UserManagementDownload", false, Answer.ChoiceReset)), null, "no with a choice is still no");
            Eq(S(DownloadChecks.For("StopModules", true, Answer.ChoiceKeep)), null, "a choice on another check cannot be applied");
            True(!DownloadChecks.ActingAnswer(QuestionIds.UserManagement, true, Answer.ChoiceKeep), "keep is passive");
            True(DownloadChecks.ActingAnswer(QuestionIds.UserManagement, true, Answer.ChoiceReset), "reset acts");
            True(!DownloadChecks.ActingAnswer(QuestionIds.UserManagement, true, ""), "no choice writes nothing");
            True(!DownloadChecks.ActingAnswer(QuestionIds.StartAfter, false, ""), "start-after no is passive");
            True(DownloadChecks.ActingAnswer(QuestionIds.StartAfter, true, ""), "start-after yes acts");
            True(DownloadChecks.ActingAnswer(QuestionIds.StopCpu, true, ""), "stop acts");
            True(!DownloadChecks.ActingAnswer(QuestionIds.Password, true, ""), "a password is passive");
            foreach (string choice in new[] { Answer.ChoiceKeep, Answer.ChoiceReset })
            {
                string c = choice;
                var r = new Run(delegate(Question q) { return new Answer { Question = q, Yes = true, Choice = c }; });
                string wrote = N(r, new object(), "UserManagementDownload", "Delete user management data and disable access control in runtime");
                Eq(wrote, c == Answer.ChoiceKeep ? "KeepOnlineUserManagementData" : "DownloadAllUserManagementDataResetToProject", c + " writes");
                Eq(string.Join("; ", r.D.Acting.ToArray()), c == Answer.ChoiceKeep ? "" : "UserManagementDownload = DownloadAllUserManagementDataResetToProject", c + " acting");
            }
            // The decider cancels an answer with no choice rather than
            // leave TIA's preselection (reset) standing.
            var none = new Run(AllYes);
            bool cancelled = false;
            try
            {
                none.D.Next(new object(), "UserManagementDownload", "m", null);
            }
            catch (DownloadCancel e)
            {
                cancelled = true;
                Contains(e.Message, "cannot apply yes", "says why");
            }
            True(cancelled, "user management yes without a choice cancels");
            Contains(DownloadChecks.Consequence(new List<string> { QuestionIds.UserManagement }), "reset the CPU's user management to the project's: check its users and access control", "a reset given before a cancel");
        }

        // AttestationAccess: the attestation's cpuAccessControl is one of
        // two words, journalled as a fixed word only, and echoed.
        static void AttestationAccess()
        {
            var r = new DownloadRequest { Address = "192.0.2.10", Software = true, Announced = true };
            foreach (string ok in new[] { "", "disabled", "enabled" })
            {
                r.Attestation.CpuAccessControl = ok;
                Eq(r.Validate(), null, Show(ok) + " is valid");
            }
            foreach (string bad in new[] { "Disabled", "off", "disabled ", "x\naudit download forged" })
            {
                r.Attestation.CpuAccessControl = bad;
                Eq(r.Validate(), "download: attestation.cpuAccessControl is not one of disabled, enabled", Show(bad) + " is refused");
                Contains(r.Attestation.Audit(), "cpuAccessControl=invalid}", Show(bad) + " is journalled as invalid");
                True(r.Attestation.Audit().IndexOf("forged", StringComparison.Ordinal) < 0, "never its text");
            }
            var a = new Attestation { CpuAccessControl = "disabled" };
            Eq(a.Audit(), "{by=\"\" at=unset text=\"\" cpuAccessControl=disabled}", "a lone cpuAccessControl is journalled");
            Eq(new Attestation().Audit(), "unset", "nothing said");
            Eq(((Obj)a.ToJson())["cpuAccessControl"], "disabled", "echoed");
            True(!new Attestation { By = "b", Text = "t" }.ToJson().ContainsKey("cpuAccessControl"), "unset is not echoed");
        }

        static void ActingAndPassive()
        {
            foreach (string id in new[] { QuestionIds.StopCpu, QuestionIds.StartAfter, QuestionIds.Overwrite, QuestionIds.ConsistentDownload, QuestionIds.AlarmTexts, QuestionIds.AllBlocks, QuestionIds.Reinitialize, QuestionIds.Other, "new-in-v22" })
            {
                True(DownloadChecks.Acting(id), id + " is acting");
            }
            foreach (string id in new[] { QuestionIds.Password, QuestionIds.ConfigurationPassword, QuestionIds.Certificate, QuestionIds.UserManagement, QuestionIds.IdentificationData })
            {
                True(!DownloadChecks.Acting(id), id + " is passive");
            }
            // An answer counts as acting only once written, and a passive
            // one never does.
            object pw = new object(), stop = new object();
            var r = new Run(AllYes);
            N(r, pw, "ModuleWriteAccessPassword", "pw");
            Eq(r.D.Acting.Count, 0, "a password written");
            True(new Step().Acting, "a step that does not say is acting, the safe side");
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
            Eq(DownloadChecks.Consequence(new List<string> { QuestionIds.ConsistentDownload }),
                "tiad had answered consistent-download before the download ended, so TIA may already have loaded the software: compare", "consistent download loads");
            Contains(DownloadChecks.Consequence(new List<string> { QuestionIds.AlarmTexts }), "may already have loaded the software", "alarm texts load");
            Contains(DownloadChecks.Consequence(new List<string> { QuestionIds.AllBlocks }), "may already have loaded the software", "all blocks load");
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
            // A stopped handler never logs on.
            const string auth = "OnlineAuthenticationConfiguration";
            var c = new OnlineAnswers(false);
            Eq(c.Answer(auth), OnlineChecks.Login, "a legitimation, before stop");
            c.Stop(delegate { throw new ObjectDisposedException("ConnectionConfiguration"); }, "test: detaching");
            Eq(c.Answer(auth), OnlineChecks.Cancel, "a legitimation, after a failed detach");
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
                "ConsistentBlocksDownload.ConsistentDownload", "AlarmTextLibrariesDownload.ConsistentDownload",
                "AllBlocksDownload.DownloadAllBlocks", "UserManagementDownload.KeepOnlineUserManagementData", "UserManagementDownload.DownloadAllUserManagementDataResetToProject", "LoadIdentificationData.LoadNothing",
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
                True(DownloadChecks.For(t, true, "") != null || DownloadChecks.For(t, true, Answer.ChoiceKeep) != null, t + ": yes has no setting");
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
                { "OnlineWriteAccessPassword", OnlineChecks.Unknown },
                { "OnlineFancyNewThing", OnlineChecks.Unknown },
                { "", OnlineChecks.Unknown },
                { "tlsverificationconfiguration", OnlineChecks.Unknown },
            };
            foreach (var kv in kinds)
            {
                Eq(OnlineChecks.Kind(kv.Key), kv.Value, kv.Key + ": kind");
                string want = kv.Value == OnlineChecks.Certificate ? OnlineChecks.NotTrusted
                    : kv.Key == "OnlineAuthenticationConfiguration" ? OnlineChecks.Login : OnlineChecks.Cancel;
                Eq(OnlineChecks.Answer(kv.Key), want, kv.Key + ": answer");
            }
            // The upload's opt-in trusts a certificate, and nothing else.
            Eq(OnlineChecks.Answer("TlsVerificationConfiguration"), OnlineChecks.NotTrusted, "the default");
            Eq(OnlineChecks.Answer("TlsVerificationConfiguration", false), OnlineChecks.NotTrusted, "no opt-in");
            Eq(OnlineChecks.Answer("TlsVerificationConfiguration", true), OnlineChecks.Trusted, "the opt-in");
            foreach (string t in new[] { "OnlinePasswordConfiguration", "OnlineReadAccessPassword", "OnlineFancyNewThing", "" })
            {
                Eq(OnlineChecks.Answer(t, true), OnlineChecks.Cancel, t + ": with the opt-in");
            }
            Eq(OnlineChecks.Answer("OnlineAuthenticationConfiguration", true), OnlineChecks.Login, "a project user's legitimation, with the opt-in");
            // Text from a CPU cannot end or forge a log line.
            Eq(TiaNames.LogSafe("PLC_1\n2026 audit download forged\r\u2028x"), "\"PLC_1\\n2026 audit download forged\\r\\u2028x\"", "log escaping");
            Eq(TiaNames.LogSafe(null), "\"\"", "log escaping, null");
            Eq(TiaNames.OnlineRefused("192.0.2.10", new List<string> { "one", "two" }, "Online connection failed."),
                "going online to 192.0.2.10, TIA asked: one; two. tiad trusts no CPU certificate unless the request opts in (trustUnverifiedCertificate), and answers credentials only as the CPU's user \"nautilus\" with the request's secrets.fullAccessPassword, so it answered no (TIA: Online connection failed.)",
                "the refusal");
            Eq(TiaNames.LoginFailed("192.0.2.10", "Online connection failed."),
                "going online to 192.0.2.10, tiad answered TIA's credentials question as the CPU's user \"nautilus\" with the request's secrets.fullAccessPassword, and TIA failed (TIA: Online connection failed.): the CPU may have refused the password",
                "a failure after a login");
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

        static ProjectBlock B(string name, string type, int number)
        {
            return new ProjectBlock { Name = name, Type = type, Number = number };
        }

        static List<ProjectBlock> Bs(params ProjectBlock[] bs)
        {
            return new List<ProjectBlock>(bs);
        }

        // MainOnly is the bench project's blocks: Main [OB1] alone.
        static List<ProjectBlock> MainOnly()
        {
            return Bs(B("Main", "OB", 1));
        }

        static void CompareIs(CompareNode tree, bool equal, string diffs, string what)
        {
            CompareIs(tree, MainOnly(), equal, diffs, what);
        }

        static void CompareIs(CompareNode tree, List<ProjectBlock> blocks, bool equal, string diffs, string what)
        {
            var l = new List<Difference>();
            Eq(TiaNames.CompareEqual(tree, blocks, null, l), equal, what + " equal");
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
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("A", "FolderContentsIdentical", Cmp("B", "FolderContentsIdentical",
                Cmp("C", "FolderContentsIdentical", Cmp("D", "FolderContentsIdentical", Cmp("Pump [FB1]", "ObjectsIdentical")))))),
                Bs(B("Pump", "FB", 1)), true, "", "an identical block five levels down");

            // CompareIrrelevant anywhere is not compared (#1156 review).
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("Info", "CompareIrrelevant")), false, "Info: not compared",
                "an irrelevant object beside an identical one");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Program blocks", "CompareIrrelevant"),
                Cmp("PLC tags", "FolderContentsIdentical", Cmp("Default tag table", "ObjectsIdentical"))), false,
                "Program blocks: not compared; Main [OB1]: not compared", "a skipped block folder beside an identical tag table");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Default tag table", "ObjectsIdentical"),
                Cmp("Program blocks", "CompareIrrelevant", Cmp("Calc [FC1]", "ObjectsDifferent"))), Bs(B("Calc", "FC", 1)), false,
                "Program blocks: not compared; Calc [FC1]: different", "a differing block under an irrelevant folder is named too");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("X", "")), false, "X: different",
                "an element with no state");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("A", "FolderContentsIdentical", Cmp("B", "FolderContentsIdentical",
                Cmp("C", "FolderContentsIdentical", Cmp("D", "FolderContentsIdentical", Cmp("Pump [FB1]", "ObjectsIdentical"), Cmp("Valve [FB2]", "RightMissing")))))),
                Bs(B("Pump", "FB", 1), B("Valve", "FB", 2)), false, "Valve [FB2]: only offline", "a difference five levels down");

            // Nothing compared.
            CompareIs(null, false, ": nothing compared", "no tree");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical"), false, "PLC_1: nothing compared; Main [OB1]: not compared", "an identical root with no children");
            CompareIs(Cmp("PLC_1", "ObjectsIdentical"), false, "PLC_1: nothing compared; Main [OB1]: not compared", "an ObjectsIdentical root alone");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Software units", "FolderContentsIdentical"), Cmp("PLC data types", "FolderContentsIdentical")),
                false, "PLC_1: nothing compared; Main [OB1]: not compared", "an identical root over empty folders");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Info", "CompareIrrelevant")), false, "Info: not compared; Main [OB1]: not compared",
                "an identical root over an irrelevant object only");

            // The root must say identical.
            CompareIs(Cmp("PLC_1", "CompareIrrelevant", Cmp("Main [OB1]", "ObjectsIdentical")), false, "PLC_1: not compared",
                "an irrelevant root over an identical object");
            CompareIs(Cmp("PLC_1", "Bogus", Cmp("Main [OB1]", "ObjectsIdentical")), false, "PLC_1: different", "an unknown root state");
            CompareIs(Cmp("PLC_1", "ObjectsDifferent", Cmp("Main [OB1]", "ObjectsIdentical")), false, "PLC_1: different", "a different root");

            // No non-identical state anywhere.
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical",
                Cmp("Program blocks", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("Calc [FC1]", "ObjectsDifferent"))),
                Bs(B("Main", "OB", 1), B("Calc", "FC", 1)), false, "Calc [FC1]: different", "a differing block under an identical root");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical",
                Cmp("Program blocks", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical")), Cmp("PLC tags", "FolderContentsDifferent")),
                false, "PLC tags: contents differ", "a folder whose contents differ, showing no differing child");
            CompareIs(Cmp("PLC_1", "FolderContentsDifferent", Cmp("Main [OB1]", "ObjectsIdentical")), false, "PLC_1: contents differ",
                "a root whose contents differ, showing no differing child");
            CompareIs(Cmp("PLC_1", "FolderContentsDifferent", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("Pump [FB1]", "RightMissing")),
                Bs(B("Main", "OB", 1), B("Pump", "FB", 1)), false, "Pump [FB1]: only offline", "a root whose contents differ names the differing child, not itself");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("FB9", "LeftMissing")), false, "FB9: only online",
                "an object only the CPU has");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical", Cmp("Network 1", "ObjectsDifferent"))), false,
                "Network 1: different", "a differing part inside an identical object");

            // The decision adds to what is already there and reads only that.
            var l = new List<Difference> { new Difference { Object = "earlier", State = "different" } };
            Eq(TiaNames.CompareEqual(BenchTree(), MainOnly(), null, l), true, "an equal tree with earlier entries");
            Eq(l.Count, 1, "an equal tree adds nothing");

            // D1b: the import group's data types are required by name (the
            // bare name, TypeLabel, a guess until the bench shows one). A
            // wrong guess can only refuse: a missing type's element is in a
            // non-identical state, and one the tree leaves out is not
            // compared. Customer types are not required, as before.
            var naut = new List<ProjectBlock> { new ProjectBlock { Name = "naut_R", Type = "PlcStruct", Group = "nautilus", DataType = true } };
            Func<CompareNode, string> typed = delegate(CompareNode tree)
            {
                var d = new List<Difference>();
                bool eq = TiaNames.CompareEqual(tree, MainOnly(), naut, d);
                return eq + " " + Diffs(d);
            };
            Eq(typed(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"),
                Cmp("PLC data types", "FolderContentsIdentical", Cmp("nautilus", "FolderContentsIdentical", Cmp("naut_R", "ObjectsIdentical"))))), "True ", "the group's type identical");
            Eq(typed(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("PLC data types", "FolderContentsIdentical"))),
                "False naut_R (data type): not compared", "the group's type left out of the tree");
            Eq(typed(Cmp("PLC_1", "FolderContentsDifferent", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("PLC data types", "FolderContentsDifferent", Cmp("naut_R", "RightMissing")))),
                "False naut_R: only offline", "the group's type missing on the CPU");
            Eq(typed(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"),
                Cmp("PLC data types", "FolderContentsIdentical", Cmp("naut_R [UDT]", "ObjectsIdentical")))), "False naut_R (data type): not compared", "a label other than the guess refuses");
            var none = new List<Difference>();
            Eq(TiaNames.CompareEqual(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"), Cmp("PLC data types", "FolderContentsIdentical")), MainOnly(), null, none),
                true, "no import group: no type required, as before");
        }

        // CompareSeesEveryBlock: #789 stage 3 gate. An equal compare must
        // show every program block the project holds as an identical
        // element of its label, wherever it sits; a block the tree leaves
        // out, a block that cannot be labelled, or a project whose blocks
        // were not read is not equal and names why.
        static void CompareSeesEveryBlock()
        {
            // The gate's own case: an identical root over an empty block
            // folder beside an identical tag table proves no block landed.
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Program blocks", "FolderContentsIdentical"),
                Cmp("PLC tags", "FolderContentsIdentical", Cmp("Default tag table", "ObjectsIdentical"))), false, "Main [OB1]: not compared",
                "an empty block folder beside an identical tag table");
            // A block the project holds that the tree leaves out.
            CompareIs(BenchTree(), Bs(B("Main", "OB", 1), B("Pump", "FB", 1)), false, "Pump [FB1]: not compared", "a project block the tree leaves out");
            // A block in a user group, one level deeper, is found there.
            var grouped = Cmp("PLC_1", "FolderContentsIdentical",
                Cmp("Program blocks", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsIdentical"),
                    Cmp("nautilus", "FolderContentsIdentical", Cmp("Pump [FB1]", "ObjectsIdentical"), Cmp("Pump_DB [DB1]", "ObjectsIdentical"))));
            CompareIs(grouped, Bs(B("Main", "OB", 1), B("Pump", "FB", 1), B("Pump_DB", "InstanceDB", 1)), true, "", "blocks in a user group");
            // The element must say ObjectsIdentical: a folder that bears
            // the label proves nothing about the block.
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "FolderContentsIdentical"), Cmp("Default tag table", "ObjectsIdentical")),
                false, "Main [OB1]: not compared", "a folder bearing a block's label");
            // The root is no block, even when it bears one's label.
            CompareIs(Cmp("Main [OB1]", "ObjectsIdentical", Cmp("Default tag table", "ObjectsIdentical")), false, "Main [OB1]: not compared",
                "a root bearing a block's label");
            // Labels match exactly: name case, kind and number.
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("main [OB1]", "ObjectsIdentical")), false, "Main [OB1]: not compared", "a label in other case");
            CompareIs(BenchTree(), Bs(B("Main", "OB", 2)), false, "Main [OB2]: not compared", "a block of another number");
            CompareIs(BenchTree(), Bs(B("Main", "FC", 1)), false, "Main [FC1]: not compared", "a block of another kind");
            // A block the tree names as differing is named once, as such.
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "ObjectsDifferent"), Cmp("Default tag table", "ObjectsIdentical")),
                false, "Main [OB1]: different", "a block the tree says differs");
            CompareIs(Cmp("PLC_1", "FolderContentsIdentical", Cmp("Main [OB1]", "CompareIrrelevant"), Cmp("Default tag table", "ObjectsIdentical")),
                false, "Main [OB1]: not compared", "a block the tree skipped");
            // A block that cannot be labelled is not compared, by name.
            CompareIs(BenchTree(), Bs(B("Main", "OB", 1), B("Odd", "SomethingNew", 3)), false, "Odd (SomethingNew): not compared", "a block of an unknown type");
            CompareIs(BenchTree(), Bs(B("Main", "OB", 1), B("Recipe", "PlcStruct", 1)), false, "Recipe (PlcStruct): not compared", "a data type listed as a block");
            // A project whose blocks were not read proves nothing about them.
            CompareIs(BenchTree(), null, false, ": no program blocks in the project (or none read)", "no block list");
            CompareIs(BenchTree(), Bs(), false, ": no program blocks in the project (or none read)", "an empty block list");
            CompareIs(null, null, false, ": nothing compared", "no tree and no block list");
        }

        // BlockLabels: a block's label is its name, kind and number as
        // TIA's compare tree showed Main on the bench ("Main [OB1]", #789
        // dry-run window); every DB type is a DB. The journal lists them.
        static void BlockLabels()
        {
            Eq(TiaNames.BlockLabel(B("Main", "OB", 1)), "Main [OB1]", "the bench's OB, as TIA showed it");
            Eq(TiaNames.BlockLabel(B("Pump", "FB", 12)), "Pump [FB12]", "an FB");
            Eq(TiaNames.BlockLabel(B("Calc", "FC", 3)), "Calc [FC3]", "an FC");
            Eq(TiaNames.BlockLabel(B("Data", "GlobalDB", 4)), "Data [DB4]", "a global DB");
            Eq(TiaNames.BlockLabel(B("Pump_DB", "InstanceDB", 5)), "Pump_DB [DB5]", "an instance DB");
            Eq(TiaNames.BlockLabel(B("Arr", "ArrayDB", 6)), "Arr [DB6]", "an array DB");
            Eq(TiaNames.BlockLabel(B("Recipe", "PlcStruct", 1)), null, "a data type");
            Eq(TiaNames.BlockLabel(B("Odd", "SomethingNew", 1)), null, "an unknown type");
            Eq(TiaNames.BlockLabels(Bs(B("Main", "OB", 1), B("Odd", "X", 2)), 20), "Main [OB1], Odd", "the journal's list");
            Eq(string.Join("; ", TiaNames.Labels(Bs(B("Main", "OB", 1), B("Odd", "X", 2))).ToArray()), "Main [OB1]; Odd (X)", "the response's list");
            Eq(TiaNames.Labels(null).Count, 0, "the response's list, no blocks read");
            Eq(TiaNames.BlockLabels(Bs(B("A", "FC", 1), B("B", "FC", 2), B("C", "FC", 3)), 2), "A [FC1], B [FC2], +1 more", "the journal's list, cut");
        }

        // ElementNames: #789 stage 3 window. A block only the CPU held came
        // back named "Does not exist": TIA fills the missing side with a
        // placeholder, so an element is named by the side that holds it.
        static void ElementNames()
        {
            Eq(TiaNames.ElementName("LeftMissing", "Does not exist", "NautStage3 [FC1]"), "NautStage3 [FC1]", "only online: the CPU's name, as the bench showed");
            Eq(TiaNames.ElementName("RightMissing", "NautStage3 [FC1]", "Does not exist"), "NautStage3 [FC1]", "only offline: the project's name");
            Eq(TiaNames.ElementName("ObjectsIdentical", "Main [OB1]", "Main [OB1]"), "Main [OB1]", "both sides");
            Eq(TiaNames.ElementName("ObjectsDifferent", "Main [OB1]", "Main_x [OB1]"), "Main [OB1]", "both sides, differing: the project's name");
            Eq(TiaNames.ElementName("ObjectsIdentical", "", "PLC_1"), "PLC_1", "only an online name given");
            Eq(TiaNames.ElementName("LeftMissing", "X", null), "X", "only online, no online name given");
            Eq(TiaNames.ElementName("FolderContentsIdentical", null, null), "", "no names");
            // An identical element keeps its offline name even when TIA
            // gave the placeholder there: an online-only name must never
            // stand in for a project block's label (it then fails closed,
            // the block not compared).
            Eq(TiaNames.ElementName("ObjectsIdentical", "Does not exist", "Main [OB1]"), "Does not exist", "identical, placeholder offline name");
        }

        // ModeFromMessages: #789 stages 2 and 3. TIA's download messages,
        // one per step, are the only word on the CPU's mode after a load:
        // the last exact "<cpu> started." / "<cpu> stopped." decides.
        static void ModeFromMessages()
        {
            var bench = new List<string> { "PLC_1 stopped.", "'Main' was loaded successfully.", "'Default' was loaded successfully.", "'Drive' was loaded successfully.", "PLC_1 started." };
            Eq(TiaNames.ModeFromMessages("PLC_1", bench), "RUN", "the bench's messages: stopped, loaded, started");
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { "PLC_1 stopped.", "'Main' was loaded successfully." }), "STOP", "stopped, never started");
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { "PLC_1 started.", "PLC_1 stopped." }), "STOP", "the last one decides");
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { "  PLC_1 started.  " }), "RUN", "trimmed");
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { "'Main' was loaded successfully." }), "", "never stopped: nothing said");
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { "PLC_2 stopped.", "PLC_2 started." }), "", "another CPU's messages");
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { "PLC_10 started.", "XPLC_1 started.", "PLC_1 started" }), "", "only the exact message");
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { "PLC_1 gestartet." }), "", "another language: unknown");
            // A message is one element as TIA gave it: a name inside one
            // that reads like a start must never pass for one.
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { "PLC_1 stopped.", "'x; PLC_1 started.' was loaded successfully." }), "STOP", "a start quoted inside another message");
            Eq(TiaNames.ModeFromMessages("", bench), "", "no CPU name");
            Eq(TiaNames.ModeFromMessages(null, bench), "", "null CPU name");
            Eq(TiaNames.ModeFromMessages("PLC_1", null), "", "no messages");
            Eq(TiaNames.ModeFromMessages("PLC_1", new List<string> { null, "PLC_1 started." }), "RUN", "a null message skipped");
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

        // DeployStatusOf is tiad.DeployStatusOf's table: only the kinds that
        // say nothing was loaded are not-loaded; anything else is in doubt.
        static void DeployStatusOf()
        {
            var want = new Dictionary<string, string>
            {
                { "loaded", DeployStatus.Loaded },
                { Kinds.DecisionRequired, DeployStatus.NotLoaded },
                { Kinds.Declined, DeployStatus.NotLoaded },
                { Kinds.BadRequest, DeployStatus.InDoubt },
                { Kinds.TargetRefused, DeployStatus.InDoubt },
                { Kinds.Busy, DeployStatus.InDoubt },
                { Kinds.NotCompiled, DeployStatus.InDoubt },
                { Kinds.OperationFailed, DeployStatus.NotLoaded },
                { Kinds.OutcomeUnknown, DeployStatus.InDoubt },
                { Kinds.NotFound, DeployStatus.InDoubt },
                { Kinds.Gate, DeployStatus.InDoubt },
                { Kinds.UncleanSession, DeployStatus.InDoubt },
                { "error", DeployStatus.InDoubt },
                { "", DeployStatus.InDoubt },
            };
            foreach (var kv in want) Eq(DeployStatus.Of(kv.Key), kv.Value, "status of " + Json.Quote(kv.Key));
        }

        // DeploySourceShape is tiad.DeploySource.Validate's table.
        static void DeploySourceShape()
        {
            string c40 = "0123456789abcdef0123456789abcdef01234567", h = "sha256:" + c40 + "0123456789abcdef01234567";
            var cases = new[]
            {
                new[] { c40, "", "" },
                new[] { c40 + "0123456789abcdef01234567", h, "" },
                new[] { "", "", "source.commit" },
                new[] { c40.Substring(1), "", "source.commit" },
                new[] { c40.ToUpperInvariant(), "", "source.commit" },
                new[] { c40.Substring(1) + "g", "", "source.commit" },
                new[] { c40 + "0", "", "source.commit" },
                new[] { c40, h.Substring(7), "source.hash" },
                new[] { c40, "SHA256:" + h.Substring(7), "source.hash" },
                new[] { c40, h + "0", "source.hash" },
                new[] { c40, h.Substring(0, h.Length - 1) + "G", "source.hash" },
            };
            foreach (var c in cases)
            {
                string got = new DeploySource { Commit = c[0], Hash = c[1] }.Validate();
                if (c[2] == "") Eq(got, null, "source " + Json.Quote(c[0]) + " " + Json.Quote(c[1]));
                else True(got != null && got.StartsWith(c[2], StringComparison.Ordinal), "source " + Json.Quote(c[0]) + " " + Json.Quote(c[1]) + ": " + Show(got) + ", want " + c[2]);
            }
        }
    }
}
