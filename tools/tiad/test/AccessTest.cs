// AccessTest.cs: the CPU's access control (core/Access.cs): what an apply
// refuses before any write, the steps it runs and their order, the read
// back, and the password checks.

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
            public HashSet<string> Roles = new HashSet<string>();
            public HashSet<string> Granted = new HashSet<string>();
            public Dictionary<string, string> Users = new Dictionary<string, string>();
            public Dictionary<string, HashSet<string>> UserRoles = new Dictionary<string, HashSet<string>>();
            public List<string> Calls = new List<string>();

            public string State()
            {
                return State_;
            }

            public void Enable()
            {
                Calls.Add("enable");
                State_ = EnableLeaves ?? AccessRule.Enabled;
            }

            public bool EnsureRole(string role)
            {
                if (Roles.Contains(role)) return false;
                Calls.Add("make-role");
                Roles.Add(role);
                return true;
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
        // (written only when it is not), the role, its grant, the user and
        // the role on the user, in that order; what is made is recorded.
        static void AccessStepsNewCpu()
        {
            var off = new ScriptedAccess { State_ = "Disabled" };
            var made = new AccessMade();
            var notes = new List<string>();
            AccessSteps.Run(off, StationRules.AccessFullAccessUser, "Full-access-1", true, made, notes);
            Eq(string.Join(",", off.Calls.ToArray()), "enable,make-role,grant,make-user,add-role", "a new CPU with access control off");
            Eq(off.State_, AccessRule.Enabled, "access control on");
            Eq(off.Users[AccessSteps.UserName], "Full-access-1", "the user has the secret");
            True(made.User && made.Role, "both recorded as made");
            Eq(notes.Count, 1, "one note");
            Contains(notes[0], "made the user \"nautilus\"", "the note says it was made");

            var fresh = new ScriptedAccess();
            AccessSteps.Run(fresh, StationRules.AccessFullAccessUser, "Full-access-1", true, new AccessMade(), new List<string>());
            Eq(string.Join(",", fresh.Calls.ToArray()), "make-role,grant,make-user,add-role", "TIA's default needs no write");

            var keep = new ScriptedAccess { State_ = "Disabled" };
            var none = new AccessMade();
            AccessSteps.Run(keep, StationRules.AccessKeep, "", true, none, new List<string>());
            Eq(keep.Calls.Count, 0, "keep calls nothing, even on a new CPU");
            True(!none.Any, "keep makes nothing");

            var absent = new ScriptedAccess { State_ = AccessRule.Absent };
            var e = Throws<TiaException>(delegate { AccessSteps.Run(absent, StationRules.AccessFullAccessUser, "Full-access-1", true, new AccessMade(), new List<string>()); }, "no setting");
            if (e != null) Contains(e.Message, "set cpu.accessControl to keep", "no setting names the fix");
            Eq(absent.Calls.Count, 0, "nothing written on a CPU without the setting");
        }

        // AccessStepsKeptCpu: on a CPU the apply kept, access control is
        // never written; a user and role the project held are found, not
        // made, and the user's password is set again.
        static void AccessStepsKeptCpu()
        {
            var off = new ScriptedAccess { State_ = "Disabled" };
            var e = Throws<TiadException>(delegate { AccessSteps.Run(off, StationRules.AccessFullAccessUser, "Full-access-1", false, new AccessMade(), new List<string>()); }, "kept CPU, access control off");
            if (e != null) Contains(e.Message, "set cpu.accessControl to keep", "kept CPU names the fix");
            Eq(off.Calls.Count, 0, "nothing written on a kept CPU with access control off");

            var held = new ScriptedAccess();
            held.Roles.Add(AccessSteps.RoleName);
            held.Granted.Add(AccessSteps.RoleName);
            held.Users[AccessSteps.UserName] = "Old-access-1";
            held.UserRoles[AccessSteps.UserName] = new HashSet<string> { AccessSteps.RoleName };
            var made = new AccessMade();
            var notes = new List<string>();
            AccessSteps.Run(held, StationRules.AccessFullAccessUser, "New-access-22", false, made, notes);
            Eq(held.Calls.Count, 0, "found with its role and grant: nothing written");
            Eq(held.Users[AccessSteps.UserName], "Old-access-1", "a found user's password is left as found (TIA refuses one it has had)");
            True(!made.Any, "nothing recorded as made: an upload must not remove it");
            Eq(notes[0], AccessSteps.FoundNote, "the note says it was found, and its password left");
            Contains(notes[0], "its password is left as found", "the note names what was not done");
            Contains(notes[0], "POST /v1/projects", "the note names the fix");

            var userOnly = new ScriptedAccess();
            userOnly.Users[AccessSteps.UserName] = "Old-access-1";
            userOnly.UserRoles[AccessSteps.UserName] = new HashSet<string>();
            var m2 = new AccessMade();
            AccessSteps.Run(userOnly, StationRules.AccessFullAccessUser, "New-access-22", false, m2, new List<string>());
            True(m2.Role && !m2.User, "only the role is recorded as made");
            Eq(string.Join(",", userOnly.Calls.ToArray()), "make-role,grant,add-role", "a found user is never made again");
        }

        // AccessStepsReadBack: a grant TIA ignored refuses; what was made
        // before the refusal stays recorded.
        static void AccessStepsReadBack()
        {
            var c = new ScriptedAccess { GrantIgnored = true };
            var made = new AccessMade();
            var e = Throws<TiaException>(delegate { AccessSteps.Run(c, StationRules.AccessFullAccessUser, "Full-access-1", true, made, new List<string>()); }, "grant ignored");
            if (e != null) Contains(e.Message, "lacks Full access", "grant ignored");
            True(made.User && made.Role, "made before the refusal, still recorded");

            var stuck = new ScriptedAccess { State_ = "Disabled", EnableLeaves = "Disabled" };
            e = Throws<TiaException>(delegate { AccessSteps.Run(stuck, StationRules.AccessFullAccessUser, "Full-access-1", true, new AccessMade(), new List<string>()); }, "enable ignored");
            if (e != null) Contains(e.Message, "after turning access control on the CPU reads Disabled, not EnabledWithAccessControl", "enable ignored");
            Eq(string.Join(",", stuck.Calls.ToArray()), "enable", "nothing after an ignored enable");
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
        }
    }
}
