// Machine.cs: the machine gates (siemens-ide.md section 3.4), computed
// from what the machine says without starting TIA. MachineGates is pure:
// it reads only an IMachine, so tiad-fake can script one and the suite can
// check every branch under Mono. The real IMachine is the host's
// (host/WindowsMachine.cs): the registry, the file system and the
// process token.

using System;
using System.Collections.Generic;
using System.Globalization;

namespace Tiad
{
    // GroupState is where the process token stands with the Openness group.
    public enum GroupState
    {
        Member,       // the token holds the group's SID
        NotInToken,   // the group exists; this logon's token lacks it
        NoSuchGroup,  // the group does not exist: Openness is not installed
    }

    // IMachine is what the gates read. Registry keys are under HKLM, in the
    // 64-bit view, which is where TIA writes and reads them.
    public interface IMachine
    {
        // RegString returns a string value, or null when the key or the
        // value is absent or not a string.
        string RegString(string key, string name);

        // RegSubKeys returns a key's subkey names, or null when the key is
        // absent.
        List<string> RegSubKeys(string key);

        bool FileExists(string path);

        // ExePath is the full path of the running executable: the
        // application TIA's Openness allowlist names.
        string ExePath { get; }

        DateTime ExeLastWriteUtc { get; }

        // ExeSha256 is the SHA-256 of the running executable's bytes.
        byte[] ExeSha256 { get; }

        GroupState OpennessGroup();
    }

    public static class MachineGates
    {
        // The Openness V21 registration TIA's setup writes.
        public const string OpennessKey = @"SOFTWARE\Siemens\Automation\Openness\21.0";
        public const string PublicApiKey = OpennessKey + @"\PublicAPI\21.0.0.0\net48";
        public const string PortalVersion = "V21";

        // The Openness allowlist TIA requires: one key per executable file
        // name, holding any number of entries, each with the full Path, the
        // file's DateModified and its FileHash.
        public const string AllowListKey = @"Software\Siemens\Automation\Openness\AllowList";
        public const string DateFormat = "yyyy/MM/dd HH:mm:ss.fff";

        public const string GroupName = "Siemens TIA Openness";

        public const string LicenceUnknown = "not readable until TIA's first start";

        // Installed reports whether Openness V21 is registered and its
        // public API assemblies are where the registration says, and why
        // not.
        static string NotInstalled(IMachine m)
        {
            string v = m.RegString(OpennessKey, "PortalVersion");
            if (v != PortalVersion)
            {
                return "TIA Portal " + PortalVersion + " with Openness is not registered (HKLM\\" + OpennessKey + " PortalVersion is " +
                    (v == null ? "absent" : Json.Quote(v)) + ")";
            }
            string b = m.RegString(PublicApiKey, "Siemens.Engineering.Base");
            if (b == null || b == "") return "the Openness net48 public API is not registered (HKLM\\" + PublicApiKey + ")";
            if (!m.FileExists(b)) return "the registered Openness assembly is missing: " + b;
            string step7 = Dir(b) + "\\Siemens.Engineering.Step7.dll";
            if (!m.FileExists(step7)) return "the Openness STEP 7 assembly is missing: " + step7;
            return null;
        }

        // TiaVersion is "V21" when Openness V21 is installed, or "".
        public static string TiaVersion(IMachine m)
        {
            return NotInstalled(m) == null ? PortalVersion : "";
        }

        // OpennessVersion is the registered public API's assembly version,
        // or "" when Openness V21 is not installed.
        public static string OpennessVersion(IMachine m)
        {
            if (NotInstalled(m) != null) return "";
            return m.RegString(PublicApiKey, "AssemblyVersion") ?? "";
        }

        // Evaluate computes the machine's gates, in order: tia,
        // openness-group, openness-allowlist, licence. noTia (TIAD_NO_TIA)
        // fails the tia gate, so no call can start TIA.
        public static List<Gate> Evaluate(IMachine m, bool noTia)
        {
            var gs = new List<Gate>();
            gs.Add(TiaGate(m, noTia));
            gs.Add(GroupGate(m));
            gs.Add(AllowListGate(m));
            gs.Add(new Gate { Name = "licence", State = Gate.StateUnknown, Detail = LicenceUnknown });
            return gs;
        }

        static Gate TiaGate(IMachine m, bool noTia)
        {
            var g = new Gate { Name = "tia" };
            string why = NotInstalled(m);
            if (why != null)
            {
                g.Detail = why;
                g.Remedy = "install TIA Portal " + PortalVersion + " with STEP 7 and TIA Openness";
                return g;
            }
            if (noTia)
            {
                g.Detail = "TIA starts are disabled on this agent (TIAD_NO_TIA=1)";
                g.Remedy = "unset TIAD_NO_TIA (install-tiad.ps1 without -NoTia) once TIA may start";
                return g;
            }
            g.State = Gate.StateOk;
            g.Detail = "TIA Portal " + PortalVersion + ", Openness " + (m.RegString(PublicApiKey, "AssemblyVersion") ?? "");
            return g;
        }

