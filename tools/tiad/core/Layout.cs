// Layout.cs: the data layout gate (#1341, siemens-ide.md section 3.5,
// "data layout").
//
// A download that loads only what changed (DownloadOptions
// .SoftwareOnlyChanges) keeps the CPU's data, retained or not, as long as
// no data block's layout changed (bench 1510SP, 2026-11-24). When one did,
// TIA asks to reinitialize with the CPU in RUN, but naming no block, and
// with the CPU in STOP it asks nothing and resets that block, retained
// values included. So the agent must know, before anything reaches the
// CPU, whether any layout changed since the load the CPU holds.
//
// The signal is TIA's own: each program block's and PLC data type's XML
// export, the root object's <AttributeList> (its interface with every
// member and start value, its number, memory layout, retain settings and
// the FB an instance DB belongs to). Measured on the TIA VM (2026-11-23):
// it stays the same over byte-identical re-imports and a body change, and
// moves exactly where a layout changed, a UDT's holders and an FB's
// instance DB included (TIA writes their members out). TIA's dates are no
// signal: every import recreates every block and moves them all.
//
// The rule: any object of the PLC, nautilus's or not, whose hash moved,
// or that was removed or could not be read, needs the caller's consent
// (decisions.resetData). An object whose hash did not move passes, which
// covers a body-only change. So does an added object of a kind
// AddedKeepsData names (#1347): no object that is unchanged can use it
// (its own hash would have moved), and a new data block holds no values
// to lose; each kind is there only once the bench showed a load that
// adds it keeps the CPU's data with the CPU in RUN and in STOP. The
// baseline is the layout recorded with the deploy the CPU verifiably
// holds (its record is the newest load for the CPU, and a verify by this
// agent found it equal), or the session's adopt baseline (LayoutCalls.cs);
// without one, or after a TIA upgrade or a change of this hashing scheme,
// every download needs the consent.
//
// The layout is read after the full compile, never earlier: blocks just
// imported are inconsistent until then, and TIA exports no inconsistent
// object (TIA VM, 2026-11-24), so a read between import and compile finds
// nautilus's own blocks unreadable.

using System;
using System.Collections.Generic;
using System.Security.Cryptography;
using System.Text;
using System.Text.RegularExpressions;
using System.Xml;

namespace Tiad
{
    // LayoutObject is one program block or PLC data type as the layout
    // gate reads it.
    public sealed class LayoutObject
    {
        public string Name = "";
        // Kind is "block" or "type"; Type is Openness's type name
        // (GlobalDB, InstanceDB, FB, OB, PlcStruct, ...).
        public string Kind = "", Type = "";
        // Hash is the SHA-256 of Type and the export's root AttributeList,
        // "" when the object could not be read (Unreadable says why).
        public string Hash = "";
        public string Unreadable = "";
        // Refs are the names this object's layout uses: every quoted data
        // type of its interface (a UDT, an FB of a multi-instance) and the
        // FB an instance DB belongs to.
        public List<string> Refs = new List<string>();
    }

    // LayoutChange is one object whose layout differs from the baseline.
    public sealed class LayoutChange
    {
        public const string Changed = "changed", Added = "added", Removed = "removed", UnreadableNow = "unreadable";
        public string Name = "", Kind = "", Type = "", Change = "";

        public Obj ToJson()
        {
            var o = new Obj();
            o["name"] = Name;
            o["kind"] = Kind;
            o["type"] = Type;
            o["change"] = Change;
            return o;
        }
    }

    // LayoutBaseline is the layout a deploy record holds, or a session's
    // adopt baseline (Deploy is then its AdoptBaseline id).
    public sealed class LayoutBaseline
    {
        public string Deploy = "", Scheme = "", Tia = "";
        public List<LayoutObject> Objects = new List<LayoutObject>();
    }

