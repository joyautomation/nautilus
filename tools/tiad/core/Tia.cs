// Tia.cs: the seam between the agent and TIA Portal.
//
// Everything that needs TIA goes through ITia, and every ITia call runs on
// one dedicated STA thread, in order (TiaThread). Openness objects are
// believed to be bound to the thread that made them (verify on the image),
// and one thread also serializes opens, which TIA wants. The real ITia is
// tiad.Openness, the only assembly that references Siemens.Engineering;
// tiad-fake's is scripted.
//
// The agent applies every rule a caller can break before an ITia call:
// the sandbox, the station rules, project names, the target allowlist.
// ITia does what only TIA can.

using System;
using System.Collections.Generic;
using System.Threading;

namespace Tiad
{
    // TiaException is a failure TIA reported. It answers operation_failed
    // with TIA's own text (siemens-ide.md section 3.2).
    public sealed class TiaException : Exception
    {
        // InDoubt marks a failure inside a download TIA had begun: the CPU
        // may or may not be loaded, so the agent answers outcome_unknown.
        public readonly bool InDoubt;

        public TiaException(string message) : base(message)
        {
        }

        public TiaException(string message, Exception inner) : base(message, inner)
        {
        }

        public TiaException(string message, Exception inner, bool inDoubt) : base(message, inner)
        {
            InDoubt = inDoubt;
        }
    }

    public sealed class SourceFile
    {
        public string Path = "";     // request form, relative to the work dir
        public string FullPath = "";
    }

    public sealed class SourceResult
    {
        public string Path = "";
        public bool Ok;
        public List<string> Messages = new List<string>();

        public Obj ToJson()
        {
            var o = new Obj();
            o["path"] = Path;
            o["ok"] = Ok;
            if (Messages.Count > 0) o["messages"] = Messages.ToArray();
            return o;
        }
    }

    public sealed class Block
    {
        public string Name = "";
        public string Kind = ""; // OB, FB, FC, DB, UDT

        public Obj ToJson()
        {
            var o = new Obj();
            o["name"] = Name;
            o["kind"] = Kind;
            return o;
        }
    }

    // FingerprintRead is what TIA's fingerprint service returned for a
    // CPU, items in TIA's order and named as TIA names them, and the
    // certificate questions answered trusted, blindly, to read it. The
    // items are checksums and counters (TextLists, Hardware, DownloadId,
    // ...), not the CPU's identity.
    public sealed class FingerprintRead
    {
        public List<KeyValuePair<string, string>> Items = new List<KeyValuePair<string, string>>();
        public List<string> TrustedBlindly = new List<string>();
    }

    public sealed class SoftwareResult
    {
        public List<SourceResult> Results = new List<SourceResult>();
        public List<Block> Blocks = new List<Block>();
        // Obs is what the import did for each cyclic interrupt OB the
        // request asked for (CyclicObs.Plan), the agent's own.
        public List<OBStep> Obs = new List<OBStep>();

        public Obj ToJson()
        {
            var rs = new List<object>();
            foreach (var r in Results) rs.Add(r.ToJson());
            var bs = new List<object>();
            foreach (var b in Blocks) bs.Add(b.ToJson());
            var o = new Obj();
            o["results"] = rs;
            o["blocks"] = bs;
            if (Obs.Count > 0)
            {
                var os = new List<object>();
                foreach (var s in Obs) os.Add(s.ToJson());
                o["obs"] = os;
            }
            return o;
        }
    }

    public sealed class CompileMessage
    {
        public string Object = "", Severity = "", Text = "";
    }

    public sealed class CompileResult
    {
        public string Result = "success"; // success, warning, error
        public int Errors, Warnings;
        public List<CompileMessage> Messages = new List<CompileMessage>();

        public Obj ToJson()
        {
            var ms = new List<object>();
            foreach (var m in Messages)
            {
                var x = new Obj();
                x["object"] = m.Object;
                x["severity"] = m.Severity;
                x["text"] = m.Text;
                ms.Add(x);
            }
            var o = new Obj();
            o["result"] = Result;
            o["errors"] = Errors;
            o["warnings"] = Warnings;
            o["messages"] = ms;
            return o;
        }
    }

    // ITia is everything the agent asks of TIA. A project is an opaque
    // handle the implementation made. Every call runs on the TIA thread.
    public interface ITia
    {
        // CanDryRun reports whether a dry run is safe: whether this side
        // can always cancel a download before anything loads. The agent
        // refuses a dry run, before calling Download, when it cannot.
        bool CanDryRun { get; }

        // Gone reports whether the TIA this side drives has died (its
        // process exited, its objects disposed), taking every project open
        // on it. It is read from any thread, also while a call runs, so it
        // never touches TIA's objects. Once true, true for good: an
        // instance never comes back. After a death only a restart of the
        // agent brings TIA back; after Retire the agent makes a new one.
        bool Gone { get; }

