// Deploys.cs: the deploy record (siemens-ide.md sections 3.5 and 7, D7).
//
// Every real download archives the session's project first, in the same
// TIA work item as the download and before it: what is archived is what
// TIA loads. The archive is content-addressed (archives/<sha256>.zap21)
// under TIAD_DEPLOYS_DIR, which the files API never reaches. The journal
// (deploys.jsonl) gets an intent line before TIA's download is called and
// an outcome line on every way the download ends. An archive or an intent
// line that cannot be written refuses the download: nothing was sent to
// the CPU. An intent with no outcome (the agent died during the download)
// lists as in doubt, never as not loaded.
//
// What the caller claims (source.commit, source.hash) is recorded as its
// claim: tiad checks only its shape. Whether the CPU still runs a deploy
// is POST /v1/deploys/verify (Verify.cs): it opens a copy of the archive
// and compares it online.

using System;
using System.Collections.Generic;
using System.IO;
using System.Security.Cryptography;
using System.Text;

namespace Tiad
{
    // DeploySource is the caller's claim of what a deploy was built from:
    // the git commit, and a hash of the sources it sent. A second copy of
    // tiad.DeploySource.Validate. Change both or neither.
    public sealed class DeploySource
    {
        public string Commit = "", Hash = "";

        public const string HashPrefix = "sha256:";

        public static DeploySource Read(Reader r)
        {
            if (r == null) return null;
            var s = new DeploySource();
            s.Commit = r.Str("commit");
            s.Hash = r.Str("hash");
            return s;
        }

        static bool LowerHex(string s, int from)
        {
            for (int i = from; i < s.Length; i++)
            {
                char c = s[i];
                if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return false;
            }
            return true;
        }

        // Validate is why the source is not one tiad records, or null: a
        // commit is a git object id (40 or 64 lower-case hex digits), a
        // hash, when given, sha256:<64 lower-case hex digits>.
        public string Validate()
        {
            if ((Commit.Length != 40 && Commit.Length != 64) || !LowerHex(Commit, 0))
            {
                return "source.commit is not a git commit id (40 or 64 lower-case hex digits)";
            }
            if (Hash != "" && (Hash.Length != HashPrefix.Length + 64 || !Hash.StartsWith(HashPrefix, StringComparison.Ordinal) || !LowerHex(Hash, HashPrefix.Length)))
            {
                return "source.hash is not sha256:<64 lower-case hex digits>";
            }
            return null;
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["commit"] = Commit;
            if (Hash != "") o["hash"] = Hash;
            // Recorded as said, never checked against anything.
            o["claimedBy"] = "caller";
            return o;
        }

        // Audit is the source for the audit line: "unset", or its parts
        // quoted (a refused request's included, so none can forge a line).
        public static string Audit(DeploySource s)
        {
            if (s == null) return "unset";
            return "{commit=" + Part(s.Commit) + " hash=" + (s.Hash == "" ? "unset" : Part(s.Hash)) + "}";
        }

        static string Part(string v)
        {
            // Over 100 bytes is never valid: only its size, so a refused
            // request cannot flood the journal.
            int n = Encoding.UTF8.GetByteCount(v);
            if (n > 100) return "<" + n + " bytes, over the limit>";
            return Json.Quote(v);
        }
    }

    // OutsideChanges is the operator's accept of a download onto a CPU whose
    // deploy record says it was changed outside nautilus, or cannot say
    // (#789): who accepts, what the caller's verify found, and the record
    // it verified. Recorded as the caller's claim on the intent line and
    // in the audit line, so an overwrite of someone's change is on the
    // record. A second copy of tiad.OutsideChanges.Validate. Change both or
    // neither.
    public sealed class OutsideChanges
    {
        public string By = "", Verdict = "", Deploy = "";

        public const int MaxBy = 200, MaxDeploy = 100;
        public const string Different = "different", Broken = "broken", OtherProject = "other-project", Unreadable = "unreadable";

        public static OutsideChanges Read(Reader r)
        {
            if (r == null) return null;
            var o = new OutsideChanges();
            o.By = r.Str("by");
            o.Verdict = r.Str("verdict");
            o.Deploy = r.Str("deploy");
            return o;
        }

