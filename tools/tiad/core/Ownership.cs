// Ownership.cs: which program blocks a software import may touch (#789).
//
// POST /v1/sessions/{id}/software generates blocks from SCL sources into
// one block group, the group the caller owns. TIA's GenerateBlocksFromSource
// does not stop at that group: on the TIA VM (2026-11-22) a source that
// declared FUNCTION_BLOCK "Main" silently replaced the project's own
// "Main" [OB1], outside the group, as an FB, and the reply (which lists
// the group) did not show it. On an adopted project that is the customer's
// program gone. So:
//
//   - before anything is imported, the agent reads the names every source
//     declares and refuses the whole call (409 foreign_block) when one
//     names a program block outside the group;
//   - the session is marked unusable before the import starts, and the
//     mark is cleared only when every block outside the group compares
//     the same afterwards (kind, number, group, modified date), whether
//     the import returned or threw. An unusable session is never saved,
//     archived for a deploy (so never downloaded) or imported into again:
//     only closed without saving;
//   - the glue fails the import when TIA generates a block outside the
//     group.
//
// A source that declares a data type (TYPE ... END_TYPE) is refused: TIA
// puts a generated type in the PLC data types' root, beside the project's
// own, so nothing yet tells nautilus's types from a customer's.

using System;
using System.Collections.Generic;
using System.Text;

namespace Tiad
{
    // Declared is one block a source declares: its kind (OB, FB, FC, DB,
    // UDT) and name, as the header spells it.
    public sealed class Declared
    {
        public string Kind = "", Name = "";
        // Source is the request's path of the source that declares it.
        public string Source = "";
    }

    public static class Ownership
    {
        static readonly string[] Heads = { "ORGANIZATION_BLOCK", "FUNCTION_BLOCK", "FUNCTION", "DATA_BLOCK", "TYPE" };
        static readonly string[] HeadKinds = { "OB", "FB", "FC", "DB", "UDT" };

        // Sources reads every block a source may declare, from its raw
        // bytes. It errs on the side of finding too much, since a missed
        // header is a customer's block replaced:
        //   - the bytes are read in every encoding they could be (a BOM's;
        //     UTF-8 when valid; Windows-1252; UTF-16 either way round when
        //     they hold NULs), and the names of all readings are taken;
        //   - a block keyword counts anywhere in the text, in any case, as
        //     a whole word (END_FUNCTION_BLOCK is none), comments and
        //     strings included, followed by any white space, line breaks
        //     too, then a name, "quoted" or bare.
        // A keyword with no name after it is skipped: the check after the
        // import (Changed) and the glue's own check of what TIA generated
        // stand behind this reading.
        public static List<Declared> Sources(byte[] raw)
        {
            var found = new List<Declared>();
            var seen = new HashSet<string>(StringComparer.Ordinal);
            foreach (string text in Readings(raw))
            {
                foreach (var d in Sources(text))
                {
                    if (seen.Add(d.Kind + "\u0000" + d.Name)) found.Add(d);
                }
            }
            return found;
        }

        // Sources reads the blocks a source's text declares (see above): it
        // reads the text a whole word at a time, and a word that is a block
        // keyword takes the name after it.
        public static List<Declared> Sources(string text)
        {
            var found = new List<Declared>();
            if (text == null) return found;
            int i = 0;
            while (i < text.Length)
            {
                if (!Word(text[i]))
                {
                    i++;
                    continue;
                }
                int e = i;
                while (e < text.Length && Word(text[e])) e++;
                int k = Keyword(text, i, e - i);
                if (k >= 0)
                {
                    int n = Gap(text, e);
                    // A quoted name may follow the keyword directly; a bare
                    // one needs a gap.
                    string name = n < text.Length && (n > e || text[n] == '"') ? Name(text, n) : null;
                    // A bare keyword is the next header, not a name.
                    if (name != null && text[n] != '"' && Keyword(text, n, name.Length) >= 0) name = null;
                    if (name != null) found.Add(new Declared { Kind = HeadKinds[k], Name = name });
                }
                i = e;
            }
            // A data type needs its END_TYPE: without one, TYPE was a word
            // in prose ("the type of pump"), not a header.
            if (!HasWord(text, "END_TYPE")) found.RemoveAll(delegate(Declared d) { return d.Kind == "UDT"; });
            return found;
        }

