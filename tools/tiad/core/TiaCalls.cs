// TiaCalls.cs: the calls that need TIA (siemens-ide.md section 3.4): GSDML
// install, projects and sessions, station, software, compile. Each one
// checks every gate first, applies the agent's own rules, and only then
// asks ITia, on the TIA thread.

using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Xml;

namespace Tiad
{
    public sealed partial class Agent
    {
        readonly TiaThread tiaThread = new TiaThread();
        readonly SessionTable sessions = new SessionTable();
        readonly Func<ITia> makeTia;
        ITia tia; // made on the TIA thread by the first call that needs it

        void RegisterTia(Router r)
        {
            r.Add("POST", "/v1/gsd/install", InstallGsd);
            r.Add("POST", "/v1/projects", CreateProject);
            r.Add("POST", "/v1/sessions", OpenSession);
            r.Add("GET", "/v1/sessions", ListSessions);
            r.Add("DELETE", "/v1/sessions/{id}", CloseSession);
            r.Add("PUT", "/v1/sessions/{id}/station", ApplyStation);
            r.Add("POST", "/v1/sessions/{id}/software", Software);
            r.Add("POST", "/v1/sessions/{id}/compile", Compile);
        }

        // StartReaper closes sessions idle longer than idle, checking every
        // period. The close runs on the TIA thread like every TIA call.
        void StartReaper(TimeSpan idle)
        {
            TimeSpan period = TimeSpan.FromMilliseconds(Math.Max(50, Math.Min(30000, idle.TotalMilliseconds / 4)));
            var t = new Thread(delegate()
            {
                while (true)
                {
                    Thread.Sleep(period);
                    foreach (var s in sessions.TakeIdle(idle))
                    {
                        try
                        {
                            Close(s);
                            Log.Line("closed idle session {0} ({1})", s.Id, s.Project);
                        }
                        catch (Exception e)
                        {
                            sessions.Restore(s);
                            Log.Line("closing idle session {0}: {1}", s.Id, e.Message);
                        }
                    }
                }
            });
            t.IsBackground = true;
            t.Name = "reaper";
            t.Start();
        }

        // NeedTia refuses a TIA call while any gate fails (412, gate).
        void NeedTia()
        {
            foreach (var g in Gates())
            {
                if (!g.Ok) throw new TiadException(412, Kinds.Gate, "gate " + g.Name + ": " + g.Detail);
            }
        }

        // OnTia runs f on the TIA thread with ITia, made on first use.
        T OnTia<T>(Func<ITia, T> f)
        {
            return tiaThread.Run(delegate
            {
                if (tia == null) tia = makeTia();
                return f(tia);
            });
        }

        // OnSession runs f on the TIA thread for an open session. A session
        // closed while the call waited is gone (404, fatal).
        T OnSession<T>(Session s, Func<ITia, T> f)
        {
            return OnTia(delegate(ITia t)
            {
                if (s.Closed) throw new TiadException(404, Kinds.NotFound, "session " + s.Id + " was closed", null, true);
                return f(t);
            });
        }

        void Close(Session s)
        {
            OnTia<object>(delegate(ITia t)
            {
                if (!s.Closed)
                {
                    t.CloseProject(s.Handle);
                    s.Closed = true;
                }
                return null;
            });
        }

        // WithSession runs a call on the session the route names.
        object WithSession(Call c, Func<Session, object> f)
        {
            Session s = sessions.Begin(c.Var("id"));
            try
            {
                return f(s);
            }
            finally
            {
                sessions.End(s);
            }
        }

        // File resolves a body path that must name an existing file.
        string ExistingFile(string requestPath)
        {
            string full = work.Resolve(requestPath);
            if (!File.Exists(full)) throw TiadException.NotFound("no file {0}", Paths.CleanPath(requestPath));
            return full;
        }

        object InstallGsd(Call c)
        {
            NeedTia();
            var r = c.Json();
            string path = r.Str("path");
            r.Done();
            string full = ExistingFile(path);
            string name = Path.GetFileName(full);
            if (!name.StartsWith("GSDML-", StringComparison.OrdinalIgnoreCase) || !name.EndsWith(".xml", StringComparison.OrdinalIgnoreCase))
            {
                throw TiadException.BadRequest("{0} is not named like a GSDML file (GSDML-*.xml)", name);
            }
            CheckGsdml(full, name);
            bool already = OnTia(delegate(ITia t) { return t.InstallGsd(full); });
            var o = new Obj();
            o["path"] = work.Rel(full);
            o["alreadyInstalled"] = already;
            return o;
        }

