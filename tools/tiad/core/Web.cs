// Web.cs: the CPU's web server (webServer, siemens-ide.md section 3.3,
// #789 stage 5 (A) step 2). tiad turns it on only on a CPU whose access
// control is on: with access control off, a FW 3.1 CPU's web server knows
// one user, "Anonymous", with full rights over the CPU (S7-1500 Web server
// manual, 11/2023), so anyone who reaches it could write tags and stop the
// CPU. Its users are the project's local users, each with a role of its own
// holding exactly its web rights on the CPU (step 2B).

using System;
using System.Collections.Generic;

namespace Tiad
{
    // IWebServer is the CPU's web server as TIA's Openness offers it: the
    // CPU's WebserverActivate and WebserverHttpsOnly, and its PROFINET
    // interface's UseWebServerForInterface. tiad.Openness wraps TIA's; the
    // fakes model it.
    public interface IWebServer
    {
        // Server is the CPU's WebserverActivate.
        bool Server();
        // Interface is UseWebServerForInterface on the interface that
        // carries the CPU's address.
        bool Interface();
        // HttpsOnly is the CPU's WebserverHttpsOnly (TIA offers it only
        // while the server is on).
        bool HttpsOnly();
        // Set writes the switches that differ: on turns the server on,
        // HTTPS only, then the interface; off turns the interface off,
        // then the server.
        void Set(bool on);
    }

    // WebRule decides whether the web server may be on.
    public static class WebRule
    {
        // Refusal is why a web server cannot be turned on over access
        // control in state (PlcAccessControlConfiguration's name), or null:
        // only EnabledWithAccessControl lets it. Absent (a CPU without the
        // setting) and any other state refuse.
        public static string Refusal(string state)
        {
            if (state == AccessRule.Enabled) return null;
            string what = state == AccessRule.Absent ? "not available (no such setting)" : "set to " + Json.Quote(state);
            return "webServer.enabled is true, and the CPU's access control is " + what +
                ": with access control off, a FW 3.1 CPU's web server lets anyone in as \"Anonymous\" with full rights;" +
                " turn access control on (a CPU tiad makes has it on), or set webServer.enabled to false";
        }

        // Plan refuses, before any write, a web server the apply would turn
        // on over a kept CPU's access control. current is the kept CPU's
        // state, or null for a CPU the apply makes (WebSteps.Run checks
        // that one once its access control is applied). Turning the server
        // off, or leaving it as found, is never refused.
        public static void Plan(string current, WebServer want)
        {
            if (want == null || !want.Enabled || current == null) return;
            string why = Refusal(current);
            if (why != null) throw TiadException.BadRequest("{0}", why);
        }
    }

    // WebSteps applies webServer in the station step, after
    // AccessSteps.Run.
    public static class WebSteps
    {
        // Run makes the web server and its users what want says and reads
        // them back. want null leaves a kept CPU's web server and users as
        // found and turns a new CPU's off. Turning it on reads the CPU's
        // access control first, as AccessSteps left it, and refuses
        // anything but EnabledWithAccessControl, and an active anonymous
        // user, before writing; the users (WebUsers.Run) come before the
        // switch.
        public static void Run(IWebServer w, IAccessControl c, WebServer want, Secrets sec, bool madeCpu, AccessMade made, List<string> notes)
        {
            if (want == null && !madeCpu)
            {
                KeptNotes(w, c, notes);
                return;
            }
            bool on = want != null && want.Enabled;
            if (on)
            {
                string why = WebRule.Refusal(c.State());
                if (why != null) throw new TiaException(why);
                if (c.AnonymousActive()) throw new TiaException(WebUsers.AnonymousActive);
            }
            WebUsers.Run(c, want, sec, made, notes);
            // HttpsOnly is offered only while the server is on.
            bool was = w.Server(), same = was == on && w.Interface() == on && (!on || w.HttpsOnly());
            if (!same) made.Writes++;
            w.Set(on);
            bool server = w.Server(), iface = w.Interface();
            if (server != on || iface != on)
            {
                throw new TiaException("after turning the web server " + (on ? "on" : "off") + " the CPU reads WebserverActivate " +
                    (server ? "true" : "false") + " and UseWebServerForInterface " + (iface ? "true" : "false"));
            }
            // Every CPU tiad serves a web server on has a user with a
            // password (the Full access user at least): never over plain
            // HTTP.
            if (on && !w.HttpsOnly()) throw new TiaException("after turning the web server on the CPU reads WebserverHttpsOnly false");
            Log.Line("web server: {0}", on ? "on" : "off");
            if (on) notes.Add(OnNote);
        }

