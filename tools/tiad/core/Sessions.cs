// Sessions.cs: open projects (siemens-ide.md section 3.2). A session is an open
// project; TIA V21 opens one project per instance, so the agent holds one
// session at a time (#616), and an idle reaper closes the one a caller
// forgot.

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
        // Unclean is set while an apply or an upload runs and stays set
        // when it fails part-way: the project then matches no spec, and
        // a later apply or save refuses (CpuReuse.Unclean).
        public bool Unclean;
        // Kept are the devices the last apply kept as found besides the
        // CPU's station: a download lists them and journals them.
        public List<KeptDevice> Kept = new List<KeptDevice>();
        // Made is what applies on this session made in the project's users
        // and roles; an upload on it removes exactly that first.
        public AccessMade Made = new AccessMade();
        // AccessControlSet is set once any apply on this session asked for
        // access control tiad sets (fullAccessUser), failed or not: a
        // later keep then keeps what tiad set, not what the project had,
        // so a download never keeps the CPU's online users for it (#1159
        // review).
        public bool AccessControlSet;
        // AccessRecordOnly is set once an apply on this session asked for
        // fullAccessRecordOnly, failed or not: its record shows what tiad
        // was told, so UserManagementRule refuses a reset to tiad's user.
        public bool AccessRecordOnly;
        // UploadedFrom is the address this session last uploaded from
        // successfully (its CPU's identity checked), "" for none: the
        // project's CPU settings are then that CPU's, a later apply that
        // keeps them included (an apply never replaces an uploaded CPU,
        // CpuReuse.Decide). An upload clears it when it starts.
        // UserManagementRule's upload route rests on it.
        public string UploadedFrom = "";
        // UsersUnclean is set when an apply's password step left the
        // project's users part-way (AccessWrites.Dirty): an upload, which
        // replaces devices but not users, refuses too.
        public bool UsersUnclean;
        public bool Closed;
        // Broken is set when closing the project failed: TIA may still
        // hold it. Every call but another close refuses.
        public bool Broken;
        // Guard is a directory the session owns ("projects/<name>/" for a
        // project tiad created): an export never writes into it.
        public string Guard = "";
        // Staging is a directory outside the work dir that only this
        // session uses (a deploy verify's copy of the archive and the
        // project TIA retrieved from it): removed once its close ends.
        public string Staging = "";
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
        // closing counts the sessions taken out of the table to be closed
        // (Remove, TakeIdle) whose close has not ended yet (Closed,
        // Restore): their projects are still open on TIA.
        int closing;

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

        public const string OneProject = "TIA opens one project at a time";

        // Reserve claims a project for a new session before it is opened.
        // TIA V21 opens one project per instance (#616, #789) and refuses
        // a second only after a create has replaced its directory, so the
        // agent holds one session at a time: a second is refused here,
        // before anything is touched, naming the session to close; so is
        // one while a closed session's project may still be open in TIA
        // (closing). guard, when set, is the directory the new session
        // owns (see Session.Guard).
        public Session Reserve(string project, string projectFull, string guard)
        {
            lock (mu)
            {
                foreach (var open in byId.Values)
                {
                    var data = new Obj();
                    data["openSession"] = open.Id;
                    data["openProject"] = open.Project;
                    string why;
                    // A session still opening has no id a close takes yet.
                    if (open.Handle == null) why = ", and session " + open.Id + " is still opening " + open.Project + ": try again once it is open, then close it first";
                    else if (open.Broken) why = ", and session " + open.Id + " has " + open.Project + " open, and closing it failed: close it again (DELETE /v1/sessions/" + open.Id + "), or restart the agent";
                    else why = ", and session " + open.Id + " has " + open.Project + " open: close it first (DELETE /v1/sessions/" + open.Id + ")";
                    throw new TiadException(409, Kinds.Busy, OneProject + why + "; nothing was touched", data, false);
                }
                if (closing > 0)
                {
                    throw new TiadException(409, Kinds.Busy, OneProject + ", and a session is still closing: try again once it is closed; nothing was touched");
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
                closing--;
            }
        }

        // Closed records that the close of a session taken out of the
        // table (Remove, TakeIdle) ended: its project is closed or gone.
        public void Closed(Session s)
        {
            lock (mu)
            {
                closing--;
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

        // Remove takes a session out of the table (it is being closed):
        // Closed or Restore must follow when it returns true.
        public bool Remove(Session s)
        {
            lock (mu)
            {
                if (!byId.Remove(s.Id)) return false;
                closing++;
                return true;
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
                closing += outp.Count;
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

        // Any reports whether any session is left: one in the table, one
        // still opening (reserved, no handle yet), or one whose close has
        // not ended (its project is still open on TIA).
        public bool Any
        {
            get
            {
                lock (mu)
                {
                    return byId.Count > 0 || closing > 0;
                }
            }
        }
    }
}