        // Retire ends this TIA instance on purpose, with no project open
        // on it: the agent retires an instance that trusted a CPU's
        // certificate blindly once no session is left (#789 D1). It
        // returns only once TIA's process has exited, so nothing the
        // instance trusted outlives it; when it cannot tell, it throws,
        // and the agent then treats TIA as gone.
        void Retire();

        // GsdInstalled reports whether a GSDML file of that name is in the
        // hardware catalogue.
        bool GsdInstalled(string fileName);

        // InstallGsd installs a GSDML file into the catalogue and reports
        // whether it was already there.
        bool InstallGsd(string fullPath);

        // CreateProject makes an empty project at fullPath (dir/name.ap21).
        object CreateProject(string fullPath, string name);

        // OpenProject opens a project or project archive.
        object OpenProject(string fullPath);

        void CloseProject(object project);

        // ApplyStation makes the project's CPU match next, as the agent
        // decided (CpuReuse.Decide): Create removes every device and makes
        // the CPU anew; Reuse keeps the project's CPU and the devices
        // besides it, as found. Either way it then applies next's CPU
        // settings, cpu.configurationProtection (ProtectionSteps.Run) and
        // cpu.accessControl (AccessSteps.Run), then webServer and its
        // users (WebSteps.Run, after access control). notes gets what the caller
        // should know besides the changes; made collects what it made in
        // the project's users and roles, as soon as it is made.
        void ApplyStation(object project, Station next, string decision, Secrets secrets, List<string> notes, AccessMade made);

        // ReadPolicy is the project's password policy for local users.
        PasswordPolicy ReadPolicy(object project);

        // PrepareAccess runs AccessSteps.PrepareAll on the project's users,
        // before any write of an apply.
        void PrepareAccess(object project, Station next, Secrets secrets, bool recordOnly, AccessWrites w, List<string> notes);

        // RemoveAccess is AccessSteps.Remove: it removes from the project's
        // users and roles what made names (AccessSteps' user and role, the
        // web users and roles), and nothing else; it returns what it
        // removed ("user \"nautilus\"", "role \"...\"").
        List<string> RemoveAccess(object project, AccessMade made);

        // UserComment is the comment of the project's local user, where
        // tiad keeps its record (AccessRecords), or null when the project
        // holds no such user. It reads only.
        string UserComment(object project, string user);

        // CannotApply is why this side cannot apply next at all yet (a
        // part of the spec it cannot configure), or null. The agent asks
        // before any write, so such a refusal leaves the session as it was.
        string CannotApply(Station next);

        // ImportSoftware generates blocks from sources into group; with
        // prune, it deletes blocks in group that no source produced. TIA
        // replaces a same-named block outside group too: the agent checks
        // first (Ownership), with Blocks.
        SoftwareResult ImportSoftware(object project, string group, List<SourceFile> sources, List<SourceFile> tagTables, bool prune);

        // Blocks is every program block the project holds, in every block
        // group, each with its group (ProjectBlock.Group). It reads only.
        List<ProjectBlock> Blocks(object project);

        // ImportBlock imports one block's SimaticML (CyclicObs.Xml) into
        // group, making the group when it is not there.
        void ImportBlock(object project, string group, string xml);

        // DeleteBlock deletes the block named name in group or one of its
        // user groups; it never touches a block outside group.
        void DeleteBlock(object project, string group, string name);

        CompileResult Compile(object project, bool hardware, bool software);

        // Download loads the project into the CPU at address, never at
        // any address of the project's own. TIA asks its pre-download
        // checks one at a time and lists them nowhere (#789 S2b), so
        // Download calls answer ONCE PER CHECK, in TIA's order, before
        // that check's answer is written: answer returns null to cancel
        // the download there, or the Answer to apply exactly as given. A
        // dry run is a download whose answer always returns null: it
        // cancels at the first check, and cannot cancel one TIA runs
        // without asking anything. startAfter is whether the CPU starts
        // after loading (null: it stays as it is). secrets carries the
        // protection password a password question needs.
        //
        // login, in Upload and ReadFingerprint, is what the call answers
        // when TIA asks credentials going online (null: none). Each
        // operation makes its own LoginUse of it, which decides every
        // answer (OnlineLogin, LoginUse). Download and Compare take the
        // LoginUse the agent made (use, never null): the agent reads from
        // it whether the download logged on before TIA asks its
        // user-management check (UserManagementRule.Gate.At), and whether
        // a compare logged on (InstanceLogons).
        DownloadOutcome Download(object project, string address, bool hardware, bool software,
            Secrets secrets, bool? startAfter, Func<Question, Answer> answer, LoginUse use);