    // LayoutDiff is what the gate found: the layout route's answer, and
    // what a download checks.
    public sealed class LayoutDiff
    {
        public string Scheme = "", Tia = "";
        // Baseline is the deploy whose layout was compared, "" for none;
        // NoBaseline then says why.
        public string Baseline = "", NoBaseline = "";
        // BaselineFrom is "deploy" (a deploy record's layout) or "adopted"
        // (the session's adopt baseline), "" for none.
        public string BaselineFrom = "";
        public bool Hardware;
        public List<LayoutChange> Changes = new List<LayoutChange>();
        // DataBlocks are the data blocks whose values a load may lose: each
        // the project holds whose own layout changed, or whose layout uses
        // a changed object, however deep; then each the load removes
        // (Fable). AllDataBlocks is set
        // when the load may reset every one (no baseline, a hardware load).
        public List<string> DataBlocks = new List<string>();
        public bool AllDataBlocks;
        public bool ResetDataNeeded;
        public string Why = "";
        // Digest names this exact layout and baseline: a download without
        // the consent must send the digest the caller was shown.
        public string Digest = "";

        public Obj ToJson()
        {
            var o = new Obj();
            o["scheme"] = Scheme;
            o["tia"] = Tia;
            o["baseline"] = Baseline == "" ? null : Baseline;
            if (NoBaseline != "") o["noBaseline"] = NoBaseline;
            if (BaselineFrom != "") o["baselineFrom"] = BaselineFrom;
            var cs = new List<object>();
            foreach (var c in Changes) cs.Add(c.ToJson());
            o["changes"] = cs;
            o["dataBlocks"] = DataBlocks.ToArray();
            o["allDataBlocks"] = AllDataBlocks;
            o["resetDataNeeded"] = ResetDataNeeded;
            o["why"] = Why;
            o["digest"] = Digest;
            return o;
        }
    }

    public static class LayoutRule
    {
        // Scheme names how the hashes are made. Any change to what is
        // hashed bumps it: a baseline of another scheme is no baseline,
        // so the next download needs the consent, and says why.
        public const string Scheme = "attributes-v1";

        // VerifyMaxAge is how long a verify that found the CPU equal to its
        // last deploy stands as the baseline's proof.
        public static readonly TimeSpan VerifyMaxAge = TimeSpan.FromHours(1);

        public const string ConsentSentence = "decisions.resetData yes is the consent to it";

        // AddedKeepsData are the kinds (Openness type names) an added
        // object of which needs no consent: a load that adds one keeps the
        // CPU's data, in RUN and in STOP (bench 1510SP, #1347). Any other
        // kind added needs the consent.
        public static readonly string[] AddedKeepsData = { "OB", "FB", "FC", "GlobalDB", "InstanceDB", "PlcStruct" };

        // NeedsConsent is whether a change needs decisions.resetData.
        public static bool NeedsConsent(LayoutChange c)
        {
            return !(c.Change == LayoutChange.Added && Array.IndexOf(AddedKeepsData, c.Type) >= 0);
        }

        // BaselineLabel names a baseline in words: "deploy <id>", or the
        // adopted project.
        public static string BaselineLabel(string id)
        {
            if (id != null && id.StartsWith(AdoptBaseline.IdPrefix, StringComparison.Ordinal)) return "the adopted project (" + id + ")";
            return "deploy " + id;
        }

        // ObjectsHash is the SHA-256 of every object's name, type and hash,
        // in name order: what names an adopt baseline.
        public static string ObjectsHash(List<LayoutObject> now)
        {
            var sorted = new List<LayoutObject>(now);
            sorted.Sort(delegate(LayoutObject a, LayoutObject b) { return string.CompareOrdinal(Key(a.Name), Key(b.Name)); });
            var sb = new StringBuilder();
            foreach (var o in sorted) sb.Append(o.Name).Append('\t').Append(o.Type).Append('\t').Append(o.Hash).Append('\n');
            return Sha256Hex(sb.ToString());
        }

        // SoftwareOption is the DownloadOptions member a software load
        // uses: Software loads every block (TIA stops the CPU and resets
        // every data block), SoftwareOnlyChanges only what changed.
        public static string SoftwareOption(bool everyBlock)
        {
            return everyBlock ? "Software" : "SoftwareOnlyChanges";
        }

        static readonly Regex Quoted = new Regex("\"([^\"]+)\"");

