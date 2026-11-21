// OpennessTia.cs: tiad.Openness.dll, the agent's TIA side over TIA Portal
// Openness V21. It is the only assembly that references Siemens.Engineering,
// and tiad.exe loads it on the first call that needs TIA (host/Host.cs).
//
// This is glue. Every decision (which question a pre-download
// configuration asks, how an answer is written back, which PC interface
// reaches the CPU, which attribute names exist, how TIA's results read) is
// in tiad-core (core/TiaRules.cs), where it is tested without TIA. Here
// TIA's objects are read into those rules and their answers written back.
//
// Everything runs on the agent's one STA thread (core/Tia.cs). Starting
// TIA happens in exactly one place: the constructor, which the agent calls
// at the first call that needs TIA, after every gate holds.
//
// Provisional until S2a and S2b (siemens-ide.md section 3.5): that a cancel
// from the pre-download callback loads nothing, whether TIA acts on an
// answer before it asks the next check, the connection addresses, and the CPU's
// attribute names (checked against GetAttributeInfos before each is set,
// never assumed). What only S2a can tell is refused, not guessed: GSDML
// install, PROFINET devices, rack modules, the web server, upload
// configurations and the I-device GSDML export.
//
// install-tiad.ps1 builds it with the machine's stock csc (C# 5).

using System;
using System.Collections.Generic;
using System.IO;
using System.Security;
using Siemens.Engineering;
using Siemens.Engineering.Cax;
using Siemens.Engineering.Compare;
using Siemens.Engineering.Compiler;
using Siemens.Engineering.Connection;
using Siemens.Engineering.Download;
using Siemens.Engineering.Download.Configurations;
using Siemens.Engineering.FingerprintData;
using Siemens.Engineering.HW;
using Siemens.Engineering.HW.Features;
using Siemens.Engineering.Online;
using Siemens.Engineering.Online.Configurations;
using Siemens.Engineering.Online.Security;
using Siemens.Engineering.SW;
using Siemens.Engineering.SW.Blocks;
using Siemens.Engineering.SW.ExternalSources;
using Siemens.Engineering.SW.Tags;
using Siemens.Engineering.SW.Types;
using Siemens.Engineering.Umac;
using Siemens.Engineering.Upload;
using Siemens.Engineering.Upload.Configurations;
using HwDevice = Siemens.Engineering.HW.Device;

namespace Tiad.Openness
{
    // OpenProject is the handle the agent holds for an open project.
    sealed class OpenProject
    {
        public Project Project;
        public string Full = "";
    }

    // OnlineAsks answers what TIA asks when it goes online to a CPU
    // (ConnectionConfiguration.OnlineLegitimation): a TLS certificate to
    // trust, or credentials. tiad trusts no CPU certificate and holds no CPU
    // credentials yet, so every answer is no, and each question is recorded
    // so the failure names it: the certificate's fingerprint is what a
    // later, explicit trust decision has to match.
    sealed class OnlineAsks
    {
        readonly ConnectionConfiguration cc;
        readonly OnlineAnswers answers;
        readonly Action gone;
        readonly string address;
        readonly OnlineRecord record = new OnlineRecord();
        public List<string> Asked
        {
            get { return record.Asked; }
        }
        // TrustedBlindly are the certificate questions answered Trusted,
        // on the upload's explicit opt-in.
        public List<string> TrustedBlindly
        {
            get { return record.TrustedBlindly; }
        }

        // gone is called when the handler cannot be detached: TIA's
        // objects are disposed, so TIA is taken to be gone.
        public OnlineAsks(ConnectionConfiguration cc, Action gone) : this(cc, gone, false, "")
        {
        }

        public OnlineAsks(ConnectionConfiguration cc, Action gone, bool trustUnverifiedCertificate, string address)
        {
            this.cc = cc;
            this.gone = gone;
            answers = new OnlineAnswers(trustUnverifiedCertificate);
            this.address = address;
            if (cc != null) cc.OnlineLegitimation += Handle;
        }

        // ForCallback answers the questions of a call that takes its own
        // OnlineConfigurationDelegate (GetFingerprintData) instead of the
        // connection's OnlineLegitimation event: pass Handle to it.
        public static OnlineAsks ForCallback(bool trustUnverifiedCertificate, string address)
        {
            return new OnlineAsks(null, delegate { }, trustUnverifiedCertificate, address);
        }

        // Stop detaches the handler and never throws: TIA may be gone
        // (#789 stage 2: it died inside a download, and detaching from its
        // disposed objects threw in place of the download's own failure).
        // A handler that cannot be detached answers no from then on, even
        // to a question it would have trusted on the upload's opt-in
        // (OnlineAnswers), and TIA counts as gone.
        public void Stop()
        {
            if (cc == null) return;
            if (!answers.Stop(delegate { cc.OnlineLegitimation -= Handle; }, "online: detaching tiad's online handler from TIA")) gone();
        }

        // Handle answers one question, as OnlineChecks decides: a
        // certificate is set NonTrusted before anything else is read, and
        // anything else cancels. Every question is recorded, so the
        // operation fails even if TIA goes on. TIA calls it from its own
        // process, so it is only Boundary.Call: a failure anywhere in it is
        // recorded as a question answered no (the operation fails), and
        // TIA gets Boundary.Fail's exception, never a tiad type
        // (OnlineRecord.Handle, in core).
        public void Handle(OnlineConfiguration c)
        {
            record.Handle(delegate { return c.GetType().Name; }, delegate(string type, OnlineRecord.Call call) { Answer(c, type, call); });
        }

        // Answer answers one question and records it; it throws to cancel.
        void Answer(OnlineConfiguration c, string type, OnlineRecord.Call call)
        {
            var tls = c as TlsVerificationConfiguration;
            string answer = answers.Answer(type);
            if ((answer == OnlineChecks.NotTrusted || answer == OnlineChecks.Trusted) && tls != null)
            {
                bool trust = answer == OnlineChecks.Trusted;
                tls.CurrentSelection = trust ? TlsVerificationConfigurationSelection.Trusted : TlsVerificationConfigurationSelection.NonTrusted;
                string plc = "", info = "";
                var fps = new List<string>();
                try
                {
                    plc = tls.PlcName ?? "";
                    info = tls.VerificationInfo ?? "";
                    int n = 0;
                    foreach (VerificationCertificate v in tls.Certificates)
                    {
                        n++;
                        if (v.Certificate == null) continue;
                        string fp = TiaNames.Fingerprint(v.Certificate.RawData);
                        fps.Add(fp);
                        Log.Line("online: TLS certificate {0}: SHA-256 {1}, subject {2}, issuer {3}, valid {4:o} to {5:o}", n, fp,
                            TiaNames.LogSafe(v.Certificate.Subject), TiaNames.LogSafe(v.Certificate.Issuer),
                            v.Certificate.NotBefore.ToUniversalTime(), v.Certificate.NotAfter.ToUniversalTime());
                    }
                    Log.Line("online: {0} asks TLS trust ({1} certificate(s)); answered {2}; {3}", TiaNames.LogSafe(plc), n,
                        trust ? "TRUSTED BLINDLY on the request's trustUnverifiedCertificate opt-in (target " + TiaNames.LogSafe(address) + ")" : "not trusted",
                        TiaNames.LogSafe(Describe(tls)));
                }
                catch (Exception e)
                {
                    Log.Line("online: reading a TLS question failed ({0}); answered {1}", e.GetType().Name, trust ? "TRUSTED BLINDLY" : "not trusted");
                }
                string question = OnlineChecks.Ask(type, plc, info, fps);
                if (trust) call.Trust(question);
                else call.Refuse(question);
                return;
            }
            string ask = OnlineChecks.Ask(type, "", "", new List<string>());
            call.Refuse(ask);
            throw new TiaException("tiad cancels going online: TIA asked " + ask);
        }