        // Gap skips white space and comments ((* *), /* */, // to the line's
        // end) from text[i] and returns where the next token starts.
        static int Gap(string text, int i)
        {
            while (i < text.Length)
            {
                if (Space(text[i]))
                {
                    i++;
                    continue;
                }
                string close = null;
                if (i + 1 < text.Length && text[i] == '(' && text[i + 1] == '*') close = "*)";
                else if (i + 1 < text.Length && text[i] == '/' && text[i + 1] == '*') close = "*/";
                if (close != null)
                {
                    int end = text.IndexOf(close, i + 2, StringComparison.Ordinal);
                    i = end < 0 ? text.Length : end + 2;
                    continue;
                }
                if (i + 1 < text.Length && text[i] == '/' && text[i + 1] == '/')
                {
                    while (i < text.Length && text[i] != '\n' && text[i] != '\r') i++;
                    continue;
                }
                break;
            }
            return i;
        }

        // HasWord is whether word stands in text as a whole word, any case.
        static bool HasWord(string text, string word)
        {
            int at = 0;
            while ((at = text.IndexOf(word, at, StringComparison.OrdinalIgnoreCase)) >= 0)
            {
                int end = at + word.Length;
                if ((at == 0 || !Word(text[at - 1])) && (end >= text.Length || !Word(text[end]))) return true;
                at = end;
            }
            return false;
        }

        // Keyword is the index of the block keyword that the word of
        // length len at text[i] is, in any case, or -1.
        static int Keyword(string text, int i, int len)
        {
            for (int k = 0; k < Heads.Length; k++)
            {
                if (Heads[k].Length == len && string.Compare(text, i, Heads[k], 0, len, StringComparison.OrdinalIgnoreCase) == 0) return k;
            }
            return -1;
        }

        static bool Word(char c)
        {
            return char.IsLetterOrDigit(c) || c == '_';
        }

        static bool Space(char c)
        {
            return char.IsWhiteSpace(c) || c == '\u00a0' || c == '\ufeff';
        }

        // Readings is every text the bytes could be.
        static List<string> Readings(byte[] raw)
        {
            var l = new List<string>();
            if (raw == null) return l;
            if (raw.Length >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF) l.Add(Encoding.UTF8.GetString(raw, 3, raw.Length - 3));
            if (raw.Length >= 2 && raw[0] == 0xFF && raw[1] == 0xFE) l.Add(Encoding.Unicode.GetString(raw, 2, raw.Length - 2));
            if (raw.Length >= 2 && raw[0] == 0xFE && raw[1] == 0xFF) l.Add(Encoding.BigEndianUnicode.GetString(raw, 2, raw.Length - 2));
            try
            {
                l.Add(new UTF8Encoding(false, true).GetString(raw));
            }
            catch (ArgumentException)
            {
                // Not UTF-8: the readings below stand.
            }
            l.Add(Windows1252(raw));
            if (Array.IndexOf(raw, (byte)0) >= 0)
            {
                l.Add(Encoding.Unicode.GetString(raw));
                l.Add(Encoding.BigEndianUnicode.GetString(raw));
            }
            return l;
        }

        // Cp1252High is Windows-1252's 0x80-0x9F (U+FFFD where undefined);
        // every other byte is its Latin-1 character. Decoded by hand: Mono
        // may lack the code page.
        static readonly string Cp1252High =
            "\u20ac\ufffd\u201a\u0192\u201e\u2026\u2020\u2021\u02c6\u2030\u0160\u2039\u0152\ufffd\u017d\ufffd" +
            "\ufffd\u2018\u2019\u201c\u201d\u2022\u2013\u2014\u02dc\u2122\u0161\u203a\u0153\ufffd\u017e\u0178";