        // FromExport reads one object's XML export: the hash of its type
        // and its root object's AttributeList, as the XML parser reads it
        // back (whitespace kept, nothing else left out: what is stripped
        // is where a change could hide), and the names its layout uses.
        // An export it cannot read makes the object unreadable.
        public static LayoutObject FromExport(string name, string kind, string type, string xml)
        {
            var o = new LayoutObject { Name = name, Kind = kind, Type = type };
            try
            {
                var doc = new XmlDocument { PreserveWhitespace = true, XmlResolver = null };
                doc.LoadXml(xml);
                XmlElement root = null;
                foreach (XmlNode n in doc.DocumentElement.ChildNodes)
                {
                    var e = n as XmlElement;
                    if (e != null && e.Name.StartsWith("SW.", StringComparison.Ordinal))
                    {
                        root = e;
                        break;
                    }
                }
                XmlElement attrs = null;
                if (root != null)
                {
                    foreach (XmlNode n in root.ChildNodes)
                    {
                        var e = n as XmlElement;
                        if (e != null && e.Name == "AttributeList")
                        {
                            attrs = e;
                            break;
                        }
                    }
                }
                if (attrs == null)
                {
                    o.Unreadable = "its export has no object attribute list";
                    return o;
                }
                o.Hash = Sha256Hex(type + "\n" + attrs.OuterXml);
                var refs = new SortedSet<string>(StringComparer.OrdinalIgnoreCase);
                foreach (XmlNode n in attrs.GetElementsByTagName("*"))
                {
                    var e = n as XmlElement;
                    if (e == null) continue;
                    if (e.LocalName == "InstanceOfName" && e.InnerText != "") refs.Add(e.InnerText);
                    string dt = e.GetAttribute("Datatype");
                    if (dt == "") continue;
                    foreach (Match m in Quoted.Matches(dt)) refs.Add(m.Groups[1].Value);
                }
                refs.Remove(name);
                o.Refs = new List<string>(refs);
            }
            catch (XmlException e)
            {
                o.Hash = "";
                o.Unreadable = "its export is not XML TIA could have written (" + e.Message + ")";
            }
            return o;
        }

        public static string Sha256Hex(string s)
        {
            using (var h = SHA256.Create())
            {
                byte[] b = h.ComputeHash(new UTF8Encoding(false).GetBytes(s));
                var sb = new StringBuilder(b.Length * 2);
                foreach (byte x in b) sb.Append(x.ToString("x2", System.Globalization.CultureInfo.InvariantCulture));
                return sb.ToString();
            }
        }

        // IsDataBlock is whether an Openness type name is a data block's
        // (GlobalDB, InstanceDB, ArrayDB, ...).
        public static bool IsDataBlock(string type)
        {
            return type != null && type.EndsWith("DB", StringComparison.Ordinal);
        }

        static string Key(string name)
        {
            return name.ToLowerInvariant();
        }

