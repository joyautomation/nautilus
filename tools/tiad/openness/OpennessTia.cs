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
using System.Globalization;
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
using Siemens.Engineering.SW.Units;
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
        // Held and HeldAsks are the connection a HoldOnline keeps online,
        // and the handler answering its questions; null when offline.
        public OnlineProvider Held;
        public OnlineAsks HeldAsks;
    }

    // OnlineAsks answers what TIA asks when it goes online to a CPU
    // (ConnectionConfiguration.OnlineLegitimation): a TLS certificate to
    // trust, or credentials. A certificate is answered not trusted unless
    // the request opts in; a project user's legitimation is answered as the
    // CPU's user "nautilus" when the operation's LoginUse (core) says so;
    // anything else is answered no. Each question answered no is recorded
    // so the failure names it: the certificate's fingerprint is what a
    // later, explicit trust decision has to match.
    sealed class OnlineAsks
    {
        readonly ConnectionConfiguration cc;
        readonly OnlineAnswers answers;
        readonly Action gone;
        readonly string address;
        // use is the operation's login, shared by its handlers.
        readonly LoginUse use;
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
        // LoggedIn is true once this handler answered credentials.
        public bool LoggedIn
        {
            get { return record.LoggedIn.Count > 0; }
        }

        // gone is called when the handler cannot be detached: TIA's
        // objects are disposed, so TIA is taken to be gone.
        public OnlineAsks(ConnectionConfiguration cc, Action gone, LoginUse use, string address) : this(cc, gone, false, address, use)
        {
        }

        public OnlineAsks(ConnectionConfiguration cc, Action gone, bool trustUnverifiedCertificate, string address, LoginUse use)
        {
            this.cc = cc;
            this.gone = gone;
            answers = new OnlineAnswers(trustUnverifiedCertificate);
            this.address = address;
            this.use = use;
            // Every question answered no tells the operation's login.
            record.Use = use;
            if (cc != null) cc.OnlineLegitimation += Handle;
        }

        // ForCallback answers the questions of a call that takes its own
        // OnlineConfigurationDelegate (GetFingerprintData) instead of a
        // connection's OnlineLegitimation event: pass Handle to it. It
        // answers through the same OnlineRecord.Handle path, so TIA gets
        // only Boundary.Fail's exception from it (#1145).
        public static OnlineAsks ForCallback(Action gone, bool trustUnverifiedCertificate, string address, LoginUse use)
        {
            return new OnlineAsks(null, gone, trustUnverifiedCertificate, address, use);
        }

        // Stop detaches the handler and never throws: TIA may be gone
        // (#789 stage 2: it died inside a download, and detaching from its
        // disposed objects threw in place of the download's own failure).
        // A handler that cannot be detached answers no from then on, even
        // to a question it would have trusted on the upload's opt-in
        // (OnlineAnswers), and TIA counts as gone.
        public void Stop()
        {
            if (!answers.Stop(delegate { if (cc != null) cc.OnlineLegitimation -= Handle; }, "online: detaching tiad's online handler from TIA")) gone();
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
            var auth = c as OnlineAuthenticationConfiguration;
            if (answer == OnlineChecks.Login && auth != null)
            {
                Login(auth, type, call);
                return;
            }
            if ((answer == OnlineChecks.NotTrusted || answer == OnlineChecks.Trusted) && tls != null)
            {
                bool trust = answer == OnlineChecks.Trusted;
                // No password follows a trust, even if what comes after
                // this write fails: recorded before the write.
                if (trust) use.Trusted();
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

        // Login answers a project user's legitimation as LoginUse decides:
        // what TIA offers is read first (whether the connection is secure,
        // the user types the CPU supports: an unreadable one refuses), then
        // the user "nautilus", as a project user, and the password are
        // written. The configuration is never described: it holds the
        // credentials. The log names the user types only, so the next
        // window shows what a CPU with access control offers.
        void Login(OnlineAuthenticationConfiguration auth, string type, OnlineRecord.Call call)
        {
            bool? secure = null;
            List<string> types = null;
            try
            {
                secure = auth.IsSecureCommunication;
            }
            catch (Exception e)
            {
                Log.Line("online: reading IsSecureCommunication failed ({0})", e.GetType().Name);
            }
            try
            {
                var t = new List<string>();
                foreach (Siemens.Engineering.Online.Configurations.AuthenticationType a in auth.GetSupportedAuthenticationTypes()) t.Add(a.CurrentUserType.ToString());
                types = t;
            }
            catch (Exception e)
            {
                Log.Line("online: reading the supported user types failed ({0})", e.GetType().Name);
            }
            Log.Line("online: {0} asks credentials ({1}): secure communication {2}, user types [{3}]", TiaNames.LogSafe(address), type,
                secure == null ? "unreadable" : secure.Value ? "yes" : "no", types == null ? "unreadable" : string.Join(", ", types.ToArray()));
            use.Answer(call, type, secure, types, delegate(string password)
            {
                OnlineCredentials cr = auth.OnlineCredentials;
                if (cr == null) throw new TiaException("TIA's credentials question holds no OnlineCredentials");
                cr.Type = UserType.ProjectUser;
                cr.Name = OnlineLogin.User;
                using (SecureString pw = OpennessTia.Secure(password)) cr.SetPassword(pw);
            });
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
            if (Asked.Count == 0)
            {
                return LoggedIn ? new TiaException(TiaNames.LoginFailed(address, TiaNames.Message(e)), e) : null;
            }
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
        volatile bool disposed, detachFailed, retiring;
        readonly System.Diagnostics.Process process;
        readonly object processMu = new object();

        // OpennessTia starts TIA Portal, without its user interface unless
        // TIAD_UI is 1 (Config.Ui, which the agent checked at its start:
        // a window to show blocks in, for viewing only). This is the one
        // place tiad starts TIA.
        public OpennessTia()
        {
            bool ui = Environment.GetEnvironmentVariable("TIAD_UI") == "1";
            pcInterface = Environment.GetEnvironmentVariable("TIAD_PC_INTERFACE") ?? "";
            // Before TIA starts: every selection tiad may write must exist
            // in this Openness, since enum names are strings to the compiler.
            CheckSelections();
            try
            {
                portal = new TiaPortal(ui ? TiaPortalMode.WithUserInterface : TiaPortalMode.WithoutUserInterface);
                if (ui) Log.Line("TIA Portal started WITH its user interface (TIAD_UI=1): viewing only, downloads are refused");
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
                    Log.Line(retiring ? "TIA Portal says it is disposed (retired on purpose)" : "TIA Portal says it is disposed");
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

        // RetireWait bounds the wait for TIA's process to exit once it is
        // disposed: TIA saves its settings on the way out.
        static readonly TimeSpan RetireWait = TimeSpan.FromSeconds(120);

        // Retire: see ITia. It disposes TIA Portal, which ends the process
        // this instance started, and waits until the process has exited:
        // the trust TIA keeps lives in that process. Without a process to
        // watch, or one that outlives the wait, it throws.
        public void Retire()
        {
            retiring = true;
            if (process == null) throw new TiaException("TIA Portal's process id is unknown, so tiad cannot tell that it exited");
            var sw = System.Diagnostics.Stopwatch.StartNew();
            try
            {
                portal.Dispose();
            }
            catch (Exception e)
            {
                throw new TiaException("disposing TIA Portal failed: " + Message(e), e);
            }
            bool exited;
            lock (processMu)
            {
                exited = process.WaitForExit((int)RetireWait.TotalMilliseconds);
            }
            if (!exited)
            {
                throw new TiaException("TIA Portal's process " + process.Id + " was disposed but had not exited " + (int)RetireWait.TotalSeconds + " s later");
            }
            Log.Line("TIA Portal's process {0} exited {1} ms after the retire began", process.Id, sw.ElapsedMilliseconds);
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

        public object AdoptProject(string archiveFull, string retrieveDir, string targetDir)
        {
            return Tia(delegate
            {
                if (Directory.Exists(retrieveDir) || File.Exists(retrieveDir)) throw new TiaException(retrieveDir + " exists: retrieve the archive elsewhere");
                Project r = portal.Projects.Retrieve(new FileInfo(archiveFull), new DirectoryInfo(retrieveDir));
                string want = Path.Combine(targetDir, Path.GetFileName(targetDir) + ".ap21");
                try
                {
                    // Seen on V21 (#1272 probe): SaveAs(<dir>\<name>) writes
                    // <dir>\<name>\<name>.ap21, renames the project to <name>
                    // and leaves it open there.
                    r.SaveAs(new DirectoryInfo(targetDir));
                    if (!string.Equals(r.Path.FullName, want, StringComparison.OrdinalIgnoreCase))
                    {
                        throw new TiaException("TIA saved the adopted project at " + r.Path.FullName + ", not " + want);
                    }
                }
                catch
                {
                    r.Close();
                    throw;
                }
                return (object)new OpenProject { Project = r, Full = want };
            });
        }

        public void CloseProject(object project)
        {
            // A project held online goes offline first, so TIA closes no
            // project with a live connection.
            ReleaseOnline(project);
            Tia(delegate { Of(project).Project.Close(); });
        }

        // HoldOnline goes online as Compare does, and stays online: TIA's
        // window then shows the CPU's state and the online/offline compare.
        // It reads and loads nothing itself.
        public string HoldOnline(object project, string address, LoginUse use)
        {
            return Tia(delegate
            {
                OpenProject o = Of(project);
                if (o.Held != null) throw new TiaException("the project is already online");
                HwDevice d = StationOf(o.Project);
                if (d == null) throw new TiaException("the project has no CPU");
                DeviceItem cpu = CpuOf(d);
                OnlineProvider op = cpu.GetService<OnlineProvider>();
                ConfigurationPcInterface pc = PcInterface(op.Configuration);
                ConfigurationAddress at = TargetInterface(pc).Addresses.Create(address);
                var online = new OnlineAsks(op.Configuration, Detached, use, address);
                bool held = false;
                try
                {
                    OnlineState st = op.GoOnline(at);
                    if (st != OnlineState.Online)
                    {
                        if (online.Asked.Count > 0) throw new TiaException(TiaNames.OnlineRefused(address, online.Asked, "online state " + st));
                        if (online.LoggedIn) throw new TiaException(TiaNames.LoginFailed(address, "online state " + st));
                        throw new TiaException("TIA could not go online to " + address + ": " + st);
                    }
                    online.Check(address, "going online");
                    o.Held = op;
                    o.HeldAsks = online;
                    held = true;
                    return st.ToString();
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
                    if (!held)
                    {
                        DownloadSteps.Quietly(delegate { op.GoOffline(); }, "online: going offline after a failed go-online (if TIA is alive, the project may still be online)");
                        online.Stop();
                    }
                }
            });
        }

        public void ReleaseOnline(object project)
        {
            Tia<bool>(delegate
            {
                OpenProject o = Of(project);
                if (o.Held == null) return true;
                // Held until TIA says offline: a GoOffline that throws
                // leaves the project held, so a retried release (or the
                // retried close of the session it broke) tries again, and
                // no close drops a live connection unasked.
                o.Held.GoOffline();
                OnlineAsks asks = o.HeldAsks;
                o.Held = null;
                o.HeldAsks = null;
                asks.Stop();
                return true;
            });
        }

        // ArchiveInto writes a compressed archive of o, already saved, into
        // dir, which must not be the project's own directory, and returns
        // its full path.
        static string ArchiveInto(OpenProject o, string dir)
        {
            string name = Path.GetFileNameWithoutExtension(o.Full) + ".zap21";
            o.Project.Archive(new DirectoryInfo(dir), name, ProjectArchivationMode.Compressed);
            string made = Path.Combine(dir, name);
            if (!File.Exists(made)) throw new TiaException("TIA archived the project somewhere other than " + made);
            return made;
        }

        public string Archive(object project, string dir)
        {
            return Tia(delegate
            {
                OpenProject o = Of(project);
                o.Project.Save();
                return ArchiveInto(o, dir);
            });
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
                    string made = ArchiveInto(o, tmp);
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
            return null;
        }

        public void ApplyStation(object project, Station next, string decision, Secrets secrets, bool turnOn, List<string> notes, AccessMade made)
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
                var access = new Access(p, station, cpu);
                AccessSteps.Run(access, next.Cpu.AccessControl, secrets.FullAccessPassword, secrets.Records, decision == CpuReuse.Create, turnOn, made, notes);
                access.LogRights();
                WebSteps.Run(new Web(this, cpu), access, next.WebServer, secrets, decision == CpuReuse.Create, made, notes);
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
                AccessSteps.PrepareAll(new Users(Of(project).Project), next, secrets, recordOnly, w, notes);
                return true;
            });
        }

        // RemoveAccess is AccessSteps.Remove: DeleteRole takes a role off
        // every user before deleting it, so a user the project held,
        // holding the role, is never in the way.
        public List<string> RemoveAccess(object project, AccessMade made)
        {
            return Tia(delegate
            {
                List<string> removed = AccessSteps.Remove(new Users(Of(project).Project), made);
                Log.Line("access control: removed what this session made: {0}", removed.Count == 0 ? "nothing left" : string.Join(", ", removed.ToArray()));
                return removed;
            });
        }

        public string UserComment(object project, string user)
        {
            return Tia(delegate
            {
                var u = new Users(Of(project).Project);
                return u.HasUser(user) ? u.Comment(user) : null;
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

            public List<string> UserNames()
            {
                var l = new List<string>();
                foreach (ProjectUser pu in Umac(p).ProjectUsers) l.Add(pu.Name);
                return l;
            }

            public List<string> RoleNames()
            {
                var l = new List<string>();
                foreach (CustomRole r in Umac(p).CustomRoles) l.Add(r.Name);
                return l;
            }

            // UserRoles names every role the user holds, system roles too
            // (ProjectUser.Roles holds both).
            public List<string> UserRoles(string user)
            {
                var l = new List<string>();
                foreach (Role r in User(user).Roles) l.Add(r.Name);
                return l;
            }

            public void RemoveRole(string user, string role)
            {
                ProjectUser pu = User(user);
                foreach (Role r in new List<Role>(pu.Roles))
                {
                    if (r.Name == role) pu.Roles.Remove(r);
                }
            }

            public void DeleteUser(string user)
            {
                User(user).Delete();
            }

            public void DeleteRole(string role)
            {
                UmacConfigurator u = Umac(p);
                CustomRole r = Role(role);
                foreach (ProjectUser pu in u.ProjectUsers)
                {
                    if (pu.Roles.Contains(r)) pu.Roles.Remove(r);
                }
                r.Delete();
            }

            public bool UserActive(string user)
            {
                return User(user).IsActive;
            }

            public void ActivateUser(string user)
            {
                User(user).Activate();
            }

            // AnonymousActive reads UmacConfigurator.AnonymousUser: none is
            // read as not active.
            public bool AnonymousActive()
            {
                ProjectUser a = Umac(p).AnonymousUser;
                return a != null && a.IsActive;
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

            DeviceFunctionRight Offered(string id)
            {
                foreach (DeviceFunctionRight r in Device().AvailableDeviceFunctionRights)
                {
                    if (r.Identifier == id) return r;
                }
                throw new TiaException("the CPU " + cpu.Name + " offers no runtime right " + Json.Quote(id));
            }

            public List<string> OfferedRights()
            {
                var l = new List<string>();
                foreach (DeviceFunctionRight r in Device().AvailableDeviceFunctionRights) l.Add(r.Identifier);
                return l;
            }

            public List<string> RoleRights(string role)
            {
                return RightsOf(Role(role));
            }

            // RightsOf is every runtime right r holds on this CPU.
            public List<string> RightsOf(CustomRole r)
            {
                var l = new List<string>();
                foreach (DeviceFunctionRight x in r.GetAssignedDeviceFunctionRights(Device())) l.Add(x.Identifier);
                l.Sort(StringComparer.Ordinal);
                return l;
            }

            public void Grant(string role, string right)
            {
                Role(role).AssignDeviceFunctionRight(Device(), Offered(right));
            }

            public void Revoke(string role, string right)
            {
                CustomRole r = Role(role);
                foreach (DeviceFunctionRight x in new List<DeviceFunctionRight>(r.GetAssignedDeviceFunctionRights(Device())))
                {
                    if (x.Identifier == right) r.UnAssignDeviceFunctionRight(Device(), x);
                }
            }

            bool Has(CustomRole r, DeviceFunctionRight full)
            {
                foreach (DeviceFunctionRight x in r.GetAssignedDeviceFunctionRights(Device()))
                {
                    if (x.Identifier == full.Identifier) return true;
                }
                return false;
            }

            // LogRights logs, once per agent run, every runtime right the
            // CPU offers and the project's users with their roles (names
            // only): the web users of step 2B need the web server rights'
            // identifiers, and an "Anonymous" user holding a role would
            // matter to the web server.
            // It never fails an apply: a read it cannot make is logged.
            public void LogRights()
            {
                if (rightsLogged) return;
                try
                {
                    if (State() != AccessRule.Enabled) return;
                    LogRightsNow();
                    rightsLogged = true;
                }
                catch (Exception e)
                {
                    Log.Line("access control: reading the runtime rights and users for the log failed: {0}", TiaNames.Message(e));
                }
            }

            void LogRightsNow()
            {
                var all = new List<string>();
                foreach (DeviceFunctionRight r in Device().AvailableDeviceFunctionRights) all.Add(Json.Quote(r.Identifier) + " (" + Json.Quote(r.Name) + ")");
                Log.Line("access control: the CPU offers the runtime rights {0}", string.Join(", ", all.ToArray()));
                var users = new List<string>();
                foreach (ProjectUser pu in Umac(p).ProjectUsers)
                {
                    var roles = new List<string>();
                    foreach (Role r in pu.Roles) roles.Add(Json.Quote(r.Name));
                    users.Add(Json.Quote(pu.Name) + " [" + string.Join(", ", roles.ToArray()) + "]");
                }
                Log.Line("access control: the project's users {0}", string.Join(", ", users.ToArray()));
                ProjectUser anon = Umac(p).AnonymousUser;
                if (anon == null) Log.Line("access control: the project offers no anonymous user");
                else
                {
                    var roles = new List<string>();
                    foreach (Role r in anon.Roles) roles.Add(Json.Quote(r.Name));
                    Log.Line("access control: the anonymous user {0} is {1}, roles [{2}]", Json.Quote(anon.Name), anon.IsActive ? "active" : "not active", string.Join(", ", roles.ToArray()));
                }
            }

            static bool rightsLogged;

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

        // Web is the CPU's web server for WebSteps: the CPU's
        // WebserverActivate and UseWebServerForInterface on the PROFINET
        // interface that carries the CPU's node (both seen on the bench
        // CPU's upload, 2026-11-21: the interface's attribute is on its
        // device item, not on the node).
        sealed class Web : IWebServer
        {
            readonly OpennessTia tia;
            readonly DeviceItem cpu;

            public Web(OpennessTia tia, DeviceItem cpu)
            {
                this.tia = tia;
                this.cpu = cpu;
            }

            DeviceItem Interface()
            {
                foreach (DeviceItem i in Items(cpu.DeviceItems))
                {
                    NetworkInterface ni = i.GetService<NetworkInterface>();
                    if (ni != null && ni.Nodes.Count > 0) return i;
                }
                throw new TiaException("the CPU " + cpu.Name + " has no network interface with a node");
            }

            bool IWebServer.Server()
            {
                return (bool)cpu.GetAttribute("WebserverActivate");
            }

            bool IWebServer.Interface()
            {
                return (bool)Interface().GetAttribute("UseWebServerForInterface");
            }

            // WebserverHttpsOnly shows only while the server is on (seen on
            // the TIA machine, 2026-11-22).
            public bool HttpsOnly()
            {
                return (bool)cpu.GetAttribute("WebserverHttpsOnly");
            }

            // Set reads each switch first and writes only one that differs,
            // so an apply that leaves a server off writes nothing.
            public void Set(bool on)
            {
                DeviceItem i = Interface();
                IWebServer self = this;
                if (on)
                {
                    if (!self.Server()) tia.Set("DeviceItem", cpu, "WebserverActivate", true);
                    // WebserverHttpsOnly shows only once the server is on;
                    // Set checks it is offered.
                    if (!HttpsOnly()) tia.Set("DeviceItem (web server on)", cpu, "WebserverHttpsOnly", true);
                    if (!self.Interface()) tia.Set("Interface", i, "UseWebServerForInterface", true);
                }
                else
                {
                    if (self.Interface()) tia.Set("Interface", i, "UseWebServerForInterface", false);
                    if (self.Server()) tia.Set("DeviceItem", cpu, "WebserverActivate", false);
                }
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

        internal static SecureString Secure(string s)
        {
            var ss = new SecureString();
            foreach (char ch in s) ss.AppendChar(ch);
            ss.MakeReadOnly();
            return ss;
        }

        // ---- software ----

        public SoftwareResult ImportSoftware(object project, string group, List<SourceFile> sources, List<SourceFile> tagTables, bool prune, bool pruneTypes)
        {
            return Tia(delegate
            {
                PlcSoftware sw = Software(Of(project).Project);
                PlcBlockUserGroup ug = sw.BlockGroup.Groups.Find(group);
                if (ug == null) ug = sw.BlockGroup.Groups.Create(group);
                // The data types' user group of the same name, made only
                // when a source declares data types.
                PlcTypeUserGroup tg = sw.TypeGroup.Groups.Find(group);
                var res = new SoftwareResult();
                var made = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
                var madeTypes = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
                foreach (var src in sources)
                {
                    var sr = new SourceResult { Path = src.Path };
                    // A name of tiad's own, so a source the project keeps
                    // under the file's name is never replaced or deleted.
                    string name = "tiad-" + Guid.NewGuid().ToString("N") + "-" + Path.GetFileName(src.FullPath);
                    PlcExternalSource es = sw.ExternalSourceGroup.ExternalSources.CreateFromFile(name, src.FullPath);
                    // outside: the failure of an object handed back outside
                    // the group, kept so a failing delete of the source
                    // below cannot replace it and lose the mark.
                    TiaException outside = null;
                    try
                    {
                        IList<IEngineeringObject> gen;
                        if (src.DataTypes)
                        {
                            // Straight into the data types' user group (TIA
                            // V21's type-group overload, TIA VM 2026-11-24).
                            if (tg == null) tg = sw.TypeGroup.Groups.Create(group);
                            gen = es.GenerateBlocksFromSource(tg, GenerateBlockOption.None);
                        }
                        else
                        {
                            gen = es.GenerateBlocksFromSource(ug, GenerateBlockOption.None);
                        }
                        foreach (IEngineeringObject o in gen)
                        {
                            // TIA regenerates a same-named object where it
                            // sits and hands it back with no error (a data
                            // type in another user group, TIA VM
                            // 2026-11-24): anything but a block in the block
                            // group or a data type in the data types' group
                            // fails the import, and the agent keeps the
                            // session unusable.
                            PlcBlock pb = o as PlcBlock;
                            PlcType pt = o as PlcType;
                            if (pb != null && InGroup(pb, ug))
                            {
                                made.Add(pb.Name);
                                continue;
                            }
                            if (pt != null && tg != null && InTypeGroup(pt, tg))
                            {
                                madeTypes.Add(pt.Name);
                                continue;
                            }
                            string what = pb != null ? "block \"" + pb.Name + "\"" : pt != null ? "data type \"" + pt.Name + "\"" : "an object (" + o.GetType().Name + ")";
                            outside = new TiaException(src.Path + ": TIA generated " + what + " outside group \"" + group + "\", where a same-named one sits") { OutsideGroup = true };
                            throw outside;
                        }
                        sr.Ok = true;
                    }
                    catch (EngineeringException e)
                    {
                        sr.Messages.Add(Message(e));
                    }
                    finally
                    {
                        try
                        {
                            es.Delete();
                        }
                        catch (EngineeringException)
                        {
                            if (outside == null) throw;
                        }
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
                    // The blocks first: a data type may be in use by one.
                    foreach (PlcBlock b in new List<PlcBlock>(ug.Blocks))
                    {
                        if (!made.Contains(b.Name)) b.Delete();
                    }
                    // Data types only when the agent says so (pruneTypes): an
                    // older caller's prune never deletes them.
                    if (pruneTypes && tg != null)
                    {
                        foreach (PlcType t in new List<PlcType>(tg.Types))
                        {
                            if (!madeTypes.Contains(t.Name)) t.Delete();
                        }
                    }
                }
                foreach (PlcBlock b in ug.Blocks)
                {
                    string kind = TiaNames.BlockKind(b.GetType().Name);
                    if (kind != "") res.Blocks.Add(new Block { Name = b.Name, Kind = kind });
                }
                if (tg != null)
                {
                    foreach (PlcType t in tg.Types) res.Blocks.Add(new Block { Name = t.Name, Kind = "UDT" });
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
                AddMessages(res.Messages, r.Messages, new List<string>());
                return res;
            });
        }

        // AddMessages collects the leaves of TIA's message tree, each with
        // the paths of its parents: an SCL error's leaf names only the line
        // in the block's body, and the block is a parent.
        static void AddMessages(List<CompileMessage> into, CompilerResultMessageComposition ms, List<string> parents)
        {
            foreach (CompilerResultMessage m in ms)
            {
                if (m.Messages.Count > 0)
                {
                    var below = new List<string>(parents);
                    below.Add(m.Path ?? "");
                    AddMessages(into, m.Messages, below);
                    continue;
                }
                into.Add(new CompileMessage { Object = m.Path ?? "", Severity = TiaNames.Severity(m.State.ToString()), Text = m.Description ?? "", Path = new List<string>(parents) });
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

        public DownloadOutcome Download(object project, string address, bool hardware, bool software, bool everyBlock,
            Secrets secrets, bool? startAfter, Func<Question, Answer> answer, LoginUse use)
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
                    online = new OnlineAsks(dp.Configuration, Detached, use, address);
                    ConfigurationPcInterface pc = PcInterface(dp.Configuration);
                    target = TargetInterface(pc);
                    // Provisional until S2b: the address on the target
                    // interface, for Download's address overload.
                    at = target.Addresses.Create(address);
                    if (hardware) options |= DownloadOptions.Hardware;
                    // Only what changed, unless the caller consented to
                    // the whole program (#1341): Software loads every
                    // block, and TIA stops the CPU for it and resets every
                    // data block (bench 1510SP, 2026-11-24).
                    if (software) options |= (DownloadOptions)Enum.Parse(typeof(DownloadOptions), LayoutRule.SoftwareOption(everyBlock));
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
                },
                delegate { return use.Answered; });
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
                // What the configuration offers instead, for the next
                // window (#789 stage 4); never a password configuration's.
                if (!(c is DownloadPasswordConfiguration))
                {
                    try
                    {
                        Log.Line("download: {0} refused {1}; as handed over: {2}", c.GetType().Name, st.Setting, TiaNames.Members(c));
                    }
                    catch (Exception)
                    {
                    }
                }
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
        public OnlineCompare Compare(object project, string address, LoginUse use)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) throw new TiaException("the project has no CPU");
                DeviceItem cpu = CpuOf(d);
                PlcSoftware sw = (PlcSoftware)cpu.GetService<SoftwareContainer>().Software;
                var blocks = new List<ProjectBlock>();
                ProjectBlocks(sw.BlockGroup, "", blocks);
                OnlineProvider op = cpu.GetService<OnlineProvider>();
                ConfigurationPcInterface pc = PcInterface(op.Configuration);
                ConfigurationAddress at = TargetInterface(pc).Addresses.Create(address);
                // The handler stays attached until offline: TIA may ask while
                // it compares, too.
                var online = new OnlineAsks(op.Configuration, Detached, use, address);
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
                            if (online.LoggedIn) throw new TiaException(TiaNames.LoginFailed(address, "online state " + st));
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
        // type, so the agent names one it cannot label. path is g's group
        // path ("" for the root).
        static void ProjectBlocks(PlcBlockGroup g, string path, List<ProjectBlock> into)
        {
            foreach (PlcBlock b in g.Blocks)
            {
                var pb = new ProjectBlock
                {
                    Name = b.Name,
                    Type = b.GetType().Name,
                    Number = b.Number,
                    Group = path,
                    Modified = b.ModifiedDate.ToUniversalTime().Ticks.ToString(CultureInfo.InvariantCulture),
                };
                OB ob = b as OB;
                if (ob != null)
                {
                    pb.Class = ob.SecondaryType ?? "";
                    // CyclicTime (microseconds) exists on a cyclic
                    // interrupt OB only (TIA VM, 2026-11-22).
                    // A cycle it cannot read (-1) never matches an asked
                    // one, and fails no import of a block it does not own.
                    if (pb.Class == CyclicObs.Cyclic)
                    {
                        try
                        {
                            pb.CycleMicros = Convert.ToInt64(b.GetAttribute("CyclicTime"), CultureInfo.InvariantCulture);
                        }
                        catch (EngineeringException)
                        {
                            pb.CycleMicros = -1;
                        }
                    }
                }
                into.Add(pb);
            }
            foreach (PlcBlockUserGroup u in g.Groups) ProjectBlocks(u, path == "" ? u.Name : path + "/" + u.Name, into);
        }

        // InTypeGroup is InGroup for a data type and a data types group.
        static bool InTypeGroup(PlcType t, PlcTypeUserGroup g)
        {
            IEngineeringObject p = t.Parent;
            while (p != null)
            {
                if (ReferenceEquals(p, g) || p.Equals(g)) return true;
                if (!(p is PlcTypeUserGroup)) return false;
                p = p.Parent;
            }
            return false;
        }

        // InGroup is whether b sits in g or one of its user groups.
        static bool InGroup(PlcBlock b, PlcBlockUserGroup g)
        {
            IEngineeringObject p = b.Parent;
            while (p != null)
            {
                if (ReferenceEquals(p, g) || p.Equals(g)) return true;
                if (!(p is PlcBlockUserGroup)) return false;
                p = p.Parent;
            }
            return false;
        }

        // ImportBlock imports one block's SimaticML into group (made when it
        // is not there), from a file of its own that is deleted after.
        public void ImportBlock(object project, string group, string xml)
        {
            Tia<bool>(delegate
            {
                PlcSoftware sw = Software(Of(project).Project);
                PlcBlockUserGroup ug = sw.BlockGroup.Groups.Find(group);
                if (ug == null) ug = sw.BlockGroup.Groups.Create(group);
                string f = Path.Combine(Path.GetTempPath(), "tiad-" + Guid.NewGuid().ToString("N") + ".xml");
                File.WriteAllText(f, xml, new System.Text.UTF8Encoding(false));
                try
                {
                    ug.Blocks.Import(new FileInfo(f), ImportOptions.None);
                }
                finally
                {
                    File.Delete(f);
                }
                return true;
            });
        }

        // DeleteBlock deletes the block named name in group or its user
        // groups, and nothing outside group.
        public void DeleteBlock(object project, string group, string name)
        {
            Tia<bool>(delegate
            {
                PlcSoftware sw = Software(Of(project).Project);
                PlcBlockUserGroup ug = sw.BlockGroup.Groups.Find(group);
                PlcBlock b = ug == null ? null : FindIn(ug, name);
                if (b == null) throw new TiaException("no block \"" + name + "\" in group \"" + group + "\" to delete");
                b.Delete();
                return true;
            });
        }

        // Show opens the block in TIA's editor (IShowable.ShowInEditor):
        // only a portal with its user interface has one.
        public void Show(object project, string group, string name)
        {
            Tia<bool>(delegate
            {
                PlcBlockGroup g = Software(Of(project).Project).BlockGroup;
                if (group != "")
                {
                    foreach (string part in group.Split('/'))
                    {
                        g = g.Groups.Find(part);
                        if (g == null) throw new TiaException("no block group \"" + group + "\" to show a block of");
                    }
                }
                PlcBlock b = null;
                foreach (PlcBlock x in g.Blocks) if (string.Equals(x.Name, name, StringComparison.OrdinalIgnoreCase)) b = x;
                if (b == null) throw new TiaException("no block \"" + name + "\" in group \"" + group + "\" to show");
                b.ShowInEditor();
                return true;
            });
        }

        static PlcBlock FindIn(PlcBlockGroup g, string name)
        {
            foreach (PlcBlock b in g.Blocks) if (string.Equals(b.Name, name, StringComparison.OrdinalIgnoreCase)) return b;
            foreach (PlcBlockUserGroup u in g.Groups)
            {
                PlcBlock f = FindIn(u, name);
                if (f != null) return f;
            }
            return null;
        }

        // Blocks is every program block the project holds, with its group.
        public List<ProjectBlock> Blocks(object project)
        {
            return Tia(delegate
            {
                var blocks = new List<ProjectBlock>();
                ProjectBlocks(Software(Of(project).Project).BlockGroup, "", blocks);
                return blocks;
            });
        }

        public List<ProjectBlock> Types(object project)
        {
            return Tia(delegate
            {
                var types = new List<ProjectBlock>();
                ProjectTypes(Software(Of(project).Project).TypeGroup, "", types);
                return types;
            });
        }

        // ProjectTypes reads every data type in g and its user groups, each
        // with its group path and modified time in ticks, as blocks are.
        static void ProjectTypes(PlcTypeGroup g, string path, List<ProjectBlock> into)
        {
            foreach (PlcType t in g.Types)
            {
                into.Add(new ProjectBlock
                {
                    Name = t.Name,
                    Type = t.GetType().Name,
                    Group = path,
                    DataType = true,
                    Modified = t.ModifiedDate.ToUniversalTime().Ticks.ToString(CultureInfo.InvariantCulture),
                });
            }
            foreach (PlcTypeUserGroup u in g.Groups) ProjectTypes(u, path == "" ? u.Name : path + "/" + u.Name, into);
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

        // Layout exports every program block and PLC data type, one at a
        // time, into a directory of its own, and reads each export
        // (LayoutRule.FromExport). An object TIA does not export (know-how
        // protection, ...) is unreadable, never left out (#1341).
        public List<LayoutObject> Layout(object project)
        {
            return Tia(delegate
            {
                PlcSoftware sw = Software(Of(project).Project);
                string dir = Path.Combine(Path.GetTempPath(), "tiad-layout-" + Guid.NewGuid().ToString("N"));
                Directory.CreateDirectory(dir);
                var into = new List<LayoutObject>();
                try
                {
                    int n = 0;
                    LayoutBlocks(sw.BlockGroup, dir, ref n, into);
                    LayoutTypes(sw.TypeGroup, dir, ref n, into);
                    // What else sets a data block's layout (Fable, #1341):
                    // a tag table's user constant (an array bound), and
                    // what the walk above cannot read, which counts as
                    // changed on every download.
                    LayoutConstants(sw.TagTableGroup, into);
                    LayoutTechnologyObjects(sw, into);
                    LayoutSoftwareUnits(sw, into);
                }
                finally
                {
                    try
                    {
                        Directory.Delete(dir, true);
                    }
                    catch (IOException e)
                    {
                        Log.Line("layout: removing {0} failed: {1}", dir, e.Message);
                    }
                    catch (UnauthorizedAccessException e)
                    {
                        Log.Line("layout: removing {0} failed: {1}", dir, e.Message);
                    }
                }
                return into;
            });
        }

        static LayoutObject LayoutOf(string name, string kind, string type, string file, Action<FileInfo> export)
        {
            try
            {
                export(new FileInfo(file));
                return LayoutRule.FromExport(name, kind, type, File.ReadAllText(file, System.Text.Encoding.UTF8));
            }
            catch (EngineeringException e)
            {
                return new LayoutObject { Name = name, Kind = kind, Type = type, Unreadable = "TIA did not export it: " + TiaNames.Message(e) };
            }
            catch (IOException e)
            {
                return new LayoutObject { Name = name, Kind = kind, Type = type, Unreadable = "its export could not be read: " + e.Message };
            }
            catch (UnauthorizedAccessException e)
            {
                return new LayoutObject { Name = name, Kind = kind, Type = type, Unreadable = "its export could not be read: " + e.Message };
            }
        }

        // LayoutConstants adds every user constant of every PLC tag table
        // as an object of its own: its data type and value hashed. A data
        // block's export names a constant it uses in quotes (an array
        // bound), so the naming reaches it.
        static void LayoutConstants(PlcTagTableGroup g, List<LayoutObject> into)
        {
            foreach (PlcTagTable t in g.TagTables)
            {
                foreach (PlcUserConstant c in t.UserConstants)
                {
                    var o = new LayoutObject { Name = c.Name, Kind = "constant", Type = "PlcUserConstant" };
                    try
                    {
                        string type = c.DataTypeName ?? "";
                        string value = c.Value ?? "";
                        o.Hash = LayoutRule.Sha256Hex("PlcUserConstant\n" + c.Name + "\n" + type + "\n" + value);
                    }
                    catch (EngineeringException e)
                    {
                        o.Unreadable = "TIA did not read the constant: " + TiaNames.Message(e);
                    }
                    into.Add(o);
                }
            }
            foreach (PlcTagTableUserGroup u in g.Groups) LayoutConstants(u, into);
        }

        // LayoutTechnologyObjects adds each technology object as
        // unreadable: tiad does not read its layout yet, so a download of a
        // PLC that holds one always needs the consent.
        static void LayoutTechnologyObjects(PlcSoftware sw, List<LayoutObject> into)
        {
            var seen = new List<string>();
            TechnologyObjectNames(sw.TechnologicalObjectGroup, seen);
            foreach (string name in seen)
            {
                into.Add(new LayoutObject { Name = name, Kind = "block", Type = "TechnologicalInstanceDB", Unreadable = "a technology object: tiad does not read its layout yet" });
            }
        }

        static void TechnologyObjectNames(Siemens.Engineering.SW.TechnologicalObjects.TechnologicalInstanceDBGroup g, List<string> into)
        {
            foreach (Siemens.Engineering.SW.TechnologicalObjects.TechnologicalInstanceDB t in g.TechnologicalObjects) into.Add(t.Name);
            foreach (Siemens.Engineering.SW.TechnologicalObjects.TechnologicalInstanceDBUserGroup u in g.Groups) TechnologyObjectNames(u, into);
        }

        // LayoutSoftwareUnits adds one unreadable object per software unit
        // and safety unit: tiad reads no unit's blocks yet (TIA VM
        // 2026-11-24: V21's PlcUnitProvider, its UnitGroup.Units and
        // .SafetyUnits). A unit read that fails
        // throws, which fails the whole read: never left out.
        static void LayoutSoftwareUnits(PlcSoftware sw, List<LayoutObject> into)
        {
            PlcUnitProvider p = sw.GetService<PlcUnitProvider>();
            if (p == null) return;
            foreach (PlcUnit u in p.UnitGroup.Units)
            {
                into.Add(new LayoutObject { Name = "unit:" + u.Name, Kind = "unit", Type = "PlcUnit", Unreadable = "a software unit: tiad does not read the blocks in it yet" });
            }
            foreach (PlcSafetyUnit u in p.UnitGroup.SafetyUnits)
            {
                into.Add(new LayoutObject { Name = "unit:" + u.Name, Kind = "unit", Type = "PlcSafetyUnit", Unreadable = "a safety unit: tiad does not read the blocks in it yet" });
            }
        }

        static void LayoutBlocks(PlcBlockGroup g, string dir, ref int n, List<LayoutObject> into)
        {
            foreach (PlcBlock b in g.Blocks)
            {
                PlcBlock x = b;
                into.Add(LayoutOf(b.Name, "block", b.GetType().Name, Path.Combine(dir, (n++).ToString(CultureInfo.InvariantCulture) + ".xml"),
                    delegate(FileInfo f) { x.Export(f, Siemens.Engineering.ExportOptions.WithDefaults); }));
            }
            foreach (PlcBlockUserGroup u in g.Groups) LayoutBlocks(u, dir, ref n, into);
        }

        static void LayoutTypes(PlcTypeGroup g, string dir, ref int n, List<LayoutObject> into)
        {
            foreach (PlcType t in g.Types)
            {
                PlcType x = t;
                into.Add(LayoutOf(t.Name, "type", t.GetType().Name, Path.Combine(dir, (n++).ToString(CultureInfo.InvariantCulture) + ".xml"),
                    delegate(FileInfo f) { x.Export(f, Siemens.Engineering.ExportOptions.WithDefaults); }));
            }
            foreach (PlcTypeUserGroup u in g.Groups) LayoutTypes(u, dir, ref n, into);
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

        // ReadTurnOn reads the project's local users and its anonymous
        // user: whether it is active, and the runtime rights on the
        // project's CPU of each role it holds (a custom role's; any other
        // role's rights are read as unreadable).
        public TurnOnFacts ReadTurnOn(object project)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) throw new TiaException("the project holds no CPU");
                var access = new Access(p, d, CpuOf(d));
                var f = new TurnOnFacts { Users = access.UserNames() };
                ProjectUser a = Users.Umac(p).AnonymousUser;
                f.AnonymousActive = a != null && a.IsActive;
                if (a != null)
                {
                    foreach (Role r in a.Roles)
                    {
                        var ar = new AnonymousRole { Name = r.Name ?? "" };
                        CustomRole cr = r as CustomRole;
                        if (cr != null) ar.Rights = access.RightsOf(cr);
                        f.AnonymousRoles.Add(ar);
                    }
                }
                return f;
            });
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
                ReadNode(cpu, pc);
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

        // ProjectCpuAddress reads the CPU node's address as ReadFingerprint
        // checks it: one read, so the two never drift.
        public string ProjectCpuAddress(object project)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) return "";
                return CpuNodeAddress(CpuOf(d));
            });
        }

        // ReadNode reads what an apply writes on the CPU's interface node
        // (ApplyStation), for CpuReuse.Found to compare with the spec. A
        // read that fails leaves NodeRead false, which Found reports as a
        // change: never a "no change" it cannot back.
        static void ReadNode(DeviceItem cpu, ProjectCpu pc)
        {
            try
            {
                Node n = CpuNode(cpu);
                pc.Address = OptionalAttribute(n, "Address");
                pc.SubnetMask = OptionalAttribute(n, "SubnetMask");
                bool router = Convert.ToBoolean(n.GetAttribute("UseRouter"), System.Globalization.CultureInfo.InvariantCulture);
                pc.Router = router ? OptionalAttribute(n, "RouterAddress") : "";
                pc.ProfinetNameAuto = Convert.ToBoolean(n.GetAttribute("PnDeviceNameAutoGeneration"), System.Globalization.CultureInfo.InvariantCulture);
                pc.ProfinetName = OptionalAttribute(n, "PnDeviceName");
                pc.NodeRead = true;
            }
            catch (Exception e)
            {
                pc.NodeRead = false;
                Log.Line("reading the CPU's interface node failed: {0}", Json.Quote(TiaNames.Message(e)));
            }
        }

        static string CpuNodeAddress(DeviceItem cpu)
        {
            return OptionalAttribute(CpuNode(cpu), "Address");
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

        // ---- upload ----

        // Upload replaces the project's station with the one the CPU at
        // address holds. It answers no upload configuration: one that
        // asks (a password, missing products) cancels it, naming it.
        // ReadFingerprint: see ITia. Going online from the project's CPU
        // asks nothing on the bench CPU; TIA's fingerprint service, which
        // the portal serves, asks TLS trust through its own callback (#789
        // stage 1b). The portal's service alone, with no project online,
        // never reached the bench CPU (stage 1), so the read goes online
        // first. Every step fails the read; offline always.
        public FingerprintRead ReadFingerprint(object project, string address, bool trustUnverifiedCertificate, OnlineLogin login)
        {
            return Tia(delegate
            {
                Project p = Of(project).Project;
                HwDevice d = StationOf(p);
                if (d == null) throw new TiaException("fingerprint read: the project has no CPU");
                DeviceItem cpu = CpuOf(d);
                string at0 = CpuNodeAddress(cpu);
                if (at0 != address) throw new TiaException("fingerprint read: the project's CPU is at " + at0 + ", not " + address);
                FingerprintDataProvider fp = portal.GetService<FingerprintDataProvider>();
                if (fp == null) throw new TiaException("fingerprint read: TIA serves no FingerprintDataProvider");
                OnlineProvider op = cpu.GetService<OnlineProvider>();
                ConfigurationAddress at = TargetInterface(PcInterface(op.Configuration)).Addresses.Create(address);
                ConfigurationPcInterface fpc = PcInterface(fp.Configuration);
                ConfigurationAddress fat = fpc.TargetInterfaces.Count > 0 ? TargetInterface(fpc).Addresses.Create(address) : fpc.Addresses.Create(address);
                var use = new LoginUse(login, "fingerprint read", address);
                var online = new OnlineAsks(op.Configuration, Detached, trustUnverifiedCertificate, address, use);
                var asks = OnlineAsks.ForCallback(Detached, trustUnverifiedCertificate, address, use);
                var r = new FingerprintRead();
                try
                {
                    OnlineState st = op.GoOnline(at);
                    bool failed = true;
                    try
                    {
                        if (st != OnlineState.Online)
                        {
                            if (online.Asked.Count > 0) throw new TiaException(TiaNames.OnlineRefused(address, online.Asked, "online state " + st));
                            if (online.LoggedIn) throw new TiaException(TiaNames.LoginFailed(address, "online state " + st));
                            throw new TiaException("fingerprint read: TIA could not go online to " + address + ": " + st);
                        }
                        FingerprintDataResult res = fp.GetFingerprintData(fat, asks.Handle);
                        if (res == null || res.FingerprintDataItems == null) throw new TiaException("fingerprint read: TIA returned no result");
                        foreach (FingerprintDataItem it in res.FingerprintDataItems)
                        {
                            r.Items.Add(new KeyValuePair<string, string>(it.FingerprintDataIdentifier ?? "", it.FingerprintDataValue ?? ""));
                        }
                        failed = false;
                    }
                    finally
                    {
                        // After a failure TIA may be gone: going offline
                        // then must not replace that failure.
                        if (failed) DownloadSteps.Quietly(delegate { op.GoOffline(); }, "fingerprint read: going offline after a failed read (if TIA is alive, the project may still be online)");
                        else op.GoOffline();
                    }
                }
                catch (TiaException)
                {
                    throw;
                }
                catch (Exception e)
                {
                    TiaException refused = asks.Explain(address, e) ?? online.Explain(address, e);
                    if (refused != null) throw refused;
                    throw;
                }
                finally
                {
                    online.Stop();
                    asks.Stop();
                    r.TrustedBlindly.AddRange(online.TrustedBlindly);
                    r.TrustedBlindly.AddRange(asks.TrustedBlindly);
                }
                // A question answered no is a refusal, even when TIA went on.
                online.Check(address, "fingerprint read");
                asks.Check(address, "fingerprint read");
                Log.Line("fingerprint read: {0} items from {1}; trusted blindly {2}", r.Items.Count, TiaNames.LogSafe(address), r.TrustedBlindly.Count);
                return r;
            });
        }

        public List<string> Upload(object project, string address, bool trustUnverifiedCertificate, OnlineLogin login)
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
                var online = new OnlineAsks(sup.Configuration, Detached, trustUnverifiedCertificate, address, new LoginUse(login, "upload", address));
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
