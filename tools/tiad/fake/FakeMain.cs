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
        int decideCalls = 1;
        string failAfterDecide = "";
        string failInDoubt = "";
        bool noDryRun;
        bool uploadAsksTls;
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

        // NoDryRun makes the fake's TIA side one that cannot promise a dry
        // run loads nothing, as tiad.Openness cannot until S2b.
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

        // DecideCalls is how many times Download calls decide (1, as the
        // ITia contract says; 0 or 2 for an ITia that breaks it).
        public int DecideCalls
        {
            get
            {
                lock (mu)
                {
                    return decideCalls;
                }
            }
        }

        // FailAfterDecide, when set, makes Download fail the way TIA fails
        // after decide has answered.
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

        // LoadDespiteCancel makes Download load even when decide says no:
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
            int? calls = r.OptInt("decideCalls");
            string after = r.Str("failAfterDecide");
            string inDoubt = r.Str("failInDoubt");
            bool noDry = r.Bool("noDryRun");
            bool asksTls = r.Bool("uploadAsksTls");
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
                decideCalls = calls.HasValue ? calls.Value : 1;
                failAfterDecide = after;
                failInDoubt = inDoubt;
                noDryRun = noDry;
                uploadAsksTls = asksTls;
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
        public List<KeptDevice> Others = new List<KeptDevice>();
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
            p.Protection = ConfigProtection.Absent;
            p.ProtectedWith = "";
            p.AccessControl = AccessRule.Absent;
            p.DropGrants();
        }
    }

    // FakeTia is ITia without TIA: it keeps projects in memory and answers
    // what the Go fake (tiadtest.Fake) answers.
    sealed class FakeTia : ITia
    {
        readonly Script script;
        readonly HashSet<string> gsd = new HashSet<string>(StringComparer.Ordinal);
        // loaded is what each CPU holds: the project last downloaded to it.
        readonly Dictionary<string, FakeProject> loaded = new Dictionary<string, FakeProject>(StringComparer.Ordinal);
        // saved is the hardware each saved project file holds, restored
        // when it is opened: its CPU, the devices besides it, protection.
        readonly Dictionary<string, FakeProject> saved = new Dictionary<string, FakeProject>(StringComparer.OrdinalIgnoreCase);

        public FakeTia(Script script)
        {
            this.script = script;
            script.Enter("start");
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

        public object CreateProject(string fullPath, string name)
        {
            script.Enter("CreateProject");
            // As TIA does, refuse to create a project over an existing one.
            if (File.Exists(fullPath) || Directory.GetFileSystemEntries(Path.GetDirectoryName(fullPath)).Length > 0)
            {
                throw new TiaException("The project directory " + Path.GetDirectoryName(fullPath) + " is not empty");
            }
            File.WriteAllText(fullPath, "fake TIA project");
            saved.Remove(fullPath);
            saved.Remove(Path.ChangeExtension(fullPath, ".zap21"));
            return new FakeProject { Full = fullPath };
        }

        public object OpenProject(string fullPath)
        {
            script.Enter("OpenProject");
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
            return p;
        }

        public void CloseProject(object project)
        {
            script.Enter("CloseProject");
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
            Secrets secrets, Func<List<Question>, List<Answer>> decide)
        {
            script.Enter("Download");
            var p = (FakeProject)project;
            if (script.FailInDoubt != "") throw new TiaException(script.FailInDoubt, null, true);
            var outcome = new DownloadOutcome { Questions = script.Questions };
            // A protected configuration asks for its password when the
            // hardware loads (provisional until S2b).
            if (hardware && p.Protection == ConfigProtection.WithPassword)
            {
                outcome.Questions.Add(new Question { Id = QuestionIds.ConfigurationPassword, Message = "password for the confidential PLC configuration data", Detail = "PlcMasterSecretPassword" });
            }
            // As tiad.Openness does: whether the CPU starts after loading is
            // always decided before loading.
            bool startAsked = false;
            foreach (var q in outcome.Questions)
            {
                if (q.Id == QuestionIds.StartAfter) startAsked = true;
            }
            if (!startAsked) outcome.Questions.Add(new Question { Id = QuestionIds.StartAfter, Message = "start the CPU after loading", Detail = DownloadDecider.StartAfterType });
            List<Answer> answers = null;
            for (int i = 0; i < script.DecideCalls; i++) answers = decide(outcome.Questions);
            if (script.FailAfterDecide != "") throw new TiaException(script.FailAfterDecide);
            if (answers == null && !(script.LoadDespiteCancel || script.DecideCalls == 0)) return outcome;
            // TIA stops the CPU to load it when it asks to; it starts it
            // again only when start-after is answered yes.
            outcome.State = "RUN";
            bool stops = false, starts = false;
            foreach (var a in answers ?? new List<Answer>())
            {
                if (a.Question.Id == QuestionIds.StopCpu && a.Yes) stops = true;
                if (a.Question.Id == QuestionIds.StartAfter && a.Yes) starts = true;
            }
            if (stops && !starts) outcome.State = "STOP";
            var rec = new Obj();
            rec["address"] = address;
            rec["hardware"] = hardware;
            rec["software"] = software;
            rec["state"] = outcome.State;
            script.Loaded(rec);
            loaded[address] = p;
            p.Changed = false;
            outcome.Loaded = true;
            return outcome;
        }

        public bool Compare(object project, string address, List<Difference> differences)
        {
            script.Enter("Compare");
            var p = (FakeProject)project;
            FakeProject there;
            if (loaded.TryGetValue(address, out there) && there == p && !p.Changed) return true;
            differences.Add(new Difference { Object = p.Station == null ? "" : p.Station.Cpu.Name, State = "different" });
            return false;
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

        public List<string> Upload(object project, string address, bool trustUnverifiedCertificate)
        {
            script.Enter("Upload");
            var trusted = new List<string>();
            // As tiad.Openness answers TIA's TLS question going online.
            if (script.UploadAsksTls)
            {
                const string type = "TlsVerificationConfiguration";
                string ask = OnlineChecks.Ask(type, "PLC_1 [" + address + "]", "certificate not matching", new List<string>());
                if (OnlineChecks.Answer(type, trustUnverifiedCertificate) != OnlineChecks.Trusted)
                {
                    throw new TiaException(TiaNames.OnlineRefused(address, new List<string> { ask }, "Online connection failed."));
                }
                trusted.Add(ask);
            }
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

        public DeviceIdentity Identify(object project, string address)
        {
            script.Enter("Identify");
            return script.NoIdentify ? null : script.Device;
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
            Agent agent;
            try
            {
                agent = new Agent(cfg, host, delegate { return new FakeTia(script); }, "+fake");
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
