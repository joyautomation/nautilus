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
        volatile ITia tia; // made on the TIA thread by the first call that needs it
        // tiaGone is why TIA is gone (ITia.Gone), null while it is not.
        // Once set it stays set: every call that needs TIA is refused,
        // naming the fix, until the agent restarts.
        volatile string tiaGone;
        readonly object goneMu = new object();

        // retired is the blind trust of the instance the agent retired
        // last (RetireIfTrusted), until the next instance is made: the
        // record is cleared only then, never before a new instance exists.
        // Written and read on the TIA thread.
        BlindTrust retired;

        // GoneFix is what to do once TIA died.
        const string GoneFix = "every project open on it is gone, and the agent starts TIA again only when it restarts. Restart the agent, then compare in a fresh session (the read-only after-check) before anything else";

        // TiaGoneNow reports why TIA is gone, or null while it is not,
        // recording it the first time. A Gone that throws counts as gone.
        string TiaGoneNow()
        {
            string why = tiaGone;
            if (why != null) return why;
            ITia t = tia;
            if (t == null) return null;
            string how = "";
            bool gone;
            try
            {
                gone = t.Gone;
            }
            catch (Exception e)
            {
                gone = true;
                how = " (reading whether it is alive failed: " + e.GetType().Name + ")";
            }
            if (!gone) return null;
            lock (goneMu)
            {
                if (tiaGone == null)
                {
                    tiaGone = "TIA Portal died" + how + ", noticed " + DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ");
                    Log.Line("{0}: {1}", tiaGone, GoneFix);
                }
                return tiaGone;
            }
        }

        // TiaGate is the host's tia gate, failed once TIA is gone.
        Gate TiaGate(Gate host)
        {
            string why = TiaGoneNow();
            if (why == null) return host;
            return new Gate { Name = "tia", State = Gate.StateFailed, Detail = why + ": " + GoneFix, Remedy = "restart the agent" };
        }

        // GoneRefusal refuses a call once TIA is gone, the way a failed
        // gate does.
        static TiadException GoneRefusal(string why)
        {
            return new TiadException(412, Kinds.Gate, "gate tia: " + why + ": " + GoneFix);
        }

        // Died is the answer for the call TIA died in. It ends the
        // session: in doubt (outcome_unknown) when TIA's side says its
        // download had begun, else a plain failure; both name the fix.
        static TiadException Died(Exception e, string why)
        {
            string msg = "TIA: " + (e is TiaException ? e.Message : TiaNames.Message(e)) + "; " + why;
            var te = e as TiaException;
            if (te != null && te.InDoubt)
            {
                return new TiadException(500, Kinds.OutcomeUnknown, msg + ", so the CPU may or may not be loaded, and " + GoneFix, null, true);
            }
            return new TiadException(500, Kinds.OperationFailed, msg + ": " + GoneFix, null, true);
        }

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
                            sessions.ReleaseCpus(s);
                            sessions.Closed(s);
                            Log.Line("closed idle session {0} ({1})", s.Id, s.Project);
                        }
                        catch (Exception e)
                        {
                            sessions.Restore(s);
                            Log.Line("closing idle session {0}: {1}", s.Id, e.Message);
                            continue;
                        }
                        RetireIfTrusted();
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
                if (g.Blocks) throw new TiadException(412, Kinds.Gate, "gate " + g.Name + ": " + g.Detail);
            }
        }

        // OnTia runs f on the TIA thread with ITia, made on first use. Once
        // TIA is gone it refuses, also a call that was queued behind the
        // one TIA died in; a call TIA dies in answers Died.
        T OnTia<T>(Func<ITia, T> f)
        {
            return tiaThread.Run(delegate
            {
                string gone = TiaGoneNow();
                if (gone != null) throw GoneRefusal(gone);
                if (tia == null)
                {
                    try
                    {
                        tia = makeTia();
                    }
                    catch (Exception e)
                    {
                        host.TiaStarted(e);
                        throw;
                    }
                    host.TiaStarted(null);
                    // A new instance after a retire trusts nothing: the
                    // retired one's record goes only now.
                    if (retired != null)
                    {
                        Log.Line("audit blind-trust cleared: a new TIA instance started; the instance that trusted blindly ({0}) was retired", BlindTrust.Audit(retired));
                        if (blindTrust == retired) blindTrust = null;
                        retired = null;
                    }
                }
                try
                {
                    return f(tia);
                }
                catch (Exception e)
                {
                    string why = TiaGoneNow();
                    if (why != null) throw Died(e, why);
                    if (e is TiaException) host.TiaFailed(e);
                    throw;
                }
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

        // Close closes a session's project. Once TIA is gone there is no
        // project left to close: the session is dropped, never kept broken.
        // No retire comes between (RetireIfTrusted waits for every close
        // to end), so the project is on the instance it was opened on.
        void Close(Session s)
        {
            bool done = tiaThread.Run<bool>(delegate
            {
                if (s.Closed) return true;
                if (TiaGoneNow() == null) return false;
                s.Closed = true;
                Log.Line("session {0}: TIA is gone, so its project is dropped without closing it", s.Id);
                return true;
            });
            if (done) return;
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

        // RetireIfTrusted retires the TIA instance once it trusted a CPU's
        // certificate blindly and no session is left on it (#789 D1): the
        // next call that needs TIA starts a new instance, which trusts
        // nothing, so a deploy's own download never rides on an earlier
        // read's trust. While any session is open, or still opening, the
        // instance stays and the download gate (BlindTrustRule) holds. A
        // retire that cannot show TIA's process exited counts as TIA gone:
        // nothing runs on an instance that may still hold the trust.
        void RetireIfTrusted()
        {
            tiaThread.Run<object>(delegate
            {
                if (blindTrust == null || tia == null || sessions.Any) return null;
                if (TiaGoneNow() != null) return null;
                ITia old = tia;
                BlindTrust bt = blindTrust;
                // Before Retire: a gate that reads the old instance's Gone
                // meanwhile must not take the retire for a death.
                tia = null;
                try
                {
                    old.Retire();
                }
                catch (Exception e)
                {
                    lock (goneMu)
                    {
                        if (tiaGone == null)
                        {
                            tiaGone = "TIA Portal could not be retired after it trusted a CPU's certificate blindly (" + TiaNames.Message(e) + "), noticed " + DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ");
                            Log.Line("{0}: {1}", tiaGone, GoneFix);
                        }
                    }
                    return null;
                }
                retired = bt;
                Log.Line("audit blind-trust retired: no session left on the TIA instance that trusted blindly ({0}); it exited, and the next call starts a new one", BlindTrust.Audit(bt));
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

        // CheckPolicy refuses, before any write, a Full access password the
        // project's own password policy for local users would refuse.
        static void CheckPolicy(ITia t, object project, Secrets sec)
        {
            string e = t.ReadPolicy(project).Check(sec.FullAccessPassword);
            if (e != null) throw TiadException.BadRequest("secrets.fullAccessPassword {0}", e);
        }

        // CheckStation applies the agent-side station rules: the spec's
        // own, an installed GSDML for every device, a password for every
        // web user.
        void CheckStation(Station st, Secrets sec)
        {
            string err = st.Validate();
            if (err != null) throw TiadException.BadRequest("{0}", err);
            err = sec.CheckFor(st);
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
            sec.Records = records;
            string project = "projects/" + name + "/" + name + ".ap21";
            string full = work.Resolve(project);
            // The session owns projects/<name>/: it is replaced only while
            // nothing in it is open, and nothing in it opens while it is.
            Session s = sessions.Reserve(project, full, "projects/" + name + "/");
            try
            {
                s.Handle = OnTia(delegate(ITia t)
                {
                    string cannot = t.CannotApply(st);
                    if (cannot != null) throw new TiaException(cannot);
                    string dir = Path.GetDirectoryName(full);
                    if (Directory.Exists(dir)) Directory.Delete(dir, true);
                    Directory.CreateDirectory(dir);
                    object h = t.CreateProject(full, name);
                    try
                    {
                        if (st.Cpu.AccessControl == StationRules.AccessFullAccessUser)
                        {
                            s.AccessControlSet = true;
                            CheckPolicy(t, h, sec);
                        }
                        t.ApplyStation(h, st, CpuReuse.Create, sec, new List<string>(), s.Made);
                    }
                    catch
                    {
                        t.CloseProject(h);
                        throw;
                    }
                    s.Station = st;
                    s.Address = st.Cpu.Address;
                    return h;
                });
            }
            catch
            {
                sessions.Drop(s);
                RetireIfTrusted();
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
                RetireIfTrusted();
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
            sessions.ReleaseCpus(s);
            sessions.Closed(s);
            RetireIfTrusted();
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
                bool recordOnly = r.Bool("fullAccessRecordOnly");
                r.Done();
                CheckStation(st, sec);
                sec.Records = records;
                if (recordOnly)
                {
                    if (st.Cpu.AccessControl != StationRules.AccessFullAccessUser) throw TiadException.BadRequest("{0}", "fullAccessRecordOnly goes with cpu.accessControl fullAccessUser");
                    Log.Line("audit access-record session={0} project={1} fullAccessRecordOnly=yes", s.Id, Json.Quote(s.Project));
                }
                // The session's state changes only on the TIA thread, in
                // call order. Until TIA has applied the spec, the project
                // is neither compiled nor described by any spec, so a
                // failure part-way leaves nothing downloadable.
                var notes = new List<string>();
                var kept = new List<KeptDevice>();
                List<string> changes = OnSession(s, delegate(ITia t)
                {
                    // Decided from the CPU the project holds, before any
                    // write: a refusal here leaves the session as it was.
                    ProjectCpu pc = s.Unclean ? null : t.ReadCpu(s.Handle);
                    string decision = CpuReuse.Decide(pc, st.Cpu, s.Unclean);
                    if (decision == CpuReuse.Reuse)
                    {
                        CpuReuse.Collides(pc, st);
                        ConfigProtection.Plan(pc.Protection, st.Cpu.ConfigurationProtection, sec.ConfigurationPassword != "");
                        AccessRule.Plan(pc.AccessControl, st.Cpu.AccessControl, sec.FullAccessPassword != "");
                        kept = pc.Others;
                    }
                    if (st.Cpu.AccessControl == StationRules.AccessFullAccessUser)
                    {
                        s.AccessControlSet = true;
                        CheckPolicy(t, s.Handle, sec);
                    }
                    string cannot = t.CannotApply(st);
                    if (cannot != null) throw new TiaException(cannot);
                    // A found user's password, before any write: a refusal
                    // leaves the session as it was, unless putting the
                    // user's record back failed.
                    var w = new AccessWrites();
                    try
                    {
                        t.PrepareAccess(s.Handle, st, sec, recordOnly, w, notes);
                    }
                    catch
                    {
                        if (w.Dirty)
                        {
                            s.Unclean = true;
                            s.UsersUnclean = true;
                        }
                        throw;
                    }
                    // A password set or recorded is a change to the project.
                    bool was = s.Compiled && !w.Wrote;
                    Station prev = s.Station;
                    s.Compiled = false;
                    s.Station = null;
                    s.Unclean = true;
                    t.ApplyStation(s.Handle, st, decision, sec, notes, s.Made);
                    s.Unclean = false;
                    string kn = CpuReuse.KeptNote(kept);
                    if (kn != null) notes.Add(kn);
                    List<string> ch = CpuReuse.Changes(decision, prev, st);
                    s.Station = st;
                    s.Kept = kept;
                    s.Address = st.Cpu.Address;
                    if (ch.Count == 0) s.Compiled = was;
                    return ch;
                });
                var o = new Obj();
                o["changes"] = changes.ToArray();
                if (notes.Count > 0) o["notes"] = notes.ToArray();
                if (kept.Count > 0) o["keptDevices"] = KeptDevice.ToJson(kept);
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
