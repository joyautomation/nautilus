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
//
// Or, first, the session's adopt baseline (#1347): the layout of the
// project as it was opened, read right after a compare found it equal to
// the CPU (POST /v1/sessions/{id}/compare with layoutBaseline). That is
// how a deploy's first download onto a CPU that runs a program nautilus
// never loaded (an adopted project) keeps the CPU's data. tiad takes it
// only when the session is untouched (UntouchedRule: nothing but a
// compare or an export ran on it since its open), the
// compare was equal, the agent has no UI (a person could change the
// project in TIA's window), and the journal for the CPU can be read. It
// stands for the hour and only while no download reached the CPU since
// (loadGen) and the CPU's journal has no newer line.

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
        // loadGen counts, per CPU, the downloads that may have reached it
        // (ForgetVerified): an adopt baseline taken at one count stands
        // only at that count.
        readonly Dictionary<string, long> loadGen = new Dictionary<string, long>(StringComparer.Ordinal);
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

        // BumpLoad voids every adopt baseline taken for address: a dry
        // run to it may have loaded (TIA asked no check, or it failed).
        void BumpLoad(string address)
        {
            lock (verifiedMu)
            {
                long g;
                loadGen.TryGetValue(address, out g);
                loadGen[address] = g + 1;
            }
        }

        // ForgetVerified drops the mark for address, and every adopt
        // baseline taken for it: a download to it ran.
        void ForgetVerified(string address)
        {
            lock (verifiedMu)
            {
                verifiedLoads.Remove(address);
                long g;
                loadGen.TryGetValue(address, out g);
                loadGen[address] = g + 1;
            }
        }

        long LoadGen(string address)
        {
            lock (verifiedMu)
            {
                long g;
                loadGen.TryGetValue(address, out g);
                return g;
            }
        }

        // JournalHead is the id of the newest line of the CPU's deploy
        // journal, any status ("" for none), or null and why when the
        // journal cannot be read whole.
        string JournalHead(string address, out string why)
        {
            why = null;
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
            if (list.Count == 0) return "";
            return RecordStr((IDictionary<string, object>)list[0], "id") ?? "";
        }

        // AdoptBaselineRefusal is why a compare that asked for an adopt
        // baseline takes none before it goes online, or null: the agent
        // and the session allow one.
        string AdoptBaselineRefusal(Session s)
        {
            if (cfg.Ui) return "the agent runs with TIA's window (TIAD_UI), where a person can change the project by hand: an adopt baseline needs a project only tiad changes";
            if (!s.Untouched) return "the session is not untouched: a call that may change the project ran on it since it was opened (only compare and export keep it); open the project again and compare first";
            return null;
        }

        // TakeAdoptBaseline reads the layout of the session's project, on
        // the TIA thread, right after a compare found it equal to the CPU
        // at address; gen and head are the CPU's load count and journal
        // head read before the compare went online.
        AdoptBaseline TakeAdoptBaseline(ITia t, Session s, string address, long gen, string head, out string why)
        {
            why = AdoptBaselineRefusal(s);
            if (why != null) return null;
            List<LayoutObject> now;
            try
            {
                now = t.Layout(s.Handle);
            }
            catch (TiaException e)
            {
                // A failed read costs the baseline, never the compare; TIA
                // failing is still noted (the licence latch, a dead TIA).
                host.TiaFailed(e);
                why = "reading the project's data layout failed (" + TiaNames.Message(e) + ")";
                return null;
            }
            // A call that raced the read (refused busy, so it changed
            // nothing) still costs the baseline: never a doubt kept.
            if (!s.Untouched)
            {
                why = "a call that may change the project arrived while the layout was read";
                return null;
            }
            return AdoptBaseline.Of(address, now, host.TiaVersion, gen, head, System.Diagnostics.Stopwatch.GetTimestamp());
        }

        // AdoptedValid is the session's adopt baseline for address when it
        // still stands, or null and why not (null why: the session has
        // none).
        AdoptBaseline AdoptedValid(Session s, string address, out string why)
        {
            why = null;
            AdoptBaseline a = s.Adopted;
            if (a == null)
            {
                // A compare asked for one and took none: its reason.
                if (s.AdoptRefused != "") why = "the adopt compare took no baseline: " + s.AdoptRefused;
                return null;
            }
            if (a.Address != address)
            {
                why = "the session's adopt baseline was taken for the CPU at " + a.Address + ", not " + address;
                return null;
            }
            if (Older(a.At, LayoutRule.VerifyMaxAge))
            {
                why = "the compare that took the session's adopt baseline is more than an hour old: open the project again and compare";
                return null;
            }
            if (LoadGen(address) != a.Gen)
            {
                why = "a download reached the CPU since the compare that took the session's adopt baseline";
                return null;
            }
            string hwhy;
            string head = JournalHead(address, out hwhy);
            if (head == null)
            {
                why = hwhy;
                return null;
            }
            if (head != a.JournalHead)
            {
                why = "the CPU's deploy journal has a newer line (" + head + ") than when the session's adopt baseline was taken";
                return null;
            }
            if (a.Tia != (host.TiaVersion ?? ""))
            {
                why = "the session's adopt baseline was read by another TIA version";
                return null;
            }
            return a;
        }

        // Baseline is the layout of the deploy the CPU at address
        // verifiably holds, when that deploy loaded this same project
        // (project, as the session names it), or null and why there is
        // none.
        LayoutBaseline Baseline(Session s, string address, out string why)
        {
            // The session's adopt baseline first: it is the newest proof,
            // bound to the journal head it was taken at (#1347).
            string adoptWhy;
            AdoptBaseline a = AdoptedValid(s, address, out adoptWhy);
            if (a != null)
            {
                why = null;
                return a.ToBaseline();
            }
            LayoutBaseline j = JournalBaseline(address, s.Project, out why);
            if (j == null && adoptWhy != null) why = adoptWhy;
            return j;
        }

        LayoutBaseline JournalBaseline(string address, string project, out string why)
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
            LayoutBaseline b = Baseline(s, address, out why);
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

