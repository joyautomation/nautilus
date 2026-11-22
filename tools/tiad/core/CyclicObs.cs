// CyclicObs.cs: the cyclic interrupt OBs a software import makes (#789).
//
// A nautilus task runs in a cyclic interrupt OB at the task's cycle. SCL
// source cannot make one: an ORGANIZATION_BLOCK source with no OB of its
// name becomes a program cycle OB, numbered from 123 (TIA VM, 2026-11-22).
// Openness has no CreateOB, and an OB's class (SecondaryType) is read-only,
// so the agent makes each cyclic interrupt OB first, by importing SimaticML
// for an empty LAD OB with its class, number and cycle. The writer's SCL
// source then replaces the body and keeps all three (proven on the VM).
// CyclicTime cannot be written afterwards, so a new cycle (or number) is a
// delete and a new import.
//
// The request names each OB ({name, number, cycleMs}). Before anything is
// imported the agent refuses:
//   - an entry out of range: numbers 30-38 or 123-32767, cycles 1 ms-60 s;
//   - an ORGANIZATION_BLOCK source with no entry (it would silently be a
//     program cycle OB), and an entry no source declares;
//   - a number held by a block outside the group (foreign_block), or by
//     another OB in the group that prune does not remove;
//   - a declared block named OB_<x> while an OB x exists or is asked for:
//     TIA keeps a system constant OB_<name> for every OB, and the names
//     clash ("Type conflict: Object ... cannot be overwritten").
// After the import every OB is checked: class, number and cycle as asked,
// else the import fails and the session is unusable.

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Security;

namespace Tiad
{
    // CyclicOB is one cyclic interrupt OB a software import makes.
    public sealed class CyclicOB
    {
        public string Name = "";
        public int Number;
        public int CycleMs;

        public long CycleMicros
        {
            get { return (long)CycleMs * 1000; }
        }
    }

    // OBStep is what an import does for one OB: "kept" (there as asked),
    // "created", "recreated" (there with another cycle, number or class:
    // deleted and imported again) or "removed" (another OB of the group
    // that holds the number, with prune). Was describes the OB before.
    public sealed class OBStep
    {
        public string Name = "", Action = "", Was = "", Now = "";
        public CyclicOB Want;

        public Obj ToJson()
        {
            var o = new Obj();
            o["name"] = Name;
            o["action"] = Action;
            if (Was != "") o["was"] = Was;
            if (Now != "") o["now"] = Now;
            return o;
        }
    }

    public static class CyclicObs
    {
        public const string Cyclic = "CyclicInterrupt";

        // Read reads the request's obs entries and refuses one out of range
        // or a name given twice.
        public static List<CyclicOB> Read(List<Reader> entries)
        {
            var l = new List<CyclicOB>();
            var names = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            var numbers = new HashSet<int>();
            foreach (var e in entries)
            {
                var ob = new CyclicOB { Name = e.Str("name"), Number = e.Int("number"), CycleMs = e.Int("cycleMs") };
                if (ob.Name == "") throw TiadException.BadRequest("obs: an entry has no name");
                if (!names.Add(ob.Name)) throw TiadException.BadRequest("obs: " + Json.Quote(ob.Name) + " is given twice");
                if (!(ob.Number >= 30 && ob.Number <= 38) && !(ob.Number >= 123 && ob.Number <= 32767))
                    throw TiadException.BadRequest("obs: " + Json.Quote(ob.Name) + " number " + ob.Number + " is no cyclic interrupt OB number (30-38, or 123-32767)");
                if (!numbers.Add(ob.Number)) throw TiadException.BadRequest("obs: OB" + ob.Number + " is given twice");
                if (ob.CycleMs < 1 || ob.CycleMs > 60000)
                    throw TiadException.BadRequest("obs: " + Json.Quote(ob.Name) + " cycleMs " + ob.CycleMs + " is outside 1 ms to 60 s");
                l.Add(ob);
            }
            return l;
        }

        // SourcesRefused is the refusal of an OB source with no obs entry,
        // or of an entry no source declares; null when they match.
        public static TiadException SourcesRefused(List<Declared> declared, List<CyclicOB> obs)
        {
            var asked = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (var ob in obs) asked.Add(ob.Name);
            var sourced = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            var problems = new List<string>();
            foreach (var d in declared)
            {
                if (d.Kind != "OB") continue;
                sourced.Add(d.Name);
                if (!asked.Contains(d.Name))
                    problems.Add(d.Source + " declares OB \"" + d.Name + "\", which obs does not name: SCL source alone makes a program cycle OB, not a cyclic interrupt one. Add it to obs with its number and cycle");
            }
            foreach (var ob in obs)
            {
                if (!sourced.Contains(ob.Name)) problems.Add("obs names \"" + ob.Name + "\", which no source declares: its body comes from an ORGANIZATION_BLOCK source");
            }
            if (problems.Count == 0) return null;
            return TiadException.BadRequest(string.Join("; ", problems.ToArray()) + ". Nothing was imported");
        }