        // CheckGsdml reads a GSDML file before TIA's importer ever parses
        // it: it must be well-formed XML (which has a root element) without
        // a DOCTYPE, so no external entity (XXE) or entity expansion
        // reaches TIA.
        static void CheckGsdml(string full, string name)
        {
            var settings = new XmlReaderSettings
            {
                DtdProcessing = DtdProcessing.Prohibit,
                XmlResolver = null,
                IgnoreComments = true,
            };
            try
            {
                using (var f = new FileStream(full, FileMode.Open, FileAccess.Read, FileShare.Read))
                using (var r = XmlReader.Create(f, settings))
                {
                    while (r.Read())
                    {
                    }
                }
            }
            catch (XmlException e)
            {
                throw TiadException.BadRequest("{0} is not well-formed XML without a DOCTYPE (line {1}): tiad never hands TIA a DOCTYPE", name, e.LineNumber);
            }
        }

        // CheckProjectName is tiad.CheckProjectName: one path segment that
        // the sandbox rule accepts as it is.
        static void CheckProjectName(string name)
        {
            string c;
            try
            {
                c = Paths.CleanPath(name);
            }
            catch (TiadException e)
            {
                throw TiadException.BadRequest("project name: {0}", e.Message);
            }
            if (c != name || name.IndexOf('/') >= 0)
            {
                throw TiadException.BadRequest("project name {0} must be a single path segment", Json.Quote(name));
            }
        }

        // CheckStation applies the agent-side station rules: the spec's
        // own, an installed GSDML for every device, a password for every
        // web user.
        void CheckStation(Station st, Secrets sec)
        {
            string err = st.Validate();
            if (err != null) throw TiadException.BadRequest("{0}", err);
            if (st.Profinet != null)
            {
                foreach (var d in st.Profinet.Devices)
                {
                    var dev = d;
                    if (!OnTia(delegate(ITia t) { return t.GsdInstalled(dev.Gsdml); }))
                    {
                        throw TiadException.BadRequest("device {0}: GSDML {1} is not installed (POST /v1/gsd/install first)", d.Name, d.Gsdml);
                    }
                }
            }
            if (st.WebServer != null && st.WebServer.Enabled)
            {
                foreach (var u in st.WebServer.Users)
                {
                    string pw;
                    if (!sec.WebUsers.TryGetValue(u.Name, out pw) || pw == "")
                    {
                        throw TiadException.BadRequest("web user {0} has no password in secrets.webUsers", u.Name);
                    }
                }
            }
        }

        static Obj SessionJson(Session s)
        {
            var o = new Obj();
            o["session"] = s.Id;
            o["project"] = s.Project;
            return o;
        }

        // CreateProject builds a fresh project from a station spec. A
        // project of that name that is not open is replaced: the work dir
        // is scratch, and every deploy rebuilds from what naut sends (D1).
        object CreateProject(Call c)
        {
            NeedTia();
            var r = c.Json();
            string name = r.Str("name");
            Station st = Station.Read(r.Child("station"));
            Secrets sec = Secrets.Read(r.Child("secrets"));
            r.Done();
            CheckProjectName(name);
            CheckStation(st, sec);
            string project = "projects/" + name + "/" + name + ".ap21";
            string full = work.Resolve(project);
            // The session owns projects/<name>/: it is replaced only while
            // nothing in it is open, and nothing in it opens while it is.
            Session s = sessions.Reserve(project, full, "projects/" + name + "/");
            try
            {
                s.Handle = OnTia(delegate(ITia t)
                {
                    string dir = Path.GetDirectoryName(full);
                    if (Directory.Exists(dir)) Directory.Delete(dir, true);
                    Directory.CreateDirectory(dir);
                    object h = t.CreateProject(full, name);
                    try
                    {
                        t.ApplyStation(h, null, st, sec);
                    }
                    catch
                    {
                        t.CloseProject(h);
                        throw;
                    }
                    s.Station = st;
                    return h;
                });
            }
            catch
            {
                sessions.Drop(s);
                throw;
            }
            sessions.End(s);
            return SessionJson(s);
        }