        static Gate GroupGate(IMachine m)
        {
            var g = new Gate { Name = "openness-group" };
            switch (m.OpennessGroup())
            {
                case GroupState.Member:
                    g.State = Gate.StateOk;
                    g.Detail = "the agent's token holds \"" + GroupName + "\"";
                    break;
                case GroupState.NotInToken:
                    g.Detail = "the agent's token does not hold \"" + GroupName + "\"";
                    g.Remedy = "add the account to the group (install-tiad.ps1 does), then log off and on: a token keeps the groups of its logon";
                    break;
                default:
                    g.Detail = "there is no \"" + GroupName + "\" group";
                    g.Remedy = "install TIA Openness, which creates the group";
                    break;
            }
            return g;
        }

        // Stamp is a file time as the allowlist holds it: UTC, truncated
        // to the millisecond, as the allowlist's DateModified holds it.
        public static DateTime Stamp(DateTime utc)
        {
            return new DateTime(utc.Year, utc.Month, utc.Day, utc.Hour, utc.Minute, utc.Second, utc.Millisecond);
        }

        // AllowListGate checks the Openness allowlist entry TIA requires
        // before it lets an application use Openness without asking: an entry whose
        // Path names this executable (directory and file name, compared
        // case-insensitively), whose DateModified is its last write time
        // and whose FileHash is its SHA-256. Whether that is all TIA needs
        // to ask no question is proven at the first TIA start.
        static Gate AllowListGate(IMachine m)
        {
            var g = new Gate { Name = "openness-allowlist" };
            g.Remedy = "run install-tiad.ps1, which writes the entry after every build";
            string exe = m.ExePath;
            string name = File(exe);
            string key = AllowListKey + "\\" + name;
            List<string> entries = m.RegSubKeys(key);
            if (entries == null || entries.Count == 0)
            {
                g.Detail = "no allowlist entry for " + name + " (HKLM\\" + key + ")";
                return g;
            }
            DateTime stamp = Stamp(m.ExeLastWriteUtc);
            byte[] hash = m.ExeSha256;
            string why = null;
            foreach (string e in entries)
            {
                string sub = key + "\\" + e;
                string path = m.RegString(sub, "Path");
                if (path == null || !SamePath(path, exe))
                {
                    if (why == null) why = "no entry names " + exe;
                    continue;
                }
                DateTime t;
                string date = m.RegString(sub, "DateModified");
                if (date == null || !DateTime.TryParseExact(date, DateFormat, CultureInfo.InvariantCulture, DateTimeStyles.None, out t) || t != stamp)
                {
                    why = "the entry's DateModified is not the executable's (" + stamp.ToString(DateFormat, CultureInfo.InvariantCulture) + "): it was rebuilt since";
                    continue;
                }
                if (!SameHash(m.RegString(sub, "FileHash"), hash))
                {
                    why = "the entry's FileHash is not the executable's SHA-256: it was rebuilt since";
                    continue;
                }
                g.State = Gate.StateOk;
                g.Detail = "an entry matches " + exe + " (path, time, SHA-256)";
                g.Remedy = "";
                return g;
            }
            g.Detail = why;
            return g;
        }

        // SamePath compares the directory and the file name, ignoring case,
        // as the allowlist entry is matched.
        static bool SamePath(string a, string b)
        {
            return string.Equals(Dir(a), Dir(b), StringComparison.OrdinalIgnoreCase) &&
                string.Equals(File(a), File(b), StringComparison.OrdinalIgnoreCase);
        }

        // Dir and File split a Windows path at its last separator, on any
        // platform: under Mono, System.IO.Path knows only '/', and the
        // suite scripts Windows paths.
        static int LastSep(string p)
        {
            return Math.Max(p.LastIndexOf('\\'), p.LastIndexOf('/'));
        }

        static string Dir(string p)
        {
            int i = LastSep(p);
            return i < 0 ? "" : p.Substring(0, i);
        }

        static string File(string p)
        {
            return p.Substring(LastSep(p) + 1);
        }

        static bool SameHash(string b64, byte[] want)
        {
            if (b64 == null || want == null) return false;
            byte[] got;
            try
            {
                got = Convert.FromBase64String(b64);
            }
            catch (FormatException)
            {
                return false;
            }
            if (got.Length != want.Length) return false;
            for (int i = 0; i < got.Length; i++)
            {
                if (got[i] != want[i]) return false;
            }
            return true;
        }
    }
}
