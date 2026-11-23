// Adopt.cs: a first deploy builds on the CPU's own TIA project (#1272).
//
// POST /v1/projects/adopt {name, archive | deploy} makes a project archive
// the deploy's saved project, projects/<name>/<name>.ap21, as a deploy
// saves one: the next open of that path is the adopted project. The source
// is named, never guessed: a project archive in the work dir (archive), or
// the archive a deploy record names (deploy), hash-checked against the
// record as a verify checks it. A copy is retrieved in staging, outside
// the work dir, then saved where the deploy looks (TIA's SaveAs renames
// the project to <name>), read and closed: nothing stays open.
//
// It replaces a project only when asked (replace, as a create replaces
// one: naut asks only for an adoption it made and never downloaded), and
// only once the new source is staged and hashed; a projects/<name>/ that
// holds anything is refused otherwise. An archive whose project holds no
// CPU tiad can read, or whose CPU is not at the address the caller
// expects (address: the deploy's target, so an archive of another CPU is
// never adopted for this one), is refused. A failed or refused adopt
// leaves no projects/<name>/ behind. Foreign
// blocks, users and devices come as the archive holds them: adoption
// changes nothing in the project but its name and place.
//
// C# 5 only (see Reuse.cs).

using System;
using System.Collections.Generic;
using System.IO;

namespace Tiad
{
    public sealed partial class Agent
    {
        void RegisterAdopt(Router r)
        {
            r.Add("POST", "/v1/projects/adopt", AdoptProject);
        }

        object AdoptProject(Call c)
        {
            NeedTia();
            var r = c.Json();
            string name = r.Str("name");
            string archivePath = r.Str("archive");
            string id = r.Str("deploy");
            bool replace = r.Bool("replace");
            string expect = r.Str("address");
            r.Done();
            if (expect != "" && !Ip.IsIPv4(expect)) throw TiadException.BadRequest("adopt: address {0} is not an IPv4 address", Json.Quote(expect));
            CheckProjectName(name);
            if ((archivePath == "") == (id == ""))
            {
                throw TiadException.BadRequest("{0}", "adopt: name exactly one source, archive (a project archive in the work dir) or deploy (a deploy record's archive)");
            }
            string project = "projects/" + name + "/" + name + ".ap21";
            string full = work.Resolve(project);
            string target = Path.GetDirectoryName(full);
            var o = new Obj();
            var source = new Obj();
            string src;
            string recordAddress = null;
            if (id != "")
            {
                if (!DeployIdShape(id)) throw TiadException.NotFound("no deploy {0}", Json.Quote(id));
                string address;
                IDictionary<string, object> record = VerifyRecord(id, new Obj(), out address);
                if (RecordStr(record, "status") == DeployStatus.NotLoaded)
                {
                    throw TiadException.BadRequest("deploy {0} loaded nothing (status not-loaded): its archive is not what any CPU runs; nothing was adopted", Json.Quote(id));
                }
                object archive;
                record.TryGetValue("archive", out archive);
                string sha;
                src = deploys.ArchivePath(archive, out sha);
                if (src == null || !File.Exists(src))
                {
                    throw new TiadException(409, Kinds.BadRequest, "deploy " + id + "'s archive is " + (src == null ? "not one tiad writes" : "gone") + "; nothing was adopted");
                }
                source["deploy"] = id;
                source["sha256"] = sha;
                recordAddress = address;
                o["address"] = address;
            }
            else
            {
                string clean = Paths.CleanPath(archivePath);
                if (!clean.EndsWith(".zap21", StringComparison.OrdinalIgnoreCase))
                {
                    throw TiadException.BadRequest("adopt: archive {0} is not a project archive (.zap21)", Json.Quote(clean));
                }
                src = ExistingFile(archivePath);
                // Never a file the replace would delete first.
                if (Paths.CleanPath(clean).StartsWith("projects/" + name + "/", StringComparison.OrdinalIgnoreCase))
                {
                    throw TiadException.BadRequest("adopt: archive {0} is inside projects/{1}/, which the adopt replaces: copy it elsewhere in the work dir first", Json.Quote(clean), name);
                }
                source["archive"] = clean;
            }

