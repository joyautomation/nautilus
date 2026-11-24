// LayoutTest.cs: the data layout gate's pure parts (core/Layout.cs, #1341):
// reading an export, the diff and the data blocks it names, the digest,
// the record and the baseline's refusals. The exports below have the
// shape TIA V21 writes (TIA VM, 2026-11-23), cut short.

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        static string Export(string created, string root, string attrs, string body)
        {
            return "<?xml version=\"1.0\" encoding=\"utf-8\"?>\r\n<Document>\r\n  <Engineering version=\"V21\" />\r\n  <DocumentInfo>\r\n    <Created>" + created + "</Created>\r\n  </DocumentInfo>\r\n" +
                "  <" + root + " ID=\"0\">\r\n    <AttributeList>\r\n" + attrs + "\r\n    </AttributeList>\r\n    <ObjectList>" + body + "</ObjectList>\r\n  </" + root + ">\r\n</Document>";
        }

        static string TagsAttrs(string sp, string number)
        {
            return "      <Interface><Sections xmlns=\"http://www.siemens.com/automation/Openness/SW/Interface/v5\"><Section Name=\"Static\">" +
                "<Member Name=\"N1\" Datatype=\"&quot;naut_Rec&quot;\" Remanence=\"NonRetain\"><Sections><Section Name=\"None\"><Member Name=\"sp\" Datatype=\"Real\"><StartValue>" + sp + "</StartValue></Member></Section></Sections></Member>" +
                "<Member Name=\"Arr\" Datatype=\"Array[0..3] of &quot;naut_Other&quot;\" />" +
                "</Section></Sections></Interface>\r\n      <Name>naut_Tags</Name>\r\n      <Number>" + number + "</Number>";
        }

        static void LayoutReadsTheRootAttributeList()
        {
            LayoutObject a = LayoutRule.FromExport("naut_Tags", "block", "GlobalDB", Export("2026-11-23T22:43:52Z", "SW.Blocks.GlobalDB", TagsAttrs("7.0", "1"), "<x/>"));
            LayoutObject same = LayoutRule.FromExport("naut_Tags", "block", "GlobalDB", Export("2026-11-23T22:43:59Z", "SW.Blocks.GlobalDB", TagsAttrs("7.0", "1"), "<x/>"));
            LayoutObject body = LayoutRule.FromExport("naut_Tags", "block", "GlobalDB", Export("2026-11-23T22:43:59Z", "SW.Blocks.GlobalDB", TagsAttrs("7.0", "1"), "<other body=\"1\"/>"));
            LayoutObject start = LayoutRule.FromExport("naut_Tags", "block", "GlobalDB", Export("2026-11-23T22:43:52Z", "SW.Blocks.GlobalDB", TagsAttrs("8.0", "1"), "<x/>"));
            LayoutObject number = LayoutRule.FromExport("naut_Tags", "block", "GlobalDB", Export("2026-11-23T22:43:52Z", "SW.Blocks.GlobalDB", TagsAttrs("7.0", "2"), "<x/>"));
            LayoutObject kind = LayoutRule.FromExport("naut_Tags", "block", "InstanceDB", Export("2026-11-23T22:43:52Z", "SW.Blocks.GlobalDB", TagsAttrs("7.0", "1"), "<x/>"));
            True(a.Hash.Length == 64 && a.Unreadable == "", "a hash");
            Eq(same.Hash, a.Hash, "the export's date is not hashed");
            Eq(body.Hash, a.Hash, "what is outside the attribute list (a body) is not hashed");
            True(start.Hash != a.Hash, "a start value is hashed");
            True(number.Hash != a.Hash, "the number is hashed");
            True(kind.Hash != a.Hash, "the type is hashed");
            Eq(string.Join(",", a.Refs.ToArray()), "naut_Other,naut_Rec", "refs: quoted data types, arrays too");

            LayoutObject inst = LayoutRule.FromExport("naut_main_DB", "block", "InstanceDB", Export("x", "SW.Blocks.InstanceDB", "<InstanceOfName>naut_main</InstanceOfName><Interface><Sections><Section Name=\"Static\"><Member Name=\"s\" Datatype=\"&quot;naut_Rec&quot;\" /></Section></Sections></Interface>", ""));
            Eq(string.Join(",", inst.Refs.ToArray()), "naut_main,naut_Rec", "an instance DB refers to its FB");
            LayoutObject self = LayoutRule.FromExport("naut_Rec", "type", "PlcStruct", Export("x", "SW.Types.PlcStruct", "<Interface><Member Name=\"n\" Datatype=\"&quot;naut_Rec&quot;\" /></Interface>", ""));
            Eq(self.Refs.Count, 0, "an object never refers to itself");

            LayoutObject none = LayoutRule.FromExport("x", "block", "FB", "<?xml version=\"1.0\"?><Document><SW.Blocks.FB ID=\"0\"><ObjectList/></SW.Blocks.FB></Document>");
            Eq(none.Hash, "", "no attribute list: unreadable");
            Contains(none.Unreadable, "no object attribute list", "says why");
            LayoutObject bad = LayoutRule.FromExport("x", "block", "FB", "<Document><SW.Blocks.FB>");
            Eq(bad.Hash, "", "not XML: unreadable");
            Contains(bad.Unreadable, "not XML", "says why");
        }

        static LayoutObject LO(string name, string type, string hash, params string[] refs)
        {
            return new LayoutObject { Name = name, Kind = type == "PlcStruct" ? "type" : "block", Type = type, Hash = hash, Refs = new List<string>(refs) };
        }

        static List<LayoutObject> Program1(string rec, string tags, string main, string mainDb)
        {
            return new List<LayoutObject>
            {
                LO("Main", "OB", "m"),
                LO("naut_Rec", "PlcStruct", rec),
                LO("naut_Tags", "GlobalDB", tags, "naut_Rec"),
                LO("naut_main", "FB", main, "naut_Rec"),
                LO("naut_main_DB", "InstanceDB", mainDb, "naut_main", "naut_Rec"),
                LO("Cust_DB", "GlobalDB", "c"),
            };
        }

        static LayoutBaseline Base(List<LayoutObject> objs)
        {
            return new LayoutBaseline { Deploy = "d-1", Scheme = LayoutRule.Scheme, Tia = "V21", Objects = objs };
        }

        static void LayoutDiffNamesTheDataBlocks()
        {
            var was = Program1("r1", "t1", "f1", "i1");

            LayoutDiff none = LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), null, "no deploy record for this CPU", "V21", false);
            True(none.ResetDataNeeded && none.AllDataBlocks, "no baseline: the consent, every data block");
            Eq(string.Join(",", none.DataBlocks.ToArray()), "Cust_DB,naut_main_DB,naut_Tags", "every data block, sorted");
            Eq(none.Baseline, "", "no baseline");
            Contains(none.Why, "no layout baseline (no deploy record for this CPU)", "why");

            LayoutDiff same = LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), Base(was), null, "V21", false);
            True(!same.ResetDataNeeded && same.Changes.Count == 0 && same.DataBlocks.Count == 0 && !same.AllDataBlocks, "unchanged: no consent needed");
            Contains(same.Why, "unchanged since deploy d-1", "why");

            // An FB static: the FB and its instance DB move (TIA writes the
            // statics out in the DB); only the DB is named.
            LayoutDiff fb = LayoutRule.Diff(Program1("r1", "t1", "f2", "i2"), Base(was), null, "V21", false);
            True(fb.ResetDataNeeded, "an FB static needs the consent");
            Eq(fb.Changes.Count, 2, "FB and instance DB changed");
            Eq(string.Join(",", fb.DataBlocks.ToArray()), "naut_main_DB", "the instance DB is named");

            // A UDT default-only change moves only the UDT (holders do not
            // carry its start values): its holders are named by refs.
            LayoutDiff udt = LayoutRule.Diff(Program1("r2", "t1", "f1", "i1"), Base(was), null, "V21", false);
            Eq(udt.Changes.Count, 1, "only the UDT changed");
            Eq(udt.Changes[0].Change, LayoutChange.Changed, "changed");
            Eq(string.Join(",", udt.DataBlocks.ToArray()), "naut_main_DB,naut_Tags", "every holder, the instance DB by its FB");

            // Named through a chain: a DB that names only an FB, whose
            // static's type changed.
            var chain = new List<LayoutObject> { LO("Inner", "PlcStruct", "a"), LO("Outer", "FB", "b", "Inner"), LO("Outer_DB", "InstanceDB", "c", "Outer") };
            var chainNow = new List<LayoutObject> { LO("Inner", "PlcStruct", "a2"), LO("Outer", "FB", "b", "Inner"), LO("Outer_DB", "InstanceDB", "c", "Outer") };
            Eq(string.Join(",", LayoutRule.Diff(chainNow, Base(chain), null, "V21", false).DataBlocks.ToArray()), "Outer_DB", "named through a chain");

            // A customer's data block edited: the consent, and it is named.
            var cust = Program1("r1", "t1", "f1", "i1");
            cust[5].Hash = "c2";
            LayoutDiff c = LayoutRule.Diff(cust, Base(was), null, "V21", false);
            True(c.ResetDataNeeded, "a customer's change needs the consent");
            Eq(string.Join(",", c.DataBlocks.ToArray()), "Cust_DB", "named");

            // Added, removed, unreadable.
            var more = Program1("r1", "t1", "f1", "i1");
            more.Add(LO("naut_New_DB", "GlobalDB", "n"));
            more.RemoveAt(5);
            more[1].Hash = "";
            more[1].Unreadable = "know-how protected";
            LayoutDiff m = LayoutRule.Diff(more, Base(was), null, "V21", false);
            var got = new List<string>();
            foreach (var ch in m.Changes) got.Add(ch.Name + ":" + ch.Change);
            Eq(string.Join(",", got.ToArray()), "naut_New_DB:added,naut_Rec:unreadable,Cust_DB:removed", "changes, removed last");
            // An unreadable object: its layout and references are unknown,
            // so every data block is named, the removed one last.
            True(m.AllDataBlocks, "an unreadable object: every data block");
            Eq(string.Join(",", m.DataBlocks.ToArray()), "naut_main_DB,naut_New_DB,naut_Tags,Cust_DB", "every data block the project holds, then the removed one");

            // A software unit tiad cannot read, which no export names:
            // never "no data block" (naut-lead, #1348).
            var withUnit = Program1("r1", "t1", "f1", "i1");
            withUnit.Add(new LayoutObject { Name = "unit:Line", Kind = "unit", Type = "PlcUnit", Unreadable = "a software unit" });
            LayoutDiff u = LayoutRule.Diff(withUnit, Base(Program1("r1", "t1", "f1", "i1")), null, "V21", false);
            True(u.ResetDataNeeded && u.AllDataBlocks, "an unreadable unit: the consent, every data block");
            Eq(string.Join(",", u.DataBlocks.ToArray()), "Cust_DB,naut_main_DB,naut_Tags", "every data block");

            // A change no data block uses (an FC's interface) names none.
            var fcWas = Program1("r1", "t1", "f1", "i1");
            fcWas.Add(LO("naut_Calc", "FC", "x1"));
            var fcNow = Program1("r1", "t1", "f1", "i1");
            fcNow.Add(LO("naut_Calc", "FC", "x2"));
            LayoutDiff fc = LayoutRule.Diff(fcNow, Base(fcWas), null, "V21", false);
            True(fc.ResetDataNeeded && !fc.AllDataBlocks && fc.DataBlocks.Count == 0, "an FC's interface: the consent, no data block");
            True(m.ResetDataNeeded, "the consent");

            // A baseline object that was unreadable is changed, whatever now.
            var wasBad = Program1("r1", "t1", "f1", "i1");
            wasBad[5].Hash = "";
            LayoutDiff wb = LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), Base(wasBad), null, "V21", false);
            Eq(wb.Changes.Count, 1, "unreadable then: changed now");
            True(wb.ResetDataNeeded, "the consent");

            // A hardware load needs the consent whatever the layout.
            LayoutDiff hw = LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), Base(was), null, "V21", true);
            True(hw.ResetDataNeeded && hw.AllDataBlocks && hw.Changes.Count == 0, "hardware: the consent, every data block");

            // Case: TIA's names compare without case.
            var lower = Program1("r1", "t1", "f1", "i1");
            lower[2].Name = "NAUT_TAGS";
            Eq(LayoutRule.Diff(lower, Base(was), null, "V21", false).Changes.Count, 0, "names compare without case");
        }

        static void LayoutDigestNamesTheExactLayout()
        {
            var was = Program1("r1", "t1", "f1", "i1");
            string d = LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), Base(was), null, "V21", false).Digest;
            var shuffled = Program1("r1", "t1", "f1", "i1");
            shuffled.Reverse();
            Eq(LayoutRule.Diff(shuffled, Base(was), null, "V21", false).Digest, d, "order does not matter");
            True(LayoutRule.Diff(Program1("r1", "t1", "f1", "i9"), Base(was), null, "V21", false).Digest != d, "any hash");
            True(LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), null, "x", "V21", false).Digest != d, "the baseline");
            var other = Base(was);
            other.Deploy = "d-2";
            True(LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), other, null, "V21", false).Digest != d, "the baseline's deploy");
            True(LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), Base(was), null, "V21", true).Digest != d, "hardware");
            True(LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), Base(was), null, "V22", false).Digest != d, "the TIA version");
        }

        // The Go copy (siemens/tiad DiffLayout, which the Go fake runs)
        // hashes alike: the same layout, the same digest
        // (layout_test.go layoutGoldenDigest).
        static void LayoutDigestMatchesTheGoCopy()
        {
            var was = Program1("r1", "t1", "f1", "i1");
            Eq(LayoutRule.Diff(Program1("r1", "t1", "f1", "i1"), Base(was), null, "V21", false).Digest,
                "5cfb85892e89e93872eed5b06e73dffad713b6cbdeb573cdc350b527cd1c7633", "the Go copy's digest");
        }

        static void LayoutRecordIsTheBaseline()
        {
            var now = Program1("r1", "t1", "f1", "i1");
            Obj rec = LayoutRule.ToRecord(now, "V21");
            // Through the journal's JSON, as List reads it back.
            object back = Json.Parse(Json.Write(rec));
            string why;
            LayoutBaseline b = LayoutRule.FromRecord("d-7", back, "V21", out why);
            True(b != null && why == null, "read back");
            Eq(b.Deploy, "d-7", "deploy");
            Eq(b.Objects.Count, now.Count, "objects");
            Eq(LayoutRule.Diff(now, b, null, "V21", false).Changes.Count, 0, "the same layout");

            Eq(LayoutRule.FromRecord("d-7", null, "V21", out why), null, "no layout");
            Contains(why, "deploy d-7 recorded no data layout (it was made before the layout gate, #1341)", "why");
            Eq(LayoutRule.FromRecord("d-7", back, "V22", out why), null, "another TIA");
            Contains(why, "a TIA upgrade invalidates every recorded layout", "why");
            var o = (IDictionary<string, object>)Json.Parse(Json.Write(rec));
            o["scheme"] = "attributes-v0";
            Eq(LayoutRule.FromRecord("d-7", o, "V21", out why), null, "another scheme");
            Contains(why, "a change of the scheme invalidates every recorded layout", "why");
            var bad = (IDictionary<string, object>)Json.Parse(Json.Write(rec));
            var objs = new List<object>();
            foreach (object x in (System.Collections.IList)bad["objects"]) objs.Add(x);
            objs.Add(new Obj());
            bad["objects"] = objs;
            Eq(LayoutRule.FromRecord("d-7", bad, "V21", out why), null, "an object without its fields");
            Contains(why, "cannot be read", "why");
        }

        // #1341: decisions.resetData is the consent a stop needs; a
        // download without it needs the layout check's digest.
        static void StopNeedsTheResetDataConsent()
        {
            Contains(ResetDataRule.Check(new Decisions { StopCpu = true }, "abc"), "decisions.stopCpu yes needs decisions.resetData yes: a load may return data blocks to their start values, retained ones included, and with the CPU in STOP TIA does so without asking (#1341); nothing was sent to the CPU", "stop, no consent");
            True(ResetDataRule.Check(new Decisions { StopCpu = true, ResetData = false }, "abc") != null, "stop, consent no");
            Contains(ResetDataRule.Check(new Decisions { StopCpu = false }, ""), "a download without decisions.resetData needs layoutDigest", "no consent, no digest");
            True(ResetDataRule.Check(null, "") != null, "no decisions, no digest");
            Eq(ResetDataRule.Check(new Decisions { StopCpu = false }, "abc"), null, "no consent with the digest: the download checks it");
            Eq(ResetDataRule.Check(new Decisions { StopCpu = true, ResetData = true }, ""), null, "stop with the consent");
            Eq(ResetDataRule.Check(new Decisions { ResetData = true }, ""), null, "consent alone");
        }

        // TIA's reinitialize check is answered only by the consent; and
        // without it a stop, a reinitialize or a load of every block is a
        // gate miss.
        static void ReinitializeNeedsTheConsent()
        {
            var q = new List<Question> { new Question { Id = QuestionIds.Reinitialize } };
            var u = new List<Question>();
            var d = new List<Question>();
            DownloadRule.Resolve(q, new Decisions { ResetData = true, StopCpu = true }, false, false, true, null, u, d);
            True(u.Count == 0 && d.Count == 0, "the consent and the stop: answered");
            // Its yes is StopPlcAndReinitialize: a reset without the stop's
            // consent never stops the CPU (Fable).
            DownloadRule.Resolve(q, new Decisions { ResetData = true }, false, false, true, null, u, d);
            Eq(u.Count, 1, "the consent without the stop: unanswered");
            DownloadRule.Resolve(q, new Decisions { StopCpu = true }, false, false, true, null, u, d);
            Eq(u.Count, 2, "the stop without the consent: unanswered");
            Eq(LayoutRule.SoftwareOption(true), "Software", "every block");
            Eq(LayoutRule.SoftwareOption(false), "SoftwareOnlyChanges", "only what changed");
            True(LayoutRule.GateMissQuestion(QuestionIds.StopCpu), "stop");
            True(LayoutRule.GateMissQuestion(QuestionIds.Reinitialize), "reinitialize");
            True(LayoutRule.GateMissQuestion(QuestionIds.AllBlocks), "every block");
            True(!LayoutRule.GateMissQuestion(QuestionIds.ConsistentDownload), "a consistent download is no miss (a body change asks it)");
            Contains(LayoutRule.GateMiss("reinitialize", "d-1"), "GATE MISS: TIA asked \"reinitialize\" although the data layout check found nothing changed since deploy d-1", "the miss's words");
        }
    }
}