        static string Windows1252(byte[] raw)
        {
            var b = new StringBuilder(raw.Length);
            foreach (byte x in raw) b.Append(x >= 0x80 && x <= 0x9F ? Cp1252High[x - 0x80] : (char)x);
            return b.ToString();
        }

        // Name is the block name at text[j]: "quoted", or a bare
        // identifier; null when there is none.
        static string Name(string text, int j)
        {
            if (j >= text.Length) return null;
            if (text[j] == '"')
            {
                int end = text.IndexOf('"', j + 1);
                if (end <= j + 1) return null;
                return text.Substring(j + 1, end - j - 1);
            }
            int e = j;
            while (e < text.Length && Word(text[e])) e++;
            if (e == j || char.IsDigit(text[j])) return null;
            return text.Substring(j, e - j);
        }

        // Owned is whether a block in blockGroup (its path from the program
        // blocks' root, "" for the root, "a/b" for a user group b in a)
        // is in group or one of group's user groups.
        public static bool Owned(string blockGroup, string group)
        {
            if (blockGroup == null) blockGroup = "";
            if (string.Equals(blockGroup, group, StringComparison.OrdinalIgnoreCase)) return true;
            return blockGroup.Length > group.Length && blockGroup[group.Length] == '/' &&
                string.Compare(blockGroup, 0, group, 0, group.Length, StringComparison.OrdinalIgnoreCase) == 0;
        }

        // Foreign is every program block outside group, by group path
        // ("" the root first) and then name: what a download loads besides
        // the group's own blocks (#1326).
        public static List<ProjectBlock> Foreign(List<ProjectBlock> blocks, string group)
        {
            var l = new List<ProjectBlock>();
            foreach (var b in blocks)
            {
                if (!Owned(b.Group, group)) l.Add(b);
            }
            l.Sort(delegate(ProjectBlock a, ProjectBlock b)
            {
                int c = string.CompareOrdinal(a.Group ?? "", b.Group ?? "");
                return c != 0 ? c : string.CompareOrdinal(a.Name, b.Name);
            });
            return l;
        }

        // KindOf is a project block's kind as the agent reports blocks
        // (OB, FB, FC, DB, UDT), or its Openness type name for one tiad has
        // no kind for: never left out.
        public static string KindOf(ProjectBlock b)
        {
            string k = TiaNames.BlockKind(b.Type);
            return k != "" ? k : b.Type;
        }

        // Conflicts is every program block outside group that a declared
        // block (not a data type) names, in TIA's case-insensitive block
        // names (OrdinalIgnoreCase: assumed to be TIA's rule, not proven),
        // each with the sources that declare it: "<source>: <block> in
        // <group>".
        public static List<string> Conflicts(List<Declared> declared, List<ProjectBlock> blocks, string group)
        {
            var by = new Dictionary<string, List<string>>(StringComparer.OrdinalIgnoreCase);
            foreach (var d in declared)
            {
                if (d.Kind == "UDT") continue;
                List<string> l;
                if (!by.TryGetValue(d.Name, out l)) by[d.Name] = l = new List<string>();
                if (!l.Contains(d.Source)) l.Add(d.Source);
            }
            var out_ = new List<string>();
            foreach (var b in blocks)
            {
                if (Owned(b.Group, group)) continue;
                List<string> srcs;
                if (by.TryGetValue(b.Name, out srcs)) out_.Add(string.Join(", ", srcs.ToArray()) + " declares " + Label(b) + " in " + GroupName(b.Group));
            }
            return out_;
        }

        // Refusal is the 409 for conflicts: each block by the source that
        // declares it, its label and group, and the fix.
        public static TiadException Refusal(List<string> conflicts, string group)
        {
            return new TiadException(409, Kinds.ForeignBlock,
                string.Join("; ", conflicts.ToArray()) + ", outside group \"" + group +
                "\": importing it would replace that block. Nothing was imported. Rename the block in the source (a prefix the project does not use)");
        }

