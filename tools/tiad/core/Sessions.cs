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
        public Station Station;         // the station last applied, or null
        public bool Compiled;           // fully compiled since the last change
        public bool Closed;
        public int InFlight;
        public DateTime LastUsed = DateTime.UtcNow;
    }

    public sealed class SessionTable
    {
        readonly object mu = new object();
        readonly Dictionary<string, Session> byId = new Dictionary<string, Session>(StringComparer.Ordinal);
        int next;

        // The work dir is on Windows, where projects/P/P.ap21 and
        // projects/p/p.ap21 are one file: one project, one writer.
        static readonly StringComparison ProjectCmp = StringComparison.OrdinalIgnoreCase;

        // Reserve claims a project for a new session before it is opened,
        // so two opens of one project cannot both get through.
        public Session Reserve(string project, string projectFull)
        {
            lock (mu)
            {
                foreach (var s in byId.Values)
                {
                    if (string.Equals(s.Project, project, ProjectCmp))
                    {
                        throw new TiadException(409, Kinds.Busy, "project " + project + " is already open");
                    }
                }
                next++;
                var n = new Session
                {
                    Id = "s" + next.ToString(CultureInfo.InvariantCulture),
                    Project = project,
                    ProjectFull = projectFull,
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

        // Begin finds an open session and marks a call in flight on it.
        public Session Begin(string id)
        {
            lock (mu)
            {
                Session s;
                if (!byId.TryGetValue(id, out s) || s.Handle == null)
                {
                    throw TiadException.NotFound("no session {0}", id);
                }
                s.InFlight++;
                return s;
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