            // One project at a time, and projects/<name>/ only once no
            // other project is open (as a create).
            Session s = sessions.Reserve(project, full, "projects/" + name + "/");
            string stage = "";
            bool placed = false, kept = false;
            try
            {
                bool occupied = Directory.Exists(target) && Directory.GetFileSystemEntries(target).Length > 0;
                if (occupied && !replace)
                {
                    throw new TiadException(409, Kinds.BadRequest,
                        "projects/" + name + "/ holds a project already: adopt makes a deploy's first project and replaces one only when asked (replace); open it (POST /v1/sessions)");
                }
                string copy;
                long bytes;
                string sha;
                try
                {
                    sha = deploys.AdoptStage(src, out stage, out copy, out bytes);
                }
                catch (Exception e)
                {
                    throw new TiadException(500, Kinds.OperationFailed, "copying the archive for the adopt failed (" + e.GetType().Name + ": " + e.Message + "); nothing was adopted");
                }
                if (source.ContainsKey("sha256") && (string)source["sha256"] != sha)
                {
                    throw new TiadException(409, Kinds.BadRequest, "deploy " + id + "'s archive has sha256 " + sha + ", not the record's: it is broken, and nothing was adopted");
                }
                source["sha256"] = sha;
                source["bytes"] = bytes;
                // The source is staged and proven: only now is a project
                // replaced (as a create replaces it: no session holds it,
                // Reserve).
                if (occupied)
                {
                    Directory.Delete(target, true);
                    Log.Line("audit adopt project={0} replaced: the project there was removed for the adopt", Json.Quote(project));
                }
                ProjectCpu pc = null;
                string cpuWhy = null, at = null;
                // From here a failure removes whatever TIA wrote at target,
                // a SaveAs that failed part-way too.
                placed = true;
                OnTia(delegate(ITia t)
                {
                    object h = t.AdoptProject(copy, Path.Combine(stage, "R"), target);
                    try
                    {
                        try
                        {
                            pc = t.ReadCpu(h);
                            if (pc != null) at = t.ProjectCpuAddress(h);
                        }
                        catch (Exception e)
                        {
                            pc = null;
                            cpuWhy = TiaNames.Message(e);
                        }
                    }
                    finally
                    {
                        try
                        {
                            t.CloseProject(h);
                        }
                        catch (Exception ce)
                        {
                            // TIA may still hold it: the session stays,
                            // broken, so nothing opens over it.
                            s.Handle = h;
                            s.Broken = true;
                            kept = true;
                            Log.Line("adopt {0}: closing the adopted project failed ({1}); session {2} is kept, broken: close it again",
                                Json.Quote(project), Json.Quote(TiaNames.Message(ce)), s.Id);
                            throw;
                        }
                    }
                    return true;
                });
                if (pc == null)
                {
                    throw new TiadException(409, Kinds.BadRequest, "the archive's project holds no CPU tiad can read" + (cpuWhy == null ? "" : " (" + cpuWhy + ")") +
                        ": nothing was adopted");
                }
                // A record's archive whose CPU is not at the record's address
                // names no CPU: nothing is adopted from it (as a verify).
                if (recordAddress != null && at != recordAddress)
                {
                    throw new TiadException(409, Kinds.BadRequest, "deploy " + id + " names CPU " + recordAddress + ", but its archived project's CPU is " +
                        (at == "" ? "at no address" : "at " + at) + ": nothing was adopted");
                }
                // An archive of another CPU is never adopted for this one.
                if (expect != "" && at != expect)
                {
                    throw new TiadException(409, Kinds.CpuMismatch, "the archive's project has its CPU " + (at == "" ? "at no address" : "at " + at) +
                        ", not at " + expect + ", the deploy's CPU: it is another CPU's project (or one from before the address changed), and nothing was adopted");
                }
                o["cpuAddress"] = at;
                o["project"] = project;
                o["source"] = source;
                o["cpu"] = pc == null ? null : pc.ToJson();
                if (cpuWhy != null) o["cpuError"] = cpuWhy;
                o["keptDevices"] = pc == null ? new List<object>() : KeptDevice.ToJson(pc.Others);
                Log.Line("audit adopt project={0} source={1} sha256={2} replace={3}", Json.Quote(project),
                    Json.Quote(id != "" ? "deploy:" + id : "archive:" + (string)source["archive"]), sha, replace ? "yes" : "no");
                placed = false;
                return o;
            }
            finally
            {
                if (kept)
                {
                    sessions.End(s);
                }
                else
                {
                    sessions.Drop(s);
                    if (placed) RemoveAdopted(target);
                    RetireIfTrusted();
                }
                if (stage != "") deploys.RemoveStaging(stage);
            }
        }

        // RemoveAdopted removes a project an adopt placed and then refused
        // or failed after: a retry must not find it saved.
        void RemoveAdopted(string target)
        {
            try
            {
                if (Directory.Exists(target)) Directory.Delete(target, true);
            }
            catch (Exception e)
            {
                Log.Line("adopt: removing {0} after the failure failed: {1}", Json.Quote(work.Rel(target)), Json.Quote(e.Message));
            }
        }
    }
}