        // Describe lists an online question's attributes and their values,
        // for the log: what TIA offers beyond its typed properties.
        static string Describe(IEngineeringObject o)
        {
            var parts = new List<string>();
            foreach (EngineeringAttributeInfo a in o.GetAttributeInfos())
            {
                string v;
                try
                {
                    object x = o.GetAttribute(a.Name);
                    v = x == null ? "null" : x.ToString();
                }
                catch (Exception e)
                {
                    v = "(" + e.GetType().Name + ")";
                }
                parts.Add(a.Name + "=" + v);
            }
            return string.Join(", ", parts.ToArray());
        }

        // Explain is the failure for e: TIA's own text, or, when TIA asked
        // something going online, what it asked.
        public TiaException Explain(string address, Exception e)
        {
            if (Asked.Count == 0) return null;
            return new TiaException(TiaNames.OnlineRefused(address, Asked, TiaNames.Message(e)), e);
        }

        // Check fails an operation TIA carried on with after a question:
        // a question answered no is a refusal, even when TIA went on.
        public void Check(string address, string what)
        {
            if (Asked.Count > 0) throw new TiaException(TiaNames.OnlineRefused(address, Asked, "TIA went on with the " + what));
        }
    }

    public sealed class OpennessTia : ITia
    {
        readonly TiaPortal portal;
        readonly string pcInterface;
        // attributesLogged holds the kinds of object whose attribute names
        // are in the log already: the first TIA start's log is how the
        // names a later change needs are learned.
        readonly HashSet<string> attributesLogged = new HashSet<string>(StringComparer.Ordinal);
        // What tells that TIA died (Gone): TIA Portal's own Disposed
        // event, a handler that could not be detached from its objects,
        // or its process having exited. The process is opened once, at
        // start, and kept (null when unknown): the open handle pins it, so
        // a later process given the same id is never taken for it.
        volatile bool disposed, detachFailed;
        readonly System.Diagnostics.Process process;
        readonly object processMu = new object();

        // OpennessTia starts TIA Portal, without its user interface. This
        // is the one place tiad starts TIA.
        public OpennessTia()
        {
            pcInterface = Environment.GetEnvironmentVariable("TIAD_PC_INTERFACE") ?? "";
            // Before TIA starts: every selection tiad may write must exist
            // in this Openness, since enum names are strings to the compiler.
            CheckSelections();
            try
            {
                portal = new TiaPortal(TiaPortalMode.WithoutUserInterface);
            }
            catch (Exception e)
            {
                throw new TiaException(Message(e), e);
            }
            // TIA raises it: nothing may be thrown back (Boundary).
            portal.Disposed += delegate
            {
                disposed = true;
                try
                {
                    Log.Line("TIA Portal says it is disposed");
                }
                catch (Exception)
                {
                }
            };
            try
            {
                int pid = portal.GetCurrentProcess().Id;
                process = System.Diagnostics.Process.GetProcessById(pid);
                Log.Line("TIA Portal runs as process {0}", pid);
            }
            catch (Exception e)
            {
                Log.Line("TIA Portal's process id is unknown ({0}): only its Disposed event and a failed detach tell that it died", e.GetType().Name);
            }
        }

        // Gone: see ITia. A read that throws counts as gone (the agent's
        // TiaGoneNow).
        public bool Gone
        {
            get
            {
                if (disposed || detachFailed) return true;
                if (process == null) return false;
                lock (processMu)
                {
                    return process.HasExited;
                }
            }
        }

        // Detached is called by a handler that could not be detached.
        void Detached()
        {
            detachFailed = true;
        }

        // The namespaces a configuration type tiad answers may live in.
        static readonly string[] ConfigurationNamespaces =
        {
            "Siemens.Engineering.Download.Configurations.",
            "Siemens.Engineering.Online.Configurations.",
        };

        // CheckSelections refuses to start TIA when a selection tiad may
        // write is not one this Openness defines: a configuration type
        // that is missing, has no CurrentSelection enum, or lacks the value.
        static void CheckSelections()
        {
            var missing = new List<string>();
            // Configuration types live in the base assembly and in STEP 7's
            // (DataBlockReinitialization* are STEP 7's).
            System.Reflection.Assembly[] asms = { typeof(DownloadConfiguration).Assembly, typeof(PlcSoftware).Assembly };
            foreach (KeyValuePair<string, string> w in DownloadChecks.Selections())
            {
                Type t = null;
                foreach (System.Reflection.Assembly asm in asms)
                {
                    foreach (string ns in ConfigurationNamespaces)
                    {
                        if (t == null) t = asm.GetType(ns + w.Key, false);
                    }
                }
                var prop = t == null ? null : t.GetProperty("CurrentSelection");
                if (prop == null || !prop.PropertyType.IsEnum || !Enum.IsDefined(prop.PropertyType, w.Value))
                {
                    missing.Add(w.Key + "." + w.Value);
                }
            }
            if (missing.Count > 0)
            {
                throw new TiaException("tiad.Openness does not match this Openness (missing selections: " + string.Join(", ", missing.ToArray()) + "), so it does not start TIA");
            }
        }

        public bool CanDryRun
        {
            // Not until S2b shows TIA always asks before it loads: with no
            // pre-download callback there is nothing to cancel from.
            get { return false; }
        }

        // Message is an Openness exception's text, with its inner ones.
        static string Message(Exception e)
        {
            return TiaNames.Message(e);
        }

        // Tia runs f and turns an Openness failure into TIA's own text.
        static T Tia<T>(Func<T> f)
        {
            try
            {
                return f();
            }
            catch (TiaException)
            {
                throw;
            }
            catch (EngineeringException e)
            {
                throw new TiaException(Message(e), e);
            }
            catch (OperationCanceledException e)
            {
                // A cancel tiad sent TIA (Boundary), or TIA's own, back out
                // of TIA: TIA's text, never an internal error.
                throw new TiaException(Message(e), e);
            }
        }

        static void Tia(Action f)
        {
            Tia<object>(delegate
            {
                f();
                return null;
            });
        }

        static OpenProject Of(object project)
        {
            return (OpenProject)project;
        }

        // ---- GSDML ----

        const string NoGsdRoute = "TIA Openness has no public call that installs a GSDML file; tiad learns TIA's own route at its first TIA start (S2a)";

        public bool GsdInstalled(string fileName)
        {
            throw new TiaException(NoGsdRoute);
        }

        public bool InstallGsd(string fullPath)
        {
            throw new TiaException(NoGsdRoute);
        }

        // ---- projects ----

        public object CreateProject(string fullPath, string name)
        {
            return Tia(delegate
            {
                // TIA makes <parent>/<name>/<name>.ap21 itself and refuses a
                // directory that exists; the agent made it empty.
                string dir = Path.GetDirectoryName(fullPath);
                if (Directory.Exists(dir))
                {
                    if (Directory.GetFileSystemEntries(dir).Length > 0) throw new TiaException("The project directory " + dir + " is not empty");
                    Directory.Delete(dir);
                }
                Project p = portal.Projects.Create(new DirectoryInfo(Path.GetDirectoryName(dir)), name);
                if (!File.Exists(fullPath))
                {
                    p.Close();
                    throw new TiaException("TIA created the project somewhere other than " + fullPath);
                }
                return (object)new OpenProject { Project = p, Full = fullPath };
            });
        }

        public object OpenProject(string fullPath)
        {
            return Tia(delegate
            {
                if (fullPath.EndsWith(".zap21", StringComparison.OrdinalIgnoreCase))
                {
                    // An archive is retrieved next to itself, into a
                    // directory named after it, which must not exist.
                    string target = fullPath.Substring(0, fullPath.Length - ".zap21".Length);
                    if (Directory.Exists(target) || File.Exists(target)) throw new TiaException(target + " exists: retrieve the archive elsewhere");
                    Project r = portal.Projects.Retrieve(new FileInfo(fullPath), new DirectoryInfo(target));
                    return (object)new OpenProject { Project = r, Full = r.Path.FullName };
                }
                Project p = portal.Projects.Open(new FileInfo(fullPath));
                return (object)new OpenProject { Project = p, Full = fullPath };
            });
        }

