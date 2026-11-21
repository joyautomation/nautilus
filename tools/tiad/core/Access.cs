// Access.cs: the CPU's PLC access control, users and roles
// (cpu.accessControl, siemens-ide.md section 3.3, #789 decision of
// 2026-11-20). A station nautilus creates has access control on and one
// user, "nautilus", with the runtime right Full access; its password comes
// from the secret store (secrets.fullAccessPassword). An adopted station
// keeps what it has.

using System;
using System.Collections.Generic;
using System.Security.Cryptography;
using System.Text;

namespace Tiad
{
    // PasswordPolicy is a project's rules for local users' passwords
    // (TIA's PasswordPolicyConfigurator). TIA's default is
    // StationRules.CheckTiaPassword's: 10 characters, one digit, upper and
    // lower case, no special character.
    public sealed class PasswordPolicy
    {
        public int MinLength = StationRules.MinTiaPassword;
        public int MinDigits = 1;
        public int MinSpecial;
        public bool UpperAndLower = true;

        // Check returns why password breaks the policy, naming every rule
        // broken, or null. It never echoes the password. Lengths are in
        // UTF-16 code units (a .NET string's), the classes ASCII; a special
        // character is ASCII punctuation only, so a character TIA might not
        // count never counts here (stricter than TIA, never looser).
        public string Check(string password)
        {
            var broken = new List<string>();
            if (password.Length < MinLength) broken.Add("shorter than " + MinLength + " characters");
            if (password.Length > StationRules.MaxTiaPassword) broken.Add("longer than " + StationRules.MaxTiaPassword + " characters");
            int digits = 0, special = 0;
            bool upper = false, lower = false;
            foreach (char r in password)
            {
                if (r >= '0' && r <= '9') digits++;
                else if (r >= 'A' && r <= 'Z') upper = true;
                else if (r >= 'a' && r <= 'z') lower = true;
                else if (r > ' ' && r < 127) special++;
            }
            if (digits < MinDigits) broken.Add("fewer than " + MinDigits + " digit(s)");
            if (special < MinSpecial) broken.Add("fewer than " + MinSpecial + " special character(s)");
            if (UpperAndLower && !(upper && lower)) broken.Add("not both an upper-case and a lower-case letter");
            if (broken.Count == 0) return null;
            return "breaks the project's password policy for local users (" + Describe() + "): " + string.Join(", ", broken.ToArray());
        }

        public string Describe()
        {
            string s = "at least " + MinLength + " characters, " + MinDigits + " digit(s), " + MinSpecial + " special character(s)";
            return UpperAndLower ? s + ", upper and lower case" : s;
        }
    }

    // AccessMade is what applies on one session made in the project's
    // users and roles: the user and the role, by AccessSteps' names. An
    // upload on the same session removes exactly these before it brings
    // the CPU's station in, so an adopted project holds no user tiad made
    // for the throwaway station a create started from; a user or role the
    // project held already is never removed.
    public sealed class AccessMade
    {
        public bool User, Role;

        public bool Any { get { return User || Role; } }
    }

    // PasswordReusedException is TIA refusing a password the user has had
    // (its current one, or one its project's policy still blocks).
    public sealed class PasswordReusedException : Exception
    {
        public PasswordReusedException(Exception inner) : base("TIA refuses a password the user has had", inner)
        {
        }
    }

    // AccessWrites says whether the password step left a project-level
    // write it could not undo: then the session is unclean, and an upload,
    // which replaces devices but not users, cannot clean it.
    public sealed class AccessWrites
    {
        public bool Dirty;
    }

    // AccessRecords is tiad's record of the Full access password it set or
    // recorded, kept in the user's comment in the project (so it travels
    // with the project), never the password: "tiad password record
    // v2:<key id>:<salt>:<HMAC-SHA256(key, salt, user, password)>". The key
    // is the agent's own random key (TIAD_ACCESS_KEY_FILE, made by the
    // installer and kept across reinstalls), not its token; the key id
    // tells this agent's records from another key's; the salt is random per
    // record, so one password gives a different record in every project.
    public sealed class AccessRecords
    {
        const string Prefix = "tiad password record v2:";
        readonly byte[] key;
        readonly string keyId;

        public AccessRecords(byte[] key)
        {
            if (key == null || key.Length != 32) throw new ArgumentException("an access record key is 32 bytes");
            this.key = (byte[])key.Clone();
            using (var h = SHA256.Create()) keyId = Hex(h.ComputeHash(this.key), 4);
        }

