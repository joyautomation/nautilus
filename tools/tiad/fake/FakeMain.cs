// FakeMain.cs: tiad-fake, the agent's core with a scripted host and a
// scripted ITia instead of TIA. It exists for the contract suite
// (siemens/tiad/tiadtest), which runs it under Mono, and is never built by
// install-tiad.ps1.
//
// Beyond the agent's own calls it answers control calls that the real
// agent never has:
//
//   PUT /fake/gates   {"gates": [{name, state, ok, detail, remedy}]}
//       the machine gates probe reports (the work dir gate stays real).
//       state is ok, failed or unknown; "bogus" tests a state the agent
//       does not know. ok is ignored: it is derived from state.
//   PUT /fake/machine {...}
//       a scripted machine (ScriptedMachine) instead: probe runs the real
//       gate evaluator, MachineGates, over it. PUT /fake/gates drops it.
//   PUT /fake/script  {"compileResult": {...}, "delays": [{call, ms}],
//                      "failures": [{call, message}]}
//       what compile returns, and ITia calls that are slow or that fail
//       the way TIA fails (a TiaException). Replaces the whole script.
//   GET /fake/calls   {"calls": [{call, thread, sta}]}
//       every ITia call so far, with the thread it ran on. "start" is
//       the moment the ITia was made, which is when TIA would start.
//   GET /fake/downloads {"downloads": [{address, hardware, software, state}]}
//       every download that loaded the CPU. Secrets are never recorded.
//
// PUT /fake/script also takes "questions": [{id, message, detail,
// objects}], TIA's pre-download checks for every download.
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
        // machine, when scripted, replaces the scripted gates: probe then
        // runs the real gate evaluator (MachineGates) over it.
        ScriptedMachine machine;
        // licence is latched by the TIA side's start, as the real host's
        // is; probe shows it with a scripted machine.
        readonly LicenceLatch licence = new LicenceLatch();

        static List<Gate> Defaults()
        {
            var gs = new List<Gate>();
            foreach (string n in new[] { "tia", "openness-group", "openness-allowlist", "licence" })
            {
                gs.Add(new Gate { Name = n, State = Gate.StateOk, Detail = "fake" });
            }
            return gs;
        }

        public string TiaVersion
        {
            get
            {
                var m = Machine();
                return m == null ? "V21" : MachineGates.TiaVersion(m);
            }
        }

        public string OpennessVersion
        {
            get
            {
                var m = Machine();
                return m == null ? "V21" : MachineGates.OpennessVersion(m);
            }
        }

        ScriptedMachine Machine()
        {
            lock (mu)
            {
                return machine;
            }
        }

        public List<Gate> Gates()
        {
            var m = Machine();
            if (m != null) return MachineGates.Evaluate(m, m.NoTia, licence.Current());
            lock (mu)
            {
                return new List<Gate>(gates);
            }
        }

        public void TiaStarted(Exception error)
        {
            licence.Started(error);
        }

        public void TiaFailed(Exception error)
        {
            licence.Failed(error);
        }

        public object SetGates(Call c)
        {
            var r = c.Json();
            var gs = new List<Gate>();
            foreach (var g in r.Children("gates"))
            {
                string state = g.Str("state");
                // ok is derived from state, as the agent derives it.
                g.OptBool("ok");
                if (!Gate.KnownState(state) && state != "bogus")
                {
                    throw TiadException.BadRequest("gate state {0}: want ok, failed or unknown (or bogus, to test an unknown state)", Json.Quote(state));
                }
                gs.Add(new Gate { Name = g.Str("name"), State = state, Detail = g.Str("detail"), Remedy = g.Str("remedy") });
            }
            r.Done();
            lock (mu)
            {
                gates = gs.Count == 0 ? Defaults() : gs;
                machine = null;
            }
            return new Obj();
        }

        public object SetMachine(Call c)
        {
            var m = ScriptedMachine.Read(c.Json());
            lock (mu)
            {
                machine = m;
            }
            return new Obj();
        }
    }

    // ScriptedMachine is an IMachine from PUT /fake/machine:
    //   {"noTia": bool,
    //    "registry": [{key, name, value}], "subkeys": [{key, names}],
    //    "files": [path], "exePath": path,
    //    "exeModified": "2026-11-18T12:00:00.1234567Z" (UTC, round trip),
    //    "exeSha256": base64, "group": "member" | "not-in-token" | "missing"}
    sealed class ScriptedMachine : IMachine
    {
        public bool NoTia;
        readonly Dictionary<string, string> values = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        readonly Dictionary<string, List<string>> subkeys = new Dictionary<string, List<string>>(StringComparer.OrdinalIgnoreCase);
        readonly HashSet<string> files = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        string exePath = "";
        DateTime exeModified;
        byte[] exeSha256 = new byte[0];
        GroupState group;

        public static ScriptedMachine Read(Reader r)
        {
            var m = new ScriptedMachine();
            m.NoTia = r.Bool("noTia");
            foreach (var v in r.Children("registry"))
            {
                m.values[v.Str("key") + "|" + v.Str("name")] = v.Str("value");
            }
            foreach (var k in r.Children("subkeys"))
            {
                m.subkeys[k.Str("key")] = k.Strs("names");
            }
            foreach (string f in r.Strs("files")) m.files.Add(f);
            m.exePath = r.Str("exePath");
            string mod = r.Str("exeModified");
            if (mod != "" && !DateTime.TryParseExact(mod, "o", System.Globalization.CultureInfo.InvariantCulture,
                System.Globalization.DateTimeStyles.RoundtripKind, out m.exeModified))
            {
                throw TiadException.BadRequest("exeModified {0}: want a round-trip UTC time", Json.Quote(mod));
            }
            if (m.exeModified.Kind != DateTimeKind.Utc && mod != "") throw TiadException.BadRequest("exeModified: want UTC (Z)");
            try
            {
                m.exeSha256 = Convert.FromBase64String(r.Str("exeSha256"));
            }
            catch (FormatException)
            {
                throw TiadException.BadRequest("exeSha256: want base64");
            }
            switch (r.Str("group"))
            {
                case "member": m.group = GroupState.Member; break;
                case "not-in-token": m.group = GroupState.NotInToken; break;
                case "missing": m.group = GroupState.NoSuchGroup; break;
                default: throw TiadException.BadRequest("group: want member, not-in-token or missing");
            }
            r.Done();
            return m;
        }

        public string RegString(string key, string name)
        {
            string v;
            return values.TryGetValue(key + "|" + name, out v) ? v : null;
        }

        public List<string> RegSubKeys(string key)
        {
            List<string> v;
            return subkeys.TryGetValue(key, out v) ? new List<string>(v) : null;
        }

        public bool FileExists(string path)
        {
            return files.Contains(path);
        }

        public string ExePath
        {
            get { return exePath; }
        }

        public DateTime ExeLastWriteUtc
        {
            get { return exeModified; }
        }

        public byte[] ExeSha256
        {
            get { return exeSha256; }
        }

        public GroupState OpennessGroup()
        {
            return group;
        }
    }

    // Script is what the control calls set and what FakeTia reads.
    sealed class Script
    {
        readonly object mu = new object();
        CompileResult compile = new CompileResult();
        Dictionary<string, int> delays = new Dictionary<string, int>(StringComparer.Ordinal);
        Dictionary<string, string> failures = new Dictionary<string, string>(StringComparer.Ordinal);
        List<Question> questions = new List<Question>();
        bool loadDespiteCancel;
        int askFirst = 1;
        string failAfterDecide = "";
        string failInDoubt = "";
        // downloadThrows makes the download fail with an exception that is
        // not TIA's, before it asks anything: an internal error.
        string downloadThrows = "";
        string tiaDies = "";
        bool noDryRun;
        // archiveSame makes every archive the same bytes, so two deploys
        // share one content address.
        bool archiveSame;
        bool cleanCancel;
        bool compareDiffers;
        string compareTree = "";
        string downloadState = "";
        bool uploadAsksTls;
        bool fingerprintAsksTls;
        List<KeyValuePair<string, string>> fingerprintItems = new List<KeyValuePair<string, string>>();
        bool fingerprintNoItems;
        string cannotApply = "";
        DeviceIdentity device = BenchDevice();
        bool noIdentify;
        PasswordPolicy policy = new PasswordPolicy();
        string uploadAccess = UploadedAccessControl;
        string accessRecords = "";

        // AccessRecords is what an open does to tiad's records in a saved
        // project: "" keeps them; "strip-users" opens the users without
        // them and "strip-roles" the roles without tiad's comment, as in a
        // project tiad did not make them in; "rekey" opens the records as
        // another agent key's.
        public string AccessRecords
        {
            get
            {
                lock (mu)
                {
                    return accessRecords;
                }
            }
        }

        // UploadedAccessControl is the bench CPU's access control as the
        // S2a upload found it, unless a script says otherwise.
        public const string UploadedAccessControl = "Disabled";

        // Policy is the projects' password policy for local users: TIA's
        // default unless a script says otherwise.
        public PasswordPolicy Policy
        {
            get
            {
                lock (mu)
                {
                    return new PasswordPolicy { MinLength = policy.MinLength, MinDigits = policy.MinDigits, MinSpecial = policy.MinSpecial, UpperAndLower = policy.UpperAndLower };
                }
            }
        }

        // UploadAccessControl is the access control an upload brings.
        public string UploadAccessControl
        {
            get
            {
                lock (mu)
                {
                    return uploadAccess;
                }
            }
        }

        // BenchDevice is the fake CPU's identity unless a script says
        // otherwise: the Go fake's tiad.BenchDevice.
        public static DeviceIdentity BenchDevice()
        {
            return new DeviceIdentity { Order = "6ES7 510-1DK03-0AB0", Firmware = "V3.1" };
        }

        // Device is the fake CPU's identity, as an upload and an online
        // read report it.
        public DeviceIdentity Device
        {
            get
            {
                lock (mu)
                {
                    return new DeviceIdentity { Order = device.Order, Firmware = device.Firmware, Serial = device.Serial, Mac = device.Mac };
                }
            }
        }

        // NoIdentify makes the fake's TIA side one that cannot read a CPU's
        // identity online, as tiad.Openness cannot yet.
        public bool NoIdentify
        {
            get
            {
                lock (mu)
                {
                    return noIdentify;
                }
            }
        }

        // CannotApply is what the fake's TIA side says it cannot apply yet,
        // as tiad.Openness says of modules today, or "".
        public string CannotApply
        {
            get
            {
                lock (mu)
                {
                    return cannotApply;
                }
            }
        }

        // UploadAsksTls makes going online for an upload ask TLS trust, as
        // a CPU with secure PG/PC communication and no known certificate does.
        public bool UploadAsksTls
        {
            get
            {
                lock (mu)
                {
                    return uploadAsksTls;
                }
            }
        }

        // FingerprintAsksTls makes a fingerprint read ask TLS trust, as the
        // bench CPU's does (#789 stage 1b: going online asks nothing).
        public bool FingerprintAsksTls
        {
            get
            {
                lock (mu)
                {
                    return fingerprintAsksTls;
                }
            }
        }

        // FingerprintItems, when not empty, is what every fingerprint read
        // returns instead of the fake CPU's own; FingerprintNoItems makes
        // it return none.
        public List<KeyValuePair<string, string>> FingerprintItems
        {
            get
            {
                lock (mu)
                {
                    return new List<KeyValuePair<string, string>>(fingerprintItems);
                }
            }
        }

        public bool FingerprintNoItems
        {
            get
            {
                lock (mu)
                {
                    return fingerprintNoItems;
                }
            }
        }

        // NoDryRun makes the fake's TIA side one that cannot promise a dry
        // run loads nothing, as tiad.Openness cannot until S2b.
        // CleanCancel makes the fake's TIA side one that answers each check
        // itself, without the decider, and reports a clean cancel (nothing
        // loaded, nothing thrown) at the first answer of null, even after
        // acting answers: a TIA side breaking the ITia contract, to prove
        // the agent itself never calls that a clean refusal.
        // CompareDiffers makes every compare differ, even right after a
        // download: a CPU that did not take what was sent.
        // CompareTree, when set, is the compare tree TIA reports, whatever
        // the CPU holds: "empty" (an identical root, nothing under it),
        // "emptyFolders" (an identical root over empty folders only),
        // "folderDiffers" (a folder says its contents differ but shows no
        // differing child) or "irrelevantRoot" (the root is
        // CompareIrrelevant over an identical block): trees that compare
        // nothing or hide a difference, which must never pass as equal.
        // DownloadState is the state TIA's download result reports when it
        // ran ("" = Success).
        public string DownloadState
        {
            get
            {
                lock (mu)
                {
                    return downloadState;
                }
            }
        }

        public bool CompareDiffers
        {
            get
            {
                lock (mu)
                {
                    return compareDiffers;
                }
            }
        }

        public string CompareTree
        {
            get
            {
                lock (mu)
                {
                    return compareTree;
                }
            }
        }

        public bool CleanCancel
        {
            get
            {
                lock (mu)
                {
                    return cleanCancel;
                }
            }
        }

        public bool ArchiveSame
        {
            get
            {
                lock (mu)
                {
                    return archiveSame;
                }
            }
        }

        public bool NoDryRun
        {
            get
            {
                lock (mu)
                {
                    return noDryRun;
                }
            }
        }

        // FailInDoubt, when set, makes Download fail the way TIA fails
        // inside a download it had begun, before any question.
        public string DownloadThrows
        {
            get
            {
                lock (mu)
                {
                    return downloadThrows;
                }
            }
        }

        public string FailInDoubt
        {
            get
            {
                lock (mu)
                {
                    return failInDoubt;
                }
            }
        }

        // TiaDies, when set, makes TIA die inside Download the way it did in
        // #789 stage 2, taking its objects with it so that detaching
        // tiad's handlers throws too: "preparing" before TIA's download
        // call, "downloading" inside it, before any question.
        public string TiaDies
        {
            get
            {
                lock (mu)
                {
                    return tiaDies;
                }
            }
        }

        // AskFirst is how many times the fake's TIA asks its first check:
        // 1 as TIA does; 0 asks nothing at all and loads; 2 asks the same
        // configuration twice.
        public int AskFirst
        {
            get
            {
                lock (mu)
                {
                    return askFirst;
                }
            }
        }

        // FailAfterDecide, when set, makes Download fail the way TIA fails
        // once its checks were asked (or after a cancel, with its own
        // exception, which carries no decline).
        public string FailAfterDecide
        {
            get
            {
                lock (mu)
                {
                    return failAfterDecide;
                }
            }
        }

        // LoadDespiteCancel makes Download load even when a check is
        // cancelled:
        // a TIA that ignores the cancel, to prove the agent says so.
        public bool LoadDespiteCancel
        {
            get
            {
                lock (mu)
                {
                    return loadDespiteCancel;
                }
            }
        }
        readonly List<object> calls = new List<object>();
        readonly List<object> downloads = new List<object>();

        public List<Question> Questions
        {
            get
            {
                lock (mu)
                {
                    var l = new List<Question>();
                    foreach (var q in questions) l.Add(q.Copy());
                    return l;
                }
            }
        }

        public void Loaded(Obj download)
        {
            lock (mu)
            {
                downloads.Add(download);
            }
        }

        public object Downloads(Call c)
        {
            var o = new Obj();
            lock (mu)
            {
                o["downloads"] = downloads.ToArray();
            }
            return o;
        }

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
            bool despite = r.Bool("loadDespiteCancel");
            int? first = r.OptInt("askFirst");
            string after = r.Str("failAfterDecide");
            string inDoubt = r.Str("failInDoubt");
            string throws = r.Str("downloadThrows");
            string dies = r.Str("tiaDies");
            if (dies != "" && dies != "preparing" && dies != "downloading") throw new TiadException(400, Kinds.BadRequest, "tiaDies is preparing or downloading");
            bool noDry = r.Bool("noDryRun");
            bool same = r.Bool("archiveSame");
            bool seamTrust = r.Bool("seamTrustBeforeDownload");
            bool seamReserve = r.Bool("seamReserveBeforeRetire");
            bool clean = r.Bool("cleanCancel");
            bool differs = r.Bool("compareDiffers");
            string tree = r.Str("compareTree");
            if (tree != "" && tree != "empty" && tree != "emptyFolders" && tree != "folderDiffers" && tree != "irrelevantRoot" && tree != "dropBlock" && tree != "noBlocks")
            {
                throw new TiadException(400, Kinds.BadRequest, "compareTree is empty, emptyFolders, folderDiffers, irrelevantRoot, dropBlock or noBlocks");
            }
            string dlState = r.Str("downloadState");
            bool asksTls = r.Bool("uploadAsksTls");
            bool fpAsksTls = r.Bool("fingerprintAsksTls");
            var fpItems = new List<KeyValuePair<string, string>>();
            foreach (var x in r.Children("fingerprintItems")) fpItems.Add(new KeyValuePair<string, string>(x.Str("identifier"), x.Str("value")));
            bool fpNone = r.Bool("fingerprintNoItems");
            string cannot = r.Str("cannotApply");
            var dr = r.Child("device");
            DeviceIdentity dev = dr == null ? BenchDevice() : DeviceIdentity.Read(dr);
            bool noId = r.Bool("noIdentify");
            var pol = new PasswordPolicy();
            var pr = r.Child("passwordPolicy");
            if (pr != null)
            {
                pol.MinLength = pr.Int("minLength");
                pol.MinDigits = pr.Int("minDigits");
                pol.MinSpecial = pr.Int("minSpecial");
                pol.UpperAndLower = pr.Bool("upperAndLower");
            }
            string upAccess = r.Str("uploadAccessControl");
            string records = r.Str("accessRecords");
            if (upAccess == "") upAccess = UploadedAccessControl;
            var qs = new List<Question>();
            foreach (var x in r.Children("questions"))
            {
                qs.Add(new Question { Id = x.Str("id"), Message = x.Str("message"), Detail = x.Str("detail"), Objects = x.Strs("objects") });
            }
            r.Done();
            lock (mu)
            {
                compile = res;
                delays = d;
                failures = f;
                questions = qs;
                loadDespiteCancel = despite;
                askFirst = first.HasValue ? first.Value : 1;
                failAfterDecide = after;
                failInDoubt = inDoubt;
                downloadThrows = throws;
                tiaDies = dies;
                noDryRun = noDry;
                archiveSame = same;
                TestSeams.TrustBeforeDownloadDispatch = seamTrust;
                TestSeams.ReserveBeforeRetire = seamReserve;
                cleanCancel = clean;
                compareDiffers = differs;
                compareTree = tree;
                downloadState = dlState;
                uploadAsksTls = asksTls;
                fingerprintAsksTls = fpAsksTls;
                fingerprintItems = fpItems;
                fingerprintNoItems = fpNone;
                cannotApply = cannot;
                device = dev;
                noIdentify = noId;
                policy = pol;
                uploadAccess = upAccess;
                accessRecords = records;
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
        public string Full = "";
        public Station Station;
        public List<Block> Blocks = new List<Block>();
        // Changed is true until a download loads the project as it is.
        public bool Changed = true;
        // Protection is the CPU's configuration protection as the project
        // holds it (ConfigProtection's states), and ProtectedWith the
        // password it was protected with. A new CPU is WithoutPassword, as
        // TIA makes one; an upload brings the bench CPU's None.
        public string Protection = ConfigProtection.Absent;
        public string ProtectedWith = "";
        // Cpu is the project's CPU (name, order, firmware), or null, and
        // Others the devices besides its station.
        public ProjectCpu Cpu;
        // CpuAddress is the CPU node's address while Cpu is set: the spec's
        // after an apply, the CPU's after an upload.
        public string CpuAddress = "";
        public List<KeptDevice> Others = new List<KeptDevice>();
        // ArchiveKey names the project as its last deploy archive holds it
        // (FakeTia.Archive), "" before one; RestoredKey is the key of the
        // archive this project was retrieved from, "" for none. The world
        // keeps the key each CPU was loaded with, so a retrieved archive
        // compares equal while its CPU still holds that load.
        public string ArchiveKey = "", RestoredKey = "";
        // AccessControl is the CPU's PLC access control (AccessRule's
        // states): a new CPU is EnabledWithAccessControl, as TIA makes one;
        // an upload brings the bench CPU's Disabled. Users and Roles are
        // the project's, not the CPU's: they outlive any device delete. A
        // role maps to whether it holds Full access on the project's CPU:
        // replacing the CPU (a create, an upload) clears every grant, as
        // deleting the device drops its rights.
        public string AccessControl = AccessRule.Absent;
        public Dictionary<string, FakeUser> Users = new Dictionary<string, FakeUser>(StringComparer.Ordinal);
        public Dictionary<string, bool> Roles = new Dictionary<string, bool>(StringComparer.Ordinal);
        public Dictionary<string, string> RoleComments = new Dictionary<string, string>(StringComparer.Ordinal);

        // CompileError is what a hardware compile of this project fails
        // with, as TIA words it, or null.
        public string CompileError()
        {
            if (Protection == ConfigProtection.WithoutPassword) return FakeTia.NotConfigured;
            if (AccessControl == AccessRule.Enabled && !AnyFullAccess()) return FakeTia.NoFullAccess;
            return null;
        }

        bool AnyFullAccess()
        {
            foreach (var u in Users.Values)
            {
                foreach (string r in u.Roles)
                {
                    bool on;
                    if (Roles.TryGetValue(r, out on) && on) return true;
                }
            }
            return false;
        }

        // FullAccessPassword is the password of user when access control is
        // on and the user holds Full access, else null.
        public string FullAccessPassword(string user)
        {
            FakeUser u;
            if (AccessControl != AccessRule.Enabled || !Users.TryGetValue(user, out u)) return null;
            foreach (string r in u.Roles)
            {
                bool on;
                if (Roles.TryGetValue(r, out on) && on) return u.Password;
            }
            return null;
        }

        // DropGrants clears every role's Full access: its CPU is gone.
        public void DropGrants()
        {
            foreach (string r in new List<string>(Roles.Keys)) Roles[r] = false;
        }

        // CopyAccess copies the access control, users and roles of from.
        public void CopyAccess(FakeProject from)
        {
            AccessControl = from.AccessControl;
            Users = new Dictionary<string, FakeUser>(StringComparer.Ordinal);
            foreach (var kv in from.Users) Users[kv.Key] = new FakeUser { Password = kv.Value.Password, Comment = kv.Value.Comment, History = new List<string>(kv.Value.History), Roles = new HashSet<string>(kv.Value.Roles) };
            Roles = new Dictionary<string, bool>(from.Roles, StringComparer.Ordinal);
            RoleComments = new Dictionary<string, string>(from.RoleComments, StringComparer.Ordinal);
        }
    }

    sealed class FakeUser
    {
        public string Password = "", Comment = "";
        // History is the user's last passwords, the current one last; TIA
        // refuses any of them as a new one (5 by default policy).
        public List<string> History = new List<string>();
        public HashSet<string> Roles = new HashSet<string>(StringComparer.Ordinal);
    }

    // FakeAccess behaves like TIA's access control and users and roles as
    // the TIA machine showed them: a new CPU has access control on, users
    // are the project's, and a user's password must meet the project's
    // password policy.
    sealed class FakeAccess : IAccessControl
    {
        readonly FakeProject p;
        readonly PasswordPolicy policy;

        public FakeAccess(FakeProject p, PasswordPolicy policy)
        {
            this.p = p;
            this.policy = policy;
        }

        public string State()
        {
            return p.AccessControl;
        }

        public void Enable()
        {
            if (p.AccessControl == AccessRule.Absent) throw new TiaException("The CPU has no access control setting.");
            p.AccessControl = AccessRule.Enabled;
        }

        public bool HasRole(string role)
        {
            return p.Roles.ContainsKey(role);
        }

        public string RoleComment(string role)
        {
            string c;
            return p.RoleComments.TryGetValue(role, out c) ? c : "";
        }

        public void MakeRole(string role, string comment)
        {
            if (p.Roles.ContainsKey(role)) throw new TiaException("A role with the name " + role + " already exists.");
            p.Roles[role] = false;
            p.RoleComments[role] = comment;
        }

        public void GrantFullAccess(string role)
        {
            p.Roles[role] = true;
        }

        public bool HasUser(string user)
        {
            return p.Users.ContainsKey(user);
        }

        public void MakeUser(string user, string password)
        {
            if (p.Users.ContainsKey(user)) throw new TiaException("A user with the name " + user + " already exists.");
            if (policy.Check(password) != null) throw new TiaException("The password does not meet the password policy.");
            p.Users[user] = new FakeUser { Password = password, History = new List<string> { password } };
        }

        public void SetPassword(string user, string password)
        {
            FakeUser u = p.Users[user];
            if (u.History.Contains(password)) throw new PasswordReusedException(new TiaException("Operation failed. The specified password has already been used. Select another password."));
            if (policy.Check(password) != null) throw new TiaException("The password does not meet the password policy.");
            u.Password = password;
            u.History.Add(password);
            if (u.History.Count > 5) u.History.RemoveAt(0);
        }

        public string Comment(string user)
        {
            return p.Users[user].Comment;
        }

        public void SetComment(string user, string comment)
        {
            p.Users[user].Comment = comment;
        }

        public void AddRole(string user, string role)
        {
            p.Users[user].Roles.Add(role);
        }

        public string Check(string user, string role)
        {
            FakeUser u;
            bool on;
            if (!p.Users.TryGetValue(user, out u)) return "the user " + Json.Quote(user) + " is not in the project";
            if (!u.Roles.Contains(role)) return "the user " + Json.Quote(user) + " lacks the role " + Json.Quote(role);
            if (!p.Roles.TryGetValue(role, out on) || !on) return "the role " + Json.Quote(role) + " lacks the runtime right Full access on the CPU";
            return null;
        }
    }

    // FakeMasterSecret behaves like TIA's PlcMasterSecretConfigurator as
    // the TIA machine showed it: a second Protect throws, Unprotect needs
    // the password the CPU was protected with.
    sealed class FakeMasterSecret : IProtectionConfigurator
    {
        readonly FakeProject p;

        public FakeMasterSecret(FakeProject p)
        {
            this.p = p;
        }

        public string State()
        {
            return p.Protection;
        }

        public void Protect(string password)
        {
            if (p.Protection == ConfigProtection.WithPassword) throw new TiaException("The PLC Master Secret is already configured.");
            p.Protection = ConfigProtection.WithPassword;
            p.ProtectedWith = password;
        }

        public void Unprotect(string password)
        {
            if (p.Protection != ConfigProtection.WithPassword || password != p.ProtectedWith) throw new TiaException("The password is not correct.");
            p.Protection = ConfigProtection.None;
            p.ProtectedWith = "";
        }

        public void RemoveCpu()
        {
            p.Cpu = null;
            p.CpuAddress = "";
            p.Protection = ConfigProtection.Absent;
            p.ProtectedWith = "";
            p.AccessControl = AccessRule.Absent;
            p.DropGrants();
        }
    }

    // FakeTia is ITia without TIA: it keeps projects in memory and answers
    // what the Go fake (tiadtest.Fake) answers.
    // FakeWorld is what outlives a fake TIA instance, as the machine and
    // the CPUs outlive a real one the agent retires: the catalogue, the
    // saved project files, and what each CPU holds.
    sealed class FakeWorld
    {
        public readonly HashSet<string> Gsd = new HashSet<string>(StringComparer.Ordinal);
        public readonly Dictionary<string, FakeProject> Loaded = new Dictionary<string, FakeProject>(StringComparer.Ordinal);
        // LoadedKey is the archive key of each CPU's last load.
        public readonly Dictionary<string, string> LoadedKey = new Dictionary<string, string>(StringComparer.Ordinal);
        public readonly Dictionary<string, FakeProject> Saved = new Dictionary<string, FakeProject>(StringComparer.OrdinalIgnoreCase);
        public readonly Dictionary<string, int> Loads = new Dictionary<string, int>(StringComparer.Ordinal);
        public readonly Dictionary<string, int> HardwareLoads = new Dictionary<string, int>(StringComparer.Ordinal);
        // Logins is the password of the user "nautilus" on each CPU whose
        // access control a hardware load turned on, as that load left it:
        // going online to such a CPU asks credentials (FakeTia.AskLogin).
        public readonly Dictionary<string, string> Logins = new Dictionary<string, string>(StringComparer.Ordinal);
    }

    sealed class FakeTia : ITia
    {
        readonly Script script;
        readonly HashSet<string> gsd;
        // loaded is what each CPU holds: the project last downloaded to it.
        readonly Dictionary<string, FakeProject> loaded;
        // saved is the hardware each saved project file holds, restored
        // when it is opened: its CPU, the devices besides it, protection.
        readonly Dictionary<string, FakeProject> saved;
        readonly Dictionary<string, string> loadedKey;

        // died is set once a scripted TIA died (tiaDies): for good, as a
        // real TIA that exited stays gone.
        volatile bool died;

        // tlsTrusted is set once this instance answered a TLS question
        // trusted: as TIA does (#789 stage 1b), it asks none again until
        // it restarts; a new instance starts without it. loads and
        // hardwareLoads count the downloads that loaded each CPU, for its
        // fingerprint.
        bool tlsTrusted;
        readonly Dictionary<string, int> loads;
        readonly Dictionary<string, int> hardwareLoads;
        readonly Dictionary<string, string> logins;

        public bool Gone
        {
            get { return died; }
        }

        public FakeTia(Script script, FakeWorld world)
        {
            this.script = script;
            gsd = world.Gsd;
            loaded = world.Loaded;
            loadedKey = world.LoadedKey;
            saved = world.Saved;
            loads = world.Loads;
            hardwareLoads = world.HardwareLoads;
            logins = world.Logins;
            script.Enter("start");
        }

        // Retire ends the instance, as a disposed TIA Portal that exited:
        // a scripted "retire" failure is a retire that could not show it.
        public void Retire()
        {
            script.Enter("retire");
            died = true;
        }

        public bool CanDryRun
        {
            get { return !script.NoDryRun; }
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

        // openProjects counts the projects open on this instance: TIA V21
        // opens one at a time and refuses a second ("Another project is
        // already open", #616), so a path that bypasses the agent's rule
        // or leaks a project fails here as on TIA.
        int openProjects;

        void OneOpen(string fullPath)
        {
            if (openProjects > 0) throw new TiaException("Unable to open project " + fullPath + ". Another project is already open.");
        }

        public object CreateProject(string fullPath, string name)
        {
            script.Enter("CreateProject");
            OneOpen(fullPath);
            // As TIA does, refuse to create a project over an existing one.
            if (File.Exists(fullPath) || Directory.GetFileSystemEntries(Path.GetDirectoryName(fullPath)).Length > 0)
            {
                throw new TiaException("The project directory " + Path.GetDirectoryName(fullPath) + " is not empty");
            }
            File.WriteAllText(fullPath, "fake TIA project");
            saved.Remove(fullPath);
            saved.Remove(Path.ChangeExtension(fullPath, ".zap21"));
            openProjects++;
            return new FakeProject { Full = fullPath };
        }

        public object OpenProject(string fullPath)
        {
            script.Enter("OpenProject");
            OneOpen(fullPath);
            if (fullPath.EndsWith(".zap21", StringComparison.OrdinalIgnoreCase))
            {
                // As tiad.Openness: retrieved next to itself, into a
                // directory named after it, which must not exist.
                string target = fullPath.Substring(0, fullPath.Length - ".zap21".Length);
                if (Directory.Exists(target) || File.Exists(target)) throw new TiaException(target + " exists: retrieve the archive elsewhere");
                Directory.CreateDirectory(target);
                string ap = Path.Combine(target, Path.GetFileName(target) + ".ap21");
                File.WriteAllText(ap, "fake TIA project");
                FakeProject r = Restore(fullPath, ap) ?? new FakeProject { Full = ap };
                openProjects++;
                return r;
            }
            var p = new FakeProject { Full = fullPath };
            FakeProject sp;
            if (saved.TryGetValue(fullPath, out sp))
            {
                p.Cpu = sp.Cpu;
                p.Others = new List<KeptDevice>(sp.Others);
                p.Protection = sp.Protection;
                p.ProtectedWith = sp.ProtectedWith;
                p.CopyAccess(sp);
                string how = script.AccessRecords;
                if (how == "strip-users") foreach (var u in p.Users.Values) u.Comment = "";
                if (how == "strip-roles") p.RoleComments.Clear();
                if (how == "rekey")
                {
                    const string v2 = "tiad password record v2:";
                    foreach (var u in p.Users.Values)
                    {
                        if (u.Comment.StartsWith(v2, StringComparison.Ordinal)) u.Comment = v2 + "00000000" + u.Comment.Substring(v2.Length + 8);
                    }
                }
            }
            openProjects++;
            return p;
        }

        public void CloseProject(object project)
        {
            script.Enter("CloseProject");
            openProjects--;
        }

        public const string NotConfigured = "Password for confidential PLC configuration data is not configured in the PLC.";
        public const string NoFullAccess = "Users and roles: At least one user must have the following runtime right: Full access";

        // ApplyStation does what tiad.Openness does: it makes the CPU anew,
        // as TIA makes one, or keeps the project's, as the agent decided;
        // then it applies cpu.configurationProtection through the agent's
        // ProtectionSteps over a configurator that behaves like TIA's.
        public void ApplyStation(object project, Station next, string decision, Secrets secrets, List<string> notes, AccessMade made)
        {
            script.Enter("ApplyStation");
            var p = (FakeProject)project;
            Station prev = p.Station;
            if (decision == CpuReuse.Create)
            {
                p.Cpu = new ProjectCpu { Name = next.Cpu.Name, Order = next.Cpu.Order, Firmware = next.Cpu.Firmware };
                p.Others = new List<KeptDevice>();
                p.Protection = ConfigProtection.WithoutPassword;
                p.ProtectedWith = "";
                p.AccessControl = AccessRule.Enabled;
                p.DropGrants();
            }
            else if (p.Cpu == null)
            {
                throw new TiaException("the project's CPU went missing between the agent's read and the apply");
            }
            p.CpuAddress = next.Cpu.Address;
            // A failure part-way counts as a change.
            bool was = p.Changed;
            p.Changed = true;
            p.Station = null;
            ProtectionSteps.Run(new FakeMasterSecret(p), next.Cpu.ConfigurationProtection, secrets.ConfigurationPassword, notes);
            AccessSteps.Run(new FakeAccess(p, script.Policy), next.Cpu.AccessControl, secrets.FullAccessPassword, secrets.Records, decision == CpuReuse.Create, made, notes);
            p.Station = next;
            p.Changed = was || CpuReuse.Changes(decision, prev, next).Count > 0;
        }

        public PasswordPolicy ReadPolicy(object project)
        {
            script.Enter("ReadPolicy");
            return script.Policy;
        }

        public void PrepareAccess(object project, Station next, Secrets secrets, bool recordOnly, AccessWrites w, List<string> notes)
        {
            script.Enter("PrepareAccess");
            AccessSteps.Prepare(new FakeAccess((FakeProject)project, script.Policy), next.Cpu.AccessControl, secrets.FullAccessPassword, secrets.Records, recordOnly, w, notes);
        }

        public List<string> RemoveAccess(object project, AccessMade made)
        {
            script.Enter("RemoveAccess");
            var p = (FakeProject)project;
            var removed = new List<string>();
            if (made.User && p.Users.Remove(AccessSteps.UserName)) removed.Add("user " + Json.Quote(AccessSteps.UserName));
            if (made.Role && p.Roles.Remove(AccessSteps.RoleName))
            {
                p.RoleComments.Remove(AccessSteps.RoleName);
                foreach (var u in p.Users.Values) u.Roles.Remove(AccessSteps.RoleName);
                removed.Add("role " + Json.Quote(AccessSteps.RoleName));
            }
            return removed;
        }

        public string CannotApply(Station next)
        {
            string c = script.CannotApply;
            return c == "" ? null : c;
        }

        public SoftwareResult ImportSoftware(object project, string group, List<SourceFile> sources, List<SourceFile> tagTables, bool prune)
        {
            script.Enter("ImportSoftware");
            var p = (FakeProject)project;
            p.Changed = true;
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
            var p = (FakeProject)project;
            string err = hardware ? p.CompileError() : null;
            if (err == null) return script.Compile;
            var res = new CompileResult { Result = "error", Errors = 1 };
            res.Messages.Add(new CompileMessage { Object = "", Severity = "error", Text = err });
            return res;
        }

        public DownloadOutcome Download(object project, string address, bool hardware, bool software,
            Secrets secrets, bool? startAfter, Func<Question, Answer> answer, OnlineLogin login)
        {
            script.Enter("Download");
            var p = (FakeProject)project;
            if (script.FailInDoubt != "") throw new TiaException(script.FailInDoubt, null, true);
            if (script.DownloadThrows != "")
            {
                // With tiaDies too, TIA is gone by the time it throws.
                if (script.TiaDies != "") died = true;
                throw new InvalidOperationException(script.DownloadThrows);
            }
            var decider = new DownloadDecider(answer, startAfter);
            string dies = script.TiaDies;
            if (dies != "")
            {
                // As tiad.Openness runs it, through DownloadSteps, with a
                // TIA that is gone: its objects are disposed, so the step
                // that dies and detaching the handlers both throw.
                return DownloadSteps.Run(decider, address,
                    delegate
                    {
                        if (dies != "preparing") return;
                        died = true;
                        throw new ObjectDisposedException("DownloadProvider", "TIA Portal has exited");
                    },
                    delegate
                    {
                        died = true;
                        throw new ObjectDisposedException("DownloadProvider", "TIA Portal has exited");
                    },
                    delegate
                    {
                        throw new ObjectDisposedException("ConnectionConfiguration", "TIA Portal has exited");
                    },
                    delegate { return new List<string>(); });
            }
            List<Question> qs = script.Questions;
            // A protected configuration asks for its password when the
            // hardware loads (provisional until S2b).
            if (hardware && p.Protection == ConfigProtection.WithPassword)
            {
                qs.Add(new Question { Id = QuestionIds.ConfigurationPassword, Message = "password for the confidential PLC configuration data", Detail = "PlcMasterSecretPassword" });
            }
            if (script.CleanCancel)
            {
                var asked = new List<Question>();
                foreach (var q in qs)
                {
                    asked.Add(q.Copy());
                    if (answer(q.Copy()) == null) break;
                }
                // A plain TIA failure after the checks, not in doubt on
                // TIA's side: the agent itself must say outcome_unknown.
                if (script.FailAfterDecide != "") throw new TiaException(script.FailAfterDecide);
                return new DownloadOutcome { Questions = asked, Loaded = false };
            }
            int askFirst = script.AskFirst;
            string failAfter = script.FailAfterDecide;
            bool despite = script.LoadDespiteCancel;
            string resultState = script.DownloadState == "" ? "Success" : script.DownloadState;
            string state = "";
            string cpu = p.Cpu == null ? "" : p.Cpu.Name;
            // As tiad.Openness runs it: through DownloadSteps, the decider
            // answering TIA's pre-download callback once per check, in
            // order, as TIA calls it (#789 S2b: one at a time, no list).
            var use = new LoginUse(login, "download", address);
            var refusedOnline = new List<string>();
            DownloadOutcome outcome = DownloadSteps.Run(decider, address,
                delegate { },
                delegate
                {
                    // Going online for the download asks credentials first.
                    AskLogin(address, use, "download", refusedOnline);
                    bool stopped = false;
                    try
                    {
                        for (int i = 0; i < qs.Count && askFirst > 0; i++)
                        {
                            for (int k = 0; k < (i == 0 ? askFirst : 1); k++)
                            {
                                Question q = qs[i];
                                object key = i;
                                Boundary.Call("download: before loading", delegate
                                {
                                    Step st = decider.Next(key, OpennessType(q), q.Message ?? "", q);
                                    if (st.Setting.Kind != Setting.Select && string.IsNullOrEmpty(Setting.Secret(st.Setting, secrets)))
                                    {
                                        throw decider.Fail("writing " + st.Setting + " into " + st.Type + " failed: no secret");
                                    }
                                    decider.Wrote(st);
                                    if (q.Id == QuestionIds.StopCpu) stopped = true;
                                }, decider.Keep);
                            }
                        }
                    }
                    catch (Exception e)
                    {
                        // As TIA does: a callback that throws cancels the
                        // download, and TIA throws its own exception with
                        // the callback's inside. failAfterDecide is a TIA
                        // that throws its own instead; loadDespiteCancel
                        // one that swallows the cancel and loads.
                        if (failAfter != "") throw new TiaException(failAfter);
                        if (!despite) throw new InvalidOperationException("Error when executing delegate.", e);
                    }
                    if (failAfter != "") throw new TiaException(failAfter);
                    // TIA stopped the CPU to load it when it asked to; it
                    // starts it again only when the after-download
                    // callback's start-after says so. The fake's TIA asks
                    // start-after after every load, and logs the answer.
                    state = "RUN";
                    Step after = null;
                    Boundary.Call("download: after loading", delegate { after = decider.After(DownloadDecider.StartAfterType); }, decider.Keep);
                    Log.Line("fake TIA: after loading, start-after written {0}", after == null ? "nothing" : after.Setting.ToString());
                    if (stopped && (after == null || after.Setting.Value != "StartModule")) state = "STOP";
                    // TIA reports its steps as messages, as on the bench
                    // (#789 stages 2 and 3): the stop, each block loaded,
                    // the start. It says nothing of a CPU it never stopped,
                    // so tiad reports no mode then. The mode tiad reports
                    // is read from these by core, never set here; state
                    // above is the fake CPU's own mode (/fake/downloads).
                    var said = new List<string>();
                    if (stopped) said.Add(cpu + " stopped.");
                    foreach (Block b in p.Blocks) said.Add("'" + b.Name + "' was loaded successfully.");
                    if (stopped && state == "RUN") said.Add(cpu + " started.");
                    var rec = new Obj();
                    rec["address"] = address;
                    rec["hardware"] = hardware;
                    rec["software"] = software;
                    rec["state"] = state;
                    script.Loaded(rec);
                    loads[address] = Count(loads, address) + 1;
                    if (hardware)
                    {
                        hardwareLoads[address] = Count(hardwareLoads, address) + 1;
                        // A hardware load sets the CPU's access control and
                        // users as the project holds them.
                        string pw = p.FullAccessPassword(AccessSteps.UserName);
                        if (pw != null) logins[address] = pw;
                        else logins.Remove(address);
                    }
                    loaded[address] = p;
                    // The archive this load was archived as, once: a later
                    // load without one (a dry run TIA ran) holds none.
                    loadedKey[address] = p.ArchiveKey;
                    p.ArchiveKey = "";
                    p.Changed = false;
                    return new DownloadCall { State = resultState, MessageList = said, Messages = string.Join("; ", said.ToArray()), Cpu = cpu };
                },
                delegate { },
                delegate { return refusedOnline; });
            return outcome;
        }

        // OpennessType is the Openness configuration type the fake's TIA
        // hands its callback for a scripted question: its detail when that
        // is a type tiad knows, else the type for its id.
        static string OpennessType(Question q)
        {
            if (q.Detail != "" && DownloadChecks.QuestionId(q.Detail) == q.Id) return q.Detail;
            switch (q.Id)
            {
                case QuestionIds.StopCpu:
                    return "StopModules";
                case QuestionIds.StartAfter:
                    return "StartModules";
                case QuestionIds.Overwrite:
                    return "OverwriteSystemData";
                case QuestionIds.ConsistentDownload:
                    return "ConsistentBlocksDownload";
                case QuestionIds.AlarmTexts:
                    return "AlarmTextLibrariesDownload";
                case QuestionIds.AllBlocks:
                    return "AllBlocksDownload";
                case QuestionIds.UserManagement:
                    return "UserManagementDownload";
                case QuestionIds.IdentificationData:
                    return "LoadIdentificationData";
                case QuestionIds.Reinitialize:
                    return "DataBlockReinitialization";
                case QuestionIds.Password:
                    return "ModuleWriteAccessPassword";
                case QuestionIds.ConfigurationPassword:
                    return "PlcMasterSecretPassword";
                case QuestionIds.Certificate:
                    return "TlsVerificationConfiguration";
                default:
                    return "ACheckTiadDoesNotKnow";
            }
        }

        // Compare answers TIA's compare tree (#789) with the project's
        // program blocks. Without a compareTree knob the tree has the
        // bench's shape: the CPU's folder over its program blocks (Main
        // [OB1], as TIA makes a CPU, then what ImportSoftware made, one
        // level deeper in a user group), a tag table and three empty
        // folders. A CPU that does
        // not hold the project reports the CPU's own element as different.
        // The fake labels blocks with the agent's own TiaNames.BlockLabel,
        // so it cannot prove TIA's labels: only the bench can.
        public OnlineCompare Compare(object project, string address, OnlineLogin login)
        {
            script.Enter("Compare");
            AskLogin(address, new LoginUse(login, "compare", address), "compare", new List<string>());
            var p = (FakeProject)project;
            string cpu = p.Station == null ? "" : p.Station.Cpu.Name;
            List<ProjectBlock> blocks = ProjectBlocks(p);
            var leaves = new List<CompareNode>();
            foreach (var b in blocks) leaves.Add(Cmp(TiaNames.BlockLabel(b), "ObjectsIdentical"));
            switch (script.CompareTree)
            {
                case "empty":
                    return new OnlineCompare { Tree = Cmp(cpu, "FolderContentsIdentical"), Blocks = blocks };
                case "emptyFolders":
                    return new OnlineCompare { Tree = Cmp(cpu, "FolderContentsIdentical", EmptyFolders()), Blocks = blocks };
                case "folderDiffers":
                    return new OnlineCompare
                    {
                        Tree = Cmp(cpu, "FolderContentsIdentical",
                            Program(leaves),
                            Cmp("PLC tags", "FolderContentsDifferent")),
                        Blocks = blocks,
                    };
                case "irrelevantRoot":
                    return new OnlineCompare { Tree = Cmp(cpu, "CompareIrrelevant", Program(leaves)), Blocks = blocks };
                case "dropBlock":
                    // The tree leaves out the project's last block; all
                    // else is identical (an identical tag table among it).
                    leaves.RemoveAt(leaves.Count - 1);
                    break;
                case "noBlocks":
                    // The project's blocks could not be read.
                    blocks = new List<ProjectBlock>();
                    break;
            }
            FakeProject there;
            string key;
            bool equal = !script.CompareDiffers &&
                ((loaded.TryGetValue(address, out there) && there == p && !p.Changed) ||
                 (p.RestoredKey != "" && loadedKey.TryGetValue(address, out key) && key == p.RestoredKey));
            var kids = new List<CompareNode>
            {
                Program(leaves),
                Cmp("PLC tags", "FolderContentsIdentical", Cmp("Default tag table", "ObjectsIdentical")),
            };
            kids.AddRange(EmptyFolders());
            return new OnlineCompare { Tree = Cmp(cpu, equal ? "FolderContentsIdentical" : "ObjectsDifferent", kids.ToArray()), Blocks = blocks };
        }

        // Program is the program blocks folder over leaves: the first at
        // its top, as TIA makes Main, the rest in a user group under it.
        static CompareNode Program(List<CompareNode> leaves)
        {
            var top = Cmp("Program blocks", "FolderContentsIdentical");
            if (leaves.Count > 0) top.Children.Add(leaves[0]);
            if (leaves.Count > 1) top.Children.Add(Cmp("nautilus", "FolderContentsIdentical", leaves.GetRange(1, leaves.Count - 1).ToArray()));
            return top;
        }

        // ProjectBlocks is the fake project's program blocks as Openness
        // would list them: Main OB1, as TIA makes a CPU (unless a source
        // made a block of that name), then every block a source made but
        // data types, numbered from 1 per kind in name order.
        static List<ProjectBlock> ProjectBlocks(FakeProject p)
        {
            var l = new List<ProjectBlock>();
            bool main = true;
            foreach (var b in p.Blocks) if (string.Equals(b.Name, "Main", StringComparison.OrdinalIgnoreCase)) main = false;
            if (main) l.Add(new ProjectBlock { Name = "Main", Type = "OB", Number = 1 });
            var next = new Dictionary<string, int> { { "OB", main ? 2 : 1 }, { "FB", 1 }, { "FC", 1 }, { "DB", 1 } };
            foreach (var b in p.Blocks)
            {
                if (!next.ContainsKey(b.Kind)) continue;
                l.Add(new ProjectBlock { Name = b.Name, Type = b.Kind == "DB" ? "GlobalDB" : b.Kind, Number = next[b.Kind]++ });
            }
            return l;
        }

        static CompareNode[] EmptyFolders()
        {
            return new[]
            {
                Cmp("Software units", "FolderContentsIdentical"),
                Cmp("Technology objects", "FolderContentsIdentical"),
                Cmp("PLC data types", "FolderContentsIdentical"),
            };
        }

        static CompareNode Cmp(string name, string state, params CompareNode[] children)
        {
            var n = new CompareNode { Name = name, State = state };
            n.Children.AddRange(children);
            return n;
        }

        public List<string> Export(object project, string dir, ExportOptions o)
        {
            script.Enter("Export");
            var p = (FakeProject)project;
            var outp = new List<string>();
            Action<string, string> put = delegate(string name, string body)
            {
                string f = Path.Combine(dir, name);
                File.WriteAllText(f, body);
                outp.Add(f);
            };
            if (o.Blocks != "")
            {
                foreach (var b in p.Blocks) put(b.Name + "." + o.Blocks, "// fake export of " + b.Kind + " \"" + b.Name + "\"\n");
            }
            if (o.TagTables) put("tagtables.xml", "<!-- fake tag tables -->\n");
            if (o.Hardware) put("station.json", Json.Write(p.Station.ToJson()));
            if (o.Gsdml) put("GSDML-V2.4-fake-" + p.Station.Cpu.Name + ".xml", "<!-- fake I-device GSDML -->\n");
            return outp;
        }

        public List<string> Upload(object project, string address, bool trustUnverifiedCertificate, OnlineLogin login)
        {
            script.Enter("Upload");
            var trusted = new List<string>();
            var use = new LoginUse(login, "upload", address);
            // As tiad.Openness answers TIA's TLS question going online.
            if (script.UploadAsksTls) AskTls(address, trustUnverifiedCertificate, trusted, use);
            AskLogin(address, use, "upload", new List<string>());
            var up = (FakeProject)project;
            up.Changed = true;
            // The bench CPU's setting, as the S2a upload found it, and its
            // IO device, which the CPU's station does not hold.
            up.Protection = ConfigProtection.None;
            up.ProtectedWith = "";
            up.AccessControl = script.UploadAccessControl;
            up.DropGrants();
            DeviceIdentity d = script.Device;
            up.Cpu = new ProjectCpu { Name = UploadedCpuName, Order = d.Order, Firmware = d.Firmware };
            up.CpuAddress = address;
            up.Others = new List<KeptDevice> { UploadedIoDevice() };
            return trusted;
        }

        public DeviceIdentity ProjectCpu(object project)
        {
            script.Enter("ProjectCpu");
            return script.Device;
        }

        // UploadedCpuName and UploadedIoDevice are what an upload brings
        // besides the CPU's identity: the Go fake's UploadedCPUName and
        // UploadedIODevice.
        public const string UploadedCpuName = "PLC_1";

        public static KeptDevice UploadedIoDevice()
        {
            return new KeptDevice { Name = "io-device-up", ProfinetName = "io-device-up", Address = "192.0.2.30" };
        }

        public string ProjectCpuAddress(object project)
        {
            script.Enter("ProjectCpuAddress");
            var p = (FakeProject)project;
            return p.Cpu == null ? "" : p.CpuAddress;
        }

        public ProjectCpu ReadCpu(object project)
        {
            script.Enter("ReadCpu");
            var p = (FakeProject)project;
            if (p.Cpu == null) return null;
            return new ProjectCpu
            {
                Name = p.Cpu.Name, Order = p.Cpu.Order, Firmware = p.Cpu.Firmware,
                Protection = p.Protection, AccessControl = p.AccessControl, Others = new List<KeptDevice>(p.Others),
            };
        }

        // AskTls asks TLS trust as TIA does going online to a CPU whose
        // certificate it does not know, unless this instance trusted one
        // already, and answers as tiad.Openness does.
        void AskTls(string address, bool trustUnverifiedCertificate, List<string> trusted, LoginUse use)
        {
            if (tlsTrusted) return;
            const string type = "TlsVerificationConfiguration";
            string ask = OnlineChecks.Ask(type, "PLC_1 [" + address + "]", "certificate not matching", new List<string>());
            if (OnlineChecks.Answer(type, trustUnverifiedCertificate) != OnlineChecks.Trusted)
            {
                throw new TiaException(TiaNames.OnlineRefused(address, new List<string> { ask }, "Online connection failed."));
            }
            tlsTrusted = true;
            trusted.Add(ask);
            use.Trusted();
        }

        // AskLogin asks credentials as TIA does going online to a CPU whose
        // access control a hardware load turned on (logins), on the
        // connection named handler, and answers as tiad.Openness does:
        // through OnlineRecord and the operation's LoginUse (core), over a
        // secure connection that offers project users. A wrong password is
        // asked again, as TIA does when a CPU refuses it; tiad refuses the
        // second ask. Refusals are added to refused, and fail the call.
        void AskLogin(string address, LoginUse use, string handler, List<string> refused)
        {
            string want;
            if (!logins.TryGetValue(address, out want)) return;
            var rec = new OnlineRecord();
            var offered = new List<string> { "AnonymousUser", LoginUse.ProjectUser };
            for (int i = 0; i < 2; i++)
            {
                string wrote = null;
                try
                {
                    rec.Handle(delegate { return OnlineChecks.Authentication; }, delegate(string type, OnlineRecord.Call call)
                    {
                        if (OnlineChecks.Answer(type) != OnlineChecks.Login) throw new TiaException("the fake asks only a project user's legitimation");
                        use.Answer(call, type, handler, true, offered, delegate(string pw) { wrote = pw; });
                    });
                }
                catch (OperationCanceledException)
                {
                    // Boundary.Fail's cancel, which TIA gets: it fails the
                    // connection.
                }
                if (rec.Asked.Count > 0)
                {
                    refused.AddRange(rec.Asked);
                    throw new TiaException(TiaNames.OnlineRefused(address, rec.Asked, "Online connection failed."));
                }
                if (wrote == want) return;
            }
            throw new TiaException("the fake asked credentials twice and tiad answered both");
        }

        static int Count(Dictionary<string, int> d, string address)
        {
            int n;
            return d.TryGetValue(address, out n) ? n : 0;
        }

        // ReadFingerprint returns the fake CPU's fingerprint: the bench's
        // eight identifiers, with synthetic values that change as TIA's do
        // (#789 stage (C)): DownloadId and the change counters on every
        // load, Hardware on a hardware load.
        public FingerprintRead ReadFingerprint(object project, string address, bool trustUnverifiedCertificate, OnlineLogin login)
        {
            script.Enter("ReadFingerprint");
            var p = (FakeProject)project;
            // As the glue: no CPU, or one at another address, is refused.
            if (p.Station == null && p.Cpu == null) throw new TiaException("fingerprint read: the project has no CPU");
            string at = p.Station != null ? p.Station.Cpu.Address : p.CpuAddress;
            if (at != address)
            {
                throw new TiaException("fingerprint read: the project's CPU is at " + at + ", not " + address);
            }
            var r = new FingerprintRead();
            var use = new LoginUse(login, "fingerprint read", address);
            // Going online first (the project's connection), then TIA's
            // fingerprint service, which asks TLS trust, then credentials on
            // its own connection.
            AskLogin(address, use, "online", new List<string>());
            if (script.FingerprintAsksTls) AskTls(address, trustUnverifiedCertificate, r.TrustedBlindly, use);
            AskLogin(address, use, "fingerprint", new List<string>());
            if (script.FingerprintNoItems) return r;
            List<KeyValuePair<string, string>> scripted = script.FingerprintItems;
            if (scripted.Count > 0)
            {
                r.Items = scripted;
                return r;
            }
            int n = Count(loads, address), hw = Count(hardwareLoads, address);
            r.Items.Add(new KeyValuePair<string, string>("TextLists", "fake-textlists-1"));
            r.Items.Add(new KeyValuePair<string, string>("Hardware", "fake-hardware-" + hw));
            r.Items.Add(new KeyValuePair<string, string>("Certificates", "fake-certificates-1"));
            r.Items.Add(new KeyValuePair<string, string>("Routing", "fake-routing-1"));
            r.Items.Add(new KeyValuePair<string, string>("Connection", "fake-connection-1"));
            r.Items.Add(new KeyValuePair<string, string>("DownloadId", "fake-download-" + n));
            r.Items.Add(new KeyValuePair<string, string>("ChangeCounterPersistent", (100 + n).ToString(System.Globalization.CultureInfo.InvariantCulture)));
            r.Items.Add(new KeyValuePair<string, string>("ChangeCounterRetain", (100 + n).ToString(System.Globalization.CultureInfo.InvariantCulture)));
            return r;
        }

        public DeviceIdentity Identify(object project, string address)
        {
            script.Enter("Identify");
            return script.NoIdentify ? null : script.Device;
        }

        // archives counts the archives this fake wrote, so each one's bytes
        // differ, as a real archive's do (time stamps).
        static int archives;

        public string Archive(object project, string dir)
        {
            script.Enter("Archive");
            var p = (FakeProject)project;
            string f = Path.Combine(dir, Path.GetFileNameWithoutExtension(p.Full) + ".zap21");
            int n = System.Threading.Interlocked.Increment(ref archives);
            if (script.ArchiveSame)
            {
                File.WriteAllText(f, "fake TIA project archive");
                return f;
            }
            // The project as an archive holds it, so a retrieve restores it
            // in any process, after an agent restart too; each archive's
            // bytes differ, as a real one's do (time stamps).
            p.ArchiveKey = Guid.NewGuid().ToString("N");
            var o = new Obj();
            o["fakeArchive"] = n;
            o["of"] = p.Full;
            o["key"] = p.ArchiveKey;
            if (p.Cpu != null)
            {
                var cpu = new Obj();
                cpu["name"] = p.Cpu.Name;
                cpu["order"] = p.Cpu.Order;
                cpu["firmware"] = p.Cpu.Firmware;
                cpu["address"] = p.CpuAddress;
                o["cpu"] = cpu;
            }
            var blocks = new List<object>();
            foreach (var b in p.Blocks) blocks.Add(b.ToJson());
            o["blocks"] = blocks;
            o["protection"] = p.Protection;
            o["accessControl"] = p.AccessControl;
            File.WriteAllText(f, Json.Write(o));
            return f;
        }

        // LoadOutside is POST /fake/load {address, archive}: the CPU at
        // address holds a load the agent did not make (another engineering
        // station's): what the archive file at archive holds, or, with no
        // archive, something no archive holds. Its fingerprint shows the
        // load. Tests call it while no TIA call runs.
        public static object LoadOutside(FakeWorld world, Call c)
        {
            var r = c.Json();
            string address = r.Str("address");
            string archive = r.Str("archive");
            r.Done();
            string key = "outside";
            if (archive != "")
            {
                FakeProject p = Restore(archive, archive);
                if (p == null) throw TiadException.BadRequest("{0} is no archive of the fake's", archive);
                key = p.RestoredKey;
            }
            world.Loaded.Remove(address);
            world.LoadedKey[address] = key;
            int n;
            world.Loads.TryGetValue(address, out n);
            world.Loads[address] = n + 1;
            return new Obj();
        }

        // Restore is the project an archive written by Archive holds, or
        // null for any other file.
        static FakeProject Restore(string archive, string full)
        {
            IDictionary<string, object> o;
            try
            {
                o = Json.Parse(File.ReadAllText(archive)) as IDictionary<string, object>;
            }
            catch (TiadException)
            {
                return null;
            }
            if (o == null || !o.ContainsKey("fakeArchive")) return null;
            var p = new FakeProject { Full = full, RestoredKey = (string)o["key"], Changed = false };
            object v;
            if (o.TryGetValue("cpu", out v) && v != null)
            {
                var cpu = (IDictionary<string, object>)v;
                p.Cpu = new ProjectCpu { Name = (string)cpu["name"], Order = (string)cpu["order"], Firmware = (string)cpu["firmware"] };
                p.CpuAddress = (string)cpu["address"];
            }
            foreach (object x in (System.Collections.IList)o["blocks"])
            {
                var b = (IDictionary<string, object>)x;
                p.Blocks.Add(new Block { Name = (string)b["name"], Kind = (string)b["kind"] });
            }
            p.Protection = (string)o["protection"];
            p.AccessControl = (string)o["accessControl"];
            return p;
        }

        public string Save(object project, bool archive)
        {
            script.Enter("Save");
            var p = (FakeProject)project;
            string f = archive ? Path.ChangeExtension(p.Full, ".zap21") : p.Full;
            File.WriteAllText(f, "fake TIA project");
            saved[f] = new FakeProject
            {
                Cpu = p.Cpu == null ? null : new ProjectCpu { Name = p.Cpu.Name, Order = p.Cpu.Order, Firmware = p.Cpu.Firmware },
                Others = new List<KeptDevice>(p.Others),
                Protection = p.Protection,
                ProtectedWith = p.ProtectedWith,
            };
            saved[f].CopyAccess(p);
            return f;
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
            var world = new FakeWorld();
            Agent agent;
            try
            {
                agent = new Agent(cfg, host, delegate { return new FakeTia(script, world); }, "+fake");
            }
            catch (ConfigException e)
            {
                Console.Error.WriteLine("tiad-fake: " + e.Message);
                return 2;
            }
            var router = new Router();
            agent.Register(router);
            router.Add("PUT", "/fake/gates", host.SetGates);
            router.Add("PUT", "/fake/machine", host.SetMachine);
            router.Add("PUT", "/fake/script", script.Set);
            router.Add("GET", "/fake/calls", script.Calls);
            router.Add("GET", "/fake/downloads", script.Downloads);
            router.Add("POST", "/fake/load", delegate(Call c) { return FakeTia.LoadOutside(world, c); });

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
