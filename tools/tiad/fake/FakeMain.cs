// FakeMain.cs: tiad-fake, the agent's core with a scripted host and a
// scripted ITia instead of TIA. It exists for the contract suite
// (siemens/tiad/tiadtest), which runs it under Mono, and is never built by
// install-tiad.ps1.
//
// Beyond the agent's own calls it answers control calls that the real
// agent never has:
//
//   PUT /fake/gates   {"gates": [{name, ok, detail, remedy}]}
//       the machine gates probe reports (the work dir gate stays real).
//   PUT /fake/script  {"compileResult": {...}, "delays": [{call, ms}],
//                      "failures": [{call, message}]}
//       what compile returns, and ITia calls that are slow or that fail
//       the way TIA fails (a TiaException). Replaces the whole script.
//   GET /fake/calls   {"calls": [{call, thread, sta}]}
//       every ITia call so far, with the thread it ran on. "start" is
//       the moment the ITia was made, which is when TIA would start.
//
// It exits when its standard input closes, so a test that dies leaves no
// process behind.

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Threading;

namespace Tiad.Fake
{
    sealed class FakeHost : IHost
    {
        readonly object mu = new object();
        List<Gate> gates = Defaults();

        static List<Gate> Defaults()
        {
            var gs = new List<Gate>();
            foreach (string n in new[] { "tia", "openness-group", "openness-allowlist", "licence" })
            {
                gs.Add(new Gate { Name = n, Ok = true, Detail = "fake" });
            }
            return gs;
        }

        public string TiaVersion
        {
            get { return "V21"; }
        }

        public string OpennessVersion
        {
            get { return "V21"; }
        }

        public List<Gate> Gates()
        {
            lock (mu)
            {
                return new List<Gate>(gates);
            }
        }

        public object SetGates(Call c)
        {
            var r = c.Json();
            var gs = new List<Gate>();
            foreach (var g in r.Children("gates"))
            {
                gs.Add(new Gate { Name = g.Str("name"), Ok = g.Bool("ok"), Detail = g.Str("detail"), Remedy = g.Str("remedy") });
            }
            r.Done();
            lock (mu)
            {
                gates = gs.Count == 0 ? Defaults() : gs;
            }
            return new Obj();
        }
    }

    // Script is what the control calls set and what FakeTia reads.
    sealed class Script
    {
        readonly object mu = new object();
        CompileResult compile = new CompileResult();
        Dictionary<string, int> delays = new Dictionary<string, int>(StringComparer.Ordinal);
        Dictionary<string, string> failures = new Dictionary<string, string>(StringComparer.Ordinal);
        readonly List<object> calls = new List<object>();

        // Enter records an ITia call, then waits or fails as scripted.
        public void Enter(string call)
        {
            int delay;
            string fail;
            lock (mu)
            {
                var o = new Obj();
                o["call"] = call;
                o["thread"] = Thread.CurrentThread.ManagedThreadId;
                o["sta"] = Thread.CurrentThread.GetApartmentState() == ApartmentState.STA;
                calls.Add(o);
                delays.TryGetValue(call, out delay);
                failures.TryGetValue(call, out fail);
            }
            if (delay > 0) Thread.Sleep(delay);
            if (fail != null) throw new TiaException(fail);
        }

        public CompileResult Compile
        {
            get
            {
                lock (mu)
                {
                    return compile;
                }
            }
        }

        public object Set(Call c)
        {
            var r = c.Json();
            var res = new CompileResult();
            var cr = r.Child("compileResult");
            if (cr != null)
            {
                res.Result = cr.Str("result");
                if (res.Result == "") res.Result = "success";
                res.Errors = cr.Int("errors");
                res.Warnings = cr.Int("warnings");
                foreach (var m in cr.Children("messages"))
                {
                    res.Messages.Add(new CompileMessage { Object = m.Str("object"), Severity = m.Str("severity"), Text = m.Str("text") });
                }
            }
            var d = new Dictionary<string, int>(StringComparer.Ordinal);
            foreach (var x in r.Children("delays")) d[x.Str("call")] = x.Int("ms");
            var f = new Dictionary<string, string>(StringComparer.Ordinal);
            foreach (var x in r.Children("failures")) f[x.Str("call")] = x.Str("message");
            r.Done();
            lock (mu)
            {
                compile = res;
                delays = d;
                failures = f;
            }
            return new Obj();
        }

