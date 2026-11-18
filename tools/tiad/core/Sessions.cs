// Sessions.cs: open projects (siemens-ide.md section 3.2). A session is an open
// project; each project has one session at a time (one writer), and an
// idle reaper closes the ones a caller forgot.

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;

namespace Tiad
{
    public sealed class Session
    {
        public string Id = "";
        public string Project = "";     // request form, relative to the work dir
        public string ProjectFull = "";
        public object Handle;           // ITia's project; null while opening
        public Station Station;         // the spec the project matches, or null
        // Address is the CPU the project belongs to, from the last spec
        // applied. It outlives Station: after an upload the project no
        // longer matches its spec, but compare and export still know the
        // CPU.
        public string Address = "";
        public bool Compiled;           // fully compiled since the last change
        public bool Closed;
        // Broken is set when closing the project failed: TIA may still
        // hold it. Every call but another close refuses.
        public bool Broken;
        // Guard is a directory the session owns ("projects/<name>/" for a
        // project tiad created): no other session may open anything in
        // it, and it may not be created while anything in it is open.
        public string Guard = "";
        public int InFlight;
        public DateTime LastUsed = DateTime.UtcNow;
    }

    public sealed class SessionTable
    {
        readonly object mu = new object();
        readonly Dictionary<string, Session> byId = new Dictionary<string, Session>(StringComparer.Ordinal);
        // cpus maps a CPU address to the session that claimed it with a
        // download. One CPU, one project: the claim holds until that
        // session is closed (a broken session keeps it).
        readonly Dictionary<string, Session> cpus = new Dictionary<string, Session>(StringComparer.Ordinal);
        int next;

        // ClaimCpu claims address for s, atomically, or refuses (busy)
        // when another session holds it.
        public void ClaimCpu(Session s, string address)
        {
            lock (mu)
            {
                Session holder;
                if (cpus.TryGetValue(address, out holder) && holder != s)
                {
                    throw new TiadException(409, Kinds.Busy, "CPU " + address + " belongs to session " + holder.Id + " (" + holder.Project + "): one CPU, one project; close that session first");
                }
                cpus[address] = s;
            }
        }

        // ReleaseCpus drops every claim s holds, once it is closed.
        public void ReleaseCpus(Session s)
        {
            lock (mu)
            {
                var drop = new List<string>();
                foreach (var kv in cpus)
                {
                    if (kv.Value == s) drop.Add(kv.Key);
                }
                foreach (var a in drop) cpus.Remove(a);
            }
        }

        // ProjectDirHolding returns the open session whose project directory
        // holds dir (request form), or null. A project at the work dir's
        // root claims no directory.
        public Session ProjectDirHolding(string dir)
        {
            string d = dir.TrimEnd('/') + "/";
            lock (mu)
            {
                foreach (var s in byId.Values)
                {
                    string own = s.Guard;
                    if (own == "")
                    {
                        int slash = s.Project.LastIndexOf('/');
                        if (slash < 0) continue;
                        own = s.Project.Substring(0, slash + 1);
                    }
                    if (d.StartsWith(own, ProjectCmp)) return s;
                }
            }
            return null;
        }

        // The work dir is on Windows, where projects/P/P.ap21 and
        // projects/p/p.ap21 are one file: one project, one writer.
        static readonly StringComparison ProjectCmp = StringComparison.OrdinalIgnoreCase;

        // Reserve claims a project for a new session before it is opened,
        // so two opens of one project cannot both get through. guard, when
        // set, is the directory the new session owns (see Session.Guard).
        public Session Reserve(string project, string projectFull, string guard)
        {
            lock (mu)
            {
                foreach (var s in byId.Values)
                {
                    if (string.Equals(s.Project, project, ProjectCmp))
                    {
                        throw new TiadException(409, Kinds.Busy, "project " + project + " is already open");
                    }
                    if (guard != "" && s.Project.StartsWith(guard, ProjectCmp))
                    {
                        throw new TiadException(409, Kinds.Busy, s.Project + " is open, and " + guard + " would be replaced");
                    }
                    if (s.Guard != "" && project.StartsWith(s.Guard, ProjectCmp))
                    {
                        throw new TiadException(409, Kinds.Busy, s.Guard + " belongs to the open project " + s.Project);
                    }
                }
                next++;
                var n = new Session
                {
                    Id = "s" + next.ToString(CultureInfo.InvariantCulture),
                    Project = project,
                    ProjectFull = projectFull,
                    Guard = guard,
                    InFlight = 1,
                };
                byId[n.Id] = n;
                return n;
            }
        }

        // Drop forgets a reserved session whose open failed.
        public void Drop(Session s)
        {
            lock (mu)
            {
                byId.Remove(s.Id);
            }
        }

        // Find finds an open session.
        public Session Find(string id)
        {
            lock (mu)
            {
                Session s;
                if (!byId.TryGetValue(id, out s) || s.Handle == null)
                {
                    throw TiadException.NotFound("no session {0}", id);
                }
                return s;
            }
        }

        // Begin finds an open session and marks a call in flight on it. A
        // session takes one call at a time: a second one, while the first
        // is in flight, is refused (409, busy), so the session's state
        // only ever changes in call order.
        public Session Begin(string id)
        {
            lock (mu)
            {
                Session s = Find(id);
                if (s.Broken)
                {
                    throw new TiadException(500, Kinds.OperationFailed, "session " + id + " is broken: closing its project failed; close it again, or restart the agent", null, true);
                }
                if (s.InFlight > 0)
                {
                    throw new TiadException(409, Kinds.Busy, "session " + id + " has a call in flight");
                }
                s.InFlight++;
                return s;
            }
        }

        // Restore puts back a session whose close failed, marked broken.
        public void Restore(Session s)
        {
            lock (mu)
            {
                s.Broken = true;
                byId[s.Id] = s;
            }
        }

        // End marks a call done; the idle clock starts now.
        public void End(Session s)
        {
            lock (mu)
            {
                s.InFlight--;
                s.LastUsed = DateTime.UtcNow;
            }
        }

        // Remove takes a session out of the table (it is being closed).
        public bool Remove(Session s)
        {
            lock (mu)
            {
                return byId.Remove(s.Id);
            }
        }

        // Idle removes and returns the sessions with no call in flight
        // that have been idle longer than limit.
        public List<Session> TakeIdle(TimeSpan limit)
        {
            var outp = new List<Session>();
            lock (mu)
            {
                var now = DateTime.UtcNow;
                foreach (var s in byId.Values)
                {
                    if (s.Handle != null && s.InFlight == 0 && now - s.LastUsed > limit) outp.Add(s);
                }
                foreach (var s in outp) byId.Remove(s.Id);
            }
            return outp;
        }

        // Open lists the open sessions, by ID in ordinal order.
        public List<Session> Open()
        {
            var outp = new List<Session>();
            lock (mu)
            {
                foreach (var s in byId.Values)
                {
                    if (s.Handle != null) outp.Add(s);
                }
            }
            outp.Sort(delegate(Session a, Session b) { return string.CompareOrdinal(a.Id, b.Id); });
            return outp;
        }

        public int Count
        {
            get { return Open().Count; }
        }
    }
}