        public string Validate()
        {
            if (By.Trim() == "") return "outsideChanges.by is empty: who accepts replacing the CPU's outside change";
            if (Encoding.UTF8.GetByteCount(By) > MaxBy) return "outsideChanges.by is longer than " + MaxBy + " bytes";
            if (Verdict != Different && Verdict != Broken && Verdict != OtherProject && Verdict != Unreadable) return "outsideChanges.verdict is not one of different, broken, other-project, unreadable";
            if (Encoding.UTF8.GetByteCount(Deploy) > MaxDeploy) return "outsideChanges.deploy is longer than " + MaxDeploy + " bytes";
            if (Deploy == "" && Verdict != Unreadable) return "outsideChanges.deploy is empty: name the deploy record whose verify found " + Verdict;
            return null;
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["by"] = By;
            o["verdict"] = Verdict;
            if (Deploy != "") o["deploy"] = Deploy;
            o["claimedBy"] = "caller";
            return o;
        }

        // Audit is the accept for the audit line: "unset", or its parts
        // quoted (a refused request's sized, so none can flood a line).
        public static string Audit(OutsideChanges o)
        {
            if (o == null) return "unset";
            return "{by=" + Part(o.By, MaxBy) + " verdict=" + Part(o.Verdict, 20) + " deploy=" + (o.Deploy == "" ? "unset" : Part(o.Deploy, MaxDeploy)) + "}";
        }

        static string Part(string v, int max)
        {
            int n = Encoding.UTF8.GetByteCount(v);
            if (n > max) return "<" + n + " bytes, over the limit>";
            return Json.Quote(v);
        }
    }

    // DeployArchive is a deploy's archived project.
    public sealed class DeployArchive
    {
        // File is relative to TIAD_DEPLOYS_DIR, with forward slashes.
        public string File = "", Sha256 = "";
        public long Bytes;

        public Obj ToJson()
        {
            var o = new Obj();
            o["file"] = File;
            o["sha256"] = Sha256;
            o["bytes"] = Bytes;
            return o;
        }
    }

    // DeployStatus is what a deploy record says of the CPU.
    public static class DeployStatus
    {
        // Loaded: TIA loaded and the compare right after was equal.
        public const string Loaded = "loaded";
        // NotLoaded: refused or cancelled before anything was loaded.
        public const string NotLoaded = "not-loaded";
        // InDoubt: the CPU may or may not be loaded (outcome_unknown, a
        // failure tiad cannot place, or no outcome line at all).
        public const string InDoubt = "in-doubt";

        // Of is the status for a download that ended with kind ("loaded" on
        // success), once its intent line was written. Only the kinds that,
        // there, say nothing was loaded are not-loaded: a check TIA asked
        // that the request does not answer yes, and TIA failing before it
        // asked one (an internal error of tiad's is outcome_unknown there).
        // Anything else is in doubt, the safe side.
        public static string Of(string kind)
        {
            if (kind == Loaded) return Loaded;
            switch (kind)
            {
                case Kinds.DecisionRequired:
                case Kinds.Declined:
                case Kinds.OperationFailed:
                    return NotLoaded;
            }
            return InDoubt;
        }
    }

    // DeployStore is TIAD_DEPLOYS_DIR: archives/, staging/ and the journal.
    public sealed class DeployStore
    {
        public const string JournalName = "deploys.jsonl";
        public const int Version = 1;
        // MaxMessage bounds a failure's text in an outcome line, in UTF-16
        // units.
        public const int MaxMessage = 4000;

        readonly string dir, archives, staging, journal;
        readonly object mu = new object();

        DeployStore(string dir)
        {
            this.dir = dir;
            archives = Path.Combine(dir, "archives");
            staging = Path.Combine(dir, "staging");
            journal = Path.Combine(dir, JournalName);
        }

