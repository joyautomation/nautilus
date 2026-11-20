// Reuse.cs: whether an apply makes the project's CPU anew or keeps it, and
// the configuration-protection steps an apply runs (siemens-ide.md
// section 3.3). Every decision here is pure, so CI compiles and tests it;
// tiad.Openness only reads the project and obeys.
//
// C# 5 only: this source is compiled on the TIA machine by the stock
// .NET Framework csc, which knows nothing newer. No string interpolation,
// no ?., no nameof, no expression-bodied members.

using System;
using System.Collections.Generic;

namespace Tiad
{
    // KeptDevice is a device in the project besides the CPU's station,
    // which an apply that reuses the CPU keeps as it was found: tiad does
    // not configure PROFINET IO devices yet. Address and ProfinetName are
    // "" when the device has none.
    public sealed class KeptDevice
    {
        public string Name = "", ProfinetName = "", Address = "";

        public Obj ToJson()
        {
            var o = new Obj();
            o["name"] = Name;
            o["profinetName"] = ProfinetName;
            o["address"] = Address;
            return o;
        }

        // Describe is the device for a note or an audit line, quoted so a
        // project's names cannot forge a line.
        public string Describe()
        {
            string s = Json.Quote(Name);
            if (ProfinetName != "") s += " profinetName=" + Json.Quote(ProfinetName);
            if (Address != "") s += " address=" + Json.Quote(Address);
            return s;
        }

        public static List<object> ToJson(List<KeptDevice> ds)
        {
            var l = new List<object>();
            foreach (var d in ds) l.Add(d.ToJson());
            return l;
        }
    }

    // ProjectCpu is the project's CPU as the project holds it: the CPU
    // device item's name (not its station's), order number, firmware and
    // configuration protection (ConfigProtection's states), and the other
    // devices in the project.
    public sealed class ProjectCpu
    {
        public string Name = "", Order = "", Firmware = "";
        public string Protection = ConfigProtection.Absent;
        public List<KeptDevice> Others = new List<KeptDevice>();

        // ToJson is the part a station spec copies: what naut needs to
        // build a spec that reuses this CPU.
        public Obj ToJson()
        {
            var o = new Obj();
            o["name"] = Name;
            o["order"] = Order;
            o["firmware"] = Firmware;
            return o;
        }
    }

    // CpuReuse decides whether an apply makes the CPU anew or reuses the
    // project's. Never silently: a CPU that differs from the spec, or one
    // left by an apply or upload that failed part-way, refuses with the fix.
    public static class CpuReuse
    {
        public const string Create = "create", Reuse = "reuse";

        // Decide returns Create when the project holds no CPU, Reuse when
        // its name, order and firmware are the spec's, or throws. unclean
        // is true when an apply or upload on this session failed
        // part-way: nothing about the project is then trusted.
        public static string Decide(ProjectCpu project, Cpu spec, bool unclean)
        {
            if (unclean) throw Unclean("an apply");
            if (project == null) return Create;
            var diff = new List<string>();
            if (project.Name != spec.Name) diff.Add("name " + Json.Quote(project.Name) + ", not " + Json.Quote(spec.Name));
            if (project.Order != spec.Order) diff.Add("order " + Json.Quote(project.Order) + ", not " + Json.Quote(spec.Order));
            if (!SameFirmware(project.Firmware, spec.Firmware))
            {
                diff.Add("firmware " + Json.Quote(project.Firmware) + ", not " + Json.Quote(spec.Firmware));
            }
            if (diff.Count == 0) return Reuse;
            var d = new Obj();
            d["cpu"] = project.ToJson();
            throw new TiadException(409, Kinds.CpuMismatch,
                "the project's CPU has " + string.Join(", ", diff.ToArray()) +
                "; tiad never replaces a project's CPU on an apply: to keep it, apply a spec whose cpu name, order and firmware are the project's (in this error's data); to make a new CPU, create the project again (POST /v1/projects)",
                d, false);
        }

        // SameFirmware compares two firmware versions exactly, a leading
        // "V" aside: TIA stores the version the station was configured
        // with, and V3.1 is not V3.1.3.
        public static bool SameFirmware(string a, string b)
        {
            return Bare(a) == Bare(b);
        }

        static string Bare(string fw)
        {
            return fw.StartsWith("V", StringComparison.Ordinal) ? fw.Substring(1) : fw;
        }

        // Unclean is the refusal of what after an apply or upload failed
        // part-way on the session.
        public static TiadException Unclean(string what)
        {
            return new TiadException(409, Kinds.UncleanSession,
                "an apply or upload on this session failed part-way, so its project matches no spec and tiad trusts nothing in it; " + what +
                " is refused: close this session without saving, then open the saved project again or create it anew");
        }

        // Collides refuses a kept device that shares a name, a PROFINET
        // name or an address with the spec: the two would coexist in one
        // project.
        public static void Collides(ProjectCpu project, Station spec)
        {
            List<KeptDevice> kept = project.Others;
            var names = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            var addresses = new HashSet<string>(StringComparer.Ordinal);
            names.Add(spec.Cpu.Name);
            names.Add(spec.Cpu.ProfinetName);
            addresses.Add(spec.Cpu.Address);
            if (spec.Profinet != null)
            {
                foreach (var d in spec.Profinet.Devices)
                {
                    names.Add(d.Name);
                    if (d.Address != "") addresses.Add(d.Address);
                }
            }
            names.Remove("");
            var bad = new List<string>();
            foreach (var k in kept)
            {
                bool clash = names.Contains(k.Name) || (k.ProfinetName != "" && names.Contains(k.ProfinetName)) ||
                    (k.Address != "" && addresses.Contains(k.Address));
                if (clash) bad.Add(k.Describe());
            }
            if (bad.Count == 0) return;
            var data = new Obj();
            data["cpu"] = project.ToJson();
            data["keptDevices"] = KeptDevice.ToJson(kept);
            throw new TiadException(409, Kinds.CpuMismatch,
                "the project keeps device(s) " + string.Join("; ", bad.ToArray()) +
                " that share a name or an address with the spec; tiad does not configure PROFINET IO devices yet and will not let two coexist: change the spec, or remove the device in the project",
                data, false);
        }

