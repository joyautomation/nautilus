// Agent.cs: the calls that never start TIA (siemens-ide.md section 3.4): health,
// probe and the work-dir files. They cost no trial time on a fresh clone,
// so nothing here may reach TIA or load an Openness assembly.

using System;
using System.Collections.Generic;
using System.IO;

namespace Tiad
{
    // Gate is one prerequisite probe reports, in one of three states:
    //   ok       checked, and it holds;
    //   failed   checked, and it does not: every TIA call answers 412 gate;
    //   unknown  not readable yet (the licence before TIA's first start):
    //            reported honestly, but it blocks nothing.
    public sealed class Gate
    {
        public const string StateOk = "ok", StateFailed = "failed", StateUnknown = "unknown";

        public string Name;
        public string State = StateFailed;
        public string Detail = "";
        public string Remedy = "";

        // Ok is true only for a gate that was checked and holds.
        public bool Ok
        {
            get { return State == StateOk; }
        }

        // Blocks reports whether this gate stops TIA calls. Any state but
        // ok and unknown blocks, so a state this code does not know fails
        // closed.
        public bool Blocks
        {
            get { return State != StateOk && State != StateUnknown; }
        }

        public static bool KnownState(string s)
        {
            return s == StateOk || s == StateFailed || s == StateUnknown;
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["name"] = Name;
            o["ok"] = Ok;
            o["state"] = State;
            o["detail"] = Detail;
            if (Remedy != "") o["remedy"] = Remedy;
            return o;
        }
    }

    // IHost is what the agent knows about the machine without starting
    // TIA. The real host reads the registry and the file system; the fake
    // host is scripted.
    public interface IHost
    {
        // TiaVersion is the installed TIA Portal major ("V21"), or "".
        string TiaVersion { get; }

        // OpennessVersion is the Openness API version tiad uses, or "".
        string OpennessVersion { get; }

        // Gates are the machine's gates, in order: tia, openness-group,
        // openness-allowlist, licence. The agent adds workdir itself.
        List<Gate> Gates();

        // TiaStarted tells the host how the TIA side's start went (error is
        // null when it started), which is when the licence becomes known.
        void TiaStarted(Exception error);

        // TiaFailed tells the host a TIA call failed after the start: a
        // licence error there fails the licence gate too.
        void TiaFailed(Exception error);
    }

    public sealed partial class Agent
    {
        public const string Version = "0.1.0";

        readonly Config cfg;
        readonly AccessRecords records;
        readonly IHost host;
        readonly WorkDir work;
        readonly string version;

        // makeTia makes the TIA side. It is called once, on the TIA thread,
        // by the first call that needs TIA, never before: health, probe and
        // the files never start TIA.
        public Agent(Config cfg, IHost host, Func<ITia> makeTia, string versionSuffix)
        {
            this.cfg = cfg;
            this.host = host;
            this.makeTia = makeTia;
            Directory.CreateDirectory(cfg.WorkDir);
            work = new WorkDir(cfg.WorkDir);
            // Refuses to start (ConfigException) on a pins dir it cannot use.
            pins = PinStore.Open(cfg.PinsDir);
            LogPins();
            // Refuses to start on a deploys dir it cannot use either: a
            // download it could not record is refused.
            deploys = DeployStore.Open(cfg.DeploysDir);
            Log.Line("deploys: the deploy record is in {0}", Json.Quote(cfg.DeploysDir));
            if (cfg.AccessKey != "")
            {
                records = AccessRecords.FromHex(cfg.AccessKey);
            }
            else
            {
                records = AccessRecords.Random();
                Log.Line("access records: no TIAD_ACCESS_KEY_FILE, so the key is this run's only: a later run cannot verify its records");
            }
            version = Version + versionSuffix;
            StartReaper(cfg.SessionIdle);
        }

        public WorkDir Work
        {
            get { return work; }
        }

        public void Register(Router r)
        {
            r.Add("GET", "/v1/health", Health);
            r.Add("GET", "/v1/probe", Probe);
            r.Add("GET", "/v1/files", ListFiles);
            r.Add("PUT", "/v1/files/{path...}", PutFile);
            r.Add("GET", "/v1/files/{path...}", GetFile);
            r.Add("DELETE", "/v1/files/{path...}", DeleteFile);
            RegisterTia(r);
            RegisterDownload(r);
            RegisterPins(r);
            RegisterFingerprint(r);
            RegisterDeploys(r);
            RegisterAdopt(r);
            RegisterShow(r);
        }

        object Health(Call c)
        {
            var o = new Obj();
            o["version"] = version;
            o["tia"] = host.TiaVersion;
            o["openness"] = host.OpennessVersion;
            o["sessions"] = sessions.Count;
            o["targets"] = cfg.Targets.ToArray();
            o["ui"] = cfg.Ui;
            return o;
        }

        // Gates is every gate, the machine's and the work dir's. A TIA call
        // runs only when all of them pass.
        public List<Gate> Gates()
        {
            var gs = new List<Gate>();
            foreach (var g in host.Gates()) gs.Add(g.Name == "tia" ? TiaGate(g) : g);
            gs.Add(WorkDirGate());
            return gs;
        }

