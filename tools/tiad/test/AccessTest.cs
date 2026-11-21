// AccessTest.cs: the CPU's access control (core/Access.cs): what an apply
// refuses before any write, the steps it runs and their order, the read
// back, tiad's password records, and the password checks.

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        // ScriptedAccess behaves like TIA's access control and users and
        // roles, recording every write.
        sealed class ScriptedAccess : IAccessControl
        {
            public string State_ = AccessRule.Enabled;
            public string EnableLeaves;           // the state Enable leaves, when not Enabled
            public bool GrantIgnored;             // GrantFullAccess does nothing
            public bool CommentFails;             // SetComment throws after the first call
            public string SetPasswordFails;       // SetPassword throws this (not a reuse refusal)
            public Dictionary<string, string> RoleComments = new Dictionary<string, string>();
            public HashSet<string> Granted = new HashSet<string>();
            public Dictionary<string, string> Users = new Dictionary<string, string>();
            public Dictionary<string, HashSet<string>> UserRoles = new Dictionary<string, HashSet<string>>();
            public Dictionary<string, string> Comments = new Dictionary<string, string>();
            public Dictionary<string, List<string>> History = new Dictionary<string, List<string>>();
            public List<string> Calls = new List<string>();
            int comments;

            public string State()
            {
                return State_;
            }

            public void Enable()
            {
                Calls.Add("enable");
                State_ = EnableLeaves ?? AccessRule.Enabled;
            }

            public bool HasRole(string role)
            {
                return RoleComments.ContainsKey(role);
            }

            public string RoleComment(string role)
            {
                return RoleComments[role];
            }

            public void MakeRole(string role, string comment)
            {
                Calls.Add("make-role");
                if (RoleComments.ContainsKey(role)) throw new InvalidOperationException("the role exists");
                RoleComments[role] = comment;
            }

            public void GrantFullAccess(string role)
            {
                if (Granted.Contains(role)) return;
                Calls.Add("grant");
                if (!GrantIgnored) Granted.Add(role);
            }

            public bool HasUser(string user)
            {
                return Users.ContainsKey(user);
            }

            // MakeUser refuses a user that exists, as TIA's Create would.
            public void MakeUser(string user, string password)
            {
                Calls.Add("make-user");
                if (Users.ContainsKey(user)) throw new InvalidOperationException("the user exists");
                Users[user] = password;
                UserRoles[user] = new HashSet<string>();
                History[user] = new List<string> { password };
            }

            // SetPassword refuses a password the user has had, as TIA does.
            public void SetPassword(string user, string password)
            {
                Calls.Add("set-password");
                if (SetPasswordFails != null) throw new InvalidOperationException(SetPasswordFails);
                List<string> h;
                if (!History.TryGetValue(user, out h)) h = History[user] = new List<string> { Users[user] };
                if (h.Contains(password)) throw new PasswordReusedException(new InvalidOperationException("already been used"));
                Users[user] = password;
                h.Add(password);
            }

            public string Comment(string user)
            {
                string c;
                return Comments.TryGetValue(user, out c) ? c : "";
            }

            public void SetComment(string user, string comment)
            {
                Calls.Add("comment");
                if (CommentFails && comments++ > 0) throw new InvalidOperationException("comment refused");
                Comments[user] = comment;
            }

            public void AddRole(string user, string role)
            {
                if (UserRoles[user].Contains(role)) return;
                Calls.Add("add-role");
                UserRoles[user].Add(role);
            }

            public string Check(string user, string role)
            {
                if (!Users.ContainsKey(user)) return "no user";
                if (!UserRoles[user].Contains(role)) return "no role on the user";
                if (!Granted.Contains(role)) return "the role " + role + " lacks Full access";
                return null;
            }
        }

        static readonly AccessRecords TestRecords = AccessRecords.FromHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f");
        static readonly AccessRecords OtherRecords = AccessRecords.Random();

        // Found is a project that holds tiad's role and a user with
        // password, recorded under records (null: no record).
        static ScriptedAccess Found(string password, AccessRecords records)
        {
            var c = new ScriptedAccess();
            c.RoleComments[AccessSteps.RoleName] = AccessSteps.RoleComment;
            c.Granted.Add(AccessSteps.RoleName);
            c.Users[AccessSteps.UserName] = password;
            c.History[AccessSteps.UserName] = new List<string> { password };
            c.UserRoles[AccessSteps.UserName] = new HashSet<string> { AccessSteps.RoleName };
            if (records != null) c.Comments[AccessSteps.UserName] = records.Make(AccessSteps.UserName, password);
            return c;
        }

        static string Calls(ScriptedAccess c)
        {
            return string.Join(",", c.Calls.ToArray());
        }

        // AccessRecordsKnownAnswer: one fixed vector (an independent HMAC),
        // so a changed derivation fails here, not on every project in the
        // field; salts differ per record; another key's record is not ours.
        static void AccessRecordsKnownAnswer()
        {
            var salt = new byte[16];
            for (int i = 0; i < 16; i++) salt[i] = (byte)i;
            string rec = TestRecords.Make(AccessSteps.UserName, "Full-access-1", salt);
            Eq(rec, "tiad password record v2:630dcd29:000102030405060708090a0b0c0d0e0f:8c885ecba91958f4e5313046132ae8a418ecfa237c244b3678da4a6ade8fc7e7", "known answer");
            True(TestRecords.Matches(rec, AccessSteps.UserName, "Full-access-1"), "matches its password");
            True(!TestRecords.Matches(rec, AccessSteps.UserName, "Full-access-2"), "not another password");
            True(!TestRecords.Matches(rec, "someone", "Full-access-1"), "not another user");
            True(!OtherRecords.Matches(rec, AccessSteps.UserName, "Full-access-1"), "not under another key");
            True(TestRecords.Ours(rec) && !OtherRecords.Ours(rec), "the key id tells whose");
            True(AccessRecords.IsRecord(rec) && !AccessRecords.IsRecord("made by an engineer") && !AccessRecords.IsRecord(""), "a record, and comments that are not");
            True(!AccessRecords.IsRecord("tiad password record v2:630dcd29:00:11"), "a cut record is none");
            string a = TestRecords.Make(AccessSteps.UserName, "Full-access-1"), b = TestRecords.Make(AccessSteps.UserName, "Full-access-1");
            True(a != b, "each record has its own salt");
            True(TestRecords.Matches(a, AccessSteps.UserName, "Full-access-1") && TestRecords.Matches(b, AccessSteps.UserName, "Full-access-1"), "both verify");
            True(!a.Contains("Full-access-1"), "a record never holds the password");
            Throws<ArgumentException>(delegate { AccessRecords.FromHex("abcd"); }, "a short key");
        }

        // AccessPlan: keep never refuses; fullAccessUser needs its password,
        // and on a kept CPU access control already on.
        static void AccessPlan()
        {
            foreach (string st in new[] { AccessRule.Enabled, "Disabled", AccessRule.Absent, "EnabledWithAccessControlViaAccessLevel" })
            {
                AccessRule.Plan(st, StationRules.AccessKeep, false);
            }
            AccessRule.Plan(null, StationRules.AccessKeep, false);
            AccessRule.Plan(null, StationRules.AccessFullAccessUser, true);
            AccessRule.Plan(AccessRule.Enabled, StationRules.AccessFullAccessUser, true);
            var e = Throws<TiadException>(delegate { AccessRule.Plan(null, StationRules.AccessFullAccessUser, false); }, "no password");
            if (e != null) Contains(e.Message, "secrets.fullAccessPassword is empty", "no password");
            foreach (var c in new[]
            {
                new[] { "Disabled", "set to \"Disabled\"" },
                new[] { "EnabledWithAccessControlViaAccessLevel", "set to \"EnabledWithAccessControlViaAccessLevel\"" },
                new[] { AccessRule.Absent, "not available (no such setting)" },
            })
            {
                e = Throws<TiadException>(delegate { AccessRule.Plan(c[0], StationRules.AccessFullAccessUser, true); }, c[0]);
                if (e == null) continue;
                Eq(e.Status, 400, c[0] + " status");
                Contains(e.Message, "the project's CPU has its access control " + c[1], c[0]);
                Contains(e.Message, "set cpu.accessControl to keep", c[0] + " names the fix");
            }
            Throws<TiadException>(delegate { AccessRule.Plan(null, "open", true); }, "an unknown mode");
        }

        // AccessStepsNewCpu: a CPU the apply made gets access control on
        // (written only when it is not), the role, its grant, the user with
        // its record, and the role on the user, in that order.
        static void AccessStepsNewCpu()
        {
            var off = new ScriptedAccess { State_ = "Disabled" };
            var made = new AccessMade();
            var notes = new List<string>();
            AccessSteps.Run(off, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, true, made, notes);
            Eq(Calls(off), "enable,make-role,grant,make-user,comment,add-role", "a new CPU with access control off");
            Eq(off.State_, AccessRule.Enabled, "access control on");
            Eq(off.RoleComments[AccessSteps.RoleName], AccessSteps.RoleComment, "the role is marked tiad's");
            Eq(off.Users[AccessSteps.UserName], "Full-access-1", "the user has the secret");
            True(TestRecords.Matches(off.Comments[AccessSteps.UserName], AccessSteps.UserName, "Full-access-1"), "the made user carries tiad's record of it");
            True(made.User && made.Role, "both recorded as made");
            Eq(string.Join("|", notes.ToArray()), AccessSteps.MadeNote, "the note says it was made");

            var fresh = new ScriptedAccess();
            AccessSteps.Run(fresh, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, true, new AccessMade(), new List<string>());
            Eq(Calls(fresh), "make-role,grant,make-user,comment,add-role", "TIA's default needs no write");

            var keep = new ScriptedAccess { State_ = "Disabled" };
            var none = new AccessMade();
            AccessSteps.Run(keep, StationRules.AccessKeep, "", null, true, none, new List<string>());
            Eq(keep.Calls.Count, 0, "keep calls nothing, even on a new CPU");
            True(!none.Any, "keep makes nothing");

            var absent = new ScriptedAccess { State_ = AccessRule.Absent };
            var e = Throws<TiaException>(delegate { AccessSteps.Run(absent, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, true, new AccessMade(), new List<string>()); }, "no setting");
            if (e != null) Contains(e.Message, "set cpu.accessControl to keep", "no setting names the fix");
            Eq(absent.Calls.Count, 0, "nothing written on a CPU without the setting");
        }

        // AccessStepsKeptCpu: on a CPU the apply kept, access control is
        // never written, and Run never touches a found user's password.
        static void AccessStepsKeptCpu()
        {
            var off = new ScriptedAccess { State_ = "Disabled" };
            var e = Throws<TiadException>(delegate { AccessSteps.Run(off, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, false, new AccessMade(), new List<string>()); }, "kept CPU, access control off");
            if (e != null) Contains(e.Message, "set cpu.accessControl to keep", "kept CPU names the fix");
            Eq(off.Calls.Count, 0, "nothing written on a kept CPU with access control off");

            var held = Found("Old-access-1", TestRecords);
            var made = new AccessMade();
            var notes = new List<string>();
            AccessSteps.Run(held, StationRules.AccessFullAccessUser, "New-access-22", TestRecords, false, made, notes);
            Eq(held.Calls.Count, 0, "found with its role and grant: nothing written");
            Eq(held.Users[AccessSteps.UserName], "Old-access-1", "Run never sets a found user's password (Prepare does)");
            True(!made.Any, "nothing recorded as made: an upload must not remove it");
            Eq(notes.Count, 0, "the found user's note is Prepare's");

            var roleOnly = Found("Old-access-1", TestRecords);
            roleOnly.RoleComments.Remove(AccessSteps.RoleName);
            roleOnly.Granted.Clear();
            roleOnly.UserRoles[AccessSteps.UserName].Clear();
            var m2 = new AccessMade();
            AccessSteps.Run(roleOnly, StationRules.AccessFullAccessUser, "Old-access-1", TestRecords, false, m2, new List<string>());
            True(m2.Role && !m2.User, "only the role is recorded as made");
            Eq(Calls(roleOnly), "make-role,grant,add-role", "a found user is never made again");
        }

        // AccessPrepare: before any write, a found user's password follows
        // the secret: unchanged when tiad's record matches, set otherwise
        // (the record first, put back when TIA refuses); a user or role
        // tiad did not make is refused unless fullAccessRecordOnly asks.
        static void AccessPrepare()
        {
            var notes = new List<string>();
            var w = new AccessWrites();
            var nobody = new ScriptedAccess();
            AccessSteps.Prepare(nobody, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, false, w, notes);
            Eq(nobody.Calls.Count + notes.Count, 0, "no user: nothing (Run makes it)");
            AccessSteps.Prepare(Found("X-access-11", null), StationRules.AccessKeep, "", null, false, w, notes);
            Eq(notes.Count, 0, "keep: nothing, even a foreign user");

            var same = Found("Same-access-1", TestRecords);
            AccessSteps.Prepare(same, StationRules.AccessFullAccessUser, "Same-access-1", TestRecords, false, w, notes);
            Eq(same.Calls.Count, 0, "the same secret: nothing written (TIA would refuse it)");
            Eq(notes[0], AccessSteps.UnchangedNote, "unchanged");

            var rot = Found("Old-access-1", TestRecords);
            notes.Clear();
            AccessSteps.Prepare(rot, StationRules.AccessFullAccessUser, "New-access-22", TestRecords, false, w, notes);
            Eq(Calls(rot), "comment,set-password", "a rotated secret: the record first, then the password");
            Eq(rot.Users[AccessSteps.UserName], "New-access-22", "set");
            True(TestRecords.Matches(rot.Comments[AccessSteps.UserName], AccessSteps.UserName, "New-access-22"), "recorded");
            Eq(notes[0], AccessSteps.SetNote, "set note");

            // Back to one the user had, with this agent's record: plainly
            // reused, and the project is as it was.
            string had = rot.Comments[AccessSteps.UserName];
            var e = Throws<TiadException>(delegate { AccessSteps.Prepare(rot, StationRules.AccessFullAccessUser, "Old-access-1", TestRecords, false, w, new List<string>()); }, "a recent password");
            if (e != null) { Eq(e.Message, AccessSteps.Reused, "reused"); Eq(e.Status, 400, "a refusal"); }
            Eq(rot.Comments[AccessSteps.UserName], had, "the record is put back");
            Eq(rot.Users[AccessSteps.UserName], "New-access-22", "the password is as it was");
            True(!w.Dirty, "nothing left part-way");

            // Another key's record (another agent): TIA's refusal cannot tell
            // current from older, and the refusal says so.
            var other = Found("Same-access-1", OtherRecords);
            e = Throws<TiadException>(delegate { AccessSteps.Prepare(other, StationRules.AccessFullAccessUser, "Same-access-1", TestRecords, false, w, new List<string>()); }, "another key's record");
            if (e != null) Eq(e.Message, AccessSteps.ReusedOtherKey, "another key: ambiguous, names fullAccessRecordOnly");
            True(OtherRecords.Ours(other.Comments[AccessSteps.UserName]), "its record is put back");
            other.Calls.Clear();
            notes.Clear();
            AccessSteps.Prepare(other, StationRules.AccessFullAccessUser, "Same-access-1", TestRecords, true, w, notes);
            Eq(Calls(other), "comment", "record only: the record, no password");
            Eq(notes[0], AccessSteps.RecordedNote, "recorded note");
            other.Calls.Clear();
            notes.Clear();
            AccessSteps.Prepare(other, StationRules.AccessFullAccessUser, "Same-access-1", TestRecords, false, w, notes);
            Eq(other.Calls.Count, 0, "after record only, nothing to write");
            Eq(notes[0], AccessSteps.UnchangedNote, "after record only, unchanged");

            // Putting the record back fails: dirty, and the refusal says to
            // close without saving.
            var w2 = new AccessWrites();
            var stuck = Found("Old-access-1", TestRecords);
            stuck.History[AccessSteps.UserName].Add("Older-access-2");
            stuck.CommentFails = true;
            var te = Throws<TiaException>(delegate { AccessSteps.Prepare(stuck, StationRules.AccessFullAccessUser, "Older-access-2", TestRecords, false, w2, new List<string>()); }, "reused, and the record cannot be put back");
            if (te != null) Contains(te.Message, "putting the user's record back failed too: close this session without saving", "names the fix");
            True(w2.Dirty, "left part-way");

            // Another TIA failure in SetPassword: the record is put back and
            // TIA's error stays its own.
            var broken = Found("Old-access-1", TestRecords);
            string before = broken.Comments[AccessSteps.UserName];
            broken.SetPasswordFails = "COM failure";
            var w3 = new AccessWrites();
            Throws<InvalidOperationException>(delegate { AccessSteps.Prepare(broken, StationRules.AccessFullAccessUser, "New-access-22", TestRecords, false, w3, new List<string>()); }, "another failure");
            Eq(broken.Comments[AccessSteps.UserName], before, "the record is put back");
            True(!w3.Dirty, "put back: not part-way");

            // A user tiad did not make (no record): refused, or taken over on
            // fullAccessRecordOnly.
            var foreign = Found("Their-access-1", null);
            foreign.Comments[AccessSteps.UserName] = "the line engineer's account";
            var be = Throws<TiadException>(delegate { AccessSteps.Prepare(foreign, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, false, w, new List<string>()); }, "a foreign user");
            if (be != null) Eq(be.Message, AccessSteps.ForeignUser, "a foreign user names keep and fullAccessRecordOnly");
            Eq(foreign.Calls.Count, 0, "nothing written for a foreign user");
            notes.Clear();
            AccessSteps.Prepare(foreign, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, true, w, notes);
            Eq(Calls(foreign), "comment", "taken over: the record only");
            Eq(foreign.Users[AccessSteps.UserName], "Their-access-1", "its password is not set");
            Eq(notes[0], AccessSteps.TakenOverNote, "taken-over note");

            // A role tiad did not make: refused before anything, even on
            // fullAccessRecordOnly.
            foreach (bool ro in new[] { false, true })
            {
                var fr = Found("Same-access-1", TestRecords);
                fr.RoleComments[AccessSteps.RoleName] = "the plant's admins";
                var re = Throws<TiadException>(delegate { AccessSteps.Prepare(fr, StationRules.AccessFullAccessUser, "Same-access-1", TestRecords, ro, w, new List<string>()); }, "a foreign role");
                if (re != null) Eq(re.Message, AccessSteps.ForeignRole, "a foreign role names keep");
                Eq(fr.Calls.Count, 0, "nothing written for a foreign role");
                Throws<TiaException>(delegate { AccessSteps.Run(fr, StationRules.AccessFullAccessUser, "Same-access-1", TestRecords, false, new AccessMade(), new List<string>()); }, "Run refuses a foreign role too");
            }
        }

        // AccessStepsReadBack: a grant TIA ignored refuses; what was made
        // before the refusal stays recorded.
        static void AccessStepsReadBack()
        {
            var c = new ScriptedAccess { GrantIgnored = true };
            var made = new AccessMade();
            var e = Throws<TiaException>(delegate { AccessSteps.Run(c, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, true, made, new List<string>()); }, "grant ignored");
            if (e != null) Contains(e.Message, "lacks Full access", "grant ignored");
            True(made.User && made.Role, "made before the refusal, still recorded");

            var stuck = new ScriptedAccess { State_ = "Disabled", EnableLeaves = "Disabled" };
            e = Throws<TiaException>(delegate { AccessSteps.Run(stuck, StationRules.AccessFullAccessUser, "Full-access-1", TestRecords, true, new AccessMade(), new List<string>()); }, "enable ignored");
            if (e != null) Contains(e.Message, "after turning access control on the CPU reads Disabled, not EnabledWithAccessControl", "enable ignored");
            Eq(Calls(stuck), "enable", "nothing after an ignored enable");
        }

        // AccessPasswordChecks: the spec-side rule and the project's policy,
        // every broken rule named, the password never echoed.
        static void AccessPasswordChecks()
        {
            var st = new Station { Cpu = new Cpu { AccessControl = StationRules.AccessFullAccessUser, ConfigurationProtection = StationRules.ProtectNone } };
            Contains(new Secrets().CheckFor(st), "cpu.accessControl is fullAccessUser, and secrets.fullAccessPassword is empty", "no password");
            Contains(new Secrets { Protection = "Prot-pass-11", ConfigurationPassword = "Conf-pass-11" }.CheckFor(st), "secrets.fullAccessPassword is empty", "other passwords never stand in");
            string weak = new Secrets { FullAccessPassword = "weakpass1x" }.CheckFor(st);
            Contains(weak, "secrets.fullAccessPassword breaks TIA's password rules", "weak");
            True(!weak.Contains("weakpass1x"), "never echoed");
            Eq(new Secrets { FullAccessPassword = "Full-access-1" }.CheckFor(st), null, "a good password");
            st.Cpu.AccessControl = StationRules.AccessKeep;
            Eq(new Secrets().CheckFor(st), null, "keep needs no password");
            // Both checks report together.
            st.Cpu.AccessControl = StationRules.AccessFullAccessUser;
            st.Cpu.ConfigurationProtection = StationRules.ProtectPassword;
            Eq(new Secrets().CheckFor(st),
                "cpu.configurationProtection is password, and secrets.configurationPassword is empty (it comes from the secret store, never the spec); cpu.accessControl is fullAccessUser, and secrets.fullAccessPassword is empty (it comes from the secret store, never the spec)",
                "both missing");

            var dflt = new PasswordPolicy();
            Eq(dflt.Check("Full-access-1"), null, "TIA's default takes a good password");
            Eq(dflt.Check("Abcdefgh1x"), null, "exactly 10");
            var strict = new PasswordPolicy { MinLength = 12, MinDigits = 2, MinSpecial = 3, UpperAndLower = true };
            string e = strict.Check("abcdefgh1x");
            Eq(e, "breaks the project's password policy for local users (at least 12 characters, 2 digit(s), 3 special character(s), upper and lower case): shorter than 12 characters, fewer than 2 digit(s), fewer than 3 special character(s), not both an upper-case and a lower-case letter", "every rule named");
            Eq(strict.Check("Ab12-!-cdefg"), null, "a password meeting every rule");
            Contains(new PasswordPolicy().Check("Aa1" + new string('x', 118)), "longer than 120 characters", "TIA's upper bound");
            Eq(new PasswordPolicy { MinLength = 4, MinDigits = 0, UpperAndLower = false }.Check("abcd"), null, "a looser project");
            // Only ASCII punctuation is special: a character TIA might not
            // count never counts here.
            var oneSpecial = new PasswordPolicy { MinSpecial = 1 };
            Contains(oneSpecial.Check("Passwort1\u00e4A"), "fewer than 1 special character(s)", "a non-ASCII letter is not special");
            Eq(oneSpecial.Check("Passwort1!A"), null, "ASCII punctuation is");
            Contains(oneSpecial.Check("Passwort1 A"), "fewer than 1 special character(s)", "a space is not");
            // Each password is its own secret.
            var eqst = new Station { Cpu = new Cpu { AccessControl = StationRules.AccessFullAccessUser, ConfigurationProtection = StationRules.ProtectPassword } };
            Contains(new Secrets { FullAccessPassword = "Same-pass-11", ConfigurationPassword = "Same-pass-11" }.CheckFor(eqst), "secrets.fullAccessPassword is the same as secrets.configurationPassword: each password is its own secret", "equal to the configuration password");
            var web = new Secrets { FullAccessPassword = "Same-pass-11", ConfigurationPassword = "Other-pass-11" };
            web.WebUsers["naut"] = "Same-pass-11";
            Contains(web.CheckFor(eqst), "secrets.fullAccessPassword is the same as secrets.webUsers[\"naut\"]", "equal to a web user's");
            True(!web.CheckFor(eqst).Contains("Same-pass-11"), "never echoed");
        }
    }
}