        // Open creates the directory and its parts if needed and proves it
        // usable (a file written, read back and removed). The agent refuses
        // to start otherwise: a download it could not record would be
        // refused anyway. Staging left by an agent that died is removed: no
        // journal line ever names it.
        public static DeployStore Open(string dir)
        {
            var st = new DeployStore(dir);
            try
            {
                Directory.CreateDirectory(dir);
                Directory.CreateDirectory(st.archives);
                Directory.CreateDirectory(st.staging);
                string probe = Path.Combine(dir, ".probe-" + Guid.NewGuid().ToString("N"));
                System.IO.File.WriteAllText(probe, "tiad deploys directory probe\n");
                if (System.IO.File.ReadAllText(probe) != "tiad deploys directory probe\n") throw new IOException("a probe file read back differently");
                System.IO.File.Delete(probe);
                // Only what NewId and a verify (VerifyStage) name: a
                // mis-set TIAD_DEPLOYS_DIR loses nothing else.
                foreach (string pattern in new[] { "d-*", VerifyPrefix + "*", AdoptPrefix + "*" })
                {
                    foreach (string d in Directory.GetDirectories(st.staging, pattern))
                    {
                        Log.Line("deploys: removing staging left by an earlier run: {0}", Json.Quote(Path.GetFileName(d)));
                        Directory.Delete(d, true);
                    }
                }
            }
            catch (Exception e)
            {
                throw new ConfigException("TIAD_DEPLOYS_DIR " + dir + " is not usable (" + e.GetType().Name + ": " + e.Message +
                    "): tiad refuses to start rather than download what it cannot record");
            }
            return st;
        }

        public string Dir
        {
            get { return dir; }
        }

        // NewId is a fresh deploy id: tiad's UTC clock and 32 random bits.
        public static string NewId()
        {
            var b = new byte[4];
            using (var rng = RandomNumberGenerator.Create()) rng.GetBytes(b);
            return "d-" + DateTime.UtcNow.ToString("yyyyMMdd'T'HHmmssfff'Z'", System.Globalization.CultureInfo.InvariantCulture) + "-" + Hex(b);
        }

        static string Hex(byte[] b)
        {
            var s = new StringBuilder(b.Length * 2);
            foreach (byte x in b) s.Append(x.ToString("x2", System.Globalization.CultureInfo.InvariantCulture));
            return s.ToString();
        }

        // Stage makes an empty staging directory for deploy id, for TIA to
        // archive into.
        public string Stage(string id)
        {
            string d = Path.Combine(staging, id);
            if (Directory.Exists(d)) Directory.Delete(d, true);
            Directory.CreateDirectory(d);
            return d;
        }

        // Discard removes deploy id's staging, if any, logging a failure.
        public void Discard(string id)
        {
            string d = Path.Combine(staging, id);
            try
            {
                if (Directory.Exists(d)) Directory.Delete(d, true);
            }
            catch (Exception e)
            {
                Log.Line("deploys: removing staging {0} failed: {1}", Json.Quote(id), Json.Quote(e.Message));
            }
        }

        // VerifyPrefix starts a verify's staging directory, AdoptPrefix an
        // adopt's (Agent.AdoptProject).
        public const string VerifyPrefix = "v-", AdoptPrefix = "a-";

        // VerifyCopy is the name of a verify's copy of an archive: short,
        // since TIA retrieves the project into a directory named after it
        // (VerifyStage), and never taken from the journal.
        public const string VerifyCopy = "deploy.zap21";

        // VerifyStage copies the archive a deploy record names into a fresh
        // staging directory of its own (dir), for a verify to open, and
        // hashes the copy: what TIA opens is what was hashed. It returns
        // why the archive is broken (the record's archive entry is not one
        // tiad writes, the file is gone, or the copy's sha256 is not the
        // record's), or null with copy set. A broken archive never
        // compares: its directory is removed and dir is "".
        public string VerifyStage(object archive, out string dir, out string copy)
        {
            dir = "";
            copy = "";
            var a = archive as IDictionary<string, object>;
            object f, h;
            string file = a != null && a.TryGetValue("file", out f) ? f as string : null;
            string sha = a != null && a.TryGetValue("sha256", out h) ? h as string : null;
            if (file == null || sha == null || sha.Length != 64 || !LowerHex(sha) || file != "archives/" + sha + ".zap21")
            {
                return "the record's archive entry is not one tiad writes (archives/<sha256>.zap21 with its sha256)";
            }
            string src = Path.Combine(archives, sha + ".zap21");
            if (!System.IO.File.Exists(src)) return "the archive " + file + " is gone";
            var b = new byte[4];
            using (var rng = RandomNumberGenerator.Create()) rng.GetBytes(b);
            string d = Path.Combine(staging, VerifyPrefix + Hex(b));
            // Another verify's, by a 32-bit chance: never shared, never
            // removed by this one.
            if (Directory.Exists(d)) throw new IOException("the staging directory " + Path.GetFileName(d) + " is in use");
            Directory.CreateDirectory(d);
            string c = Path.Combine(d, VerifyCopy);
            string got;
            try
            {
                System.IO.File.Copy(src, c);
                using (var fs = new FileStream(c, FileMode.Open, FileAccess.Read, FileShare.Read))
                using (var hs = SHA256.Create())
                {
                    got = Hex(hs.ComputeHash(fs));
                }
            }
            catch
            {
                RemoveStaging(d);
                throw;
            }
            if (got != sha)
            {
                RemoveStaging(d);
                return "the archive " + file + " has sha256 " + got + ", not the record's";
            }
            dir = d;
            copy = c;
            return null;
        }