        // KeptNotes says what is worth knowing about a kept CPU's web
        // server left as found, never silently and never failing the
        // apply: a read it cannot make is logged and said.
        static void KeptNotes(IWebServer w, IAccessControl c, List<string> notes)
        {
            string state;
            try
            {
                if (!w.Server()) return;
                state = c.State();
            }
            catch (Exception e)
            {
                Log.Line("WARNING web server: reading the kept CPU's web server failed ({0}); left as found (webServer left out)", Json.Quote(TiaNames.Message(e)));
                notes.Add(UnreadNote);
                return;
            }
            // A kept CPU serving the web over access control off serves
            // "Anonymous" with full rights, and a download would load that.
            if (WebRule.Refusal(state) != null)
            {
                Log.Line("WARNING web server: the kept CPU's web server is on over access control {0}, left as found (webServer left out)", Json.Quote(state));
                notes.Add(AnonymousNote);
                return;
            }
            try
            {
                if (!w.HttpsOnly())
                {
                    Log.Line("WARNING web server: the kept CPU's web server is on and allows plain HTTP, left as found (webServer left out)");
                    notes.Add(PlainHttpNote);
                }
            }
            catch (Exception e)
            {
                Log.Line("WARNING web server: reading the kept CPU's WebserverHttpsOnly failed ({0})", Json.Quote(TiaNames.Message(e)));
                notes.Add(UnreadNote);
            }
            try
            {
                if (c.AnonymousActive())
                {
                    Log.Line("WARNING web server: the kept CPU's web server is on and the project's anonymous user is active, left as found (webServer left out)");
                    notes.Add(AnonymousUserNote);
                }
            }
            catch (Exception e)
            {
                Log.Line("WARNING web server: reading the project's anonymous user failed ({0})", Json.Quote(TiaNames.Message(e)));
                notes.Add(UnreadNote);
            }
        }

        public const string UnreadNote = "WARNING: the kept CPU's web server could not be read in full (see the agent's log); webServer is left out, so tiad leaves it as found";

        public const string PlainHttpNote = "WARNING: the kept CPU's web server is on and allows plain HTTP, so web passwords may cross the network in clear; webServer is left out, so tiad leaves it as found: set webServer.enabled to true (HTTPS only) or false";

        public const string AnonymousUserNote = "WARNING: the kept CPU's web server is on and the project's anonymous user is active, so anyone who reaches it gets the rights of the anonymous user's roles; webServer is left out, so tiad leaves it as found: deactivate the anonymous user in TIA, or set webServer.enabled to false";

        public const string AnonymousNote = "WARNING: the kept CPU's web server is on and its access control is not on, so it lets anyone in as \"Anonymous\" with full rights; webServer is left out, so tiad leaves it as found, and a download loads it so: set webServer.enabled to false to turn it off";

        public const string OnNote = "the web server is on, HTTPS only, over access control EnabledWithAccessControl: a web client may do what the runtime rights of its user's roles on this CPU allow";
    }
}

namespace Tiad
{
    // WebUsers applies webServer.users: each web user is a local user of
    // the project, its password from secrets.webUsers (with tiad's record
    // in its comment, AccessRecords), holding one role, "nautilus web
    // <name>", made by tiad and marked by its comment, whose runtime rights
    // on the CPU are exactly the user's web rights. A web user or role of
    // that name tiad did not make is never taken over.
    public static class WebUsers
    {
        public const string RolePrefix = "nautilus web ";
        const string RoleCommentPrefix = "made by nautilus: the web server rights of the user ";

        public static string RoleName(string user)
        {
            return RolePrefix + user;
        }

        // RoleComment marks the role tiad made for user: the only way tiad
        // tells its web roles, so a stale one is ever removed.
        public static string RoleComment(string user)
        {
            return RoleCommentPrefix + Json.Quote(user);
        }

