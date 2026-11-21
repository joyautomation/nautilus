// Access.cs: the CPU's PLC access control, users and roles
// (cpu.accessControl, siemens-ide.md section 3.3, #789 decision of
// 2026-11-20). A station nautilus creates has access control on and one
// user, "nautilus", with the runtime right Full access; its password comes
// from the secret store (secrets.fullAccessPassword). An adopted station
// keeps what it has.

using System;
using System.Collections.Generic;

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
        // UTF-16 code units (a .NET string's), the classes ASCII; a
        // special character is any other character.
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
                else special++;
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

    // IAccessControl is the CPU's access control and the project's users
    // and roles as TIA's Openness offers them (PlcAccessControlConfiguration
    // provider, UmacConfigurator, UmacDevice). tiad.Openness wraps TIA's;
    // the fakes model it. Passwords cross as strings: the glue makes TIA's
    // SecureString.
    public interface IAccessControl
    {
        // State is PlcAccessControlConfiguration's name, or
        // AccessRule.Absent for a CPU without the setting.
        string State();
        // Enable sets PlcAccessControlConfiguration to
        // EnabledWithAccessControl.
        void Enable();
        // EnsureRole finds the custom role, or makes it; it returns true
        // when it made it.
        bool EnsureRole(string role);
        // GrantFullAccess gives role the CPU's runtime right Full access,
        // unless it has it.
        void GrantFullAccess(string role);
        // HasUser reports whether the project holds the local user.
        bool HasUser(string user);
        // MakeUser makes the local user with password. There is no call
        // that sets a found user's password: TIA refuses any password the
        // user has had (its current one included, as many as the project's
        // policy blocks for reuse), and cannot check one offline.
        void MakeUser(string user, string password);
        // AddRole assigns role to user, unless it has it.
        void AddRole(string user, string role);
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

    // AccessSteps applies cpu.accessControl fullAccessUser: access control
    // on (written only for a CPU the apply made), the role with the runtime
    // right Full access, the user with its password and that role. After
    // the writes it reads back: anything but the promised state refuses.
    public static class AccessSteps
    {
        public const string UserName = "nautilus", RoleName = "nautilus full access";

        // FoundNote is what an apply says about a user the project held:
        // its password is left as found.
        public const string FoundNote = "the project's user \"nautilus\" has the runtime right Full access (role \"nautilus full access\"); its password is left as found: TIA refuses to set a password the user has had and cannot check one offline, so a changed secrets.fullAccessPassword does not reach this project; a project made again (POST /v1/projects) gets the user anew with the secret";

        // Run does what want asks. made is the CPU the apply made (true) or
        // kept (false); into collects what this run made in the project's
        // users and roles, as soon as it is made, so a failure part-way
        // still leaves it on the session.
        public static void Run(IAccessControl c, string want, string password, bool madeCpu, AccessMade into, List<string> notes)
        {
            if (want == StationRules.AccessKeep) return;
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
            bool madeRole = c.EnsureRole(RoleName);
            if (madeRole) into.Role = true;
            c.GrantFullAccess(RoleName);
            bool madeUser = !c.HasUser(UserName);
            if (madeUser)
            {
                c.MakeUser(UserName, password);
                into.User = true;
            }
            c.AddRole(UserName, RoleName);
            string bad = c.Check(UserName, RoleName);
            if (bad != null) throw new TiaException(bad);
            notes.Add(madeUser
                ? "made the user " + Json.Quote(UserName) + " with the runtime right Full access (role " + Json.Quote(RoleName) + "), its password from secrets.fullAccessPassword"
                : FoundNote);
            Log.Line("access control: {0}, user {1}, role {2}", c.State(), madeUser ? "made" : "found", madeRole ? "made" : "found");
        }
    }
}