        public object Calls(Call c)
        {
            var o = new Obj();
            lock (mu)
            {
                o["calls"] = calls.ToArray();
            }
            return o;
        }
    }

    sealed class FakeProject
    {
        public List<Block> Blocks = new List<Block>();
    }

    // FakeTia is ITia without TIA: it keeps projects in memory and answers
    // what the Go fake (tiadtest.Fake) answers.
    sealed class FakeTia : ITia
    {
        readonly Script script;
        readonly HashSet<string> gsd = new HashSet<string>(StringComparer.Ordinal);

        public FakeTia(Script script)
        {
            this.script = script;
            script.Enter("start");
        }

        public bool GsdInstalled(string fileName)
        {
            script.Enter("GsdInstalled");
            return gsd.Contains(fileName);
        }

        public bool InstallGsd(string fullPath)
        {
            script.Enter("InstallGsd");
            return !gsd.Add(Path.GetFileName(fullPath));
        }

        public object CreateProject(string fullPath, string name)
        {
            script.Enter("CreateProject");
            // As TIA does, refuse to create a project over an existing one.
            if (File.Exists(fullPath) || Directory.GetFileSystemEntries(Path.GetDirectoryName(fullPath)).Length > 0)
            {
                throw new TiaException("The project directory " + Path.GetDirectoryName(fullPath) + " is not empty");
            }
            File.WriteAllText(fullPath, "fake TIA project");
            return new FakeProject();
        }

        public object OpenProject(string fullPath)
        {
            script.Enter("OpenProject");
            return new FakeProject();
        }

        public void CloseProject(object project)
        {
            script.Enter("CloseProject");
        }

        public List<string> ApplyStation(object project, Station previous, Station next, Secrets secrets)
        {
            script.Enter("ApplyStation");
            return StationChanges(previous, next);
        }

        public SoftwareResult ImportSoftware(object project, string group, List<SourceFile> sources, List<SourceFile> tagTables, bool prune)
        {
            script.Enter("ImportSoftware");
            var p = (FakeProject)project;
            var res = new SoftwareResult();
            var made = new List<Block>();
            foreach (var src in sources)
            {
                var sr = new SourceResult { Path = src.Path, Ok = true };
                int found = 0;
                foreach (string line in File.ReadAllText(src.FullPath, Encoding.UTF8).Split('\n'))
                {
                    Block b = BlockHead(line.TrimEnd('\r'));
                    if (b != null)
                    {
                        made.Add(b);
                        found++;
                    }
                }
                if (found == 0)
                {
                    sr.Ok = false;
                    sr.Messages.Add("no block found in source");
                }
                res.Results.Add(sr);
            }
            foreach (var t in tagTables) res.Results.Add(new SourceResult { Path = t.Path, Ok = true });

            var byName = new Dictionary<string, Block>(StringComparer.Ordinal);
            if (!prune)
            {
                foreach (var b in p.Blocks) byName[b.Name.ToLowerInvariant()] = b;
            }
            foreach (var b in made) byName[b.Name.ToLowerInvariant()] = b;
            p.Blocks = new List<Block>(byName.Values);
            p.Blocks.Sort(delegate(Block a, Block b) { return string.CompareOrdinal(a.Name, b.Name); });
            res.Blocks.AddRange(p.Blocks);
            return res;
        }

        public CompileResult Compile(object project, bool hardware, bool software)
        {
            script.Enter("Compile");
            return script.Compile;
        }

        static readonly string[] Heads = { "ORGANIZATION_BLOCK", "FUNCTION_BLOCK", "FUNCTION", "DATA_BLOCK", "TYPE" };
        static readonly string[] Kinds = { "OB", "FB", "FC", "DB", "UDT" };

        static bool Space(char c)
        {
            return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == '\v';
        }

