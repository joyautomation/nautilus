// FakeMain.cs: tiad-fake, the agent's core with a scripted host instead
// of TIA. It exists for the contract suite (siemens/tiad/tiadtest), which
// runs it under Mono, and is never built by install-tiad.ps1.
//
// Beyond the agent's own calls it answers a control call that the real
// agent never has:
//
//   PUT /fake/gates  {"gates": [{name, ok, detail, remedy}]}
//       the machine gates probe reports (the work dir gate stays real).
//
// It exits when its standard input closes, so a test that dies leaves no
// process behind.

using System;
using System.Collections.Generic;
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
                g.Done();
            }
            r.Done();
            lock (mu)
            {
                gates = gs.Count == 0 ? Defaults() : gs;
            }
            return new Obj();
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
            var agent = new Agent(cfg, host, "+fake");
            var router = new Router();
            agent.Register(router);
            router.Add("PUT", "/fake/gates", host.SetGates);

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
