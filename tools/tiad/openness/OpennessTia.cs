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
// from the pre-download callback loads nothing, that a configuration's
// parent lists every pending one, the connection addresses, and the CPU's
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
        readonly bool trustUnverifiedCertificate;
        readonly string address;
        public readonly List<string> Asked = new List<string>();
        // TrustedBlindly are the certificate questions answered Trusted,
        // on the upload's explicit opt-in.
        public readonly List<string> TrustedBlindly = new List<string>();

        public OnlineAsks(ConnectionConfiguration cc) : this(cc, false, "")
        {
        }

        public OnlineAsks(ConnectionConfiguration cc, bool trustUnverifiedCertificate, string address)
        {
            this.cc = cc;
            this.trustUnverifiedCertificate = trustUnverifiedCertificate;
            this.address = address;
            cc.OnlineLegitimation += Handle;
        }

        public void Stop()
        {
            cc.OnlineLegitimation -= Handle;
        }

        // Handle answers one question, as OnlineChecks decides: a
        // certificate is set NonTrusted before anything else is read, and
        // anything else cancels. Every question is recorded, so the
        // operation fails even if TIA goes on.
        void Handle(OnlineConfiguration c)
        {
            string type = c.GetType().Name;
            var tls = c as TlsVerificationConfiguration;
            string answer = OnlineChecks.Answer(type, trustUnverifiedCertificate);
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
                if (trust) TrustedBlindly.Add(question);
                else Asked.Add(question);
                return;
            }
            string ask = OnlineChecks.Ask(type, "", "", new List<string>());
            Asked.Add(ask);
            Log.Line("online: TIA asks {0}; tiad cancels", TiaNames.LogSafe(ask));
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

        public void ApplyStation(object project, Station next, string decision, Secrets secrets, List<string> notes)
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
                return true;
            });
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
            Secrets secrets, Func<List<Question>, List<Answer>> decide)
        {
            Project p = Of(project).Project;
            var decider = new DownloadDecider(decide);
            string state = null, messages = "";
            Exception error = null;
            bool entered = false;
            OnlineAsks online = null;
            try
            {
                HwDevice d = StationOf(p);
                if (d == null) throw new TiaException("the project has no CPU");
                DownloadProvider dp = CpuOf(d).GetService<DownloadProvider>();
                online = new OnlineAsks(dp.Configuration);
                ConfigurationPcInterface pc = PcInterface(dp.Configuration);
                ConfigurationTargetInterface target = TargetInterface(pc);
                // Provisional until S2b: the address on the target
                // interface, for Download's address overload.
                ConfigurationAddress at = target.Addresses.Create(address);
                DownloadOptions options = DownloadOptions.None;
                if (hardware) options |= DownloadOptions.Hardware;
                if (software) options |= DownloadOptions.Software;
                entered = true;
                DownloadResult r = dp.Download(target, at,
                    delegate(DownloadConfiguration c)
                    {
                        Step st = decider.Next(c, c.GetType().Name, c.Message ?? "", delegate { return PendingOf(c); });
                        Write(decider, c, st, secrets);
                    },
                    delegate(DownloadConfiguration c)
                    {
                        Step st = decider.After(c.GetType().Name);
                        if (st != null) Write(decider, c, st, secrets);
                        else Log.Line("download: after loading, TIA reports {0}: {1}", c.GetType().Name, c.Message ?? "");
                    },
                    options);
                // Read inside the try: a failure reading the result of a
                // download TIA ran is in doubt too.
                messages = DownloadMessages(r.Messages);
                state = r.State.ToString();
            }
            catch (Exception e)
            {
                state = null;
                error = e;
            }
            finally
            {
                if (online != null) online.Stop();
            }
            // How it ended is decided in core (DownloadDecider.Finish).
            DownloadEnd end = decider.Finish(entered, state, messages, error);
            // A question going online fails the download, even one TIA went
            // on with: once TIA's download call was entered, in doubt.
            if (online != null && online.Asked.Count > 0)
            {
                throw new TiaException(TiaNames.OnlineRefused(address, online.Asked, end.Failure ?? "TIA went on with the download"), error, entered);
            }
            if (end.Failure != null) throw new TiaException(end.Failure, error, end.InDoubt);
            return new DownloadOutcome { Questions = decider.Asked, Loaded = end.Loaded };
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

        // Pending lists every configuration pending with c, from c's parent.
        // null when the parent does not list them.
        static List<Pending> PendingOf(DownloadConfiguration c)
        {
            var parent = c.Parent as IEngineeringObject;
            if (parent == null) return null;
            var all = new List<Pending>();
            foreach (EngineeringCompositionInfo ci in parent.GetCompositionInfos())
            {
                var comp = parent.GetComposition(ci.Name) as IEngineeringComposition;
                if (comp == null) continue;
                foreach (IEngineeringObject o in comp)
                {
                    var d = o as DownloadConfiguration;
                    if (d != null) all.Add(new Pending { Key = d, Type = d.GetType().Name, Message = d.Message ?? "" });
                }
            }
            return all;
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

        static string DownloadMessages(DownloadResultMessageComposition ms)
        {
            var l = new List<string>();
            foreach (DownloadResultMessage m in ms)
            {
                if (m.Messages.Count > 0) l.Add(DownloadMessages(m.Messages));
                else if (!string.IsNullOrEmpty(m.Message)) l.Add(m.Message);
            }
            return string.Join("; ", l.ToArray());
        }

        // ---- compare ----

        // Compare goes online to the CPU at address and compares the PLC
        // software with it.
        public bool Compare(object project, string address, List<Difference> differences)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) throw new TiaException("the project has no CPU");
                DeviceItem cpu = CpuOf(d);
                OnlineProvider op = cpu.GetService<OnlineProvider>();
                ConfigurationPcInterface pc = PcInterface(op.Configuration);
                ConfigurationAddress at = TargetInterface(pc).Addresses.Create(address);
                // The handler stays attached until offline: TIA may ask while
                // it compares, too.
                var online = new OnlineAsks(op.Configuration);
                try
                {
                    OnlineState st = op.GoOnline(at);
                    try
                    {
                        if (st != OnlineState.Online)
                        {
                            if (online.Asked.Count > 0) throw new TiaException(TiaNames.OnlineRefused(address, online.Asked, "online state " + st));
                            throw new TiaException("TIA could not go online to " + address + ": " + st);
                        }
                        CompareResult cr = ((PlcSoftware)cpu.GetService<SoftwareContainer>().Software).CompareToOnline();
                        Walk(cr.RootElement, differences);
                    }
                    finally
                    {
                        op.GoOffline();
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
                return differences.Count == 0;
            });
        }

        static void Walk(CompareResultElement e, List<Difference> into)
        {
            string state = TiaNames.Difference(e.ComparisonResult.ToString());
            if (state != "") into.Add(new Difference { Object = e.LeftName ?? "", State = state });
            foreach (CompareResultElement c in e.Elements) Walk(c, into);
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
                };
                foreach (HwDevice o in AllDevices(p))
                {
                    if (!o.Equals(d)) pc.Others.Add(Kept(o));
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

        // Kept describes a device besides the CPU's station by its name and
        // its first node's address and PROFINET name.
        static KeptDevice Kept(HwDevice d)
        {
            var k = new KeptDevice { Name = d.Name ?? "" };
            foreach (DeviceItem i in Items(d.DeviceItems))
            {
                NetworkInterface ni = i.GetService<NetworkInterface>();
                if (ni == null || ni.Nodes.Count == 0) continue;
                Node n = ni.Nodes[0];
                k.Address = OptionalAttribute(n, "Address");
                k.ProfinetName = OptionalAttribute(n, "PnDeviceName");
                break;
            }
            return k;
        }

        // Identify cannot read a CPU's identity online yet: no Openness
        // read of it is proven (a read-only probe is planned on #789). The
        // agent refuses online calls to a pinned CPU until one is.
        public DeviceIdentity Identify(object project, string address)
        {
            return null;
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
                string asked = null;
                UploadResult r;
                var online = new OnlineAsks(sup.Configuration, trustUnverifiedCertificate, address);
                try
                {
                    r = sup.StationUpload(at, delegate(UploadConfiguration c)
                    {
                        asked = c.GetType().Name + " (" + (c.Message ?? "") + ")";
                        throw new TiaException("upload asked " + asked + ", which tiad does not answer");
                    });
                }
                catch (Exception e)
                {
                    if (asked != null) throw new TiaException("upload asked " + asked + ", which tiad does not answer; nothing was uploaded", e);
                    TiaException refused = online.Explain(address, e);
                    if (refused != null) throw refused;
                    throw;
                }
                finally
                {
                    online.Stop();
                }
                // A refusal TIA swallowed is still a refusal.
                if (asked != null) throw new TiaException("upload asked " + asked + ", which tiad does not answer; TIA went on anyway");
                online.Check(address, "upload");
                string state = r.State.ToString();
                if (!TiaNames.DownloadLoaded(state)) throw new TiaException("TIA's upload ended " + state);
                if (StationOf(p) == null) throw new TiaException("the upload brought no CPU into the project");
                return online.TrustedBlindly;
            });
        }
    }
}
