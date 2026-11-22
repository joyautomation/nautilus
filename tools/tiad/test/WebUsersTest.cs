// WebUsersTest.cs: the web server's users (core/Web.cs WebUsers): what an
// apply refuses before any write, the exact rights and roles it makes and
// reads back, stale web users and roles removed by tiad's marker only, the
// passwords, and the notes a kept web server left as found gets.

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        static WebUser WU(string name, params string[] rights)
        {
            return new WebUser { Name = name, Rights = new List<string>(rights) };
        }

        static WebServer WebOn(params WebUser[] users)
        {
            return new WebServer { Enabled = true, Users = new List<WebUser>(users) };
        }

        static Secrets WebSecrets(params string[] namePw)
        {
            var s = new Secrets { Records = TestRecords };
            for (int i = 0; i + 1 < namePw.Length; i += 2) s.WebUsers[namePw[i]] = namePw[i + 1];
            return s;
        }

        // WebFound adds a web user tiad made (its role marked, its record
        // in its comment) to c.
        static void WebFound(ScriptedAccess c, string user, string password, params string[] rights)
        {
            string role = WebUsers.RoleName(user);
            c.RoleComments[role] = WebUsers.RoleComment(user);
            c.Rights[role] = new SortedSet<string>(rights, StringComparer.Ordinal);
            c.Users[user] = password;
            c.History[user] = new List<string> { password };
            c.UserRoles[user] = new HashSet<string> { role };
            c.Comments[user] = TestRecords.Make(user, password);
        }

        // WebUsersCheck: before any write, an active anonymous user while
        // the server is to be on, and a web user or role of a wanted name
        // tiad did not make, refuse; nothing is written.
        static void WebUsersCheck()
        {
            var want = WebOn(WU("op", "read"));
            var anon = new ScriptedAccess { Anonymous = true };
            var e = Throws<TiadException>(delegate { WebUsers.Check(anon, want); }, "anonymous active");
            if (e != null) { Eq(e.Status, 400, "a refusal"); Eq(e.Message, WebUsers.AnonymousActive, "anonymous message"); }
            WebUsers.Check(anon, new WebServer { Enabled = false });
            WebUsers.Check(anon, null);

            var role = new ScriptedAccess();
            role.RoleComments[WebUsers.RoleName("op")] = "someone's";
            e = Throws<TiadException>(delegate { WebUsers.Check(role, want); }, "foreign role");
            if (e != null) Eq(e.Message, WebUsers.ForeignRole("op"), "foreign role message");

            var user = new ScriptedAccess();
            user.Users["op"] = "Theirs-pw-1";
            e = Throws<TiadException>(delegate { WebUsers.Check(user, want); }, "foreign user");
            if (e != null) Eq(e.Message, WebUsers.ForeignUser("op"), "foreign user message");
            Eq(anon.Calls.Count + role.Calls.Count + user.Calls.Count, 0, "nothing written");

            // PrepareAll checks before the Full access user's password step
            // writes anything.
            var both = Found("Old-access-1", TestRecords);
            both.Anonymous = true;
            var st = new Station { WebServer = want };
            st.Cpu.AccessControl = StationRules.AccessFullAccessUser;
            var sec = WebSecrets("op", "Op-password-1");
            sec.FullAccessPassword = "New-access-22";
            Throws<TiadException>(delegate { AccessSteps.PrepareAll(both, st, sec, false, new AccessWrites(), new List<string>()); }, "PrepareAll: anonymous");
            Eq(both.Calls.Count, 0, "PrepareAll: refused before the Full access password");
        }

        // WebUsersRunNew: a new web user gets its own marked role holding
        // exactly its rights, a password and tiad's record, and that role
        // alone; what is made is collected.
        static void WebUsersRunNew()
        {
            var c = new ScriptedAccess();
            var made = new AccessMade();
            var notes = new List<string>();
            var want = WebOn(WU("op", "write", "read", "read"), WU("mon", "write"));
            WebUsers.Run(c, want, WebSecrets("op", "Op-password-1", "mon", "Mon-password-1"), made, notes);
            string role = WebUsers.RoleName("op");
            Eq(c.RoleComments[role], WebUsers.RoleComment("op"), "the role is marked");
            Eq(string.Join(",", c.RoleRights(role).ToArray()), "WebReadTags,WebWriteTags", "exactly its rights");
            Eq(c.Users["op"], "Op-password-1", "its password");
            True(TestRecords.Matches(c.Comments["op"], "op", "Op-password-1"), "its record");
            Eq(string.Join(",", ((IProjectUsers)c).UserRoles("op").ToArray()), role, "its role alone");
            Eq(string.Join(",", made.WebUsers.ToArray()), "op,mon", "users made");
            Eq(string.Join(",", made.WebRoles.ToArray()), role + "," + WebUsers.RoleName("mon"), "roles made");
            True(made.Any, "made counts");
            Contains(string.Join("|", notes.ToArray()), WebUsers.MadeNote(want.Users[0]), "made note");
            Contains(string.Join("|", notes.ToArray()), WebUsers.NoReadNote("mon"), "write without read is said");
            True(!string.Join("|", notes.ToArray()).Contains(WebUsers.NoReadNote("op")), "op reads");
            Eq(WebUsers.Ids(WU("x", "mode", "diagnostics")).Count, 2, "mode, diagnostics");
            Eq(string.Join(",", WebUsers.Ids(WU("x", "mode", "diagnostics")).ToArray()), "WebAllowDiagnostics,WebChangeOperatingMode", "their ids");
        }

        // WebUsersRunExact: a found web user is made exact: extra rights on
        // its role revoked (Full access too), missing ones granted, other
        // roles taken off it, its role taken off any other user, the user
        // activated.
        static void WebUsersRunExact()
        {
            var c = Found("Full-access-1", TestRecords);
            WebFound(c, "op", "Op-password-1", "WebReadTags", "WebReadSyslog");
            string role = WebUsers.RoleName("op");
            c.Granted.Add(role);
            c.UserRoles["op"].Add(AccessSteps.RoleName);
            c.UserRoles[AccessSteps.UserName].Add(role);
            c.Inactive.Add("op");
            var notes = new List<string>();
            var made = new AccessMade();
            WebUsers.Run(c, WebOn(WU("op", "read", "write")), WebSecrets("op", "Op-password-1"), made, notes);
            Eq(string.Join(",", c.RoleRights(role).ToArray()), "WebReadTags,WebWriteTags", "exactly its rights");
            Eq(string.Join(",", ((IProjectUsers)c).UserRoles("op").ToArray()), role, "its role alone");
            True(!c.UserRoles[AccessSteps.UserName].Contains(role), "no other user holds it");
            True(c.UserRoles[AccessSteps.UserName].Contains(AccessSteps.RoleName), "the Full access user keeps its own role");
            True(c.UserActive("op"), "activated");
            Eq(c.Users["op"], "Op-password-1", "Run never sets a found user's password");
            True(!made.Any, "nothing made");
            Contains(string.Join("|", notes.ToArray()), WebUsers.FoundNote(WU("op", "read", "write")), "found note");

            // A role found with no rights (its CPU replaced) is granted again.
            var bare = new ScriptedAccess();
            WebFound(bare, "op", "Op-password-1");
            WebUsers.Run(bare, WebOn(WU("op", "read")), WebSecrets("op", "Op-password-1"), new AccessMade(), new List<string>());
            Eq(string.Join(",", bare.RoleRights(role).ToArray()), "WebReadTags", "re-granted");
        }

        // WebUsersStale: a tiad web role no longer wanted is deleted, and
        // its user when it carries a tiad record; a role without tiad's
        // marker, a foreign user, and "nautilus" are never removed.
        static void WebUsersStale()
        {
            var c = Found("Full-access-1", TestRecords);
            WebFound(c, "old", "Old-password-1", "WebReadTags");
            WebFound(c, "op", "Op-password-1", "WebReadTags");
            c.RoleComments[WebUsers.RoleName("theirs")] = "someone's";
            c.RoleComments[WebUsers.RoleName("bare")] = WebUsers.RoleComment("bare");
            c.Users["bare"] = "Bare-password-1";
            // A marked web role for "nautilus" (never made by tiad: the
            // name is refused) must not take the Full access user with it.
            c.RoleComments[WebUsers.RoleName(AccessSteps.UserName)] = WebUsers.RoleComment(AccessSteps.UserName);
            // ... in any case, should TIA's names ignore it.
            c.RoleComments[WebUsers.RoleName("Nautilus")] = WebUsers.RoleComment("Nautilus");
            c.Users["Nautilus"] = "Other-password-1";
            c.Comments["Nautilus"] = TestRecords.Make("Nautilus", "Other-password-1");
            var notes = new List<string>();
            WebUsers.Run(c, WebOn(WU("op", "read")), WebSecrets("op", "Op-password-1"), new AccessMade(), notes);
            True(!c.HasRole(WebUsers.RoleName("old")) && !c.HasUser("old"), "stale user and role removed");
            True(c.HasUser("op") && c.HasRole(WebUsers.RoleName("op")), "the wanted one stays");
            True(c.HasRole(WebUsers.RoleName("theirs")), "an unmarked role stays");
            True(!c.HasRole(WebUsers.RoleName("bare")) && c.HasUser("bare"), "a marked role goes; its user without a record stays");
            True(c.HasUser(AccessSteps.UserName) && c.HasRole(AccessSteps.RoleName), "the Full access user and role stay");
            True(c.HasUser("Nautilus"), "\"Nautilus\" is never deleted either");
            Contains(string.Join("|", notes.ToArray()), WebUsers.RemovedNote("old", true), "said");
            Contains(string.Join("|", notes.ToArray()), WebUsers.RemovedNote("bare", false), "said, role only");

            // Off removes every tiad web user; left out on a kept CPU keeps
            // them; left out on a new CPU removes them.
            var off = new ScriptedAccess();
            WebFound(off, "op", "Op-password-1", "WebReadTags");
            WebSteps.Run(new ScriptedWeb { On = true, Iface = true, Https = true }, off, new WebServer { Enabled = false }, WebSec(), false, new AccessMade(), new List<string>());
            True(!off.HasUser("op") && !off.HasRole(WebUsers.RoleName("op")), "off removes them");
            var kept = new ScriptedAccess();
            WebFound(kept, "op", "Op-password-1", "WebReadTags");
            WebSteps.Run(new ScriptedWeb { On = true, Iface = true, Https = true }, kept, null, WebSec(), false, new AccessMade(), new List<string>());
            True(kept.HasUser("op"), "left out on a kept CPU: as found");
            Eq(kept.Calls.Count, 0, "left out on a kept CPU: nothing written");
            var fresh = new ScriptedAccess();
            WebFound(fresh, "op", "Op-password-1");
            WebSteps.Run(new ScriptedWeb(), fresh, null, WebSec(), true, new AccessMade(), new List<string>());
            True(!fresh.HasUser("op"), "left out on a new CPU: removed with the server off");
        }

        // WebUsersRunRefuses: a right the CPU does not offer refuses before
        // any user or role is written; a foreign user or role found in the
        // station step refuses; a right that does not take refuses on the
        // read back.
        static void WebUsersRunRefuses()
        {
            var c = new ScriptedAccess();
            c.Offered.Remove("WebWriteTags");
            var e = Throws<TiaException>(delegate { WebUsers.Run(c, WebOn(WU("a", "read"), WU("b", "write")), WebSecrets("a", "Aa-password-1", "b", "Bb-password-1"), new AccessMade(), new List<string>()); }, "not offered");
            if (e != null) Contains(e.Message, "the CPU offers no runtime right \"WebWriteTags\" for the web user \"b\"; it offers ", "names it");
            Eq(c.Calls.Count, 0, "nothing written");

            var foreign = new ScriptedAccess();
            foreign.Users["op"] = "Theirs-pw-1";
            e = Throws<TiaException>(delegate { WebUsers.Run(foreign, WebOn(WU("op", "read")), WebSecrets("op", "Op-password-1"), new AccessMade(), new List<string>()); }, "foreign user");
            if (e != null) Eq(e.Message, WebUsers.ForeignUser("op"), "foreign user");
            foreign = new ScriptedAccess();
            foreign.RoleComments[WebUsers.RoleName("op")] = "x";
            e = Throws<TiaException>(delegate { WebUsers.Run(foreign, WebOn(WU("op", "read")), WebSecrets("op", "Op-password-1"), new AccessMade(), new List<string>()); }, "foreign role");
            if (e != null) Eq(e.Message, WebUsers.ForeignRole("op"), "foreign role");

            var ignored = new ScriptedAccess { GrantIgnoredWeb = true };
            e = Throws<TiaException>(delegate { WebUsers.Run(ignored, WebOn(WU("op", "read")), WebSecrets("op", "Op-password-1"), new AccessMade(), new List<string>()); }, "grant ignored");
            if (e != null) Eq(e.Message, "the role \"nautilus web op\" holds the runtime rights [] on the CPU, not exactly [WebReadTags]", "read back");

            // The excess direction: a revoke, or a role taken off, that does
            // not take refuses on the read back too.
            var extra = new ScriptedAccess { RevokeIgnored = true };
            WebFound(extra, "op", "Op-password-1", "WebReadTags", "WebWriteTags");
            e = Throws<TiaException>(delegate { WebUsers.Run(extra, WebOn(WU("op", "read")), WebSecrets("op", "Op-password-1"), new AccessMade(), new List<string>()); }, "revoke ignored");
            if (e != null) Eq(e.Message, "the role \"nautilus web op\" holds the runtime rights [WebReadTags, WebWriteTags] on the CPU, not exactly [WebReadTags]", "revoke read back");
            var held = new ScriptedAccess { RemoveRoleIgnored = true };
            WebFound(held, "op", "Op-password-1", "WebReadTags");
            held.UserRoles["op"].Add("other role");
            e = Throws<TiaException>(delegate { WebUsers.Run(held, WebOn(WU("op", "read")), WebSecrets("op", "Op-password-1"), new AccessMade(), new List<string>()); }, "remove-role ignored");
            if (e != null) Contains(e.Message, "the web user \"op\" holds the roles [", "roles read back");
            var shared = new ScriptedAccess { RemoveRoleIgnored = true };
            WebFound(shared, "op", "Op-password-1", "WebReadTags");
            shared.Users["x"] = "X-password-11";
            shared.UserRoles["x"] = new HashSet<string> { WebUsers.RoleName("op") };
            e = Throws<TiaException>(delegate { WebUsers.Run(shared, WebOn(WU("op", "read")), WebSecrets("op", "Op-password-1"), new AccessMade(), new List<string>()); }, "role on another user");
            if (e != null) Eq(e.Message, "the user \"x\" holds the web role \"nautilus web op\" too", "others read back");

            var w = new ScriptedWeb();
            e = Throws<TiaException>(delegate { WebSteps.Run(w, new ScriptedAccess { Anonymous = true }, WebOn(), WebSec(), true, new AccessMade(), new List<string>()); }, "on with anonymous");
            if (e != null) Eq(e.Message, WebUsers.AnonymousActive, "anonymous");
            Eq(w.Calls.Count, 0, "the server stays off");
        }

        // WebUsersPasswords: a found web user's password is settled before
        // the station step: unchanged when the record matches, else the
        // record first, then the password; a refused one puts the record
        // back; a refusal after an earlier user's was set leaves that one
        // set and says it wrote.
        static void WebUsersPasswords()
        {
            var c = new ScriptedAccess();
            WebFound(c, "a", "Aa-password-1");
            WebFound(c, "b", "Bb-password-1");
            var notes = new List<string>();
            var w = new AccessWrites();
            WebUsers.Passwords(c, WebOn(WU("a"), WU("b"), WU("new")), WebSecrets("a", "Aa-password-1", "b", "Bb-password-2", "new", "New-password-1"), w, notes);
            Eq(Calls(c), "comment,set-password", "only b: the record first");
            Eq(c.Users["b"], "Bb-password-2", "b set");
            True(TestRecords.Matches(c.Comments["b"], "b", "Bb-password-2"), "b recorded");
            Eq(string.Join("|", notes.ToArray()), WebUsers.UnchangedNote("a") + "|" + WebUsers.SetNote("b"), "notes");
            True(w.Wrote, "a set password is a write");

            c.Calls.Clear();
            w = new AccessWrites();
            string hadB = c.Comments["b"];
            var e = Throws<TiadException>(delegate { WebUsers.Passwords(c, WebOn(WU("a"), WU("b")), WebSecrets("a", "Aa-password-2", "b", "Bb-password-1"), w, new List<string>()); }, "b reused");
            if (e != null) { Eq(e.Status, 400, "a refusal"); Eq(e.Message, WebUsers.Reused("b"), "reused"); }
            Eq(c.Users["a"], "Aa-password-2", "a was set before b's refusal");
            True(TestRecords.Matches(c.Comments["a"], "a", "Aa-password-2"), "a's record matches what was set");
            Eq(c.Comments["b"], hadB, "b's record put back");
            True(w.Wrote && !w.Dirty, "wrote, not dirty");

            WebUsers.Passwords(c, null, WebSecrets(), w, notes);
            WebUsers.Passwords(c, new WebServer { Enabled = false }, WebSecrets(), w, notes);
        }

        // WebKeptNotes: a kept CPU's web server left as found is never
        // silent about plain HTTP or an active anonymous user, and a read
        // that fails is logged and said, never failing the apply.
        static void WebKeptNotes()
        {
            var notes = new List<string>();
            WebSteps.Run(new ScriptedWeb { On = true, Iface = true }, new ScriptedAccess(), null, WebSec(), false, new AccessMade(), notes);
            Eq(string.Join("|", notes.ToArray()), WebSteps.PlainHttpNote, "plain HTTP");
            notes.Clear();
            WebSteps.Run(new ScriptedWeb { On = true, Iface = true, Https = true }, new ScriptedAccess { Anonymous = true }, null, WebSec(), false, new AccessMade(), notes);
            Eq(string.Join("|", notes.ToArray()), WebSteps.AnonymousUserNote, "anonymous user");
            notes.Clear();
            WebSteps.Run(new ScriptedWeb { On = true, Iface = true, Https = true }, new ScriptedAccess { AnonymousFails = "unreadable" }, null, WebSec(), false, new AccessMade(), notes);
            Eq(string.Join("|", notes.ToArray()), WebSteps.UnreadNote, "an unreadable anonymous user");
            notes.Clear();
            WebSteps.Run(new ScriptedWeb { Https = true }, new ScriptedAccess { Anonymous = true }, null, WebSec(), false, new AccessMade(), notes);
            Eq(notes.Count, 0, "server off: nothing to say");
        }

        // WebUserNames: the name rule both sides share; duplicates ignore
        // case; users need the server on.
        static void WebUserNames()
        {
            Eq(StationRules.CheckWebUserName("op.1_a-b"), null, "fine");
            Eq(StationRules.CheckWebUserName(new string('a', 32)), null, "32");
            foreach (string bad in new[] { "", "1op", "_op", "op name", "op\n", new string('a', 33), "\u00e9t\u00e9" })
            {
                Contains(StationRules.CheckWebUserName(bad) ?? "", "a web user name is 1 to 32 characters", Show(bad));
            }
            foreach (string r in new[] { "nautilus", "NAUTILUS", "Anonymous", "anonymous" })
            {
                Contains(StationRules.CheckWebUserName(r) ?? "", "is reserved", r);
            }
            var st = new Station { WebServer = WebOn(WU("op", "read"), WU("OP", "read")) };
            Contains(st.Validate() ?? "", "webServer.users: \"OP\" twice (names are compared ignoring case)", "case");
            st = new Station { WebServer = new WebServer { Users = new List<WebUser> { WU("op", "read") } } };
            Contains(st.Validate() ?? "", "webServer.users is set and webServer.enabled is false", "off with users");
        }

        // WebUserSecrets: every web user's password by TIA's rules, each
        // its own secret; only when the server is on.
        static void WebUserSecrets()
        {
            var st = new Station { WebServer = WebOn(WU("a", "read"), WU("b", "read"), WU("c", "read")) };
            st.Cpu.AccessControl = StationRules.AccessKeep;
            var sec = new Secrets { ConfigurationPassword = "Config-pass-1", FullAccessPassword = "Full-access-1" };
            sec.WebUsers["a"] = "short";
            sec.WebUsers["b"] = "Full-access-1";
            string e = sec.CheckFor(st) ?? "";
            Contains(e, "secrets.webUsers[\"a\"] breaks TIA's password rules", "a weak");
            Contains(e, "secrets.webUsers[\"b\"] is the same as secrets.fullAccessPassword: each password is its own secret", "b same as full access");
            Contains(e, "web user c has no password in secrets.webUsers", "c missing");
            True(!e.Contains("Full-access-1"), "never echoes a password");
            sec.WebUsers["a"] = "Same-pass-11";
            sec.WebUsers["b"] = "Config-pass-1";
            sec.WebUsers["c"] = "Same-pass-11";
            e = sec.CheckFor(st) ?? "";
            Contains(e, "secrets.webUsers[\"b\"] is the same as secrets.configurationPassword", "b same as config");
            Contains(e, "secrets.webUsers[\"c\"] is the same as secrets.webUsers[\"a\"]", "c same as a");
            st.WebServer.Enabled = false;
            st.WebServer.Users.Clear();
            Eq(sec.CheckFor(st), null, "off: not checked");
        }

        // AccessRemoveWeb: an upload removes the web users and roles the
        // session made, and nothing else.
        static void AccessRemoveWeb()
        {
            var c = Found("Full-access-1", TestRecords);
            WebFound(c, "op", "Op-password-1", "WebReadTags");
            WebFound(c, "theirs", "Their-password-1", "WebReadTags");
            var made = new AccessMade();
            made.WebUsers.Add("op");
            made.WebRoles.Add(WebUsers.RoleName("op"));
            made.WebUsers.Add("gone");
            var removed = AccessSteps.Remove(c, made);
            Eq(string.Join(",", removed.ToArray()), "user \"op\",role \"nautilus web op\"", "removed");
            True(c.HasUser("theirs") && c.HasUser(AccessSteps.UserName) && c.HasRole(WebUsers.RoleName("theirs")), "nothing else");
        }
    }
}
