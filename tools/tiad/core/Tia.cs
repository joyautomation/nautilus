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
        public TiaException(string message) : base(message)
        {
        }

        public TiaException(string message, Exception inner) : base(message, inner)
        {
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

    public sealed class SoftwareResult
    {
        public List<SourceResult> Results = new List<SourceResult>();
        public List<Block> Blocks = new List<Block>();

        public Obj ToJson()
        {
            var rs = new List<object>();
            foreach (var r in Results) rs.Add(r.ToJson());
            var bs = new List<object>();
            foreach (var b in Blocks) bs.Add(b.ToJson());
            var o = new Obj();
            o["results"] = rs;
            o["blocks"] = bs;
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

        // ApplyStation makes the project's hardware match next, removing
        // what next lacks, and says what changed. previous is the station
        // this session last applied, or null.
        List<string> ApplyStation(object project, Station previous, Station next, Secrets secrets);

        // ImportSoftware generates blocks from sources into group; with
        // prune, it deletes blocks in group that no source produced.
        SoftwareResult ImportSoftware(object project, string group, List<SourceFile> sources, List<SourceFile> tagTables, bool prune);

        CompileResult Compile(object project, bool hardware, bool software);
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
