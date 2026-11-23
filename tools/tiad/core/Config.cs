// Config.cs: the agent's own configuration, from its environment only
// (siemens-ide.md section 3.1). Nothing a request carries changes it.

using System;
using System.Collections.Generic;
using System.IO;

namespace Tiad
{
    public static class Ip
    {
        // IsIPv4 is siemens/station.IsIPv4: a strict dotted quad, as Go's
        // net.ParseIP reads one. Each part is 1 to 3 decimal digits, at most
        // 255, with no leading zero; no other form (no "1.2", no hex, no
        // IPv6 and no IPv4-in-IPv6).
        public static bool IsIPv4(string s)
        {
            if (s == null) return false;
            string[] parts = s.Split('.');
            if (parts.Length != 4) return false;
            foreach (string p in parts)
            {
                if (p.Length < 1 || p.Length > 3) return false;
                if (p.Length > 1 && p[0] == '0') return false;
                int v = 0;
                foreach (char c in p)
                {
                    if (c < '0' || c > '9') return false;
                    v = v * 10 + (c - '0');
                }
                if (v > 255) return false;
            }
            return true;
        }
    }

    public sealed class Config
    {
        public const string DefaultAddr = "http://127.0.0.1:8190";

        // Host and Port to listen on. Port 0 picks a free port (tests).
        public string Host;
        public int Port;
        public bool Loopback;
        public string Token = "";
        public string WorkDir;
        // PinsDir holds the device pins (TIAD_PINS_DIR; on Windows
        // %ProgramData%\tiad\pins by default). Never inside the work dir.
        public string PinsDir;
        // DeploysDir holds the deploy record: every deploy's project
        // archive and the journal (TIAD_DEPLOYS_DIR; on Windows
        // %ProgramData%\tiad\deploys by default). Never inside the work
        // dir, and never nested with the pins dir.
        public string DeploysDir;
        public List<string> Targets = new List<string>();
        // AccessKey is the agent's access record key (TIAD_ACCESS_KEY_FILE,
        // 64 hex characters, made by the installer and kept across
        // reinstalls), or "" for a key of this run only.
        public string AccessKey = "";

        // MaxFileBytes caps one uploaded file (TIAD_MAX_FILE_BYTES).
        public long MaxFileBytes = 1L << 30;

        // SessionIdle is how long a session may sit unused before the
        // reaper closes it (TIAD_SESSION_IDLE, a Go duration).
        public TimeSpan SessionIdle = TimeSpan.FromHours(1);

        // RetireWatch is how long the agent waits for a retire of a TIA
        // instance that trusted blindly before it counts TIA as gone
        // (TIAD_RETIRE_WATCH, a Go duration): TIA's Dispose has no bound
        // of its own.
        public TimeSpan RetireWatch = TimeSpan.FromSeconds(180);

        // NoTia (TIAD_NO_TIA=1) fails the tia gate, so no call can start
        // TIA: the agent is installed and testable before the trial clock
        // may start.
        public bool NoTia;

        // ExitOnStdin (TIAD_EXIT_ON_STDIN=1) ends the agent when its
        // standard input closes, so a test that dies leaves no process. Off
        // by default: under a scheduled task stdin may be closed from the
        // start.
        public bool ExitOnStdin;

        // Ui (TIAD_UI=1) starts TIA Portal with its user interface, for
        // viewing only: a block can then be shown in TIA's editor (POST
        // /v1/sessions/{id}/show), and every download is refused, since a
        // click or a dialog in the visible Portal could change one. Off by
        // default: tiad drives TIA without a window.
        public bool Ui;

        // Flag reads a switch that is "1" or unset; anything else is a
        // mistake worth refusing to start over.
        static bool Flag(string name)
        {
            string v = Env(name);
            if (v == "") return false;
            if (v == "1") return true;
            throw new ConfigException(name + " must be 1 or unset");
        }

        static string Env(string name)
        {
            return Environment.GetEnvironmentVariable(name) ?? "";
        }

        // Inside reports whether path is dir or below it, compared as full
        // paths, case-insensitively on Windows.
        static bool Inside(string path, string dir)
        {
            string p = Path.GetFullPath(path).TrimEnd(Path.DirectorySeparatorChar);
            string d = Path.GetFullPath(dir).TrimEnd(Path.DirectorySeparatorChar);
            StringComparison cmp = Path.DirectorySeparatorChar == '\\' ? StringComparison.OrdinalIgnoreCase : StringComparison.Ordinal;
            return string.Equals(p, d, cmp) || p.StartsWith(d + Path.DirectorySeparatorChar, cmp);
        }