        // FromHex reads a key file's text: 64 hex characters.
        public static AccessRecords FromHex(string text)
        {
            string t = text.Trim();
            if (t.Length != 64) throw new ArgumentException("the access key is not 64 hex characters");
            var k = new byte[32];
            for (int i = 0; i < 32; i++) k[i] = Convert.ToByte(t.Substring(2 * i, 2), 16);
            return new AccessRecords(k);
        }

        // Random is a key for this run only (no key file): its records do
        // not verify after a restart.
        public static AccessRecords Random()
        {
            var k = new byte[32];
            using (var r = new RNGCryptoServiceProvider()) r.GetBytes(k);
            return new AccessRecords(k);
        }

        public string Make(string user, string password)
        {
            var salt = new byte[16];
            using (var r = new RNGCryptoServiceProvider()) r.GetBytes(salt);
            return Make(user, password, salt);
        }

        // Make with a given salt: for the known-answer test.
        public string Make(string user, string password, byte[] salt)
        {
            return Prefix + keyId + ":" + Hex(salt, salt.Length) + ":" + Mac(salt, user, password);
        }

        string Mac(byte[] salt, string user, string password)
        {
            byte[] msg = Encoding.UTF8.GetBytes(user + "\n" + password);
            var all = new byte[salt.Length + msg.Length];
            Buffer.BlockCopy(salt, 0, all, 0, salt.Length);
            Buffer.BlockCopy(msg, 0, all, salt.Length, msg.Length);
            using (var h = new HMACSHA256(key))
            {
                byte[] d = h.ComputeHash(all);
                return Hex(d, d.Length);
            }
        }

        // Parts splits a record: key id, salt, MAC; or null for a comment
        // that is no record of tiad's.
        static string[] Parts(string comment)
        {
            if (comment == null || !comment.StartsWith(Prefix, StringComparison.Ordinal)) return null;
            string[] p = comment.Substring(Prefix.Length).Split(':');
            if (p.Length != 3 || p[0].Length != 8 || p[1].Length != 32 || p[2].Length != 64) return null;
            return p;
        }

        public static bool IsRecord(string comment)
        {
            return Parts(comment) != null;
        }

        // Ours reports whether a record was made with this agent's key.
        public bool Ours(string comment)
        {
            string[] p = Parts(comment);
            return p != null && p[0] == keyId;
        }

        // Matches reports whether a record of this agent's key is of user
        // and password.
        public bool Matches(string comment, string user, string password)
        {
            string[] p = Parts(comment);
            if (p == null || p[0] != keyId) return false;
            var salt = new byte[16];
            for (int i = 0; i < 16; i++) salt[i] = Convert.ToByte(p[1].Substring(2 * i, 2), 16);
            string want = Mac(salt, user, password);
            int diff = 0;
            for (int i = 0; i < want.Length; i++) diff |= want[i] ^ p[2][i];
            return diff == 0;
        }

        static string Hex(byte[] d, int n)
        {
            var sb = new StringBuilder();
            for (int i = 0; i < n; i++) sb.Append(d[i].ToString("x2", System.Globalization.CultureInfo.InvariantCulture));
            return sb.ToString();
        }
    }

    // IProjectUsers is the project's users and roles as TIA's Openness
    // offers them (UmacConfigurator): the project's, not a CPU's, so they
    // outlive any device delete. Passwords cross as strings: the glue makes
    // TIA's SecureString.
    public interface IProjectUsers
    {
        bool HasUser(string user);
        // MakeUser makes the local user with password.
        void MakeUser(string user, string password);
        // SetPassword sets a found user's password. TIA refuses any
        // password the user has had (its current one included, as many as
        // the project's policy blocks for reuse): then it throws
        // PasswordReusedException.
        void SetPassword(string user, string password);
        // Comment and SetComment are the user's comment, where tiad keeps
        // its record (AccessRecords).
        string Comment(string user);
        void SetComment(string user, string comment);
        bool HasRole(string role);
        // RoleComment is a custom role's comment: tiad's own roles carry
        // AccessSteps.RoleComment.
        string RoleComment(string role);
        void MakeRole(string role, string comment);
        // AddRole assigns role to user, unless it has it.
        void AddRole(string user, string role);
    }

    // IAccessControl is a CPU's access control besides the project's users
    // and roles (PlcAccessControlConfiguration provider, UmacDevice).
    // tiad.Openness wraps TIA's; the fakes model it.
    public interface IAccessControl : IProjectUsers
    {
        // State is PlcAccessControlConfiguration's name, or
        // AccessRule.Absent for a CPU without the setting.
        string State();
        // Enable sets PlcAccessControlConfiguration to
        // EnabledWithAccessControl.
        void Enable();
        // GrantFullAccess gives role the CPU's runtime right Full access,
        // unless it has it.
        void GrantFullAccess(string role);
        // Check returns why user does not hold the runtime right Full access
        // on this CPU through role, or null.
        string Check(string user, string role);
    }