        // AdoptStage copies an archive into a fresh staging directory of
        // its own (dir), for an adopt to retrieve, and returns the copy's
        // sha256: what TIA retrieves is what was hashed. The adopt removes
        // dir (RemoveStaging); a start removes one an agent that died left.
        public string AdoptStage(string src, out string dir, out string copy, out long bytes)
        {
            dir = "";
            copy = "";
            bytes = 0;
            var b = new byte[4];
            using (var rng = RandomNumberGenerator.Create()) rng.GetBytes(b);
            string d = Path.Combine(staging, AdoptPrefix + Hex(b));
            if (Directory.Exists(d)) throw new IOException("the staging directory " + Path.GetFileName(d) + " is in use");
            Directory.CreateDirectory(d);
            string c = Path.Combine(d, VerifyCopy);
            string sha;
            try
            {
                System.IO.File.Copy(src, c);
                using (var fs = new FileStream(c, FileMode.Open, FileAccess.Read, FileShare.Read))
                using (var hs = SHA256.Create())
                {
                    sha = Hex(hs.ComputeHash(fs));
                    bytes = fs.Length;
                }
            }
            catch
            {
                RemoveStaging(d);
                throw;
            }
            dir = d;
            copy = c;
            return sha;
        }

        // ArchivePath is where the archive a deploy record names lives, or
        // null when the record's entry is not one tiad writes.
        public string ArchivePath(object archive, out string sha)
        {
            var a = archive as IDictionary<string, object>;
            object f, h;
            string file = a != null && a.TryGetValue("file", out f) ? f as string : null;
            sha = a != null && a.TryGetValue("sha256", out h) ? h as string : null;
            if (file == null || sha == null || sha.Length != 64 || !LowerHex(sha) || file != "archives/" + sha + ".zap21") return null;
            return Path.Combine(archives, sha + ".zap21");
        }

        static bool LowerHex(string s)
        {
            foreach (char c in s)
            {
                if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return false;
            }
            return true;
        }

        // RemoveStaging removes a verify's staging directory, logging a
        // failure (the next start removes it).
        public void RemoveStaging(string d)
        {
            try
            {
                if (Directory.Exists(d)) Directory.Delete(d, true);
            }
            catch (Exception e)
            {
                Log.Line("deploys: removing staging {0} failed: {1}", Json.Quote(Path.GetFileName(d)), Json.Quote(e.Message));
            }
        }

        // Adopt hashes the archive TIA wrote into deploy id's staging and
        // moves it to archives/<sha256>.zap21. A file already at that name
        // is replaced, never trusted by its name: the file the record names
        // is always the one just hashed.
        public DeployArchive Adopt(string id, string staged)
        {
            string stageDir = Path.GetFullPath(Path.Combine(staging, id));
            string full = Path.GetFullPath(staged);
            if (!string.Equals(Path.GetDirectoryName(full), stageDir, StringComparison.OrdinalIgnoreCase))
            {
                throw new IOException("TIA's archive is not in the deploy's staging directory");
            }
            string sha;
            long bytes;
            // Through to the disk before it is hashed, as the intent line
            // that will name it is.
            using (var f = new FileStream(full, FileMode.Open, FileAccess.ReadWrite, FileShare.Read))
            using (var h = SHA256.Create())
            {
                f.Flush(true);
                sha = Hex(h.ComputeHash(f));
                bytes = f.Length;
            }
            string name = sha + ".zap21";
            string target = Path.Combine(archives, name);
            lock (mu)
            {
                if (System.IO.File.Exists(target)) System.IO.File.Replace(full, target, null);
                else System.IO.File.Move(full, target);
            }
            Discard(id);
            return new DeployArchive { File = "archives/" + name, Sha256 = sha, Bytes = bytes };
        }