        // FromEnv reads TIAD_ADDR, TIAD_TOKEN or TIAD_TOKEN_FILE,
        // TIAD_WORKDIR, TIAD_PINS_DIR, TIAD_DEPLOYS_DIR and TIAD_TARGETS, and refuses a configuration that
        // would be unsafe: no token, or a target that is not an IPv4
        // address.
        public static Config FromEnv()
        {
            var c = new Config();

            string addr = Env("TIAD_ADDR");
            if (addr == "") addr = DefaultAddr;
            Uri u;
            if (!Uri.TryCreate(addr, UriKind.Absolute, out u) || u.Scheme != "http" ||
                (u.AbsolutePath != "/" && u.AbsolutePath != "") || u.Query != "" || u.UserInfo != "")
            {
                throw new ConfigException("TIAD_ADDR must be http://host:port (e.g. " + DefaultAddr + ")");
            }
            // One IPv4 address: loopback, or the machine's address on a
            // trusted network (a tailnet). Never 0.0.0.0, which is every
            // interface, and never a name, which could resolve to anything.
            if (!Ip.IsIPv4(u.Host) || u.Host == "0.0.0.0")
            {
                throw new ConfigException("TIAD_ADDR must name one IPv4 address to listen on (e.g. " + DefaultAddr + "), not " + Json.Quote(u.Host));
            }
            c.Host = u.Host;
            c.Port = u.Port;
            c.Loopback = u.IsLoopback;

            c.Token = Env("TIAD_TOKEN").Trim();
            string tokenFile = Env("TIAD_TOKEN_FILE");
            if (c.Token == "" && tokenFile != "")
            {
                try
                {
                    c.Token = File.ReadAllText(tokenFile).Trim();
                }
                catch (Exception e)
                {
                    throw new ConfigException("TIAD_TOKEN_FILE: " + e.Message);
                }
                if (c.Token == "") throw new ConfigException("TIAD_TOKEN_FILE " + tokenFile + " is empty");
            }
            if (c.Token == "")
            {
                // A download stops a CPU. Loopback is no exception: any
                // process on the machine, or a browser on it, reaches
                // loopback too.
                throw new ConfigException("a token is required (TIAD_TOKEN or TIAD_TOKEN_FILE), on loopback too");
            }

            string keyFile = Env("TIAD_ACCESS_KEY_FILE");
            if (keyFile != "")
            {
                try
                {
                    c.AccessKey = File.ReadAllText(keyFile).Trim();
                    AccessRecords.FromHex(c.AccessKey);
                }
                catch (Exception e)
                {
                    throw new ConfigException("TIAD_ACCESS_KEY_FILE: " + e.Message);
                }
            }

            c.WorkDir = Env("TIAD_WORKDIR");
            if (c.WorkDir == "")
            {
                if (Path.DirectorySeparatorChar != '\\') throw new ConfigException("TIAD_WORKDIR is required off Windows");
                c.WorkDir = @"C:\tiad-work";
            }

            c.PinsDir = Env("TIAD_PINS_DIR");
            if (c.PinsDir == "")
            {
                if (Path.DirectorySeparatorChar != '\\') throw new ConfigException("TIAD_PINS_DIR is required off Windows");
                c.PinsDir = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "tiad", "pins");
            }
            // The files API writes anywhere in the work dir: a pin there
            // could be replaced by any caller, unaudited.
            if (Inside(c.PinsDir, c.WorkDir) || Inside(c.WorkDir, c.PinsDir))
            {
                throw new ConfigException("TIAD_PINS_DIR must be outside TIAD_WORKDIR (and not contain it): the files API must never reach a pin");
            }

            c.DeploysDir = Env("TIAD_DEPLOYS_DIR");
            if (c.DeploysDir == "")
            {
                if (Path.DirectorySeparatorChar != '\\') throw new ConfigException("TIAD_DEPLOYS_DIR is required off Windows");
                c.DeploysDir = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "tiad", "deploys");
            }
            // The files API writes anywhere in the work dir: an archive or a
            // journal line there could be replaced by any caller, unaudited.
            if (Inside(c.DeploysDir, c.WorkDir) || Inside(c.WorkDir, c.DeploysDir))
            {
                throw new ConfigException("TIAD_DEPLOYS_DIR must be outside TIAD_WORKDIR (and not contain it): the files API must never reach the deploy record");
            }
            // Each store owns its directory: neither lists the other's files.
            if (Inside(c.DeploysDir, c.PinsDir) || Inside(c.PinsDir, c.DeploysDir))
            {
                throw new ConfigException("TIAD_DEPLOYS_DIR and TIAD_PINS_DIR must not be the same directory or one inside the other");
            }

            string max = Env("TIAD_MAX_FILE_BYTES");
            if (max != "" && (!long.TryParse(max, System.Globalization.NumberStyles.None, System.Globalization.CultureInfo.InvariantCulture, out c.MaxFileBytes) || c.MaxFileBytes < 1))
            {
                throw new ConfigException("TIAD_MAX_FILE_BYTES must be a positive number of bytes");
            }

            string idle = Env("TIAD_SESSION_IDLE");
            if (idle != "")
            {
                long ns;
                if (!GoDuration.TryParse(idle, out ns) || ns < 1000000L)
                {
                    throw new ConfigException("TIAD_SESSION_IDLE must be a duration of at least 1ms (e.g. 1h)");
                }
                c.SessionIdle = TimeSpan.FromTicks(ns / 100);
            }

            string watch = Env("TIAD_RETIRE_WATCH");
            if (watch != "")
            {
                long ns;
                // At most 1h: a timer takes no more than about 49 days, and
                // a retire that long is stuck anyway.
                if (!GoDuration.TryParse(watch, out ns) || ns < 1000000L || ns > 3600L * 1000000000L)
                {
                    throw new ConfigException("TIAD_RETIRE_WATCH must be a duration from 1ms to 1h (e.g. 3m)");
                }
                c.RetireWatch = TimeSpan.FromTicks(ns / 100);
            }

            c.NoTia = Flag("TIAD_NO_TIA");
            c.ExitOnStdin = Flag("TIAD_EXIT_ON_STDIN");
            c.Ui = Flag("TIAD_UI");

            foreach (string t in Env("TIAD_TARGETS").Split(new[] { ',', ' ', ';' }, StringSplitOptions.RemoveEmptyEntries))
            {
                if (!Ip.IsIPv4(t)) throw new ConfigException("TIAD_TARGETS: " + Json.Quote(t) + " is not an IPv4 address");
                if (!c.Targets.Contains(t)) c.Targets.Add(t);
            }
            return c;
        }
    }
}