    // AccessRule decides what an apply does about cpu.accessControl.
    public static class AccessRule
    {
        // States are PlcAccessControlConfiguration's names. Absent is a CPU
        // without the setting.
        public const string Enabled = "EnabledWithAccessControl", Absent = "";

        // Plan refuses, before any write, what an apply cannot do: a Full
        // access user needs its password, and on a CPU the apply keeps
        // (reuse), access control must already be on, since tiad never
        // turns it on for a CPU it did not make. keep never refuses: it
        // changes nothing, and a new CPU without a Full access user fails
        // its compile, which says why. current is the kept CPU's state, or
        // null for a CPU the apply makes.
        public static void Plan(string current, string want, bool havePassword)
        {
            if (want == StationRules.AccessKeep) return;
            if (want != StationRules.AccessFullAccessUser)
            {
                throw TiadException.BadRequest("cpu.accessControl: {0}", StationRules.CheckAccessControl(want) ?? Json.Quote(want));
            }
            if (!havePassword)
            {
                throw TiadException.BadRequest("{0}", "cpu.accessControl is fullAccessUser, and secrets.fullAccessPassword is empty (it comes from the secret store, never the spec)");
            }
            if (current == null || current == Enabled) return;
            throw TiadException.BadRequest(
                "the project's CPU has its access control {0}, and tiad turns access control on only for a CPU it makes: to keep the CPU as found, set cpu.accessControl to keep",
                current == Absent ? "not available (no such setting)" : "set to " + Json.Quote(current));
        }
    }

    // AccessSteps applies cpu.accessControl fullAccessUser in two steps.
    // Prepare, before any write of the apply, settles a found user's
    // password and refuses what it cannot take over; Run, in the station
    // step, sets access control on (only for a CPU the apply made), makes
    // the role and the user when missing, grants Full access, and reads
    // back: anything but the promised state refuses.
    public static class AccessSteps
    {
        public const string UserName = "nautilus", RoleName = "nautilus full access";

        // RoleComment marks a role tiad made.
        public const string RoleComment = "made by nautilus: the runtime right Full access";

        public const string MadeNote = "made the user \"nautilus\" with the runtime right Full access (role \"nautilus full access\"), its password from secrets.fullAccessPassword";

        // UnchangedNote, SetNote, RecordedNote and TakenOverNote are what an
        // apply says about a user the project held.
        public const string UnchangedNote = "the project's user \"nautilus\" has the runtime right Full access (role \"nautilus full access\"); its password is unchanged: secrets.fullAccessPassword is the one tiad last set or recorded in this project (a change made in TIA since would not show)";
        public const string SetNote = "the project's user \"nautilus\" has the runtime right Full access (role \"nautilus full access\"); its password is now secrets.fullAccessPassword";
        public const string RecordedNote = "the project's user \"nautilus\" has the runtime right Full access (role \"nautilus full access\"); its password was not set: on fullAccessRecordOnly, tiad now records secrets.fullAccessPassword as the user's password, which it cannot check offline";
        public const string TakenOverNote = "the project's user \"nautilus\", which carried no tiad record, is taken over on fullAccessRecordOnly: its comment is now tiad's record of secrets.fullAccessPassword as its password, which tiad did not set and cannot check offline, and it gets the runtime right Full access (role \"nautilus full access\")";

        // Reused, ReusedOtherKey, ForeignUser and ForeignRole are the
        // refusals; each leaves the project as it was.
        public const string Reused = "TIA refuses secrets.fullAccessPassword for the user \"nautilus\": the user had that password recently, and TIA blocks a user's recent passwords for reuse: choose another password, or make the project again (POST /v1/projects)";
        public const string ReusedOtherKey = "TIA refuses secrets.fullAccessPassword for the user \"nautilus\" as a password the user has now or had recently, and the project's record of it was made under another agent key, so tiad cannot tell which: if the password did not change, apply again with fullAccessRecordOnly to record it under this agent's key without setting it (journalled); otherwise choose another password, or make the project again (POST /v1/projects)";
        public const string ForeignUser = "the project holds a user \"nautilus\" that carries no tiad record, so tiad did not make it and does not take it over unasked: set cpu.accessControl to keep, or apply with fullAccessRecordOnly to take it over with secrets.fullAccessPassword recorded as its password (journalled; tiad does not set it and cannot check it)";
        public const string ForeignRole = "the project holds a role \"nautilus full access\" that tiad did not make, and tiad does not grant Full access to a role it did not make: set cpu.accessControl to keep";