        // TypesRefused is the refusal of a source that declares a data type:
        // TIA puts a generated type in the PLC data types' root (the TIA VM,
        // 2026-11-22), beside the project's own, so nothing yet tells
        // nautilus's types from a customer's. Nothing was imported.
        public static TiadException TypesRefused(List<Declared> declared)
        {
            var named = new List<string>();
            foreach (var d in declared) if (d.Kind == "UDT") named.Add(d.Source + " declares data type \"" + d.Name + "\"");
            if (named.Count == 0) return null;
            return new TiadException(400, Kinds.BadRequest,
                string.Join("; ", named.ToArray()) + ": data types are not imported yet (TIA puts them beside the project's own, so tiad cannot keep a customer's apart). Nothing was imported");
        }

        // GroupRefused refuses a group name with a path separator: a group
        // is one user group under the program blocks' root.
        public static TiadException GroupRefused(string group)
        {
            if (group.IndexOf('/') < 0 && group.IndexOf('\\') < 0) return null;
            return TiadException.BadRequest("group " + Json.Quote(group) + " holds a path separator: name one user group under the program blocks' root");
        }

        // Changed lists every block outside group that the import changed:
        // gone, or there with another kind or number, or new. Read from
        // the blocks before and after the import.
        public static List<string> Changed(List<ProjectBlock> before, List<ProjectBlock> after, string group)
        {
            var was = new Dictionary<string, ProjectBlock>(StringComparer.OrdinalIgnoreCase);
            foreach (var b in before) if (!Owned(b.Group, group)) was[b.Name] = b;
            var now = new Dictionary<string, ProjectBlock>(StringComparer.OrdinalIgnoreCase);
            foreach (var b in after) if (!Owned(b.Group, group)) now[b.Name] = b;
            var changed = new List<string>();
            foreach (var kv in was)
            {
                ProjectBlock n;
                if (!now.TryGetValue(kv.Key, out n)) changed.Add(Label(kv.Value) + " in " + GroupName(kv.Value.Group) + " is gone");
                else if (n.Type != kv.Value.Type || n.Number != kv.Value.Number || !string.Equals(n.Group ?? "", kv.Value.Group ?? "", StringComparison.OrdinalIgnoreCase))
                    changed.Add(Label(kv.Value) + " in " + GroupName(kv.Value.Group) + " is now " + Label(n) + " in " + GroupName(n.Group));
                else if (n.Modified != kv.Value.Modified)
                    changed.Add(Label(kv.Value) + " in " + GroupName(kv.Value.Group) + " was modified");
            }
            foreach (var kv in now)
            {
                if (!was.ContainsKey(kv.Key)) changed.Add(Label(kv.Value) + " in " + GroupName(kv.Value.Group) + " is new");
            }
            changed.Sort(StringComparer.Ordinal);
            return changed;
        }

        // ForeignChangedError is the failure of an import that changed
        // blocks outside its group: the session is left unusable.
        public static TiadException ForeignChangedError(List<string> changed, string group)
        {
            return new TiadException(500, Kinds.OperationFailed,
                "the import changed program blocks outside group \"" + group + "\" (" + string.Join("; ", changed.ToArray()) +
                "): this session can no longer be saved, archived for a deploy or downloaded. Close it without saving");
        }

        // Unusable is the refusal of what after an import changed blocks
        // outside its group.
        public static TiadException Unusable(string what)
        {
            return new TiadException(409, Kinds.UncleanSession,
                "refusing " + what + ": an import on this session left the project as tiad did not ask (blocks outside its group changed, or a cyclic interrupt OB is not as asked). Close it without saving");
        }

        static string Label(ProjectBlock b)
        {
            string l = TiaNames.BlockLabel(b);
            return "\"" + b.Name + "\"" + (l == null ? " (" + b.Type + ")" : l.Substring(b.Name.Length));
        }

        static string GroupName(string g)
        {
            return string.IsNullOrEmpty(g) ? "the program blocks' root" : "group \"" + g + "\"";
        }
    }
}