        // Append writes one journal line, through to the disk. A line
        // left torn by an agent that died is closed first, so it never
        // swallows this one.
        public void Append(Obj line)
        {
            line["v"] = Version;
            byte[] b = new UTF8Encoding(false).GetBytes(Json.Write(line) + "\n");
            lock (mu)
            {
                using (var f = new FileStream(journal, FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.Read, 4096, FileOptions.WriteThrough))
                {
                    if (f.Length > 0)
                    {
                        f.Seek(-1, SeekOrigin.End);
                        if (f.ReadByte() != '\n')
                        {
                            f.Seek(0, SeekOrigin.End);
                            f.WriteByte((byte)'\n');
                        }
                    }
                    f.Seek(0, SeekOrigin.End);
                    f.Write(b, 0, b.Length);
                    f.Flush(true);
                }
            }
        }

        // List is the deploy records, newest first, each intent with its
        // outcome (address "" for every CPU). unreadable counts the lines
        // it could not read; they are skipped, never guessed at.
        public List<object> List(string address, out int unreadable)
        {
            unreadable = 0;
            var intents = new List<IDictionary<string, object>>();
            var outcomes = new Dictionary<string, IDictionary<string, object>>(StringComparer.Ordinal);
            string[] lines;
            lock (mu)
            {
                if (!System.IO.File.Exists(journal)) return new List<object>();
                using (var f = new FileStream(journal, FileMode.Open, FileAccess.Read, FileShare.ReadWrite))
                using (var r = new StreamReader(f, new UTF8Encoding(false)))
                {
                    lines = r.ReadToEnd().Split('\n');
                }
            }
            foreach (string raw in lines)
            {
                string line = raw.TrimEnd('\r');
                if (line.Trim() == "") continue;
                IDictionary<string, object> o;
                try
                {
                    o = Json.Parse(line) as IDictionary<string, object>;
                }
                catch (TiadException)
                {
                    o = null;
                }
                object v, kind, id;
                if (o == null || !o.TryGetValue("v", out v) || !(v is int) || (int)v != Version ||
                    !o.TryGetValue("kind", out kind) || !(kind is string) || !o.TryGetValue("deploy", out id) || !(id is string))
                {
                    unreadable++;
                    continue;
                }
                if ((string)kind == "intent") intents.Add(o);
                else if ((string)kind == "outcome")
                {
                    if (!outcomes.ContainsKey((string)id)) outcomes[(string)id] = o;
                }
                else unreadable++;
            }
            var list = new List<object>();
            for (int i = intents.Count - 1; i >= 0; i--)
            {
                var it = intents[i];
                object a;
                if (address != "" && !(it.TryGetValue("address", out a) && a as string == address)) continue;
                var e = new Obj();
                string id = (string)it["deploy"];
                e["id"] = id;
                foreach (var kv in it)
                {
                    if (kv.Key == "v" || kv.Key == "kind" || kv.Key == "deploy") continue;
                    e[kv.Key] = kv.Value;
                }
                IDictionary<string, object> oc;
                string status = DeployStatus.InDoubt;
                if (outcomes.TryGetValue(id, out oc))
                {
                    var x = new Obj();
                    foreach (var kv in oc)
                    {
                        if (kv.Key == "v" || kv.Key == "kind" || kv.Key == "deploy") continue;
                        x[kv.Key] = kv.Value;
                    }
                    object s;
                    if (x.TryGetValue("status", out s) && (s as string == DeployStatus.Loaded || s as string == DeployStatus.NotLoaded)) status = (string)s;
                    e["outcome"] = x;
                }
                else
                {
                    e["outcome"] = null;
                }
                e["status"] = status;
                list.Add(e);
            }
            return list;
        }

        // Bound is a failure's text for an outcome line.
        public static string Bound(string s)
        {
            if (s == null) return "";
            if (s.Length <= MaxMessage) return s;
            return s.Substring(0, MaxMessage) + " ... (" + s.Length + " characters in all)";
        }
    }