        // Prepare runs before any write of the apply. A refusal leaves the
        // project as it was: a new record is written before the password
        // and put back when TIA refuses it; w.Dirty says when putting it
        // back failed.
        public static void Prepare(IProjectUsers u, string want, string password, AccessRecords records, bool recordOnly, AccessWrites w, List<string> notes)
        {
            if (want != StationRules.AccessFullAccessUser) return;
            if (records == null) throw new TiaException("no access record key: the agent sets one for every apply");
            if (u.HasRole(RoleName) && u.RoleComment(RoleName) != RoleComment) throw TiadException.BadRequest("{0}", ForeignRole);
            if (!u.HasUser(UserName)) return;
            string had = u.Comment(UserName);
            if (!AccessRecords.IsRecord(had))
            {
                if (!recordOnly) throw TiadException.BadRequest("{0}", ForeignUser);
                u.SetComment(UserName, records.Make(UserName, password));
                notes.Add(TakenOverNote);
                Log.Line("audit access-record user={0}: taken over, secrets.fullAccessPassword recorded as its password without setting it (fullAccessRecordOnly)", Json.Quote(UserName));
                return;
            }
            if (records.Matches(had, UserName, password))
            {
                notes.Add(UnchangedNote);
                return;
            }
            if (recordOnly)
            {
                u.SetComment(UserName, records.Make(UserName, password));
                notes.Add(RecordedNote);
                Log.Line("audit access-record user={0}: secrets.fullAccessPassword recorded as its password without setting it (fullAccessRecordOnly)", Json.Quote(UserName));
                return;
            }
            bool ours = records.Ours(had);
            // The record first: a password set under a stale record would
            // later read as unchanged.
            u.SetComment(UserName, records.Make(UserName, password));
            try
            {
                u.SetPassword(UserName, password);
            }
            catch (PasswordReusedException e)
            {
                PutBack(u, had, w);
                if (w.Dirty) throw new TiaException((ours ? Reused : ReusedOtherKey) + "; putting the user's record back failed too: close this session without saving", e);
                throw TiadException.BadRequest("{0}", ours ? Reused : ReusedOtherKey);
            }
            catch (Exception)
            {
                PutBack(u, had, w);
                throw;
            }
            notes.Add(SetNote);
        }

        static void PutBack(IProjectUsers u, string had, AccessWrites w)
        {
            try
            {
                u.SetComment(UserName, had);
            }
            catch (Exception)
            {
                w.Dirty = true;
            }
        }

        // Run does the rest of what want asks, in the station step. madeCpu
        // is the CPU the apply made (true) or kept (false); into collects
        // what this run made in the project's users and roles, as soon as
        // it is made, so a failure part-way still leaves it on the session.
        // A found user's password is Prepare's, never Run's.
        public static void Run(IAccessControl c, string want, string password, AccessRecords records, bool madeCpu, AccessMade into, List<string> notes)
        {
            if (want == StationRules.AccessKeep) return;
            if (records == null) throw new TiaException("no access record key: the agent sets one for every apply");
            string current = c.State();
            AccessRule.Plan(madeCpu ? null : current, want, password != "");
            if (madeCpu && current != AccessRule.Enabled)
            {
                if (current == AccessRule.Absent)
                {
                    throw new TiaException("the new CPU has no access control setting, so it cannot have a Full access user: set cpu.accessControl to keep");
                }
                c.Enable();
                string now = c.State();
                if (now != AccessRule.Enabled)
                {
                    throw new TiaException("after turning access control on the CPU reads " + (now == AccessRule.Absent ? "absent" : now) + ", not " + AccessRule.Enabled);
                }
            }
            bool madeRole = !c.HasRole(RoleName);
            if (madeRole)
            {
                c.MakeRole(RoleName, RoleComment);
                into.Role = true;
            }
            else if (c.RoleComment(RoleName) != RoleComment)
            {
                throw new TiaException(ForeignRole);
            }
            c.GrantFullAccess(RoleName);
            bool madeUser = !c.HasUser(UserName);
            if (madeUser)
            {
                c.MakeUser(UserName, password);
                into.User = true;
                c.SetComment(UserName, records.Make(UserName, password));
                notes.Add(MadeNote);
            }
            else if (!AccessRecords.IsRecord(c.Comment(UserName)))
            {
                throw new TiaException(ForeignUser);
            }
            c.AddRole(UserName, RoleName);
            string bad = c.Check(UserName, RoleName);
            if (bad != null) throw new TiaException(bad);
            Log.Line("access control: {0}, user {1}, role {2}", c.State(), madeUser ? "made" : "found", madeRole ? "made" : "found");
        }
    }
}