        // Diff compares now with the baseline (null: none, noBaseline says
        // why) and decides whether the download needs the consent.
        public static LayoutDiff Diff(List<LayoutObject> now, LayoutBaseline baseline, string noBaseline, string tia, bool hardware)
        {
            var d = new LayoutDiff { Scheme = Scheme, Tia = tia ?? "", Hardware = hardware };
            var sorted = new List<LayoutObject>(now);
            sorted.Sort(delegate(LayoutObject a, LayoutObject b) { return string.CompareOrdinal(Key(a.Name), Key(b.Name)); });
            if (baseline == null)
            {
                d.NoBaseline = noBaseline == null || noBaseline == "" ? "no layout baseline" : noBaseline;
            }
            else
            {
                d.Baseline = baseline.Deploy;
                d.BaselineFrom = baseline.Deploy.StartsWith(AdoptBaseline.IdPrefix, StringComparison.Ordinal) ? "adopted" : "deploy";
                var was = new Dictionary<string, LayoutObject>();
                foreach (var b in baseline.Objects) was[Key(b.Name)] = b;
                var seen = new HashSet<string>();
                foreach (var o in sorted)
                {
                    string k = Key(o.Name);
                    seen.Add(k);
                    LayoutObject b;
                    string change = null;
                    if (o.Hash == "") change = LayoutChange.UnreadableNow;
                    else if (!was.TryGetValue(k, out b)) change = LayoutChange.Added;
                    // A baseline object that was unreadable ("") differs
                    // from any hash read now.
                    else if (b.Hash != o.Hash) change = LayoutChange.Changed;
                    if (change != null) d.Changes.Add(new LayoutChange { Name = o.Name, Kind = o.Kind, Type = o.Type, Change = change });
                }
                var gone = new List<LayoutObject>();
                foreach (var b in baseline.Objects)
                {
                    if (!seen.Contains(Key(b.Name))) gone.Add(b);
                }
                gone.Sort(delegate(LayoutObject a, LayoutObject b) { return string.CompareOrdinal(Key(a.Name), Key(b.Name)); });
                foreach (var b in gone) d.Changes.Add(new LayoutChange { Name = b.Name, Kind = b.Kind, Type = b.Type, Change = LayoutChange.Removed });
            }
            // An object read as unreadable (a software unit, an export that
            // failed) has no known layout or references: any data block may
            // depend on it, so every one is named (naut-lead, #1348).
            bool unknown = d.Changes.Exists(delegate(LayoutChange c) { return c.Change == LayoutChange.UnreadableNow; });
            d.AllDataBlocks = baseline == null || hardware || unknown;
            d.DataBlocks = d.AllDataBlocks ? AllDataBlockNames(sorted) : Reached(sorted, d.Changes);
            if (baseline != null)
            {
                foreach (var c in d.Changes)
                {
                    if (c.Change == LayoutChange.Removed && c.Kind == "block" && IsDataBlock(c.Type)) d.DataBlocks.Add(c.Name);
                }
            }
            int needing = d.Changes.FindAll(NeedsConsent).Count;
            d.ResetDataNeeded = baseline == null || hardware || needing > 0;
            string label = baseline == null ? "" : BaselineLabel(baseline.Deploy);
            if (hardware) d.Why = "the download loads the hardware configuration, and what that does to the CPU's data is not measured: every data block may return to its start values, retained ones included; " + ConsentSentence;
            else if (baseline == null) d.Why = "no layout baseline (" + d.NoBaseline + "), so the load replaces the whole program: every data block returns to its start values, retained ones included; " + ConsentSentence;
            else if (needing > 0) d.Why = "the data layout changed since " + label + " (" + needing + " object(s)): the data blocks it reaches may return to their start values, retained ones included, and with the CPU in STOP TIA resets them without asking; " + ConsentSentence;
            else if (d.Changes.Count > 0) d.Why = "the data layout of everything the CPU holds is unchanged since " + label + ", which the CPU compared equal to, and the load only adds " + d.Changes.Count + " object(s): the load keeps the CPU's data";
            else d.Why = "the data layout is unchanged since " + label + ", which the CPU compared equal to: the load keeps the CPU's data";
            d.Digest = Digest(d, sorted);
            return d;
        }

        static List<string> AllDataBlockNames(List<LayoutObject> sorted)
        {
            var l = new List<string>();
            foreach (var o in sorted)
            {
                if (o.Kind == "block" && IsDataBlock(o.Type)) l.Add(o.Name);
            }
            return l;
        }

        // Reached is every data block the project holds whose own layout
        // changed, or whose layout uses a changed, added or unreadable
        // object, however deep (by the names each export uses).
        static List<string> Reached(List<LayoutObject> sorted, List<LayoutChange> changes)
        {
            var hit = new HashSet<string>();
            foreach (var c in changes)
            {
                if (c.Change != LayoutChange.Removed) hit.Add(Key(c.Name));
            }
            var byName = new Dictionary<string, LayoutObject>();
            foreach (var o in sorted) byName[Key(o.Name)] = o;
            var l = new List<string>();
            foreach (var o in sorted)
            {
                if (o.Kind != "block" || !IsDataBlock(o.Type)) continue;
                bool added = false;
                foreach (var c in changes)
                {
                    if (c.Change == LayoutChange.Added && Key(c.Name) == Key(o.Name)) added = true;
                }
                // A data block that is new holds no values to lose.
                if (added) continue;
                if (hit.Contains(Key(o.Name)) || Reaches(o, hit, byName, new HashSet<string>())) l.Add(o.Name);
            }
            return l;
        }

        static bool Reaches(LayoutObject o, HashSet<string> hit, Dictionary<string, LayoutObject> byName, HashSet<string> visiting)
        {
            if (!visiting.Add(Key(o.Name))) return false;
            foreach (string r in o.Refs)
            {
                string k = Key(r);
                if (hit.Contains(k)) return true;
                LayoutObject next;
                if (byName.TryGetValue(k, out next) && Reaches(next, hit, byName, visiting)) return true;
            }
            return false;
        }