        public void CloseProject(object project)
        {
            Tia(delegate { Of(project).Project.Close(); });
        }

        public string Save(object project, bool archive)
        {
            return Tia(delegate
            {
                OpenProject o = Of(project);
                o.Project.Save();
                if (!archive) return o.Full;
                string dir = Path.GetDirectoryName(o.Full);
                string name = Path.GetFileNameWithoutExtension(o.Full) + ".zap21";
                string full = Path.Combine(dir, name);
                // TIA refuses to archive into the project's own directory
                // ("A project directory that already exists cannot be
                // saved"), so it archives into a fresh directory of its own,
                // and the archive is moved beside the project from there.
                string tmp = Path.Combine(Path.GetTempPath(), "tiad-archive-" + Guid.NewGuid().ToString("N"));
                Directory.CreateDirectory(tmp);
                try
                {
                    o.Project.Archive(new DirectoryInfo(tmp), name, ProjectArchivationMode.Compressed);
                    string made = Path.Combine(tmp, name);
                    if (!File.Exists(made)) throw new TiaException("TIA archived the project somewhere other than " + made);
                    if (File.Exists(full)) File.Delete(full);
                    File.Move(made, full);
                }
                finally
                {
                    try
                    {
                        Directory.Delete(tmp, true);
                    }
                    catch (IOException)
                    {
                    }
                }
                return full;
            });
        }

        // ---- hardware ----

        // Station is the project's station: the device holding the CPU.
        static HwDevice StationOf(Project p)
        {
            foreach (HwDevice d in AllDevices(p))
            {
                if (CpuOf(d) != null) return d;
            }
            return null;
        }

        // AllDevices is every device in the project, each once: its own,
        // the ungrouped ones (where TIA puts IO devices) and those in
        // user device groups, at any depth.
        static List<HwDevice> AllDevices(Project p)
        {
            var all = new List<HwDevice>();
            var seen = new HashSet<HwDevice>();
            Action<IEnumerable<HwDevice>> add = delegate(IEnumerable<HwDevice> ds)
            {
                foreach (HwDevice d in ds)
                {
                    if (seen.Add(d)) all.Add(d);
                }
            };
            add(p.Devices);
            add(p.UngroupedDevicesGroup.Devices);
            var groups = new List<DeviceUserGroup>();
            foreach (DeviceUserGroup g in p.DeviceGroups) groups.Add(g);
            for (int i = 0; i < groups.Count; i++)
            {
                add(groups[i].Devices);
                foreach (DeviceUserGroup g in groups[i].Groups) groups.Add(g);
            }
            return all;
        }

        // Cpu is the device item that holds the PLC software, or null.
        static DeviceItem CpuOf(HwDevice d)
        {
            foreach (DeviceItem i in Items(d.DeviceItems))
            {
                SoftwareContainer sc = i.GetService<SoftwareContainer>();
                if (sc != null && sc.Software is PlcSoftware) return i;
            }
            return null;
        }

        static List<DeviceItem> Items(DeviceItemComposition items)
        {
            var all = new List<DeviceItem>();
            foreach (DeviceItem i in items)
            {
                all.Add(i);
                all.AddRange(Items(i.DeviceItems));
            }
            return all;
        }

        static PlcSoftware Software(Project p)
        {
            HwDevice d = StationOf(p);
            if (d == null) throw new TiaException("the project has no CPU");
            return (PlcSoftware)CpuOf(d).GetService<SoftwareContainer>().Software;
        }

        // CpuNode is the CPU's first network interface with a node: its
        // PROFINET interface (X1).
        static Node CpuNode(DeviceItem cpu)
        {
            foreach (DeviceItem i in Items(cpu.DeviceItems))
            {
                NetworkInterface ni = i.GetService<NetworkInterface>();
                if (ni != null && ni.Nodes.Count > 0) return ni.Nodes[0];
            }
            throw new TiaException("the CPU " + cpu.Name + " has no network interface with a node");
        }

        // Attributes reads an object's attribute names, and logs them the
        // first time tiad meets that kind of object.
        List<string> Attributes(string kind, IEngineeringObject o)
        {
            var names = new List<string>();
            foreach (EngineeringAttributeInfo a in o.GetAttributeInfos()) names.Add(a.Name);
            if (attributesLogged.Add(kind))
            {
                var sorted = new List<string>(names);
                sorted.Sort(StringComparer.Ordinal);
                Log.Line("attributes of {0}: {1}", kind, string.Join(", ", sorted.ToArray()));
            }
            return names;
        }

        // Set sets attributes in order, each checked against the object's
        // attribute names first.
        void Set(string kind, IEngineeringObject o, params object[] nameValues)
        {
            List<string> names = Attributes(kind, o);
            for (int i = 0; i < nameValues.Length; i += 2) TiaNames.CheckAttribute(kind, names, (string)nameValues[i]);
            for (int i = 0; i < nameValues.Length; i += 2) o.SetAttribute((string)nameValues[i], nameValues[i + 1]);
        }

        // ApplyStation places the CPU, or keeps the project's (the agent
        // decided which, CpuReuse.Decide), and sets its PROFINET interface:
        // address, subnet mask, router, PROFINET name (generated names
        // off). Everything else in a spec waits for what S2a learns.
        // CannotApply is why this side cannot apply next yet, or null.
        public string CannotApply(Station next)
        {
            if (next.Modules.Count > 0) return "tiad cannot place rack modules yet: the first TIA start (S2a) learns the rack";
            if (next.Profinet != null) return "tiad cannot configure a PROFINET IO system yet: the IO-device attribute names are learned at the first TIA start (S2a)";
            if (next.WebServer != null && next.WebServer.Enabled) return "tiad cannot configure the web server yet: its attribute names are learned at the first TIA start (S2a)";
            return null;
        }