        // BlockHead is the Go fake's ^\s*(ORGANIZATION_BLOCK|...)\s+"([^"]+)"
        // with ASCII whitespace, as Go's \s is.
        static Block BlockHead(string line)
        {
            int i = 0;
            while (i < line.Length && Space(line[i])) i++;
            for (int k = 0; k < Heads.Length; k++)
            {
                if (string.CompareOrdinal(line, i, Heads[k], 0, Heads[k].Length) != 0) continue;
                int j = i + Heads[k].Length;
                if (j >= line.Length || !Space(line[j])) continue;
                while (j < line.Length && Space(line[j])) j++;
                if (j >= line.Length || line[j] != '"') continue;
                int end = line.IndexOf('"', j + 1);
                if (end <= j + 1) continue;
                return new Block { Name = line.Substring(j + 1, end - j - 1), Kind = Kinds[k] };
            }
            return null;
        }

        static bool Same(object a, object b)
        {
            return Json.Write(a) == Json.Write(b);
        }

        // StationChanges is tiadtest.StationChanges: in words, what applying
        // next over prev changes.
        static List<string> StationChanges(Station prev, Station next)
        {
            var outp = new List<string>();
            if (prev == null)
            {
                outp.Add("station created");
                return outp;
            }
            if (!Same(prev.Cpu.ToJson(), next.Cpu.ToJson())) outp.Add("cpu " + next.Cpu.Name + " changed");
            if (!Same(prev.WebServer == null ? null : prev.WebServer.ToJson(), next.WebServer == null ? null : next.WebServer.ToJson()))
            {
                outp.Add("web server changed");
            }
            var pm = new SortedDictionary<int, Module>();
            foreach (var m in prev.Modules) pm[m.Slot] = m;
            var nm = new SortedDictionary<int, Module>();
            foreach (var m in next.Modules) nm[m.Slot] = m;
            var slots = new SortedSet<int>(pm.Keys);
            slots.UnionWith(nm.Keys);
            foreach (int slot in slots)
            {
                Module a, b;
                bool inA = pm.TryGetValue(slot, out a), inB = nm.TryGetValue(slot, out b);
                string s = slot.ToString(System.Globalization.CultureInfo.InvariantCulture);
                if (!inA) outp.Add("module slot " + s + " added");
                else if (!inB) outp.Add("module slot " + s + " removed");
                else if (!Same(a.ToJson(), b.ToJson())) outp.Add("module slot " + s + " changed");
            }
            var pd = Devices(prev.Profinet);
            var nd = Devices(next.Profinet);
            var names = new SortedSet<string>(pd.Keys, StringComparer.Ordinal);
            names.UnionWith(nd.Keys);
            foreach (string n in names)
            {
                Device a, b;
                bool inA = pd.TryGetValue(n, out a), inB = nd.TryGetValue(n, out b);
                if (!inA) outp.Add("device " + n + " added");
                else if (!inB) outp.Add("device " + n + " removed");
                else if (!Same(a.ToJson(), b.ToJson())) outp.Add("device " + n + " changed");
            }
            if ((prev.Profinet == null) != (next.Profinet == null) ||
                prev.Profinet != null && next.Profinet != null && prev.Profinet.UpdateTime != next.Profinet.UpdateTime)
            {
                outp.Add("IO system changed");
            }
            return outp;
        }

        static Dictionary<string, Device> Devices(Profinet p)
        {
            var m = new Dictionary<string, Device>(StringComparer.Ordinal);
            if (p != null)
            {
                foreach (var d in p.Devices) m[d.Name] = d;
            }
            return m;
        }
    }

    static class Program
    {
        static int Main(string[] args)
        {
            Config cfg;
            try
            {
                cfg = Config.FromEnv();
            }
            catch (ConfigException e)
            {
                Console.Error.WriteLine("tiad-fake: " + e.Message);
                return 2;
            }

            var host = new FakeHost();
            var script = new Script();
            var agent = new Agent(cfg, host, delegate { return new FakeTia(script); }, "+fake");
            var router = new Router();
            agent.Register(router);
            router.Add("PUT", "/fake/gates", host.SetGates);
            router.Add("PUT", "/fake/script", script.Set);
            router.Add("GET", "/fake/calls", script.Calls);

            var server = new Server(cfg, router);
            server.Start();

            var stdin = new Thread(delegate()
            {
                try
                {
                    while (Console.In.ReadLine() != null)
                    {
                    }
                }
                catch (Exception)
                {
                }
                Environment.Exit(0);
            });
            stdin.IsBackground = true;
            stdin.Start();

            server.Run();
            return 0;
        }
    }
}
