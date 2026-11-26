// AdoptBaselineTest.cs: the adopt baseline's pure parts (#1347): which
// session calls keep a session untouched, the router hook that touches it,
// which added kinds need no consent, and how an adopt baseline is named.

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        static void UntouchedRuleKeepsOnlyReads()
        {
            // Pinned: a route joins the list only deliberately.
            Eq(string.Join("|", UntouchedRule.Keeps), "POST /v1/sessions/{id}/compare|POST /v1/sessions/{id}/export", "the keep list");
            foreach (string k in UntouchedRule.Keeps) True(!UntouchedRule.Touches(k), k + " keeps");
            foreach (string r in new[] { "POST /v1/sessions/{id}/software", "PUT /v1/sessions/{id}/station", "POST /v1/sessions/{id}/compile", "POST /v1/sessions/{id}/upload", "POST /v1/sessions/{id}/layout",
                "POST /v1/sessions/{id}/download", "POST /v1/sessions/{id}/show", "POST /v1/sessions/{id}/online", "POST /v1/sessions/{id}/save", "DELETE /v1/sessions/{id}",
                "POST /v1/sessions/{id}/a-route-added-later" })
            {
                True(UntouchedRule.Touches(r), r + " touches");
            }
            True(!UntouchedRule.Touches("GET /v1/sessions"), "no session named");
            True(!UntouchedRule.Touches("POST /v1/projects"), "not a session call");
            True(!UntouchedRule.Touches("GET /v1/deploys/{id}"), "an id that is not a session's");
        }

        static void RouterHookSeesEveryMatchedRoute()
        {
            var r = new Router();
            var seen = new List<string>();
            r.Matched = delegate(string route, Dictionary<string, string> v)
            {
                string id;
                v.TryGetValue("id", out id);
                seen.Add(route + "#" + id);
            };
            Handler h = delegate(Call c) { return null; };
            r.Add("POST", "/v1/sessions/{id}/compare", h);
            r.Add("POST", "/v1/sessions/{id}/software", h);
            Eq(string.Join("|", r.Routes().ToArray()), "POST /v1/sessions/{id}/compare|POST /v1/sessions/{id}/software", "routes, as registered");
            Dictionary<string, string> vars;
            bool known;
            True(r.Match("POST", new List<string> { "v1", "sessions", "s3", "software" }, out vars, out known) != null, "matched");
            True(r.Match("GET", new List<string> { "v1", "sessions", "s3", "software" }, out vars, out known) == null && known, "wrong method: no handler");
            Eq(string.Join("|", seen.ToArray()), "POST /v1/sessions/{id}/software#s3", "the hook ran once, for the route that runs, before its handler");
            bool threw = false;
            try
            {
                r.Matched = delegate(string route, Dictionary<string, string> v) { };
            }
            catch (InvalidOperationException)
            {
                threw = true;
            }
            True(threw, "the hook is set once: a second one would drop the agent's touch");
        }

        static void AddedKindsNeedNoConsent()
        {
            var was = Program1("r1", "t1", "f1", "i1");
            // nautilus's first deploy onto an adopted project: only its own
            // blocks and types are added.
            var now = Program1("r1", "t1", "f1", "i1");
            now.Add(LO("naut_OB_main", "OB", "o"));
            now.Add(LO("naut_X", "FB", "x"));
            now.Add(LO("naut_X_DB", "InstanceDB", "xd", "naut_X"));
            now.Add(LO("naut_F", "FC", "fc"));
            now.Add(LO("naut_T", "GlobalDB", "t", "naut_U"));
            now.Add(LO("naut_U", "PlcStruct", "u"));
            LayoutDiff d = LayoutRule.Diff(now, Base(was), null, "V21", false);
            Eq(d.Changes.Count, 6, "six added");
            True(!d.ResetDataNeeded, "added OB, FB, FC, DBs and a UDT need no consent");
            Eq(d.DataBlocks.Count, 0, "a new data block holds nothing to lose");
            Contains(d.Why, "the load only adds 6 object(s): the load keeps the CPU's data", "why");

            // A kind the bench did not prove still needs it.
            var arr = Program1("r1", "t1", "f1", "i1");
            arr.Add(LO("naut_A", "ArrayDB", "a"));
            True(LayoutRule.Diff(arr, Base(was), null, "V21", false).ResetDataNeeded, "an added ArrayDB needs the consent");
            foreach (string k in new[] { "OB", "FB", "FC", "GlobalDB", "InstanceDB", "PlcStruct" })
            {
                True(!LayoutRule.NeedsConsent(new LayoutChange { Name = "x", Type = k, Change = LayoutChange.Added }), k + " added: no consent");
                True(LayoutRule.NeedsConsent(new LayoutChange { Name = "x", Type = k, Change = LayoutChange.Changed }), k + " changed: the consent");
                True(LayoutRule.NeedsConsent(new LayoutChange { Name = "x", Type = k, Change = LayoutChange.Removed }), k + " removed: the consent");
                True(LayoutRule.NeedsConsent(new LayoutChange { Name = "x", Type = k, Change = LayoutChange.UnreadableNow }), k + " unreadable: the consent");
            }

            // Added beside a change: the change still needs it, and the why
            // counts only what needs it.
            now[3].Hash = "f2";
            LayoutDiff both = LayoutRule.Diff(now, Base(was), null, "V21", false);
            True(both.ResetDataNeeded, "an added block beside an FB change: the consent");
            Contains(both.Why, "changed since deploy d-1 (1 object(s))", "counts the FB only");

            // An added unreadable object is unreadable, not added.
            var un = Program1("r1", "t1", "f1", "i1");
            un.Add(new LayoutObject { Name = "naut_New", Kind = "block", Type = "FB", Unreadable = "inconsistent" });
            True(LayoutRule.Diff(un, Base(was), null, "V21", false).ResetDataNeeded, "an added object that cannot be read: the consent");

            // A hardware load needs it whatever was added.
            True(LayoutRule.Diff(now, Base(was), null, "V21", true).ResetDataNeeded, "hardware");
        }

        static void AdoptBaselineIsNamedByItsObjects()
        {
            var objs = Program1("r1", "t1", "f1", "i1");
            AdoptBaseline a = AdoptBaseline.Of("192.0.2.10", objs, "V21", 3, "d-9", 77);
            var rev = new List<LayoutObject>(objs);
            rev.Reverse();
            AdoptBaseline b = AdoptBaseline.Of("192.0.2.10", rev, "V21", 4, "", 78);
            True(a.Id.StartsWith(AdoptBaseline.IdPrefix, StringComparison.Ordinal) && a.Id.Length == AdoptBaseline.IdPrefix.Length + 16, "adopted:<16 hex>");
            Eq(b.Id, a.Id, "named by the objects, in any order");
            var moved = Program1("r2", "t1", "f1", "i1");
            True(AdoptBaseline.Of("192.0.2.10", moved, "V21", 3, "d-9", 77).Id != a.Id, "another layout, another name");
            Eq(a.Unreadable, 0, "none unreadable");
            moved[0].Hash = "";
            Eq(AdoptBaseline.Of("192.0.2.10", moved, "V21", 3, "", 77).Unreadable, 1, "one unreadable");

            LayoutDiff d = LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), a.ToBaseline(), null, "V21", false);
            Eq(d.Baseline, a.Id, "the diff names the adopt baseline");
            Eq(d.BaselineFrom, "adopted", "from the adopted project");
            Contains(d.Why, "unchanged since the adopted project (" + a.Id + "), which the CPU compared equal to", "why");
            Eq(LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), Base(objs), null, "V21", false).BaselineFrom, "deploy", "a deploy record's");
            Contains(LayoutRule.GateMiss("stop", a.Id), "since the adopted project (" + a.Id + ")", "the gate miss names it");
            // An unreadable object in the adopt baseline: anything read now
            // differs from it.
            var withHole = Program1("r1", "t1", "f1", "i1");
            withHole[5].Hash = "";
            LayoutDiff h = LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), AdoptBaseline.Of("192.0.2.10", withHole, "V21", 0, "", 0).ToBaseline(), null, "V21", false);
            True(h.ResetDataNeeded, "an object unreadable in the adopt baseline needs the consent");
            Eq(string.Join(",", h.DataBlocks.ToArray()), "Cust_DB", "and is named");
        }
    }
}