namespace Tiad
{
    // UntouchedRule is which session calls keep a session untouched: the
    // ones that only read the project. Every other route under
    // /v1/sessions/{id}, a route added later too, touches it before it
    // runs, failed or not (Agent.Register hooks the router), so an adopt
    // baseline is only ever taken of the project as opened (#1347). A
    // route joins Keeps only deliberately: the test pins the list.
    public static class UntouchedRule
    {
        public static readonly string[] Keeps =
        {
            "POST /v1/sessions/{id}/compare",
            "POST /v1/sessions/{id}/export",
        };

        // Touches is whether a call on route (as Router.Routes names it)
        // touches the session it names.
        public static bool Touches(string route)
        {
            if (route.IndexOf(" /v1/sessions/{id}", StringComparison.Ordinal) < 0) return false;
            return Array.IndexOf(Keeps, route) < 0;
        }
    }

    // AdoptBaseline is the layout of a session's project as opened, which
    // an equal compare proved is what the CPU at Address runs (#1347).
    public sealed class AdoptBaseline
    {
        public string Address = "", Id = "", Tia = "", JournalHead = "";
        public List<LayoutObject> Objects = new List<LayoutObject>();
        public long Gen, At;

        public const string IdPrefix = "adopted:";

        public static AdoptBaseline Of(string address, List<LayoutObject> now, string tia, long gen, string head, long at)
        {
            var a = new AdoptBaseline { Address = address, Tia = tia ?? "", Gen = gen, JournalHead = head ?? "", At = at };
            a.Objects = new List<LayoutObject>(now);
            a.Id = IdPrefix + LayoutRule.ObjectsHash(now).Substring(0, 16);
            return a;
        }

        public int Unreadable
        {
            get
            {
                int n = 0;
                foreach (var o in Objects) if (o.Hash == "") n++;
                return n;
            }
        }

        public LayoutBaseline ToBaseline()
        {
            return new LayoutBaseline { Deploy = Id, Scheme = LayoutRule.Scheme, Tia = Tia, Objects = new List<LayoutObject>(Objects) };
        }
    }
}