        // ConstantClash is the refusal of a declared block named OB_<x>
        // while an OB x is in the project or asked for; null when none.
        public static TiadException ConstantClash(List<Declared> declared, List<ProjectBlock> blocks, List<CyclicOB> obs)
        {
            var obNames = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (var b in blocks) if (b.Type == "OB") obNames.Add(b.Name);
            foreach (var ob in obs) obNames.Add(ob.Name);
            var problems = new List<string>();
            foreach (var d in declared)
            {
                if (d.Kind == "UDT" || d.Name.Length <= 3 || string.Compare(d.Name, 0, "OB_", 0, 3, StringComparison.OrdinalIgnoreCase) != 0) continue;
                string of = d.Name.Substring(3);
                if (obNames.Contains(of))
                    problems.Add(d.Source + " declares \"" + d.Name + "\", the name of TIA's system constant for OB \"" + of + "\" (TIA keeps OB_<name> for every OB)");
            }
            // The other way round: an asked OB whose constant would take the
            // name of a block the project holds.
            foreach (var ob in obs)
            {
                foreach (var b in blocks)
                {
                    if (string.Equals(b.Name, "OB_" + ob.Name, StringComparison.OrdinalIgnoreCase))
                        problems.Add("obs asks OB \"" + ob.Name + "\", whose system constant OB_" + ob.Name + " would take the name of the project's block \"" + b.Name + "\": rename the task's OB");
                }
            }
            if (problems.Count == 0) return null;
            return TiadException.BadRequest(string.Join("; ", problems.ToArray()) + ". Nothing was imported. Rename the block (a name that does not start with OB_)");
        }

        // Plan is what the import does for every asked OB, in order:
        // removals first, then each asked OB kept, created or recreated.
        public static List<OBStep> Plan(List<CyclicOB> obs, List<ProjectBlock> blocks, string group, bool prune)
        {
            var asked = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (var ob in obs) asked.Add(ob.Name);
            var removals = new List<OBStep>();
            var steps = new List<OBStep>();
            foreach (var ob in obs)
            {
                ProjectBlock same = null;
                foreach (var b in blocks) if (string.Equals(b.Name, ob.Name, StringComparison.OrdinalIgnoreCase)) same = b;
                foreach (var b in blocks)
                {
                    if (b.Type != "OB" || b.Number != ob.Number || string.Equals(b.Name, ob.Name, StringComparison.OrdinalIgnoreCase)) continue;
                    if (!Ownership.Owned(b.Group, group))
                        throw new TiadException(409, Kinds.ForeignBlock, "obs asks OB" + ob.Number + " for \"" + ob.Name + "\", but \"" + b.Name + "\" [OB" + b.Number + "] outside group \"" + group + "\" holds it. Nothing was imported. Choose another number");
                    // An OB the request asks for under another number frees
                    // this one: it is recreated, and every delete comes
                    // before any import.
                    if (asked.Contains(b.Name)) continue;
                    if (!prune)
                        throw TiadException.BadRequest("obs asks OB" + ob.Number + " for \"" + ob.Name + "\", but \"" + b.Name + "\" [OB" + b.Number + "] in the group holds it. Nothing was imported. Choose another number, or import with prune to remove it");
                    removals.Add(new OBStep { Name = b.Name, Action = "removed", Was = Describe(b) });
                }
                var st = new OBStep { Name = ob.Name, Want = ob, Now = Describe(ob) };
                if (same == null) st.Action = "created";
                else if (same.Type == "OB" && same.Class == Cyclic && same.Number == ob.Number && same.CycleMicros == ob.CycleMicros) st.Action = "kept";
                else
                {
                    st.Action = "recreated";
                    st.Was = Describe(same);
                }
                steps.Add(st);
            }
            removals.AddRange(steps);
            return removals;
        }

        // Verify lists every asked OB that is not, after the import, a
        // cyclic interrupt OB in the group with its number and cycle.
        public static List<string> Verify(List<CyclicOB> obs, List<ProjectBlock> blocks, string group)
        {
            var bad = new List<string>();
            foreach (var ob in obs)
            {
                ProjectBlock b = null;
                foreach (var x in blocks) if (string.Equals(x.Name, ob.Name, StringComparison.OrdinalIgnoreCase)) b = x;
                if (b == null) bad.Add("\"" + ob.Name + "\" is missing");
                else if (b.Type != "OB" || b.Class != Cyclic || b.Number != ob.Number || b.CycleMicros != ob.CycleMicros || !Ownership.Owned(b.Group, group))
                    bad.Add("\"" + ob.Name + "\" is " + Describe(b) + (Ownership.Owned(b.Group, group) ? "" : " outside the group") + ", not " + Describe(ob));
            }
            return bad;
        }