    // DeployRun is one real download's deploy record as it is made.
    public sealed class DeployRun
    {
        public string Id = "";
        public DeployArchive Archive;
        // IntentWritten: the intent line is on disk, so an outcome line
        // must follow; Recorded: so did the outcome line.
        public bool IntentWritten, Recorded;
        public string RecordError = "";
        // Kind is how the download ended: DeployStatus.Loaded, or the
        // error's kind; Message and Payload are the error's.
        public string Kind = "", Message = "";
        public Obj Payload;
        // A successful download's parts, for the outcome line.
        public string State = "", StateSource = "", UserManagementReset = "";
        public bool Hardware;
        public List<string> Blocks = new List<string>();
        public FingerprintRun Fingerprint;
        // FingerprintUnavailable is why the CPU has no fingerprint to read
        // (fingerprint_unavailable: its access control is on, #1336), in
        // place of FingerprintError: the read was not tried, nothing failed.
        public string FingerprintError = "", FingerprintUnavailable = "", DownloadId = "";
        // TrustAfter is the instance's blind trust once the post-load read
        // ended, read or failed: a failed read with the opt-in may have
        // trusted (UnknownTrust).
        public BlindTrust TrustAfter;
        // Response is the success's "deploy" object; "recorded" is set once
        // the outcome line was written.
        public Obj Response;
    }

    public sealed partial class Agent
    {
        readonly DeployStore deploys;

        static string Now()
        {
            return DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ", System.Globalization.CultureInfo.InvariantCulture);
        }

        // JournalLine starts a journal line: the format's version first.
        static Obj JournalLine(string kind, string id)
        {
            var o = new Obj();
            o["v"] = DeployStore.Version;
            o["kind"] = kind;
            o["deploy"] = id;
            o["at"] = Now();
            return o;
        }

        // Record archives the session's project and writes the intent
        // line, on the TIA thread, in the download's own work item, just
        // before TIA's download. Either failing refuses the download, with
        // nothing sent to the CPU.
        void Record(ITia t, Session s, DownloadRequest req, BlindTrust rode, DeployRun run, List<LayoutObject> layout, string baseline)
        {
            // A project left part-way is never archived as a deploy.
            if (s.Unclean) throw CpuReuse.Unclean("archiving it for the deploy record");
            if (s.ForeignChanged) throw Ownership.Unusable("archiving it for the deploy record");
            string id = DeployStore.NewId();
            DeployArchive a;
            try
            {
                string stage = deploys.Stage(id);
                string staged = t.Archive(s.Handle, stage);
                a = deploys.Adopt(id, staged);
            }
            catch (Exception e)
            {
                deploys.Discard(id);
                if (e is TiadException) throw;
                if (e is TiaException) host.TiaFailed(e);
                string why = e is TiaException ? "TIA: " + TiaNames.Message(e) : e.GetType().Name + ": " + e.Message;
                Log.Line("audit deploy archive-failed session={0} address={1} why={2}", s.Id, Json.Quote(req.Address), Json.Quote(why));
                throw new TiadException(500, Kinds.OperationFailed, "archiving the project for the deploy record failed (" + why + "); nothing was sent to the CPU");
            }
            var line = JournalLine("intent", id);
            line["session"] = s.Id;
            line["project"] = s.Project;
            line["address"] = req.Address;
            line["hardware"] = req.Hardware;
            line["software"] = req.Software;
            line["source"] = req.Source == null ? null : req.Source.ToJson();
            if (req.OutsideChanges != null) line["outsideChanges"] = req.OutsideChanges.ToJson();
            var tia = new Obj();
            tia["version"] = host.TiaVersion;
            tia["openness"] = host.OpennessVersion;
            tia["agent"] = version;
            line["tia"] = tia;
            line["archive"] = a.ToJson();
            line["attestation"] = req.Hardware ? req.Attestation.ToJson() : null;
            line["deviceAttestation"] = req.DeviceAttested == null ? null : req.DeviceAttested.ToJson();
            line["keptDevices"] = KeptDevice.ToJson(s.Kept);
            // The import group: a verify of this deploy requires the data
            // types in it by name, as the download's own compare did.
            if (s.ImportGroup != "") line["group"] = s.ImportGroup;
            // A turn-on download: an adopt of this archive stays gated
            // unless the record says loaded (TurnOnRule.MarkerSuffix).
            if (s.AccessTurnOn && !s.AccessTurnOnLoaded) line["accessTurnOn"] = true;
            line["blindTrust"] = rode == null ? null : rode.ToJson();
            // The data layout loaded (#1341): the baseline of the next
            // download, once a verify finds the CPU equal to this deploy.
            line["layout"] = LayoutRule.ToRecord(layout, host.TiaVersion);
            // What the gate compared it with: a deploy, the adopted project
            // (its adopt baseline, #1347), or nothing.
            line["layoutBaseline"] = baseline == "" ? null : baseline;
            try
            {
                deploys.Append(line);
            }
            catch (Exception e)
            {
                Log.Line("audit deploy intent-failed deploy={0} session={1} address={2} archive={3} why={4}", id, s.Id, Json.Quote(req.Address), a.File, Json.Quote(e.GetType().Name + ": " + e.Message));
                throw new TiadException(500, Kinds.OperationFailed, "writing the deploy record's intent line failed (" + e.GetType().Name + ": " + e.Message +
                    "); nothing was sent to the CPU (the archive " + a.File + " is named by no record)");
            }
            run.Id = id;
            run.Archive = a;
            run.IntentWritten = true;
            Log.Line("audit deploy intent deploy={0} session={1} address={2} archive={3} bytes={4} source={5} outsideChanges={6}", id, s.Id, Json.Quote(req.Address), a.File, a.Bytes, DeploySource.Audit(req.Source), OutsideChanges.Audit(req.OutsideChanges));
        }

