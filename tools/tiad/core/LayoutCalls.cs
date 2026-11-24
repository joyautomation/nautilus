// LayoutCalls.cs: the data layout gate's route and its baseline
// (#1341; Layout.cs has the rule).
//
// POST /v1/sessions/{id}/layout {"address", "hardware"} reads the
// session's compiled project and answers what a download of it would do to
// the CPU's data: the changes since the baseline, the data blocks they
// reach, whether the download needs decisions.resetData, and the digest a
// download without that consent must send. It reads the project only; it
// never reaches the CPU.
//
// The baseline is the layout recorded with the CPU's newest load (loaded
// or in doubt), only when a verify by this agent found the CPU equal to
// that very deploy within the last hour (VerifyDeploy marks it), and the
// journal holds no line the agent cannot read.

using System;
using System.Collections.Generic;

namespace Tiad
{
    public sealed partial class Agent
    {
        // VerifiedLoad is a verify that found a CPU equal to its newest
        // load: the deploy, and when (tiad's clock).
        sealed class VerifiedLoad
        {
            public string Deploy = "";
            // At is a monotonic timestamp (Stopwatch): a clock set back
            // never extends the hour.
            public long At;
        }

        static bool Older(long at, TimeSpan age)
        {
            long ticks = System.Diagnostics.Stopwatch.GetTimestamp() - at;
            return ticks < 0 || ticks / (double)System.Diagnostics.Stopwatch.Frequency > age.TotalSeconds;
        }

        readonly Dictionary<string, VerifiedLoad> verifiedLoads = new Dictionary<string, VerifiedLoad>(StringComparer.Ordinal);
        readonly object verifiedMu = new object();

        void RegisterLayout(Router r)
        {
            r.Add("POST", "/v1/sessions/{id}/layout", Layout);
        }

        // MarkVerified records that a verify found the CPU at address
        // equal to deploy, its newest load.
        void MarkVerified(string address, string deploy)
        {
            lock (verifiedMu)
            {
                verifiedLoads[address] = new VerifiedLoad { Deploy = deploy, At = System.Diagnostics.Stopwatch.GetTimestamp() };
            }
        }

        // ForgetVerified drops the mark for address: a download to it ran.
        void ForgetVerified(string address)
        {
            lock (verifiedMu)
            {
                verifiedLoads.Remove(address);
            }
        }

        // Baseline is the layout of the deploy the CPU at address
        // verifiably holds, when that deploy loaded this same project
        // (project, as the session names it), or null and why there is
        // none.
        LayoutBaseline Baseline(string address, string project, out string why)
        {
            VerifiedLoad v;
            lock (verifiedMu)
            {
                verifiedLoads.TryGetValue(address, out v);
            }
            if (v == null)
            {
                why = "no verify by this agent, since it started, found the CPU equal to its last deploy (a deploy runs one before every download)";
                return null;
            }
            if (Older(v.At, LayoutRule.VerifyMaxAge))
            {
                why = "the verify that found the CPU equal to deploy " + v.Deploy + " is more than an hour old: verify again";
                return null;
            }
            int unreadable;
            List<object> list;
            try
            {
                list = deploys.List(address, out unreadable);
            }
            catch (Exception e)
            {
                if (!(e is System.IO.IOException) && !(e is UnauthorizedAccessException)) throw;
                why = "the deploy journal cannot be read (" + e.Message + ")";
                return null;
            }
            if (unreadable > 0)
            {
                why = "the deploy journal holds " + unreadable + " line(s) the agent cannot read, which may be a newer load";
                return null;
            }
            IDictionary<string, object> latest = null;
            foreach (object x in list)
            {
                var d = (IDictionary<string, object>)x;
                if (RecordStr(d, "status") != DeployStatus.NotLoaded)
                {
                    latest = d;
                    break;
                }
            }
            if (latest == null)
            {
                why = "no deploy record for this CPU";
                return null;
            }
            string id = RecordStr(latest, "id") ?? "";
            if (id != v.Deploy)
            {
                why = "the CPU's newest load is deploy " + id + ", not deploy " + v.Deploy + ", which the verify found equal";
                return null;
            }
            // Another project's layout is no baseline, even an equal one
            // (an other-project verdict accepted as an outside change).
            string loaded = RecordStr(latest, "project") ?? "";
            if (!string.Equals(loaded, project, StringComparison.OrdinalIgnoreCase))
            {
                why = "the CPU's newest load, deploy " + id + ", loaded another project (" + loaded + ")";
                return null;
            }
            object layout;
            latest.TryGetValue("layout", out layout);
            return LayoutRule.FromRecord(id, layout, host.TiaVersion, out why);
        }

        // ReadLayout reads the project's layout on the TIA thread and
        // diffs it with the CPU's baseline.
        LayoutDiff ReadLayout(ITia t, Session s, string address, bool hardware, out List<LayoutObject> now)
        {
            now = t.Layout(s.Handle);
            string why;
            LayoutBaseline b = Baseline(address, s.Project, out why);
            return LayoutRule.Diff(now, b, why, host.TiaVersion, hardware);
        }

        object Layout(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                string address = r.Str("address");
                bool hardware = r.Bool("hardware");
                r.Done();
                if (!Ip.IsIPv4(address)) throw TiadException.BadRequest("layout: address {0} is not an IPv4 address", Json.Quote(address));
                if (s.Station == null) throw TiadException.BadRequest("the project has no station spec");
                if (s.Station.Cpu.Address != address)
                {
                    throw TiadException.BadRequest("the project's CPU is at {0}, not {1}: one station spec per CPU", s.Station.Cpu.Address, address);
                }
                if (!s.Compiled)
                {
                    throw new TiadException(409, Kinds.NotCompiled, "the project changed since the last full compile; compile hardware and software first");
                }
                LayoutDiff d = OnSession(s, delegate(ITia t)
                {
                    List<LayoutObject> now;
                    return ReadLayout(t, s, address, hardware, out now);
                });
                Log.Line("layout session={0} project={1} address={2} baseline={3} changes={4} dataBlocks={5} resetDataNeeded={6}",
                    s.Id, Json.Quote(s.Project), Json.Quote(address), d.Baseline == "" ? "none" : d.Baseline, d.Changes.Count, d.AllDataBlocks ? "all" : d.DataBlocks.Count.ToString(System.Globalization.CultureInfo.InvariantCulture), d.ResetDataNeeded ? "yes" : "no");
                return d.ToJson();
            });
        }
    }
}