        // FailedSources lists every OB whose declaring source did not import
        // (results by the request's path). Every OB source names an asked OB
        // by then (SourcesRefused).
        public static List<string> FailedSources(List<Declared> declared, List<SourceResult> results)
        {
            var failed = new Dictionary<string, SourceResult>(StringComparer.Ordinal);
            foreach (var r in results) if (!r.Ok) failed[r.Path] = r;
            var l = new List<string>();
            foreach (var d in declared)
            {
                SourceResult r;
                if (d.Kind == "OB" && failed.TryGetValue(d.Source, out r))
                    l.Add("the source of OB \"" + d.Name + "\" (" + d.Source + ") did not import: " + string.Join(" ", r.Messages.ToArray()));
            }
            return l;
        }

        public static TiadException VerifyFailed(List<string> bad)
        {
            return new TiadException(500, Kinds.OperationFailed,
                "after the import, " + string.Join("; ", bad.ToArray()) + ": this session can no longer be saved, archived for a deploy or downloaded. Close it without saving");
        }

        // Describe names an OB the way a step shows it: "OB30, cyclic
        // interrupt, 100 ms".
        public static string Describe(CyclicOB ob)
        {
            return "OB" + ob.Number.ToString(CultureInfo.InvariantCulture) + ", cyclic interrupt, " + Ms(ob.CycleMicros);
        }

        public static string Describe(ProjectBlock b)
        {
            if (b.Type != "OB") return b.Type + " " + b.Number.ToString(CultureInfo.InvariantCulture);
            string s = "OB" + b.Number.ToString(CultureInfo.InvariantCulture) + ", " + (b.Class == Cyclic ? "cyclic interrupt" : b.Class == "" ? "class unknown" : b.Class);
            if (b.Class == Cyclic) s += ", " + Ms(b.CycleMicros);
            return s;
        }

        static string Ms(long micros)
        {
            if (micros < 0) return "cycle unreadable";
            if (micros % 1000 == 0) return (micros / 1000).ToString(CultureInfo.InvariantCulture) + " ms";
            return micros.ToString(CultureInfo.InvariantCulture) + " \u00b5s";
        }

        // Xml is the SimaticML for an empty LAD cyclic interrupt OB: the
        // shape TIA V21 exported and imported on the VM (2026-11-22; tiad
        // runs on V21 only, so the document says V21), with
        // AutoNumber off so TIA never picks another number, and no texts,
        // so no project language is assumed.
        public static string Xml(CyclicOB ob)
        {
            return "<?xml version=\"1.0\" encoding=\"utf-8\"?>\r\n" +
                "<Document>\r\n" +
                "  <Engineering version=\"V21\" />\r\n" +
                "  <SW.Blocks.OB ID=\"0\">\r\n" +
                "    <AttributeList>\r\n" +
                "      <AutoNumber>false</AutoNumber>\r\n" +
                "      <CyclicTime>" + ob.CycleMicros.ToString(CultureInfo.InvariantCulture) + "</CyclicTime>\r\n" +
                "      <HeaderAuthor />\r\n" +
                "      <HeaderFamily />\r\n" +
                "      <HeaderName />\r\n" +
                "      <HeaderVersion>0.1</HeaderVersion>\r\n" +
                "      <Interface>\r\n" +
                "        <Sections xmlns=\"http://www.siemens.com/automation/Openness/SW/Interface/v5\">\r\n" +
                "          <Section Name=\"Input\">\r\n" +
                "            <Member Name=\"Initial_Call\" Datatype=\"Bool\" Accessibility=\"Public\" Informative=\"true\" />\r\n" +
                "            <Member Name=\"Event_Count\" Datatype=\"Int\" Accessibility=\"Public\" Informative=\"true\" />\r\n" +
                "          </Section>\r\n" +
                "          <Section Name=\"Temp\" />\r\n" +
                "          <Section Name=\"Constant\" />\r\n" +
                "        </Sections>\r\n" +
                "      </Interface>\r\n" +
                "      <IsIECCheckEnabled>false</IsIECCheckEnabled>\r\n" +
                "      <MemoryLayout>Optimized</MemoryLayout>\r\n" +
                "      <Name>" + SecurityElement.Escape(ob.Name) + "</Name>\r\n" +
                "      <Namespace />\r\n" +
                "      <Number>" + ob.Number.ToString(CultureInfo.InvariantCulture) + "</Number>\r\n" +
                "      <PhaseOffset>0</PhaseOffset>\r\n" +
                "      <ProgrammingLanguage>LAD</ProgrammingLanguage>\r\n" +
                "      <SecondaryType>CyclicInterrupt</SecondaryType>\r\n" +
                "      <SetENOAutomatically>false</SetENOAutomatically>\r\n" +
                "    </AttributeList>\r\n" +
                "  </SW.Blocks.OB>\r\n" +
                "</Document>\r\n";
        }
    }
}