        // Digest is the SHA-256 of the scheme, the TIA version, the
        // baseline, the hardware flag and every object's name, type and
        // hash: any change to any of them between the caller's read and
        // the download changes it.
        static string Digest(LayoutDiff d, List<LayoutObject> sorted)
        {
            var sb = new StringBuilder();
            sb.Append(d.Scheme).Append('\n').Append(d.Tia).Append('\n').Append(d.Baseline).Append('\n').Append(d.Hardware ? "hardware" : "software").Append('\n');
            foreach (var o in sorted) sb.Append(o.Name).Append('\t').Append(o.Type).Append('\t').Append(o.Hash).Append('\n');
            return Sha256Hex(sb.ToString());
        }

        // ToRecord is the layout as a deploy record's intent line keeps it.
        public static Obj ToRecord(List<LayoutObject> now, string tia)
        {
            var o = new Obj();
            o["scheme"] = Scheme;
            o["tia"] = tia ?? "";
            var l = new List<object>();
            foreach (var x in now)
            {
                var e = new Obj();
                e["name"] = x.Name;
                e["kind"] = x.Kind;
                e["type"] = x.Type;
                e["hash"] = x.Hash;
                l.Add(e);
            }
            o["objects"] = l;
            return o;
        }

        // FromRecord reads a deploy record's layout as the baseline for a
        // download read by TIA tia, or returns null and why not.
        public static LayoutBaseline FromRecord(string deploy, object layout, string tia, out string why)
        {
            why = null;
            var o = layout as IDictionary<string, object>;
            if (o == null)
            {
                why = "deploy " + deploy + " recorded no data layout (it was made before the layout gate, #1341)";
                return null;
            }
            object v;
            string scheme = o.TryGetValue("scheme", out v) ? v as string : null;
            string rtia = o.TryGetValue("tia", out v) ? v as string : null;
            var list = o.TryGetValue("objects", out v) ? v as System.Collections.IList : null;
            if (scheme == null || rtia == null || list == null)
            {
                why = "deploy " + deploy + "'s data layout record cannot be read";
                return null;
            }
            if (scheme != Scheme)
            {
                why = "deploy " + deploy + "'s data layout was hashed with scheme " + Json.Quote(scheme) + ", and this agent hashes with " + Json.Quote(Scheme) + ": a change of the scheme invalidates every recorded layout";
                return null;
            }
            if (rtia != (tia ?? ""))
            {
                why = "deploy " + deploy + "'s data layout was read by TIA " + Json.Quote(rtia) + ", and this agent runs TIA " + Json.Quote(tia ?? "") + ": a TIA upgrade invalidates every recorded layout";
                return null;
            }
            var b = new LayoutBaseline { Deploy = deploy, Scheme = scheme, Tia = rtia };
            foreach (object x in list)
            {
                var e = x as IDictionary<string, object>;
                string name = e != null && e.TryGetValue("name", out v) ? v as string : null;
                string kind = e != null && e.TryGetValue("kind", out v) ? v as string : null;
                string type = e != null && e.TryGetValue("type", out v) ? v as string : null;
                string hash = e != null && e.TryGetValue("hash", out v) ? v as string : null;
                if (name == null || kind == null || type == null || hash == null)
                {
                    why = "deploy " + deploy + "'s data layout record cannot be read";
                    return null;
                }
                b.Objects.Add(new LayoutObject { Name = name, Kind = kind, Type = type, Hash = hash });
            }
            return b;
        }

        // GateMiss is the refusal of a download TIA asked to stop the CPU
        // or reinitialize data blocks although the gate found the layout
        // unchanged and the caller gave no consent: the gate missed a
        // change, so the download is cancelled at that check.
        public static string GateMiss(string question, string baseline)
        {
            return "GATE MISS: TIA asked " + Json.Quote(question) + " although the data layout check found no change that needs it since " + BaselineLabel(baseline) +
                " (no decisions.resetData was given, so tiad answers no such check): the download was cancelled at that check. The check missed a change TIA sees; the CPU's data is as it was if TIA asked before loading. Report it (#1341), and load with decisions.resetData if the reset is acceptable";
        }

        // GateMissQuestion is whether a check TIA asks during a
        // download that loads without the consent means the gate missed a
        // change: TIA stops the CPU, reinitializes data blocks, or loads
        // every block.
        public static bool GateMissQuestion(string id)
        {
            return id == QuestionIds.StopCpu || id == QuestionIds.Reinitialize || id == QuestionIds.AllBlocks;
        }
    }
}