        object OpenSession(Call c)
        {
            NeedTia();
            var r = c.Json();
            string path = r.Str("path");
            r.Done();
            string full = work.Resolve(path);
            if (!File.Exists(full)) throw TiadException.NotFound("no project {0}", Paths.CleanPath(path));
            Session s = sessions.Reserve(work.Rel(full), full, "");
            try
            {
                s.Handle = OnTia(delegate(ITia t) { return t.OpenProject(full); });
            }
            catch
            {
                sessions.Drop(s);
                throw;
            }
            sessions.End(s);
            return SessionJson(s);
        }

        object ListSessions(Call c)
        {
            var list = new List<object>();
            var now = DateTime.UtcNow;
            foreach (var s in sessions.Open())
            {
                var o = SessionJson(s);
                o["idleSeconds"] = s.InFlight > 0 ? 0 : (int)(now - s.LastUsed).TotalSeconds;
                list.Add(o);
            }
            var outp = new Obj();
            outp["sessions"] = list;
            return outp;
        }

        // CloseSession closes even a session with a call in flight: the
        // close waits on the TIA thread behind that call, and anything
        // queued after it finds the session gone. A close that fails keeps
        // the session, broken, so it can be closed again.
        object CloseSession(Call c)
        {
            Session s = sessions.Find(c.Var("id"));
            if (!sessions.Remove(s)) return new Obj();
            try
            {
                Close(s);
            }
            catch
            {
                sessions.Restore(s);
                throw;
            }
            return new Obj();
        }

        object ApplyStation(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                Station st = Station.Read(r.Child("station"));
                Secrets sec = Secrets.Read(r.Child("secrets"));
                r.Done();
                CheckStation(st, sec);
                // The session's state changes only on the TIA thread, in
                // call order. Until TIA has applied the spec, the project
                // is neither compiled nor described by any spec, so a
                // failure part-way leaves nothing downloadable.
                List<string> changes = OnSession(s, delegate(ITia t)
                {
                    bool was = s.Compiled;
                    Station prev = s.Station;
                    s.Compiled = false;
                    s.Station = null;
                    List<string> ch = t.ApplyStation(s.Handle, prev, st, sec);
                    s.Station = st;
                    if (ch.Count == 0) s.Compiled = was;
                    return ch;
                });
                var o = new Obj();
                o["changes"] = changes.ToArray();
                return o;
            });
        }

        object Software(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                string group = r.Str("group");
                List<string> srcs = r.Strs("sources");
                List<string> tables = r.Strs("tagTables");
                bool prune = r.Bool("prune");
                r.Done();
                if (group == "") throw TiadException.BadRequest("group is empty");
                var sources = new List<SourceFile>();
                foreach (string p in srcs) sources.Add(new SourceFile { Path = Paths.CleanPath(p), FullPath = ExistingFile(p) });
                var tagTables = new List<SourceFile>();
                foreach (string p in tables) tagTables.Add(new SourceFile { Path = Paths.CleanPath(p), FullPath = ExistingFile(p) });
                SoftwareResult res = OnSession(s, delegate(ITia t)
                {
                    s.Compiled = false;
                    return t.ImportSoftware(s.Handle, group, sources, tagTables, prune);
                });
                return res.ToJson();
            });
        }

        object Compile(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                bool hw = r.Bool("hardware");
                bool sw = r.Bool("software");
                r.Done();
                if (!hw && !sw) throw TiadException.BadRequest("neither hardware nor software selected");
                CompileResult res = OnSession(s, delegate(ITia t)
                {
                    CompileResult cr;
                    try
                    {
                        cr = t.Compile(s.Handle, hw, sw);
                    }
                    catch
                    {
                        s.Compiled = false;
                        throw;
                    }
                    // Only a full compile without errors makes the
                    // project downloadable.
                    if (cr.Errors > 0) s.Compiled = false;
                    else if (hw && sw) s.Compiled = true;
                    return cr;
                });
                if (res.Errors > 0)
                {
                    throw new TiadException(422, Kinds.CompileFailed, res.Errors + " errors", res.ToJson(), false);
                }
                return res.ToJson();
            });
        }
    }
}