        // Rights maps a spec's web rights to the CPU's runtime rights (their
        // identifiers as the TIA machine listed them, 2026-11-22). Nothing
        // is implied: write does not give read.
        public static readonly Dictionary<string, string> Rights = new Dictionary<string, string>(StringComparer.Ordinal)
        {
            { "read", "WebReadTags" },
            { "write", "WebWriteTags" },
            { "mode", "WebChangeOperatingMode" },
            { "diagnostics", "WebAllowDiagnostics" },
        };

        // Ids is the runtime rights u's role holds, sorted.
        public static List<string> Ids(WebUser u)
        {
            var set = new SortedSet<string>(StringComparer.Ordinal);
            foreach (string r in u.Rights) set.Add(Rights[r]);
            return new List<string>(set);
        }

        public const string AnonymousActive = "webServer.enabled is true, and the project's anonymous user is active: anyone who reaches the web server would get the rights of its roles; deactivate the anonymous user in TIA, or set webServer.enabled to false";

        public static string ForeignRole(string user)
        {
            return "the project holds a role " + Json.Quote(RoleName(user)) + " that tiad did not make, and tiad does not give web rights through a role it did not make: rename or delete it in TIA, or leave the web user " + Json.Quote(user) + " out";
        }

        public static string ForeignUser(string user)
        {
            return "the project holds a user " + Json.Quote(user) + " that carries no tiad record, so tiad did not make it and does not take it over: rename or delete it in TIA, or name the web user otherwise";
        }

        static string Secret(string user)
        {
            return "secrets.webUsers[" + Json.Quote(user) + "]";
        }

        public static string Reused(string user)
        {
            return "TIA refuses " + Secret(user) + " for the web user " + Json.Quote(user) + ": the user had that password recently, and TIA blocks a user's recent passwords for reuse: choose another password, or make the project again (POST /v1/projects)";
        }

        public static string ReusedOtherKey(string user)
        {
            return "TIA refuses " + Secret(user) + " for the web user " + Json.Quote(user) + " as a password the user has now or had recently, and the project's record of it was made under another agent key, so tiad cannot tell which; web users have no record-only route, so the password must change: set a new one in " + Secret(user) + " and apply again (or make the project again, POST /v1/projects)";
        }

        public static string MadeNote(WebUser u)
        {
            return "made the web user " + Json.Quote(u.Name) + " (role " + Json.Quote(RoleName(u.Name)) + ", runtime rights " + Describe(u) + "), its password from " + Secret(u.Name);
        }

        public static string FoundNote(WebUser u)
        {
            return "the web user " + Json.Quote(u.Name) + " holds exactly the role " + Json.Quote(RoleName(u.Name)) + ", runtime rights " + Describe(u);
        }

        public static string UnchangedNote(string user)
        {
            return "the web user " + Json.Quote(user) + "'s password is unchanged: " + Secret(user) + " is the one tiad last set in this project (a change made in TIA since would not show)";
        }

        public static string SetNote(string user)
        {
            return "the web user " + Json.Quote(user) + "'s password is now " + Secret(user);
        }

        public static string RemovedNote(string user, bool userToo)
        {
            return "removed " + (userToo ? "the web user " + Json.Quote(user) + " and " : "") + "its role " + Json.Quote(RoleName(user)) + ": no longer in webServer.users";
        }

        public static string NoReadNote(string user)
        {
            return "the web user " + Json.Quote(user) + " may write tags or change the operating mode but not read tags (no \"read\" right): add \"read\" to its rights unless that is meant";
        }

        static string Describe(WebUser u)
        {
            var ids = Ids(u);
            return ids.Count == 0 ? "none" : string.Join(", ", ids.ToArray());
        }

        // UserOfRole is the user a tiad web role is for, or null for a
        // role tiad did not make as a web role.
        static string UserOfRole(IProjectUsers u, string role)
        {
            if (!role.StartsWith(RolePrefix, StringComparison.Ordinal)) return null;
            string user = role.Substring(RolePrefix.Length);
            return u.RoleComment(role) == RoleComment(user) ? user : null;
        }

