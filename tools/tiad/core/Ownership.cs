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
//   - after the import, it compares every block outside the group with
//     what was there before. A change (a header the reading missed) makes
//     the session unusable: no save, no deploy archive, so no download,
//     and no further import. Only closing it without saving is left.
//
// Data types (TYPE ... END_TYPE) are not checked yet: where TIA puts a
// type a source generates is unproven, so no rule is guessed for them.

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
    }

    public static class Ownership
    {
        static readonly string[] Heads = { "ORGANIZATION_BLOCK", "FUNCTION_BLOCK", "FUNCTION", "DATA_BLOCK", "TYPE" };
        static readonly string[] HeadKinds = { "OB", "FB", "FC", "DB", "UDT" };

        // Sources reads the blocks every header in an SCL source declares:
        // a line that starts (after blanks and comments) with a block
        // keyword in any case, then a name, "quoted" or bare. It errs on
        // the side of finding too much: a header inside a comment that
        // spans lines is still read, which can only refuse more.
        public static List<Declared> Sources(string text)
        {
            var found = new List<Declared>();
            if (text == null) return found;
            string clean = StripComments(text);
            foreach (string raw in clean.Split('\n'))
            {
                string line = raw.TrimEnd('\r');
                int i = 0;
                while (i < line.Length && Blank(line[i])) i++;
                for (int k = 0; k < Heads.Length; k++)
                {
                    string h = Heads[k];
                    if (line.Length - i < h.Length) continue;
                    if (string.Compare(line, i, h, 0, h.Length, StringComparison.OrdinalIgnoreCase) != 0) continue;
                    int j = i + h.Length;
                    if (j >= line.Length || !Blank(line[j])) continue;
                    while (j < line.Length && Blank(line[j])) j++;
                    string name = Name(line, j);
                    if (name == null) continue;
                    found.Add(new Declared { Kind = HeadKinds[k], Name = name });
                    break;
                }
            }
            return found;
        }

        // StripComments blanks // line comments and (* *) and /* */ block
        // comments, keeping line breaks so lines stay lines. Text inside
        // "quoted" names and 'strings' is kept as it is.
        static string StripComments(string s)
        {
            var b = new StringBuilder(s.Length);
            int i = 0;
            while (i < s.Length)
            {
                char c = s[i];
                if (c == '"' || c == '\'')
                {
                    int end = s.IndexOf(c, i + 1);
                    int nl = s.IndexOf('\n', i + 1);
                    if (end < 0 || (nl >= 0 && nl < end)) end = nl < 0 ? s.Length - 1 : nl - 1;
                    b.Append(s, i, end - i + 1);
                    i = end + 1;
                    continue;
                }
                if (c == '/' && i + 1 < s.Length && s[i + 1] == '/')
                {
                    while (i < s.Length && s[i] != '\n') i++;
                    continue;
                }
                string close = null;
                if (c == '(' && i + 1 < s.Length && s[i + 1] == '*') close = "*)";
                else if (c == '/' && i + 1 < s.Length && s[i + 1] == '*') close = "*/";
                if (close != null)
                {
                    int end = s.IndexOf(close, i + 2, StringComparison.Ordinal);
                    int stop = end < 0 ? s.Length : end + 2;
                    for (int k = i; k < stop; k++) if (s[k] == '\n') b.Append('\n');
                    b.Append(' ');
                    i = stop;
                    continue;
                }
                b.Append(c);
                i++;
            }
            return b.ToString();
        }

        static bool Blank(char c)
        {
            return c == ' ' || c == '\t';
        }

        // Name is the block name at line[j]: "quoted", or a bare
        // identifier; null when there is none.
        static string Name(string line, int j)
        {
            if (j >= line.Length) return null;
            if (line[j] == '"')
            {
                int end = line.IndexOf('"', j + 1);
                if (end <= j + 1) return null;
                return line.Substring(j + 1, end - j - 1);
            }
            int e = j;
            while (e < line.Length && (char.IsLetterOrDigit(line[e]) || line[e] == '_')) e++;
            if (e == j || char.IsDigit(line[j])) return null;
            return line.Substring(j, e - j);
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

        // Conflicts is every program block outside group that a declared
        // block (not a data type) names, in TIA's case-insensitive block
        // names, each once.
        public static List<ProjectBlock> Conflicts(List<Declared> declared, List<ProjectBlock> blocks, string group)
        {
            var names = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            foreach (var d in declared) if (d.Kind != "UDT") names.Add(d.Name);
            var out_ = new List<ProjectBlock>();
            foreach (var b in blocks)
            {
                if (Owned(b.Group, group)) continue;
                if (names.Contains(b.Name)) out_.Add(b);
            }
            return out_;
        }

        // Refusal is the 409 for conflicts: each block by its label and
        // group, and the fix.
        public static TiadException Refusal(List<ProjectBlock> conflicts, string group)
        {
            var named = new List<string>();
            foreach (var b in conflicts) named.Add(Label(b) + " in " + GroupName(b.Group));
            return new TiadException(409, Kinds.ForeignBlock,
                "a source declares " + string.Join(", ", named.ToArray()) + ", outside group \"" + group +
                "\": importing it would replace that block. Nothing was imported. Rename the block in the source (a prefix the project does not use)");
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
                "refusing " + what + ": an import on this session changed program blocks outside its group. Close it without saving");
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