        Gate WorkDirGate()
        {
            var g = new Gate { Name = "workdir", Detail = work.Root };
            try
            {
                string probe = Path.Combine(work.Root, ".tiad-probe-" + Guid.NewGuid().ToString("N"));
                File.WriteAllBytes(probe, new byte[0]);
                File.Delete(probe);
                g.State = Gate.StateOk;
            }
            catch (Exception e)
            {
                g.Detail = work.Root + ": " + e.Message;
                g.Remedy = "make TIAD_WORKDIR a directory the agent's account can write";
            }
            return g;
        }

        object Probe(Call c)
        {
            var gates = Gates();
            bool usable = true;
            var list = new List<object>();
            foreach (var g in gates)
            {
                usable = usable && !g.Blocks;
                list.Add(g.ToJson());
            }
            var o = new Obj();
            o["usable"] = usable;
            o["gates"] = list;
            return o;
        }

        // --- files ------------------------------------------------------

        // UrlPath is a {path...} route part: it must already be clean.
        // Go's own router redirects an unclean URL, and the client never
        // sends one, so the agent refuses it rather than guess.
        string UrlPath(Call c)
        {
            string p = c.Var("path");
            string full = work.Resolve(p);
            if (Paths.CleanPath(p) != p)
            {
                throw TiadException.BadRequest("path {0} is not in clean form", Json.Quote(p));
            }
            return full;
        }

        static Obj FileEntry(string path, long size)
        {
            var o = new Obj();
            o["path"] = path;
            o["size"] = size;
            return o;
        }

        // replaceLock serializes the last step of a put and a delete, so two
        // puts of one path never race each other's rename. Windows needs it:
        // MoveFile refuses an existing target, so two first puts of a new
        // path collide. POSIX rename overwrites, so Mono never shows the
        // race; the suite under TIAD_BIN, on Windows, does.
        readonly object replaceLock = new object();

        object PutFile(Call c)
        {
            c.RequireContentType("application/octet-stream");
            string full = UrlPath(c);
            if (Directory.Exists(full))
            {
                throw TiadException.BadRequest("{0} is a directory", work.Rel(full));
            }
            string dir = Path.GetDirectoryName(full);
            try
            {
                Directory.CreateDirectory(dir);
            }
            catch (IOException)
            {
                throw TiadException.BadRequest("a parent of {0} is a file", work.Rel(full));
            }
            // Write beside the target, then rename over it: a reader never
            // sees half a file, and a link at the target is replaced, not
            // followed.
            string tmp = Path.Combine(dir, ".tiad-put-" + Guid.NewGuid().ToString("N"));
            long size = 0;
            try
            {
                using (var f = new FileStream(tmp, FileMode.CreateNew, FileAccess.Write))
                {
                    var buf = new byte[81920];
                    int n;
                    while ((n = c.Body.Read(buf, 0, buf.Length)) > 0)
                    {
                        size += n;
                        if (size > cfg.MaxFileBytes)
                        {
                            throw new TiadException(413, Kinds.BadRequest, "file larger than " + cfg.MaxFileBytes + " bytes (TIAD_MAX_FILE_BYTES)");
                        }
                        f.Write(buf, 0, n);
                    }
                }
                lock (replaceLock)
                {
                    // Replace is one rename: a reader sees the old file or
                    // the new one, never none.
                    if (File.Exists(full)) File.Replace(tmp, full, null);
                    else File.Move(tmp, full);
                }
            }
            finally
            {
                if (File.Exists(tmp)) File.Delete(tmp);
            }
            return FileEntry(work.Rel(full), size);
        }

        object GetFile(Call c)
        {
            string full = UrlPath(c);
            if (!File.Exists(full))
            {
                throw TiadException.NotFound("no file {0}", c.Var("path"));
            }
            return new FileReply(full);
        }

        object DeleteFile(Call c)
        {
            string full = UrlPath(c);
            if (!File.Exists(full))
            {
                throw TiadException.NotFound("no file {0}", c.Var("path"));
            }
            lock (replaceLock)
            {
                File.Delete(full);
            }
            return new Obj();
        }

        object ListFiles(Call c)
        {
            var files = new List<KeyValuePair<string, long>>();
            Walk(work.Root, files);
            files.Sort(delegate(KeyValuePair<string, long> a, KeyValuePair<string, long> b)
            {
                return string.CompareOrdinal(a.Key, b.Key);
            });
            var list = new List<object>();
            foreach (var f in files) list.Add(FileEntry(f.Key, f.Value));
            var o = new Obj();
            o["files"] = list;
            return o;
        }

        // Walk lists the files under dir. Links are skipped, never
        // followed: they are not part of the work dir.
        void Walk(string dir, List<KeyValuePair<string, long>> files)
        {
            foreach (string f in Directory.GetFiles(dir))
            {
                var fi = new FileInfo(f);
                if ((fi.Attributes & FileAttributes.ReparsePoint) != 0) continue;
                string name = fi.Name;
                if (name.StartsWith(".tiad-", StringComparison.Ordinal)) continue;
                files.Add(new KeyValuePair<string, long>(work.Rel(f), fi.Length));
            }
            foreach (string d in Directory.GetDirectories(dir))
            {
                if ((new DirectoryInfo(d).Attributes & FileAttributes.ReparsePoint) != 0) continue;
                Walk(d, files);
            }
        }
    }
}