        // Check refuses, before any write of the apply, what it can without
        // writing: an active anonymous user while the server is to be on,
        // and a web user or role of a wanted name that tiad did not make.
        public static void Check(IProjectUsers u, WebServer want)
        {
            if (want == null || !want.Enabled) return;
            if (u.AnonymousActive()) throw TiadException.BadRequest("{0}", AnonymousActive);
            foreach (var wu in want.Users)
            {
                string role = RoleName(wu.Name);
                if (u.HasRole(role) && u.RoleComment(role) != RoleComment(wu.Name)) throw TiadException.BadRequest("{0}", ForeignRole(wu.Name));
                if (u.HasUser(wu.Name) && !AccessRecords.IsRecord(u.Comment(wu.Name))) throw TiadException.BadRequest("{0}", ForeignUser(wu.Name));
            }
        }

        // Passwords settles a found web user's password, before the station
        // step, as AccessSteps.Prepare does the Full access user's: the
        // record first, put back when TIA refuses the password. A refusal
        // after an earlier user's password was set leaves that one set
        // (w.Wrote): its record matches it, so the project stays
        // consistent.
        public static void Passwords(IProjectUsers u, WebServer want, Secrets sec, AccessWrites w, List<string> notes)
        {
            if (want == null || !want.Enabled) return;
            AccessRecords records = sec.Records;
            if (records == null) throw new TiaException("no access record key: the agent sets one for every apply");
            foreach (var wu in want.Users)
            {
                if (!u.HasUser(wu.Name)) continue;
                string pw = sec.WebUsers[wu.Name];
                string had = u.Comment(wu.Name);
                if (records.Matches(had, wu.Name, pw))
                {
                    notes.Add(UnchangedNote(wu.Name));
                    continue;
                }
                bool ours = records.Ours(had);
                u.SetComment(wu.Name, records.Make(wu.Name, pw));
                try
                {
                    u.SetPassword(wu.Name, pw);
                }
                catch (PasswordReusedException e)
                {
                    AccessSteps.PutBack(u, wu.Name, had, w);
                    string why = ours ? Reused(wu.Name) : ReusedOtherKey(wu.Name);
                    if (w.Dirty) throw new TiaException(why + "; putting the user's record back failed too: close this session without saving", e);
                    throw TiadException.BadRequest("{0}", why);
                }
                catch (Exception)
                {
                    AccessSteps.PutBack(u, wu.Name, had, w);
                    throw;
                }
                w.Wrote = true;
                notes.Add(SetNote(wu.Name));
            }
        }