        // Compare compares the project's PLC software with the CPU at
        // address and returns TIA's compare tree as TIA reports it (null
        // when TIA reports none), with every program block the project
        // holds, in every block group (null when it read none). It decides
        // nothing: the agent runs TiaNames.CompareEqual on both, so an
        // empty tree, one with an element skipped or differing, or one
        // that leaves out a block the project holds, never passes as
        // equal (#789).
        OnlineCompare Compare(object project, string address, LoginUse use);

        // Export writes what o asks for under dir and returns the full
        // paths it wrote.
        List<string> Export(object project, string dir, ExportOptions o);

        // Upload loads the CPU at address into the project. With
        // trustUnverifiedCertificate, the request's explicit opt-in, a TLS
        // certificate question is answered trusted, blindly; Upload returns
        // the questions it so answered (empty when none was asked).
        List<string> Upload(object project, string address, bool trustUnverifiedCertificate, OnlineLogin login);

        // ReadFingerprint goes online, read-only, from the project's CPU
        // to address, reads what TIA's fingerprint service reports for it,
        // and goes offline, always. It writes nothing and never saves.
        // With trustUnverifiedCertificate, the request's explicit opt-in, a
        // TLS certificate question is answered trusted, blindly; anything
        // else TIA asks fails the read. It fails, never returns part of a
        // read, when the CPU is not reached online or any step fails. It
        // refuses before going online when address is not the project
        // CPU's address.
        FingerprintRead ReadFingerprint(object project, string address, bool trustUnverifiedCertificate, OnlineLogin login);

        // ProjectCpu is the identity of the project's CPU as the project
        // holds it: after an upload, what the CPU reported. Serial and Mac
        // are null when the project does not hold them.
        DeviceIdentity ProjectCpu(object project);

        // ReadCpu is the project's CPU as the project holds it (after an
        // upload, what the CPU reported): its name, order, firmware and
        // configuration protection, with the project's other devices; or
        // null when the project holds no CPU.
        ProjectCpu ReadCpu(object project);

        // ProjectCpuAddress is the address the project's CPU has on its
        // first network interface with a node (the one a fingerprint read
        // checks), or "" when the project holds no CPU; it fails when the
        // CPU has no network node. A deploy's verify
        // checks it against the deploy record's address before it goes
        // online (#789 D1b).
        string ProjectCpuAddress(object project);

        // Identify reads the identity of the CPU at address online, or
        // returns null when this TIA side cannot: tiad.Openness, until an
        // online read is proven (section 3.8). The agent then refuses an
        // online call to a pinned address, since the pin cannot be checked.
        DeviceIdentity Identify(object project, string address);

        // Save saves the project, or writes a project archive, and returns
        // its full path.
        string Save(object project, bool archive);

        // Archive saves the project and writes a project archive of it
        // into dir, an empty directory of the deploy record's outside the
        // work dir, and returns the archive's full path: the deploy record
        // (section 7, D7) archives the project just before TIA's download.
        string Archive(object project, string dir);
    }

    // TiaThread runs work items one at a time on one STA thread.
    public sealed class TiaThread
    {
        readonly Queue<Action> queue = new Queue<Action>();
        readonly object mu = new object();
        readonly Thread thread;

        public TiaThread()
        {
            thread = new Thread(Loop);
            thread.Name = "tia";
            thread.IsBackground = true;
            thread.SetApartmentState(ApartmentState.STA);
            thread.Start();
        }

        void Loop()
        {
            while (true)
            {
                Action a;
                lock (mu)
                {
                    while (queue.Count == 0) Monitor.Wait(mu);
                    a = queue.Dequeue();
                }
                // A work item never throws (Run catches), so the thread
                // outlives every failure.
                a();
            }
        }

        // Run runs f on the TIA thread and waits for it. An exception f
        // throws comes back here, on the caller's thread.
        public T Run<T>(Func<T> f)
        {
            T result = default(T);
            Exception err = null;
            using (var done = new ManualResetEvent(false))
            {
                Action a = delegate
                {
                    try
                    {
                        result = f();
                    }
                    catch (Exception e)
                    {
                        err = e;
                    }
                    done.Set();
                };
                lock (mu)
                {
                    queue.Enqueue(a);
                    Monitor.Pulse(mu);
                }
                done.WaitOne();
            }
            if (err is TiadException) throw (TiadException)err;
            if (err is TiaException && ((TiaException)err).InDoubt)
            {
                throw new TiadException(500, Kinds.OutcomeUnknown, "TIA: " + err.Message + "; the CPU may or may not be loaded: compare before anything else", null, false);
            }
            if (err is TiaException) throw new TiadException(500, Kinds.OperationFailed, "TIA: " + err.Message);
            if (err != null)
            {
                Log.Line("TIA call failed: {0}", err);
                throw new TiadException(500, Kinds.OperationFailed, "internal error in the agent's TIA call (" + err.GetType().Name + "); see its log", null, false);
            }
            return result;
        }

        public void Run(Action f)
        {
            Run<object>(delegate
            {
                f();
                return null;
            });
        }
    }
}