        // WriteOutcome writes the outcome line of a download whose intent
        // line was written, whatever its end. One that cannot be written is
        // logged, and a success says so (deploy.recorded false): the record
        // then lists as in doubt, the safe side.
        void WriteOutcome(DeployRun run, BlindTrust rode, List<string> acting)
        {
            string kind = run.Kind == "" ? "error" : run.Kind;
            var line = JournalLine("outcome", run.Id);
            line["status"] = DeployStatus.Of(kind);
            line["outcome"] = kind;
            if (kind == DeployStatus.Loaded)
            {
                if (run.State != "")
                {
                    line["state"] = run.State;
                    line["stateSource"] = run.StateSource;
                }
                line["compareEqual"] = true;
                line["blocks"] = new List<object>(run.Blocks.ToArray());
                if (run.Hardware) line["hardwareVerified"] = false;
                line["fingerprint"] = run.Fingerprint == null ? null : run.Fingerprint.ToJson();
                if (run.FingerprintError != "") line["fingerprintError"] = DeployStore.Bound(run.FingerprintError);
                if (run.FingerprintUnavailable != "") line["fingerprintUnavailable"] = DeployStore.Bound(run.FingerprintUnavailable);
                if (run.DownloadId != "") line["downloadId"] = run.DownloadId;
                line["blindTrustAfter"] = run.TrustAfter == null ? null : run.TrustAfter.ToJson();
            }
            else
            {
                line["message"] = DeployStore.Bound(run.Message);
                if (run.Payload != null)
                {
                    object v;
                    if (run.Payload.TryGetValue("state", out v)) line["state"] = v;
                    if (run.Payload.TryGetValue("stateSource", out v)) line["stateSource"] = v;
                }
            }
            // A reset tiad gave, by its route, whatever the end: a download
            // in doubt after it may have deleted the CPU's users.
            if (run.UserManagementReset != "") line["userManagementReset"] = run.UserManagementReset;
            if (acting.Count > 0) line["actingAnswers"] = new List<object>(acting.ToArray());
            line["blindTrust"] = rode == null ? null : rode.ToJson();
            try
            {
                deploys.Append(line);
                run.Recorded = true;
            }
            catch (Exception e)
            {
                run.RecordError = "writing the deploy record's outcome line failed (" + e.GetType().Name + ": " + e.Message + "); the record lists as in doubt";
                Log.Line("audit deploy outcome-failed deploy={0} outcome={1} why={2}", run.Id, kind, Json.Quote(e.GetType().Name + ": " + e.Message));
            }
            if (run.Response != null)
            {
                run.Response["recorded"] = run.Recorded;
                if (!run.Recorded) run.Response["recordError"] = run.RecordError;
            }
        }