        // KeptNote is what an apply that reuses the CPU says about the
        // devices it keeps, or null when there are none.
        public static string KeptNote(List<KeptDevice> kept)
        {
            if (kept.Count == 0) return null;
            var ds = new List<string>();
            foreach (var k in kept) ds.Add(k.Describe());
            return "the project's other devices are kept as found (tiad does not configure PROFINET IO devices yet), and a download loads them too: " + string.Join("; ", ds.ToArray());
        }

        // Changes is what an apply changed, in words: a new station, the
        // spec's changes over the one this session last applied, or a CPU
        // taken over from an upload or an opened project.
        public static List<string> Changes(string decision, Station prev, Station next)
        {
            if (decision == Create) return Station.Changes(null, next);
            if (prev != null) return Station.Changes(prev, next);
            var l = new List<string>();
            l.Add("cpu " + next.Cpu.Name + " taken over from the project");
            return l;
        }
    }

    // KeptRule is the download's question about kept devices: a download
    // loads the devices an apply kept as found, so it needs a yes that
    // names exactly that set. A yes for another set (an older one carried
    // by a script) is refused; no kept devices, no question.
    public static class KeptRule
    {
        public static Question Question(List<KeptDevice> kept)
        {
            var q = new Question
            {
                Id = QuestionIds.KeptDevices,
                Message = "load the devices the project keeps as found besides the CPU (name each in decisions.keptDevices)",
            };
            q.Objects = Names(kept);
            return q;
        }

        // Check returns null when answer names exactly the kept devices,
        // or why not.
        public static string Check(List<KeptDevice> kept, List<string> answer)
        {
            List<string> want = Names(kept);
            var got = new SortedSet<string>(answer, StringComparer.Ordinal);
            if (got.SetEquals(want)) return null;
            if (got.Count == 0)
            {
                return "the project keeps device(s) " + Quoted(want) + " that a download loads too: name them in decisions.keptDevices to load them";
            }
            return "decisions.keptDevices names " + Quoted(new List<string>(got)) + ", but the project keeps " + (want.Count == 0 ? "none" : Quoted(want)) +
                " now: the answer must name exactly the devices kept, so a yes for another set does not carry over";
        }

        static List<string> Names(List<KeptDevice> kept)
        {
            var s = new SortedSet<string>(StringComparer.Ordinal);
            foreach (var k in kept) s.Add(k.Name);
            return new List<string>(s);
        }

        static string Quoted(List<string> names)
        {
            var q = new List<string>();
            foreach (var n in names) q.Add(Json.Quote(n));
            return "[" + string.Join(", ", q.ToArray()) + "]";
        }
    }

    // IProtectionConfigurator is the CPU's configuration protection as
    // TIA's PlcMasterSecretConfigurator offers it. tiad.Openness wraps
    // TIA's; the fakes model it. Passwords cross as strings: the glue
    // makes TIA's SecureString.
    public interface IProtectionConfigurator
    {
        // State is MasterSecretConfiguration's name, or
        // ConfigProtection.Absent for a CPU without one.
        string State();
        void Protect(string password);
        void Unprotect(string password);
        // RemoveCpu removes the CPU's station from the project.
        void RemoveCpu();
    }

    // ProtectionSteps applies cpu.configurationProtection from the state
    // the project holds now (ConfigProtection decides), then reads it back:
    // anything but the promised state refuses.
    public static class ProtectionSteps
    {
        // Run returns the action it took. password is
        // secrets.configurationPassword.
        public static string Run(IProtectionConfigurator c, string want, string password, List<string> notes)
        {
            string current = c.State();
            string action = ConfigProtection.Plan(current, want, password != "");
            string note = ConfigProtection.Note(current, want);
            if (note != null) notes.Add(note);
            if (action == ConfigProtection.Nothing) return action;
            if (action == ConfigProtection.ProtectThenUnprotect)
            {
                // Once the throwaway Protect is called, any failure could
                // leave the CPU under a password nobody holds: remove it.
                try
                {
                    string throwaway = ConfigProtection.Throwaway();
                    c.Protect(throwaway);
                    string mid = ConfigProtection.Check(ConfigProtection.Protect, c.State());
                    if (mid != null) throw new TiaException(mid);
                    c.Unprotect(throwaway);
                    string end = ConfigProtection.Check(action, c.State());
                    if (end != null) throw new TiaException(end);
                }
                catch (Exception e)
                {
                    string removeFailed = null;
                    try
                    {
                        c.RemoveCpu();
                    }
                    catch (Exception d)
                    {
                        removeFailed = TiaNames.Message(d);
                    }
                    throw new TiaException(ConfigProtection.Stranded(TiaNames.Message(e), removeFailed));
                }
            }
            else
            {
                if (action == ConfigProtection.Protect) c.Protect(password);
                else c.Unprotect(password);
                string bad = ConfigProtection.Check(action, c.State());
                if (bad != null) throw new TiaException(bad);
            }
            Log.Line("configuration protection: {0}, now {1}", action, c.State());
            return action;
        }
    }
}