        // Run makes the project's web users what want says, in the station
        // step, before the web server's switch. want null keeps them (a
        // kept CPU's web server left as found) on a kept CPU and removes
        // tiad's on a new one, whose server goes off. A web role tiad made
        // (by its comment) for a user no longer wanted is deleted, and so
        // is that user when it carries a tiad record; nothing else is ever
        // removed. into collects what this run made as soon as it is made.
        // A found user's password is Passwords', never Run's.
        public static void Run(IAccessControl c, WebServer want, Secrets sec, AccessMade into, List<string> notes)
        {
            var wanted = want != null && want.Enabled ? want.Users : new List<WebUser>();
            // TIA's names may ignore case: so do the keep set and the
            // guard for the Full access user.
            var keep = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (var wu in wanted) keep.Add(RoleName(wu.Name));
            var roles = c.RoleNames();
            roles.Sort(StringComparer.Ordinal);
            foreach (string role in roles)
            {
                string user = UserOfRole(c, role);
                if (user == null || keep.Contains(role)) continue;
                c.DeleteRole(role);
                into.Writes++;
                bool userToo = !string.Equals(user, AccessSteps.UserName, StringComparison.OrdinalIgnoreCase) && c.HasUser(user) && AccessRecords.IsRecord(c.Comment(user));
                if (userToo) c.DeleteUser(user);
                notes.Add(RemovedNote(user, userToo));
                Log.Line("web users: removed {0}role {1}", userToo ? "user " + Json.Quote(user) + " and " : "", Json.Quote(role));
            }
            if (wanted.Count == 0) return;
            AccessRecords records = sec.Records;
            if (records == null) throw new TiaException("no access record key: the agent sets one for every apply");
            // Every runtime right resolved before the first user or role.
            var offered = c.OfferedRights();
            foreach (var wu in wanted)
            {
                foreach (string id in Ids(wu))
                {
                    if (!offered.Contains(id))
                    {
                        throw new TiaException("the CPU offers no runtime right " + Json.Quote(id) + " for the web user " + Json.Quote(wu.Name) + "; it offers " + string.Join(", ", offered.ToArray()));
                    }
                }
            }
            foreach (var wu in wanted)
            {
                string role = RoleName(wu.Name);
                if (!c.HasRole(role))
                {
                    c.MakeRole(role, RoleComment(wu.Name));
                    into.WebRoles.Add(role);
                    into.Writes++;
                }
                else if (c.RoleComment(role) != RoleComment(wu.Name))
                {
                    throw new TiaException(ForeignRole(wu.Name));
                }
                var ids = Ids(wu);
                var have = c.RoleRights(role);
                foreach (string id in ids)
                {
                    if (!have.Contains(id))
                    {
                        c.Grant(role, id);
                        into.Writes++;
                    }
                }
                foreach (string id in have)
                {
                    if (!ids.Contains(id))
                    {
                        c.Revoke(role, id);
                        into.Writes++;
                    }
                }
                bool madeUser = !c.HasUser(wu.Name);
                if (madeUser)
                {
                    string pw = sec.WebUsers[wu.Name];
                    c.MakeUser(wu.Name, pw);
                    into.WebUsers.Add(wu.Name);
                    into.Writes++;
                    c.SetComment(wu.Name, records.Make(wu.Name, pw));
                    notes.Add(MadeNote(wu));
                }
                else if (!AccessRecords.IsRecord(c.Comment(wu.Name)))
                {
                    throw new TiaException(ForeignUser(wu.Name));
                }
                else
                {
                    notes.Add(FoundNote(wu));
                }
                if (!c.UserActive(wu.Name))
                {
                    c.ActivateUser(wu.Name);
                    into.Writes++;
                }
                // Exactly its own role: the user's rights are its role's.
                bool had = false;
                foreach (string r in c.UserRoles(wu.Name))
                {
                    if (r == role) had = true;
                    else
                    {
                        c.RemoveRole(wu.Name, r);
                        into.Writes++;
                    }
                }
                c.AddRole(wu.Name, role);
                if (!had) into.Writes++;
                // And the role is its user's alone.
                foreach (string other in c.UserNames())
                {
                    if (other != wu.Name && c.UserRoles(other).Contains(role))
                    {
                        c.RemoveRole(other, role);
                        into.Writes++;
                    }
                }
                if (!wu.Rights.Contains("read") && (wu.Rights.Contains("write") || wu.Rights.Contains("mode"))) notes.Add(NoReadNote(wu.Name));
            }
            foreach (var wu in wanted)
            {
                string bad = ReadBack(c, wu);
                if (bad != null) throw new TiaException(bad);
            }
            Log.Line("web users: {0}", wanted.Count);
        }

        // ReadBack returns why the project does not hold u as Run promises,
        // or null.
        public static string ReadBack(IAccessControl c, WebUser u)
        {
            string role = RoleName(u.Name);
            if (!c.HasUser(u.Name)) return "the web user " + Json.Quote(u.Name) + " is not in the project";
            if (!c.UserActive(u.Name)) return "the web user " + Json.Quote(u.Name) + " is not active";
            var roles = c.UserRoles(u.Name);
            if (roles.Count != 1 || roles[0] != role)
            {
                return "the web user " + Json.Quote(u.Name) + " holds the roles [" + string.Join(", ", roles.ToArray()) + "], not exactly " + Json.Quote(role);
            }
            foreach (string other in c.UserNames())
            {
                if (other != u.Name && c.UserRoles(other).Contains(role)) return "the user " + Json.Quote(other) + " holds the web role " + Json.Quote(role) + " too";
            }
            var have = new SortedSet<string>(c.RoleRights(role), StringComparer.Ordinal);
            var want = Ids(u);
            if (have.Count != want.Count || !have.SetEquals(want))
            {
                return "the role " + Json.Quote(role) + " holds the runtime rights [" + string.Join(", ", new List<string>(have).ToArray()) + "] on the CPU, not exactly [" + string.Join(", ", want.ToArray()) + "]";
            }
            return null;
        }
    }
}
