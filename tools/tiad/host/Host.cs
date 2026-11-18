// Host.cs: tiad.exe, the agent on a TIA machine. It is the core with the
// Windows host: the machine gates read from the registry, the file system
// and the process token (core/Machine.cs computes them), and the TIA side
// loaded from tiad.Openness.dll on the first call that needs TIA.
//
// tiad.exe references nothing of Siemens's. tiad.Openness.dll, the only
// assembly that does, is loaded by name from beside tiad.exe, never
// before a TIA call, so health, probe and the files cannot start TIA. If
// it is absent the call fails, and with TIAD_NO_TIA=1 every TIA call is
// refused by the tia gate before anything is loaded.
//
// install-tiad.ps1 builds it with the machine's stock csc (C# 5).

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Reflection;
using System.Security.Cryptography;
using System.Security.Principal;
using System.Threading;
using Microsoft.Win32;

namespace Tiad.Host
{
    // WindowsMachine is what the gates read on the real machine.
    sealed class WindowsMachine : IMachine
    {
        readonly string exePath;

        public WindowsMachine()
        {
            // The process's own image: the application TIA's allowlist
            // check names.
            using (var p = Process.GetCurrentProcess())
            {
                exePath = p.MainModule.FileName;
            }
        }

        static RegistryKey Open(string key)
        {
            using (var hklm = RegistryKey.OpenBaseKey(RegistryHive.LocalMachine, RegistryView.Registry64))
            {
                return hklm.OpenSubKey(key);
            }
        }

        public string RegString(string key, string name)
        {
            using (var k = Open(key))
            {
                if (k == null) return null;
                return k.GetValue(name) as string;
            }
        }

        public List<string> RegSubKeys(string key)
        {
            using (var k = Open(key))
            {
                if (k == null) return null;
                return new List<string>(k.GetSubKeyNames());
            }
        }

        public bool FileExists(string path)
        {
            return File.Exists(path);
        }

        public string ExePath
        {
            get { return exePath; }
        }

        public DateTime ExeLastWriteUtc
        {
            get { return File.GetLastWriteTimeUtc(exePath); }
        }

        public byte[] ExeSha256
        {
            get
            {
                using (var s = File.OpenRead(exePath))
                using (var h = SHA256.Create())
                {
                    return h.ComputeHash(s);
                }
            }
        }

        // OpennessGroup reads the process token, not the group's members:
        // a member added after this logon is not in the token, and TIA
        // goes by the token.
        public GroupState OpennessGroup()
        {
            SecurityIdentifier sid;
            try
            {
                sid = (SecurityIdentifier)new NTAccount(Environment.MachineName, MachineGates.GroupName).Translate(typeof(SecurityIdentifier));
            }
            catch (IdentityNotMappedException)
            {
                return GroupState.NoSuchGroup;
            }
            using (var id = WindowsIdentity.GetCurrent())
            {
                if (id.Groups != null)
                {
                    foreach (var g in id.Groups)
                    {
                        if (sid.Equals(g)) return GroupState.Member;
                    }
                }
            }
            return GroupState.NotInToken;
        }
    }

    sealed class WindowsHost : IHost
    {
        readonly IMachine machine;
        readonly bool noTia;

        public WindowsHost(IMachine machine, bool noTia)
        {
            this.machine = machine;
            this.noTia = noTia;
        }

        public string TiaVersion
        {
            get { return MachineGates.TiaVersion(machine); }
        }

        public string OpennessVersion
        {
            get { return MachineGates.OpennessVersion(machine); }
        }

        public List<Gate> Gates()
        {
            return MachineGates.Evaluate(machine, noTia);
        }
    }

    static class Openness
    {
        public const string Assembly = "tiad.Openness.dll";
        public const string Type = "Tiad.Openness.OpennessTia";

        // Load makes the TIA side from tiad.Openness.dll beside tiad.exe.
        // It runs on the TIA thread, at the first call that needs TIA.
        public static ITia Load()
        {
            string dir = Path.GetDirectoryName(System.Reflection.Assembly.GetEntryAssembly().Location);
            string path = Path.Combine(dir, Assembly);
            if (!File.Exists(path))
            {
                throw new TiaException(Assembly + " is not installed beside tiad.exe, so this agent cannot start TIA");
            }
            Type t = System.Reflection.Assembly.LoadFrom(path).GetType(Type, false);
            if (t == null || !typeof(ITia).IsAssignableFrom(t))
            {
                throw new TiaException(Assembly + " has no " + Type + " implementing ITia");
            }
            return (ITia)Activator.CreateInstance(t);
        }
    }

    static class Program
    {
        static int Main(string[] args)
        {
            if (Path.DirectorySeparatorChar != '\\')
            {
                Console.Error.WriteLine("tiad: runs on Windows only (tools/tiad/out/tiad-fake.exe is the test agent)");
                return 2;
            }
            Config cfg;
            try
            {
                cfg = Config.FromEnv();
            }
            catch (ConfigException e)
            {
                Console.Error.WriteLine("tiad: " + e.Message);
                return 2;
            }

            var host = new WindowsHost(new WindowsMachine(), cfg.NoTia);
            var agent = new Agent(cfg, host, Openness.Load, "");
            var router = new Router();
            agent.Register(router);
            var server = new Server(cfg, router);
            server.Start();

            if (cfg.ExitOnStdin)
            {
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
            }

            server.Run();
            return 0;
        }
    }
}
