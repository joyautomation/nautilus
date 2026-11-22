// CyclicObsTest.cs: the cyclic interrupt OBs a software import makes
// (core/CyclicObs.cs, #789): what a request may ask, what the agent
// refuses, what it plans (kept, created, recreated, removed), and the
// check after the import.

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        static List<CyclicOB> Obs(string json)
        {
            var r = Reader.Root(Json.Parse("{\"obs\":" + json + "}"));
            var l = CyclicObs.Read(r.Children("obs"));
            r.Done();
            return l;
        }

        static void CyclicObsReadsAndRefuses()
        {
            var l = Obs("[{\"name\":\"naut_OB_main\",\"number\":30,\"cycleMs\":100},{\"name\":\"naut_OB_fast\",\"number\":123,\"cycleMs\":1}]");
            Eq(l.Count, 2, "two entries");
            Eq(l[0].CycleMicros, 100000L, "100 ms is 100000 us, TIA's CyclicTime");
            Func<string, string> refused = delegate(string json)
            {
                try { Obs(json); return ""; }
                catch (TiadException e) { return e.Kind + ": " + e.Message; }
            };
            Contains(refused("[{\"name\":\"\",\"number\":30,\"cycleMs\":100}]"), "has no name", "a name");
            Contains(refused("[{\"name\":\"a\",\"number\":39,\"cycleMs\":100}]"), "number 39 is no cyclic interrupt OB number", "39 is no cyclic number");
            Contains(refused("[{\"name\":\"a\",\"number\":122,\"cycleMs\":100}]"), "number 122", "122 is not either");
            Contains(refused("[{\"name\":\"a\",\"number\":1,\"cycleMs\":100}]"), "number 1 ", "OB1 is not");
            Eq(refused("[{\"name\":\"a\",\"number\":38,\"cycleMs\":100}]"), "", "38 is");
            Contains(refused("[{\"name\":\"a\",\"number\":30,\"cycleMs\":0}]"), "outside 1 ms to 60 s", "0 ms");
            Contains(refused("[{\"name\":\"a\",\"number\":30,\"cycleMs\":60001}]"), "outside 1 ms to 60 s", "over 60 s");
            Eq(refused("[{\"name\":\"a\",\"number\":30,\"cycleMs\":60000}]"), "", "60 s is");
            Contains(refused("[{\"name\":\"a\",\"number\":30,\"cycleMs\":1},{\"name\":\"A\",\"number\":31,\"cycleMs\":1}]"), "given twice", "a name twice, any case");
            Contains(refused("[{\"name\":\"a\",\"number\":30,\"cycleMs\":1},{\"name\":\"b\",\"number\":30,\"cycleMs\":1}]"), "OB30 is given twice", "a number twice");
            Contains(refused("[{\"name\":\"a\",\"number\":30,\"cycleMs\":1,\"phase\":2}]"), "phase", "an unknown field");
            Eq(Obs("[]").Count, 0, "none");
        }

        static void CyclicObsMatchSources()
        {
            var obs = new List<CyclicOB> { new CyclicOB { Name = "naut_OB_main", Number = 30, CycleMs = 100 } };
            var d = new List<Declared>
            {
                new Declared { Kind = "OB", Name = "naut_OB_main", Source = "ob.scl" },
                new Declared { Kind = "FB", Name = "naut_Main", Source = "fb.scl" },
            };
            True(CyclicObs.SourcesRefused(d, obs) == null, "an OB source with its entry");
            d.Add(new Declared { Kind = "OB", Name = "naut_OB_other", Source = "other.scl" });
            TiadException e = CyclicObs.SourcesRefused(d, obs);
            True(e != null && e.Kind == Kinds.BadRequest && e.Message.Contains("other.scl declares OB \"naut_OB_other\", which obs does not name") &&
                e.Message.Contains("program cycle OB") && e.Message.Contains("Nothing was imported"), "an OB source with no entry is refused, naming why");
            e = CyclicObs.SourcesRefused(new List<Declared>(), obs);
            True(e != null && e.Message.Contains("obs names \"naut_OB_main\", which no source declares"), "an entry with no source");
            True(CyclicObs.SourcesRefused(new List<Declared> { new Declared { Kind = "OB", Name = "NAUT_OB_MAIN", Source = "x" } }, obs) == null, "names match in any case");
        }

        static void CyclicObsRefuseTheSystemConstantClash()
        {
            var blocks = new List<ProjectBlock> { PB("Main", "OB", 1, "") };
            var obs = new List<CyclicOB> { new CyclicOB { Name = "naut_OB_main", Number = 30, CycleMs = 100 } };
            Func<string, string, TiadException> clash = delegate(string kind, string name)
            {
                return CyclicObs.ConstantClash(new List<Declared> { new Declared { Kind = kind, Name = name, Source = "s.scl" } }, blocks, obs);
            };
            TiadException e = clash("FB", "OB_main");
            True(e != null && e.Kind == Kinds.BadRequest && e.Message.Contains("s.scl declares \"OB_main\", the name of TIA's system constant for OB \"main\""), "OB_main against Main, any case");
            True(clash("DB", "ob_NAUT_OB_MAIN") != null, "against an OB the request asks for");
            True(clash("FB", "OB_other") == null, "no OB named other");
            True(clash("FB", "naut_OB_main") == null, "a name that does not start with OB_");
            True(clash("UDT", "OB_Main") == null, "a data type is refused on its own");
            True(clash("FB", "OB_") == null, "OB_ alone names no OB");
            // the other way round: an asked OB whose constant would take a
            // block's name
            var held = new List<ProjectBlock> { PB("Main", "OB", 1, ""), PB("OB_naut_OB_main", "FB", 7, "Customer") };
            TiadException r = CyclicObs.ConstantClash(new List<Declared>(), held, obs);
            True(r != null && r.Message.Contains("obs asks OB \"naut_OB_main\", whose system constant OB_naut_OB_main would take the name of the project's block \"OB_naut_OB_main\""),
                "an asked OB against a block named like its constant");
        }

        static OBStep Step(List<OBStep> l, string name)
        {
            foreach (var s in l) if (s.Name == name) return s;
            return null;
        }

        static void CyclicObsPlan()
        {
            var want = new List<CyclicOB> { new CyclicOB { Name = "naut_OB_main", Number = 30, CycleMs = 100 } };
            var main = PB("Main", "OB", 1, "");
            main.Class = "ProgramCycle";
            // created
            var l = CyclicObs.Plan(want, new List<ProjectBlock> { main }, "nautilus", false);
            Eq(l.Count, 1, "one step");
            Eq(l[0].Action, "created", "a missing OB is created");
            Eq(l[0].Now, "OB30, cyclic interrupt, 100 ms", "what it will be");
            // kept
            var ob = PB("naut_OB_main", "OB", 30, "nautilus");
            ob.Class = CyclicObs.Cyclic;
            ob.CycleMicros = 100000;
            l = CyclicObs.Plan(want, new List<ProjectBlock> { main, ob }, "nautilus", false);
            Eq(l[0].Action, "kept", "the same OB is kept");
            Eq(l[0].Was, "", "a kept OB has no was");
            // recreated: cycle, number, class
            ob.CycleMicros = 50000;
            l = CyclicObs.Plan(want, new List<ProjectBlock> { main, ob }, "nautilus", false);
            Eq(l[0].Action + " " + l[0].Was + " -> " + l[0].Now, "recreated OB30, cyclic interrupt, 50 ms -> OB30, cyclic interrupt, 100 ms", "a new cycle shows old and new");
            ob.CycleMicros = 100000;
            ob.Number = 31;
            Eq(CyclicObs.Plan(want, new List<ProjectBlock> { main, ob }, "nautilus", false)[0].Action, "recreated", "a new number");
            ob.Number = 30;
            ob.Class = "ProgramCycle";
            ob.CycleMicros = 100000; // only the class differs
            l = CyclicObs.Plan(want, new List<ProjectBlock> { main, ob }, "nautilus", false);
            Eq(l[0].Action + " " + l[0].Was, "recreated OB30, ProgramCycle", "a program cycle OB of that name");
            // the number held outside the group
            var foreign = PB("Legacy", "OB", 30, "");
            foreign.Class = CyclicObs.Cyclic;
            TiadException e = Throws<TiadException>(delegate { CyclicObs.Plan(want, new List<ProjectBlock> { main, foreign }, "nautilus", true); }, "a foreign OB30");
            True(e != null && e.Kind == Kinds.ForeignBlock && e.Message.Contains("\"Legacy\" [OB30] outside group \"nautilus\" holds it"), "names the foreign holder");
            // the number held by another OB of the group
            var other = PB("naut_OB_old", "OB", 30, "nautilus");
            other.Class = CyclicObs.Cyclic;
            e = Throws<TiadException>(delegate { CyclicObs.Plan(want, new List<ProjectBlock> { main, other }, "nautilus", false); }, "an owned OB30 without prune");
            True(e != null && e.Kind == Kinds.BadRequest && e.Message.Contains("import with prune"), "names prune");
            l = CyclicObs.Plan(want, new List<ProjectBlock> { main, other }, "nautilus", true);
            Eq(l.Count, 2, "a removal and a creation");
            Eq(l[0].Name + " " + l[0].Action, "naut_OB_old removed", "the removal comes first");
            Eq(Step(l, "naut_OB_main").Action, "created", "then the creation");
            // an OB of the group asked for under another number frees its own
            // (recreated; every delete comes before any import), with or
            // without prune: two OBs may trade numbers
            var two = new List<CyclicOB> { want[0], new CyclicOB { Name = "naut_OB_old", Number = 31, CycleMs = 10 } };
            l = CyclicObs.Plan(two, new List<ProjectBlock> { main, other }, "nautilus", false);
            Eq(Step(l, "naut_OB_main").Action + "; " + Step(l, "naut_OB_old").Action + " " + Step(l, "naut_OB_old").Was + " -> " + Step(l, "naut_OB_old").Now,
                "created; recreated OB30, cyclic interrupt, 0 ms -> OB31, cyclic interrupt, 10 ms", "a renumbering");
            Eq(l.Count, 2, "nothing removed");
        }

        static void CyclicObsVerifyAndXml()
        {
            var want = new List<CyclicOB> { new CyclicOB { Name = "naut_OB_main", Number = 30, CycleMs = 100 } };
            var ob = PB("naut_OB_main", "OB", 30, "nautilus");
            ob.Class = CyclicObs.Cyclic;
            ob.CycleMicros = 100000;
            Eq(CyclicObs.Verify(want, new List<ProjectBlock> { ob }, "nautilus").Count, 0, "as asked");
            ob.Class = "ProgramCycle";
            ob.Number = 123;
            Eq(string.Join("; ", CyclicObs.Verify(want, new List<ProjectBlock> { ob }, "nautilus").ToArray()),
                "\"naut_OB_main\" is OB123, ProgramCycle, not OB30, cyclic interrupt, 100 ms", "a program cycle OB is named");
            Eq(string.Join("; ", CyclicObs.Verify(want, new List<ProjectBlock>(), "nautilus").ToArray()), "\"naut_OB_main\" is missing", "missing");
            ob.Class = CyclicObs.Cyclic;
            ob.Number = 30;
            ob.CycleMicros = 100000;
            ob.Group = "";
            Contains(string.Join("; ", CyclicObs.Verify(want, new List<ProjectBlock> { ob }, "nautilus").ToArray()), "outside the group", "outside the group");
            ob.Group = "nautilus";
            ob.CycleMicros = 99999;
            Contains(string.Join("; ", CyclicObs.Verify(want, new List<ProjectBlock> { ob }, "nautilus").ToArray()), "99999 \u00b5s", "an odd cycle in microseconds");
            Eq(CyclicObs.VerifyFailed(new List<string> { "x" }).Kind, Kinds.OperationFailed, "the failure's kind");
            var decl = new List<Declared> { new Declared { Kind = "OB", Name = "naut_OB_main", Source = "ob.scl" }, new Declared { Kind = "FB", Name = "F", Source = "fb.scl" } };
            var results = new List<SourceResult> { new SourceResult { Path = "ob.scl", Ok = false, Messages = new List<string> { "syntax error" } }, new SourceResult { Path = "fb.scl", Ok = false } };
            Eq(string.Join("; ", CyclicObs.FailedSources(decl, results).ToArray()), "the source of OB \"naut_OB_main\" (ob.scl) did not import: syntax error",
                "an asked OB's failed source, not another failed source");
            results[0].Ok = true;
            Eq(CyclicObs.FailedSources(decl, results).Count, 0, "an imported OB source");

            string x = CyclicObs.Xml(new CyclicOB { Name = "a<b&c", Number = 31, CycleMs = 250 });
            Contains(x, "<CyclicTime>250000</CyclicTime>", "the cycle in microseconds");
            Contains(x, "<Number>31</Number>", "the number");
            Contains(x, "<AutoNumber>false</AutoNumber>", "no auto number");
            Contains(x, "<SecondaryType>CyclicInterrupt</SecondaryType>", "the class");
            Contains(x, "<ProgrammingLanguage>LAD</ProgrammingLanguage>", "an empty LAD body (an empty SCL one is refused)");
            Contains(x, "<Name>a&lt;b&amp;c</Name>", "the name escaped");
            Contains(x, "<Member Name=\"Event_Count\" Datatype=\"Int\"", "the cyclic interrupt interface");
            True(!x.Contains("en-US"), "no project language assumed");
            var step = new OBStep { Name = "n", Action = "recreated", Was = "w", Now = "z" };
            Eq(Json.Write(step.ToJson()), "{\"name\":\"n\",\"action\":\"recreated\",\"was\":\"w\",\"now\":\"z\"}", "a step's JSON");
        }
    }
}