        public void ApplyStation(object project, Station next, string decision, Secrets secrets, List<string> notes, AccessMade made)
        {
            string cannot = CannotApply(next);
            if (cannot != null) throw new TiaException(cannot);
            Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice station;
                if (decision == CpuReuse.Reuse)
                {
                    station = StationOf(p);
                    if (station == null) throw new TiaException("the project's CPU went missing between the agent's read and the apply");
                }
                else
                {
                    foreach (HwDevice d in AllDevices(p)) d.Delete();
                    station = p.Devices.CreateWithItem(TiaNames.TypeIdentifier(next.Cpu.Order, next.Cpu.Firmware), next.Cpu.Name, next.Cpu.Name + " station");
                }
                DeviceItem cpu = CpuOf(station);
                Node node = CpuNode(cpu);
                bool router = next.Cpu.Router != "";
                Set("Node", node,
                    "Address", next.Cpu.Address,
                    "SubnetMask", next.Cpu.SubnetMask,
                    "UseRouter", router);
                if (router) Set("Node", node, "RouterAddress", next.Cpu.Router);
                Set("Node", node,
                    "PnDeviceNameAutoGeneration", false,
                    "PnDeviceName", next.Cpu.ProfinetName);
                ProtectionSteps.Run(new MasterSecret(station, cpu), next.Cpu.ConfigurationProtection, secrets.ConfigurationPassword, notes);
                AccessSteps.Run(new Access(p, station, cpu), next.Cpu.AccessControl, secrets.FullAccessPassword, secrets.Records, decision == CpuReuse.Create, made, notes);
                return true;
            });
        }

        public PasswordPolicy ReadPolicy(object project)
        {
            return Tia(delegate
            {
                PasswordPolicyConfigurator c = Of(project).Project.GetService<PasswordPolicyConfigurator>();
                if (c == null) throw new TiaException("TIA offers no password policy (PasswordPolicyConfigurator) for the project");
                var pol = new PasswordPolicy
                {
                    MinLength = c.MinimumLength,
                    MinDigits = c.MinimumNumericCharacterLength,
                    MinSpecial = c.MinimumSpecialCharacterLength,
                    UpperAndLower = c.IncludesLowerCaseAndUpperCaseCharacters,
                };
                Log.Line("password policy: {0}", pol.Describe());
                return pol;
            });
        }

        public void PrepareAccess(object project, Station next, Secrets secrets, bool recordOnly, AccessWrites w, List<string> notes)
        {
            Tia(delegate
            {
                AccessSteps.Prepare(new Users(Of(project).Project), next.Cpu.AccessControl, secrets.FullAccessPassword, secrets.Records, recordOnly, w, notes);
                return true;
            });
        }

        // RemoveAccess takes the role off every user before deleting it, so
        // a user the project held, holding the role, is never in the way.
        public List<string> RemoveAccess(object project, AccessMade made)
        {
            return Tia(delegate
            {
                var removed = new List<string>();
                UmacConfigurator u = Users.Umac(Of(project).Project);
                if (made.User)
                {
                    ProjectUser user = u.ProjectUsers.Find(AccessSteps.UserName);
                    if (user != null)
                    {
                        user.Delete();
                        removed.Add("user " + Json.Quote(AccessSteps.UserName));
                    }
                }
                if (made.Role)
                {
                    CustomRole role = u.CustomRoles.Find(AccessSteps.RoleName);
                    if (role != null)
                    {
                        foreach (ProjectUser pu in u.ProjectUsers)
                        {
                            if (pu.Roles.Contains(role)) pu.Roles.Remove(role);
                        }
                        role.Delete();
                        removed.Add("role " + Json.Quote(AccessSteps.RoleName));
                    }
                }
                Log.Line("access control: removed what this session made: {0}", removed.Count == 0 ? "nothing left" : string.Join(", ", removed.ToArray()));
                return removed;
            });
        }

        // Users is the project's users and roles (TIA's UmacConfigurator)
        // for AccessSteps, with the SecureStrings made here.
        class Users : IProjectUsers
        {
            protected readonly Project p;

            public Users(Project p)
            {
                this.p = p;
            }

            public static UmacConfigurator Umac(Project p)
            {
                UmacConfigurator u = p.GetService<UmacConfigurator>();
                if (u == null) throw new TiaException("TIA offers no users and roles (UmacConfigurator) for the project");
                return u;
            }

            protected ProjectUser User(string user)
            {
                ProjectUser pu = Umac(p).ProjectUsers.Find(user);
                if (pu == null) throw new TiaException("the user " + Json.Quote(user) + " is not in the project");
                return pu;
            }

            protected CustomRole Role(string role)
            {
                CustomRole r = Umac(p).CustomRoles.Find(role);
                if (r == null) throw new TiaException("the role " + Json.Quote(role) + " is not in the project");
                return r;
            }

            public bool HasUser(string user)
            {
                return Umac(p).ProjectUsers.Find(user) != null;
            }

            public void MakeUser(string user, string password)
            {
                using (var ss = Secure(password)) Umac(p).ProjectUsers.Create(user, ss);
            }

            // SetPassword maps TIA's refusal of a password the user has had to
            // PasswordReusedException. Openness has no typed error for it,
            // so it matches the one phrase TIA V21 words it with ("The
            // specified password has already been used", seen on the TIA
            // machine 2026-11-20), and nothing wider; any other failure
            // stays TIA's own.
            public void SetPassword(string user, string password)
            {
                ProjectUser pu = User(user);
                using (var ss = Secure(password))
                {
                    try
                    {
                        pu.SetPassword(ss);
                    }
                    catch (EngineeringException e)
                    {
                        if (TiaNames.Message(e).IndexOf("password has already been used", StringComparison.Ordinal) >= 0) throw new PasswordReusedException(e);
                        throw;
                    }
                }
            }

            public string Comment(string user)
            {
                return User(user).Comment ?? "";
            }

            public void SetComment(string user, string comment)
            {
                User(user).Comment = comment;
            }

            public bool HasRole(string role)
            {
                return Umac(p).CustomRoles.Find(role) != null;
            }

            public string RoleComment(string role)
            {
                return Role(role).Comment ?? "";
            }

            public void MakeRole(string role, string comment)
            {
                Umac(p).CustomRoles.Create(role, comment);
            }

            public void AddRole(string user, string role)
            {
                ProjectUser pu = User(user);
                CustomRole r = Role(role);
                if (!pu.Roles.Contains(r)) pu.Roles.Add(r);
            }
        }

        // Access is the CPU's access control besides the project's users and
        // roles: TIA's PlcAccessControlConfigurationProvider on the CPU and
        // the station's UmacDevice.
        sealed class Access : Users, IAccessControl
        {
            // FullAccessId is the identifier of the runtime right Full access
            // ("Full access" in TIA's English UI), as the TIA machine showed
            // it (2026-11-20).
            const string FullAccessId = "ProtectionLevelFullAccess";

            readonly HwDevice station;
            readonly DeviceItem cpu;
            readonly PlcAccessControlConfigurationProvider prov;
            UmacDevice dev;

            public Access(Project p, HwDevice station, DeviceItem cpu) : base(p)
            {
                this.station = station;
                this.cpu = cpu;
                prov = cpu.GetService<PlcAccessControlConfigurationProvider>();
            }

            // Device is the CPU's runtime rights. The station serves them
            // (learned on the TIA machine, 2026-11-20), not the CPU's
            // device item.
            UmacDevice Device()
            {
                if (dev != null) return dev;
                dev = station.GetService<UmacDevice>();
                if (dev == null) throw new TiaException("TIA offers no runtime rights (UmacDevice) for the station of the CPU " + cpu.Name);
                return dev;
            }

            // FullAccess is the CPU's runtime right Full access. A miss names
            // every right the CPU offers.
            DeviceFunctionRight FullAccess()
            {
                var all = new List<string>();
                foreach (DeviceFunctionRight r in Device().AvailableDeviceFunctionRights)
                {
                    if (r.Identifier == FullAccessId) return r;
                    all.Add(Json.Quote(r.Identifier) + " (" + Json.Quote(r.Name) + ")");
                }
                throw new TiaException("the CPU " + cpu.Name + " offers no runtime right " + Json.Quote(FullAccessId) + " (Full access); it offers " + string.Join(", ", all.ToArray()));
            }

            public string State()
            {
                return prov == null ? AccessRule.Absent : prov.PlcAccessControlConfiguration.ToString();
            }

            public void Enable()
            {
                prov.PlcAccessControlConfiguration = PlcAccessControlConfiguration.EnabledWithAccessControl;
            }

            public void GrantFullAccess(string role)
            {
                CustomRole r = Role(role);
                DeviceFunctionRight full = FullAccess();
                if (Has(r, full)) return;
                r.AssignDeviceFunctionRight(Device(), full);
            }

            bool Has(CustomRole r, DeviceFunctionRight full)
            {
                foreach (DeviceFunctionRight x in r.GetAssignedDeviceFunctionRights(Device()))
                {
                    if (x.Identifier == full.Identifier) return true;
                }
                return false;
            }

            public string Check(string user, string role)
            {
                UmacConfigurator u = Umac(p);
                ProjectUser pu = u.ProjectUsers.Find(user);
                if (pu == null) return "the user " + Json.Quote(user) + " is not in the project";
                CustomRole r = u.CustomRoles.Find(role);
                if (r == null || !pu.Roles.Contains(r)) return "the user " + Json.Quote(user) + " lacks the role " + Json.Quote(role);
                if (!Has(r, FullAccess())) return "the role " + Json.Quote(role) + " lacks the runtime right Full access on the CPU";
                return null;
            }
        }

        // MasterSecret is TIA's PlcMasterSecretConfigurator for
        // ProtectionSteps, with the SecureStrings made here.
        sealed class MasterSecret : IProtectionConfigurator
        {
            readonly HwDevice station;
            readonly PlcMasterSecretConfigurator c;

            public MasterSecret(HwDevice station, DeviceItem cpu)
            {
                this.station = station;
                c = cpu.GetService<PlcMasterSecretConfigurator>();
            }

            public string State()
            {
                return c == null ? ConfigProtection.Absent : c.MasterSecretConfiguration.ToString();
            }

            public void Protect(string password)
            {
                using (var ss = Secure(password)) c.Protect(ss);
            }

            public void Unprotect(string password)
            {
                using (var ss = Secure(password)) c.Unprotect(ss);
            }

            public void RemoveCpu()
            {
                station.Delete();
            }
        }

        static SecureString Secure(string s)
        {
            var ss = new SecureString();
            foreach (char ch in s) ss.AppendChar(ch);
            ss.MakeReadOnly();
            return ss;
        }

        // ---- software ----

        public SoftwareResult ImportSoftware(object project, string group, List<SourceFile> sources, List<SourceFile> tagTables, bool prune)
        {
            return Tia(delegate
            {
                PlcSoftware sw = Software(Of(project).Project);
                PlcBlockUserGroup ug = sw.BlockGroup.Groups.Find(group);
                if (ug == null) ug = sw.BlockGroup.Groups.Create(group);
                var res = new SoftwareResult();
                var made = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
                foreach (var src in sources)
                {
                    var sr = new SourceResult { Path = src.Path };
                    string name = Path.GetFileName(src.FullPath);
                    PlcExternalSource old = sw.ExternalSourceGroup.ExternalSources.Find(name);
                    if (old != null) old.Delete();
                    PlcExternalSource es = sw.ExternalSourceGroup.ExternalSources.CreateFromFile(name, src.FullPath);
                    try
                    {
                        foreach (IEngineeringObject b in es.GenerateBlocksFromSource(ug, GenerateBlockOption.None))
                        {
                            PlcBlock pb = b as PlcBlock;
                            if (pb != null) made.Add(pb.Name);
                        }
                        sr.Ok = true;
                    }
                    catch (EngineeringException e)
                    {
                        sr.Messages.Add(Message(e));
                    }
                    finally
                    {
                        es.Delete();
                    }
                    res.Results.Add(sr);
                }
                foreach (var t in tagTables)
                {
                    var sr = new SourceResult { Path = t.Path };
                    try
                    {
                        sw.TagTableGroup.TagTables.Import(new FileInfo(t.FullPath), ImportOptions.Override);
                        sr.Ok = true;
                    }
                    catch (EngineeringException e)
                    {
                        sr.Messages.Add(Message(e));
                    }
                    res.Results.Add(sr);
                }
                if (prune)
                {
                    foreach (PlcBlock b in new List<PlcBlock>(ug.Blocks))
                    {
                        if (!made.Contains(b.Name)) b.Delete();
                    }
                }
                foreach (PlcBlock b in ug.Blocks)
                {
                    string kind = TiaNames.BlockKind(b.GetType().Name);
                    if (kind != "") res.Blocks.Add(new Block { Name = b.Name, Kind = kind });
                }
                res.Blocks.Sort(delegate(Block a, Block b) { return string.CompareOrdinal(a.Name, b.Name); });
                return res;
            });
        }

        // Compile compiles the station (hardware and software) when
        // hardware is asked, else the PLC software.
        public CompileResult Compile(object project, bool hardware, bool software)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                IEngineeringServiceProvider target;
                if (hardware) target = StationOf(p);
                else target = Software(p);
                if (target == null) throw new TiaException("the project has no CPU");
                CompilerResult r = target.GetService<ICompilable>().Compile();
                var res = new CompileResult
                {
                    Result = TiaNames.CompileResult(r.State.ToString()),
                    Errors = r.ErrorCount,
                    Warnings = r.WarningCount,
                };
                AddMessages(res.Messages, r.Messages);
                return res;
            });
        }

        // AddMessages collects the leaves of TIA's message tree.
        static void AddMessages(List<CompileMessage> into, CompilerResultMessageComposition ms)
        {
            foreach (CompilerResultMessage m in ms)
            {
                if (m.Messages.Count > 0)
                {
                    AddMessages(into, m.Messages);
                    continue;
                }
                into.Add(new CompileMessage { Object = m.Path ?? "", Severity = TiaNames.Severity(m.State.ToString()), Text = m.Description ?? "" });
            }
        }

        // ---- reaching the CPU ----

        // PcInterface is the PC interface of the PN/IE mode TIA reaches CPUs
        // through.
        ConfigurationPcInterface PcInterface(ConnectionConfiguration cc)
        {
            ConfigurationMode mode = cc.Modes.Find(TiaNames.ConnectionMode);
            if (mode == null) throw new TiaException("TIA has no " + TiaNames.ConnectionMode + " connection mode");
            var names = new List<string>();
            foreach (ConfigurationPcInterface pc in mode.PcInterfaces) names.Add(pc.Name);
            string name = TiaNames.PcInterface(names, pcInterface);
            foreach (ConfigurationPcInterface pc in mode.PcInterfaces)
            {
                if (pc.Name == name) return pc;
            }
            throw new TiaException("PC interface " + name + " disappeared");
        }

        static ConfigurationTargetInterface TargetInterface(ConfigurationPcInterface pc)
        {
            var names = new List<string>();
            foreach (ConfigurationTargetInterface t in pc.TargetInterfaces) names.Add(t.Name);
            string name = TiaNames.Only("target interface", names);
            return pc.TargetInterfaces.Find(name);
        }

        // ---- download ----

        public DownloadOutcome Download(object project, string address, bool hardware, bool software,
            Secrets secrets, bool? startAfter, Func<Question, Answer> answer)
        {
            var decider = new DownloadDecider(answer, startAfter);
            int checks = 0;
            OnlineAsks online = null;
            DownloadProvider dp = null;
            ConfigurationTargetInterface target = null;
            ConfigurationAddress at = null;
            DownloadOptions options = DownloadOptions.None;
            string cpuName = "";
            // DownloadSteps (core) decides how it ended: from TIA's download
            // call on, any failure is in doubt, and detaching the handlers
            // from a TIA that died never replaces that.
            return DownloadSteps.Run(decider, address,
                delegate
                {
                    HwDevice d = StationOf(Of(project).Project);
                    if (d == null) throw new TiaException("the project has no CPU");
                    DeviceItem cpu = CpuOf(d);
                    // Its name, which TIA's download messages use (core
                    // reads the CPU's mode from them).
                    cpuName = cpu.Name ?? "";
                    dp = cpu.GetService<DownloadProvider>();
                    online = new OnlineAsks(dp.Configuration, Detached);
                    ConfigurationPcInterface pc = PcInterface(dp.Configuration);
                    target = TargetInterface(pc);
                    // Provisional until S2b: the address on the target
                    // interface, for Download's address overload.
                    at = target.Addresses.Create(address);
                    if (hardware) options |= DownloadOptions.Hardware;
                    if (software) options |= DownloadOptions.Software;
                },
                delegate
                {
                    // TIA calls both callbacks from its own process: each is
                    // only Boundary.Call, and the decider keeps every cancel
                    // and failure (Keep) before TIA gets Boundary.Fail's.
                    // TIA asks one configuration per call and lists them
                    // nowhere (#789 S2b): each is logged as handed over
                    // (touching only c, every read dropped if it fails),
                    // then answered on its own.
                    DownloadResult r = dp.Download(target, at,
                        delegate(DownloadConfiguration c)
                        {
                            Boundary.Call("download: before loading", delegate
                            {
                                DownloadDecider.Seen(++checks,
                                    delegate { return c.GetType().Name; },
                                    delegate { var p = c.Parent; return p == null ? null : p.GetType().Name; });
                                Step st = decider.Next(c, c.GetType().Name, c.Message ?? "", null);
                                Write(decider, c, st, secrets);
                            }, decider.Keep);
                        },
                        delegate(DownloadConfiguration c)
                        {
                            Boundary.Call("download: after loading", delegate
                            {
                                Step st = decider.After(c.GetType().Name);
                                if (st != null) Write(decider, c, st, secrets);
                                else Log.Line("download: after loading, TIA reports {0}: {1}", c.GetType().Name, TiaNames.LogSafe(c.Message ?? ""));
                            }, decider.Keep);
                        },
                        options);
                    // Read inside the call: a failure reading the result of
                    // a download TIA ran is in doubt too.
                    var list = new List<string>();
                    DownloadMessages(r.Messages, list);
                    return new DownloadCall { Messages = string.Join("; ", list.ToArray()), MessageList = list, State = r.State.ToString(), Cpu = cpuName };
                },
                delegate
                {
                    if (online != null) online.Stop();
                },
                delegate
                {
                    return online == null ? new List<string>() : online.Asked;
                });
        }

        // Write writes one step into a configuration and reports it. A
        // write that fails cancels the download, even if TIA swallows the
        // exception: the decider keeps the cancel.
        static void Write(DownloadDecider decider, DownloadConfiguration c, Step st, Secrets secrets)
        {
            try
            {
                Apply(c, st.Setting, secrets);
            }
            catch (Exception e)
            {
                throw decider.Fail("writing " + st.Setting + " into " + c.GetType().Name + " failed: " + Message(e));
            }
            decider.Wrote(st);
        }

        // Apply writes a setting into a configuration: a selection by its
        // enum name, or a password: the protection password or the
        // configuration password, each only where Setting.Secret puts it.
        static void Apply(DownloadConfiguration c, Setting s, Secrets secrets)
        {
            string secret = Setting.Secret(s, secrets);
            if (secret != null)
            {
                var pw = c as DownloadPasswordConfiguration;
                if (pw == null || secret == "") throw new TiaException("no " + s.Kind + " secret for " + c.GetType().Name);
                using (var ss = Secure(secret)) pw.SetPassword(ss);
                return;
            }
            var prop = c.GetType().GetProperty("CurrentSelection");
            if (prop == null || !prop.PropertyType.IsEnum) throw new TiaException(c.GetType().Name + " has no selection to set");
            prop.SetValue(c, Enum.Parse(prop.PropertyType, s.Value), null);
        }

        // DownloadMessages adds TIA's download result's messages to into,
        // one per element, depth first: a message with messages under it
        // is those messages.
        static void DownloadMessages(DownloadResultMessageComposition ms, List<string> into)
        {
            foreach (DownloadResultMessage m in ms)
            {
                if (m.Messages.Count > 0) DownloadMessages(m.Messages, into);
                else if (!string.IsNullOrEmpty(m.Message)) into.Add(m.Message);
            }
        }

        // ---- compare ----

        // Compare reads every program block the project holds, then goes
        // online to the CPU at address, compares the PLC software with it
        // and returns TIA's compare tree as is, with those blocks; the
        // agent decides what they prove (TiaNames.CompareEqual).
        public OnlineCompare Compare(object project, string address)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) throw new TiaException("the project has no CPU");
                DeviceItem cpu = CpuOf(d);
                PlcSoftware sw = (PlcSoftware)cpu.GetService<SoftwareContainer>().Software;
                var blocks = new List<ProjectBlock>();
                ProjectBlocks(sw.BlockGroup, blocks);
                OnlineProvider op = cpu.GetService<OnlineProvider>();
                ConfigurationPcInterface pc = PcInterface(op.Configuration);
                ConfigurationAddress at = TargetInterface(pc).Addresses.Create(address);
                // The handler stays attached until offline: TIA may ask while
                // it compares, too.
                var online = new OnlineAsks(op.Configuration, Detached);
                CompareNode tree = null;
                try
                {
                    OnlineState st = op.GoOnline(at);
                    bool failed = true;
                    try
                    {
                        if (st != OnlineState.Online)
                        {
                            if (online.Asked.Count > 0) throw new TiaException(TiaNames.OnlineRefused(address, online.Asked, "online state " + st));
                            throw new TiaException("TIA could not go online to " + address + ": " + st);
                        }
                        CompareResult cr = sw.CompareToOnline();
                        tree = cr == null ? null : Node(cr.RootElement);
                        failed = false;
                    }
                    finally
                    {
                        // After a failure TIA may be gone: going offline
                        // then must not replace that failure.
                        if (failed) DownloadSteps.Quietly(delegate { op.GoOffline(); }, "compare: going offline after a failed compare (if TIA is alive, the project may still be online)");
                        else op.GoOffline();
                    }
                }
                catch (TiaException)
                {
                    throw;
                }
                catch (Exception e)
                {
                    TiaException refused = online.Explain(address, e);
                    if (refused != null) throw refused;
                    throw;
                }
                finally
                {
                    online.Stop();
                }
                online.Check(address, "compare");
                return new OnlineCompare { Tree = tree, Blocks = blocks };
            });
        }

        // ProjectBlocks adds every block in g and in its user groups, at
        // any depth, as Openness reports it: no block is left out for its
        // type, so the agent names one it cannot label.
        static void ProjectBlocks(PlcBlockGroup g, List<ProjectBlock> into)
        {
            foreach (PlcBlock b in g.Blocks)
            {
                into.Add(new ProjectBlock { Name = b.Name, Type = b.GetType().Name, Number = b.Number });
            }
            foreach (PlcBlockUserGroup u in g.Groups) ProjectBlocks(u, into);
        }

        // Node copies one element of TIA's compare tree, and its children,
        // named by TiaNames.ElementName from both sides' names. A null
        // child is kept with no state, which the agent reads as different:
        // the glue drops nothing.
        static CompareNode Node(CompareResultElement e)
        {
            if (e == null) return null;
            string state = e.ComparisonResult.ToString();
            var n = new CompareNode { Name = TiaNames.ElementName(state, e.LeftName, e.RightName), State = state };
            foreach (CompareResultElement c in e.Elements)
            {
                n.Children.Add(Node(c) ?? new CompareNode());
            }
            return n;
        }

        // ---- export ----

        public List<string> Export(object project, string dir, ExportOptions o)
        {
            if (o.Gsdml) throw new TiaException("tiad cannot export an I-device GSDML yet");
            return Tia(delegate
            {
                Project p = Of(project).Project;
                var written = new List<string>();
                if (o.Blocks != "" || o.TagTables)
                {
                    PlcSoftware sw = Software(p);
                    if (o.Blocks != "")
                    {
                        ExportBlocks(sw, sw.BlockGroup, Path.Combine(dir, "blocks"), o.Blocks == "scl", written);
                        ExportTypes(sw, sw.TypeGroup, Path.Combine(dir, "types"), o.Blocks == "scl", written);
                    }
                    if (o.TagTables) ExportTags(sw.TagTableGroup, Path.Combine(dir, "tags"), written);
                }
                if (o.Hardware)
                {
                    HwDevice d = StationOf(p);
                    if (d == null) throw new TiaException("the project has no CPU");
                    string f = Path.Combine(dir, TiaNames.FileName(d.Name, ".aml"));
                    string log = Path.Combine(dir, TiaNames.FileName(d.Name, ".aml.log"));
                    if (!p.GetService<CaxProvider>().Export(d, new FileInfo(f), new FileInfo(log)))
                    {
                        throw new TiaException("TIA's hardware export failed; see " + log);
                    }
                    written.Add(f);
                    written.Add(log);
                }
                return written;
            });
        }

        static void ExportBlocks(PlcSoftware sw, PlcBlockGroup g, string dir, bool scl, List<string> written)
        {
            Directory.CreateDirectory(dir);
            foreach (PlcBlock b in g.Blocks)
            {
                if (scl && TiaNames.ExportsAsScl(b.ProgrammingLanguage.ToString()))
                {
                    string f = Path.Combine(dir, TiaNames.FileName(b.Name, ".scl"));
                    sw.ExternalSourceGroup.GenerateSource(new IGenerateSource[] { b }, new FileInfo(f), GenerateOptions.None);
                    written.Add(f);
                }
                else
                {
                    string f = Path.Combine(dir, TiaNames.FileName(b.Name, ".xml"));
                    b.Export(new FileInfo(f), Siemens.Engineering.ExportOptions.WithDefaults);
                    written.Add(f);
                }
            }
            foreach (PlcBlockUserGroup u in g.Groups) ExportBlocks(sw, u, Path.Combine(dir, TiaNames.FileName(u.Name, "")), scl, written);
        }

        static void ExportTypes(PlcSoftware sw, PlcTypeGroup g, string dir, bool scl, List<string> written)
        {
            Directory.CreateDirectory(dir);
            foreach (PlcType t in g.Types)
            {
                if (scl)
                {
                    string f = Path.Combine(dir, TiaNames.FileName(t.Name, ".udt"));
                    sw.ExternalSourceGroup.GenerateSource(new IGenerateSource[] { t }, new FileInfo(f), GenerateOptions.None);
                    written.Add(f);
                }
                else
                {
                    string f = Path.Combine(dir, TiaNames.FileName(t.Name, ".xml"));
                    t.Export(new FileInfo(f), Siemens.Engineering.ExportOptions.WithDefaults);
                    written.Add(f);
                }
            }
            foreach (PlcTypeUserGroup u in g.Groups) ExportTypes(sw, u, Path.Combine(dir, TiaNames.FileName(u.Name, "")), scl, written);
        }

        static void ExportTags(PlcTagTableGroup g, string dir, List<string> written)
        {
            Directory.CreateDirectory(dir);
            foreach (PlcTagTable t in g.TagTables)
            {
                string f = Path.Combine(dir, TiaNames.FileName(t.Name, ".xml"));
                t.Export(new FileInfo(f), Siemens.Engineering.ExportOptions.WithDefaults);
                written.Add(f);
            }
            foreach (PlcTagTableUserGroup u in g.Groups) ExportTags(u, Path.Combine(dir, TiaNames.FileName(u.Name, "")), written);
        }

        // ---- identity (siemens-ide.md section 3.8) ----

        // ProjectCpu reads the CPU's order number and firmware from the
        // project's CPU device item: after an upload, what the CPU reported.
        // The project holds no serial number or MAC (S2a's upload and its
        // AML export have none), so those are not read.
        public DeviceIdentity ProjectCpu(object project)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) throw new TiaException("the project has no CPU");
                DeviceItem cpu = CpuOf(d);
                return new DeviceIdentity { Order = CpuAttribute(cpu, "OrderNumber"), Firmware = CpuAttribute(cpu, "FirmwareVersion") };
            });
        }

        // CpuAttribute is one of the CPU's attributes as text, checked
        // against its attribute names first.
        string CpuAttribute(DeviceItem cpu, string name)
        {
            TiaNames.CheckAttribute("the CPU " + cpu.Name, Attributes("DeviceItem", cpu), name);
            return Convert.ToString(cpu.GetAttribute(name), System.Globalization.CultureInfo.InvariantCulture) ?? "";
        }

        // ReadCpu reads the project's CPU: the CPU device item's name,
        // order number and firmware (after an upload, what the CPU
        // reported), its configuration protection, and the project's other
        // devices. The project holds no serial number or MAC (S2a's upload
        // and its AML export have none).
        public ProjectCpu ReadCpu(object project)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) return (ProjectCpu)null;
                DeviceItem cpu = CpuOf(d);
                var pc = new ProjectCpu
                {
                    Name = cpu.Name ?? "",
                    Order = CpuAttribute(cpu, "OrderNumber"),
                    Firmware = CpuAttribute(cpu, "FirmwareVersion"),
                    Protection = new MasterSecret(d, cpu).State(),
                    AccessControl = new Access(p, d, cpu).State(),
                };
                var cpus = new List<string>();
                foreach (HwDevice o in AllDevices(p))
                {
                    if (CpuOf(o) != null) cpus.Add(Json.Quote(o.Name ?? ""));
                    if (!o.Equals(d)) pc.Others.AddRange(Kept(o));
                }
                // A second PLC is no device a download of this one loads.
                if (cpus.Count > 1)
                {
                    throw new TiaException("the project holds " + cpus.Count + " CPUs (" + string.Join(", ", cpus.ToArray()) + "); tiad handles a project with one");
                }
                return pc;
            });
        }

        // OptionalAttribute is a node attribute's text, or "" when the node
        // has no attribute of that name (a node of another kind).
        static string OptionalAttribute(Node n, string name)
        {
            foreach (EngineeringAttributeInfo a in n.GetAttributeInfos())
            {
                if (a.Name == name) return Convert.ToString(n.GetAttribute(name), System.Globalization.CultureInfo.InvariantCulture) ?? "";
            }
            return "";
        }

        // Kept describes a device besides the CPU's station: one entry per
        // node (every interface, every node), each with the device's name,
        // so a collision on any of them is seen; one bare entry for a
        // device without a node.
        static List<KeptDevice> Kept(HwDevice d)
        {
            var ks = new List<KeptDevice>();
            string name = d.Name ?? "";
            foreach (DeviceItem i in Items(d.DeviceItems))
            {
                NetworkInterface ni = i.GetService<NetworkInterface>();
                if (ni == null) continue;
                foreach (Node n in ni.Nodes)
                {
                    ks.Add(new KeptDevice { Name = name, Address = OptionalAttribute(n, "Address"), ProfinetName = OptionalAttribute(n, "PnDeviceName") });
                }
            }
            if (ks.Count == 0) ks.Add(new KeptDevice { Name = name });
            return ks;
        }

        // Identify cannot read a CPU's identity online yet: no Openness
        // read of it is proven (a read-only probe is planned on #789). The
        // agent refuses online calls to a pinned CPU until one is.
        public DeviceIdentity Identify(object project, string address)
        {
            return null;
        }

        // ReadFingerprint asks TIA's fingerprint service, which the portal
        // serves (not a project), for the CPU at address. Read-only: no
        // project is opened and nothing is written. Every question TIA asks
        // is recorded; one answered no fails the read even if TIA went on.
        public FingerprintRead ReadFingerprint(string address, bool trustUnverifiedCertificate)
        {
            return Tia(delegate
            {
                FingerprintDataProvider fp = portal.GetService<FingerprintDataProvider>();
                if (fp == null) throw new TiaException("TIA serves no FingerprintDataProvider");
                ConfigurationPcInterface pc = PcInterface(fp.Configuration);
                var targets = new List<string>();
                foreach (ConfigurationTargetInterface t in pc.TargetInterfaces) targets.Add(t.Name);
                Log.Line("identify: PC interface {0}, target interfaces [{1}]", TiaNames.LogSafe(pc.Name), TiaNames.LogSafe(string.Join(", ", targets.ToArray())));
                ConfigurationAddress at = targets.Count > 0 ? TargetInterface(pc).Addresses.Create(address) : pc.Addresses.Create(address);
                var online = OnlineAsks.ForCallback(trustUnverifiedCertificate, address);
                var r = new FingerprintRead();
                FingerprintDataResult res;
                try
                {
                    res = fp.GetFingerprintData(at, online.Handle);
                }
                catch (TiaException)
                {
                    throw;
                }
                catch (Exception e)
                {
                    TiaException refused = online.Explain(address, e);
                    if (refused != null) throw refused;
                    throw new TiaException(Message(e), e);
                }
                online.Check(address, "fingerprint read");
                r.TrustedBlindly.AddRange(online.TrustedBlindly);
                foreach (FingerprintDataItem it in res.FingerprintDataItems)
                {
                    r.Items.Add(new KeyValuePair<string, string>(it.FingerprintDataIdentifier ?? "", it.FingerprintDataValue ?? ""));
                }
                return r;
            });
        }

        // OnlineCheck: see ITia. Every step after going online records its
        // own failure instead of throwing, so the check always goes offline
        // and reports what it saw.
        public OnlineCheckResult OnlineCheck(object project, string address, bool trustUnverifiedCertificate)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) throw new TiaException("the project has no CPU");
                DeviceItem cpu = CpuOf(d);
                string at0 = OptionalAttribute(CpuNode(cpu), "Address");
                if (at0 != address) throw new TiaException("online-check: the project's CPU is at " + at0 + ", not " + address);
                var res = new OnlineCheckResult();
                res.ProjectCertificate = DescribeCertificate(cpu);
                Log.Line("online-check: project CPU certificate {0}", TiaNames.LogSafe(res.ProjectCertificate));
                OnlineProvider op = cpu.GetService<OnlineProvider>();
                ConfigurationAddress at = TargetInterface(PcInterface(op.Configuration)).Addresses.Create(address);
                var online = new OnlineAsks(op.Configuration, trustUnverifiedCertificate, address);
                try
                {
                    OnlineState st = OnlineState.Offline;
                    try
                    {
                        st = op.GoOnline(at);
                        res.State = st.ToString();
                    }
                    catch (Exception e)
                    {
                        res.State = op.State.ToString();
                        res.OnlineError = Message(e);
                    }
                    Log.Line("online-check: state {0}{1}", res.State, res.OnlineError == "" ? "" : "; " + TiaNames.LogSafe(res.OnlineError));
                    if (st == OnlineState.Online)
                    {
                        try
                        {
                            FingerprintDataProvider fp = portal.GetService<FingerprintDataProvider>();
                            ConfigurationPcInterface fpc = PcInterface(fp.Configuration);
                            ConfigurationAddress fat = fpc.TargetInterfaces.Count > 0 ? TargetInterface(fpc).Addresses.Create(address) : fpc.Addresses.Create(address);
                            var fpAsks = OnlineAsks.ForCallback(trustUnverifiedCertificate, address);
                            FingerprintDataResult fr = fp.GetFingerprintData(fat, fpAsks.Handle);
                            foreach (FingerprintDataItem it in fr.FingerprintDataItems)
                            {
                                res.Fingerprint.Add(new KeyValuePair<string, string>(it.FingerprintDataIdentifier ?? "", it.FingerprintDataValue ?? ""));
                            }
                            res.Asked.AddRange(fpAsks.Asked);
                            res.TrustedBlindly.AddRange(fpAsks.TrustedBlindly);
                        }
                        catch (Exception e)
                        {
                            res.FingerprintError = Message(e);
                        }
                        Log.Line("online-check: fingerprint items {0}{1}", res.Fingerprint.Count, res.FingerprintError == "" ? "" : "; " + TiaNames.LogSafe(res.FingerprintError));
                        try
                        {
                            CompareResult cr = ((PlcSoftware)cpu.GetService<SoftwareContainer>().Software).CompareToOnline();
                            Walk(cr.RootElement, res.Differences);
                            res.Compared = true;
                        }
                        catch (Exception e)
                        {
                            res.CompareError = Message(e);
                        }
                        Log.Line("online-check: compared {0}, differences {1}{2}", res.Compared, res.Differences.Count, res.CompareError == "" ? "" : "; " + TiaNames.LogSafe(res.CompareError));
                    }
                }
                finally
                {
                    try
                    {
                        if (op.State != OnlineState.Offline) op.GoOffline();
                    }
                    catch (Exception e)
                    {
                        Log.Line("online-check: going offline failed: {0}", TiaNames.LogSafe(Message(e)));
                    }
                    online.Stop();
                    res.Asked.AddRange(online.Asked);
                    res.TrustedBlindly.AddRange(online.TrustedBlindly);
                    Log.Line("online-check: offline ({0}); asked {1}, trusted blindly {2}", op.State, online.Asked.Count, online.TrustedBlindly.Count);
                }
                return res;
            });
        }

        // DescribeCertificate is the project CPU's PlcCommunicationCertificate
        // as TIA holds it: its type, and the SHA-256 of any X.509 inside.
        static string DescribeCertificate(DeviceItem cpu)
        {
            object c;
            try
            {
                c = cpu.GetAttribute("PlcCommunicationCertificate");
            }
            catch (Exception e)
            {
                return "unreadable (" + e.GetType().Name + ": " + Message(e) + ")";
            }
            if (c == null) return "none";
            var parts = new List<string> { c.GetType().FullName };
            var x = c as System.Security.Cryptography.X509Certificates.X509Certificate2;
            if (x != null) parts.Add("SHA-256 " + TiaNames.Fingerprint(x.RawData) + " subject " + x.Subject + " valid to " + x.NotAfter.ToUniversalTime().ToString("o"));
            foreach (System.Reflection.PropertyInfo pi in c.GetType().GetProperties())
            {
                if (pi.GetIndexParameters().Length > 0) continue;
                object v;
                try
                {
                    v = pi.GetValue(c, null);
                }
                catch (Exception e)
                {
                    parts.Add(pi.Name + "=(" + e.GetType().Name + ")");
                    continue;
                }
                var vx = v as System.Security.Cryptography.X509Certificates.X509Certificate2;
                if (vx != null) parts.Add(pi.Name + "=X509 SHA-256 " + TiaNames.Fingerprint(vx.RawData) + " subject " + vx.Subject + " valid to " + vx.NotAfter.ToUniversalTime().ToString("o"));
                else if (v is byte[]) parts.Add(pi.Name + "=bytes SHA-256 " + TiaNames.Fingerprint((byte[])v));
                else parts.Add(pi.Name + "=" + (v == null ? "null" : v.ToString()));
            }
            return string.Join("; ", parts.ToArray());
        }

        // ---- upload ----

        // Upload replaces the project's station with the one the CPU at
        // address holds. It answers no upload configuration: one that
        // asks (a password, missing products) cancels it, naming it.
        public List<string> Upload(object project, string address, bool trustUnverifiedCertificate)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                StationUploadProvider sup = p.GetService<StationUploadProvider>();
                ConfigurationPcInterface pc = PcInterface(sup.Configuration);
                // Provisional until S2a: the address on the PC interface, as
                // the Openness manual's station upload does (no station yet,
                // so no target interface).
                ConfigurationAddress at = pc.Addresses.Create(address);
                foreach (HwDevice d in AllDevices(p)) d.Delete();
                var ups = new UploadAsks();
                UploadResult r;
                var online = new OnlineAsks(sup.Configuration, Detached, trustUnverifiedCertificate, address);
                try
                {
                    // TIA calls it from its own process: UploadAsks (core)
                    // records what it asked, then sends only Boundary.Fail's.
                    r = sup.StationUpload(at, delegate(UploadConfiguration c)
                    {
                        ups.Handle(delegate { return c.GetType().Name + " (" + (c.Message ?? "") + ")"; });
                    });
                }
                catch (Exception e)
                {
                    if (ups.Asked != null) throw new TiaException("upload asked " + ups.Asked + ", which tiad does not answer; nothing was uploaded", e);
                    TiaException refused = online.Explain(address, e);
                    if (refused != null) throw refused;
                    throw;
                }
                finally
                {
                    online.Stop();
                }
                // A refusal TIA swallowed is still a refusal.
                if (ups.Asked != null) throw new TiaException("upload asked " + ups.Asked + ", which tiad does not answer; TIA went on anyway");
                online.Check(address, "upload");
                string state = r.State.ToString();
                if (!TiaNames.DownloadLoaded(state)) throw new TiaException("TIA's upload ended " + state);
                if (StationOf(p) == null) throw new TiaException("the upload brought no CPU into the project");
                return online.TrustedBlindly;
            });
        }
    }
}