        // WithDeploy is e with the deploy id in its data, so a caller can
        // find the record of a download that ended in a failure.
        static TiadException WithDeploy(TiadException e, string id)
        {
            var data = new Obj();
            var old = e.Payload as Obj;
            if (e.Payload != null && old == null) return e;
            if (old != null) foreach (var kv in old) data[kv.Key] = kv.Value;
            data["deploy"] = id;
            return new TiadException(e.Status, e.Kind, e.Message, data, e.Fatal);
        }

        // DeployTrustFix is the fix a post-load fingerprint read TIA's TLS
        // question refused names.
        const string DeployTrustFix = "to read it with the download, trust the CPU's certificate blindly (fingerprint.trustUnverifiedCertificate: true); the agent's TIA instance then keeps that trust, and a later download in it needs decisions.acceptBlindTrust, until every session is closed (the agent then retires that instance) or the agent restarts";

        // ReadDeployFingerprint reads the CPU's fingerprint after a load
        // that compared equal, and makes the response's deploy object. A
        // failed read is recorded, never thrown: the load stands.
        void ReadDeployFingerprint(Session s, DownloadRequest req, DeployRun run)
        {
            FingerprintRun fp = null;
            try
            {
                fp = ReadFingerprintAudited(s, run.Id, DeployTrustFix, delegate(FingerprintRun r)
                {
                    r.Trust = req.FingerprintTrust;
                    r.Address = req.Address;
                });
            }
            catch (TiadException e)
            {
                // A CPU with access control on has no fingerprint to read
                // (#1336): recorded as unavailable, never as an error.
                if (e.Kind == Kinds.FingerprintUnavailable) run.FingerprintUnavailable = e.Message;
                else run.FingerprintError = e.Message;
            }
            catch (Exception e)
            {
                run.FingerprintError = e.Message;
            }
            run.Fingerprint = fp;
            run.TrustAfter = fp != null ? fp.BlindTrust : blindTrust;
            var d = new Obj();
            d["id"] = run.Id;
            d["archive"] = run.Archive.ToJson();
            d["source"] = req.Source == null ? null : req.Source.ToJson();
            if (req.OutsideChanges != null) d["outsideChanges"] = req.OutsideChanges.ToJson();
            d["fingerprint"] = fp == null ? null : fp.ToJson();
            if (fp == null && run.FingerprintUnavailable != "") d["fingerprintUnavailable"] = run.FingerprintUnavailable;
            else if (fp == null) d["fingerprintError"] = run.FingerprintError;
            string dl = fp == null ? null : fp.Value("DownloadId");
            if (dl != null)
            {
                run.DownloadId = dl;
                d["downloadId"] = dl;
            }
            // The instance's trust after the read, whether it read or not:
            // what a later download in it rides on.
            d["blindTrustAfter"] = run.TrustAfter == null ? null : run.TrustAfter.ToJson();
            d["recorded"] = false;
            run.Response = d;
        }

        void RegisterDeploys(Router r)
        {
            r.Add("GET", "/v1/deploys", ListDeploys);
            r.Add("POST", "/v1/deploys/verify", VerifyDeploy);
        }

        // ListDeploys is GET /v1/deploys[?address=<ipv4>]: the deploy
        // records, newest first. It never starts TIA.
        object ListDeploys(Call c)
        {
            var q = c.Request.QueryString;
            string address = "";
            foreach (string k in q.AllKeys)
            {
                if (k != "address") throw TiadException.BadRequest("deploys: unknown query parameter {0}", Json.Quote(k ?? ""));
                string[] vs = q.GetValues(k);
                if (vs == null || vs.Length != 1) throw TiadException.BadRequest("deploys: address given more than once");
                address = vs[0];
                if (!Ip.IsIPv4(address)) throw TiadException.BadRequest("deploys: address {0} is not an IPv4 address", Json.Quote(address));
            }
            int unreadable;
            List<object> list;
            try
            {
                list = deploys.List(address, out unreadable);
            }
            catch (Exception e)
            {
                if (!(e is IOException) && !(e is UnauthorizedAccessException)) throw;
                throw new TiadException(500, Kinds.OperationFailed, "the deploy journal cannot be read (" + e.Message + ")");
            }
            var o = new Obj();
            o["deploys"] = list;
            o["unreadableLines"] = unreadable;
            return o;
        }
    }
}
