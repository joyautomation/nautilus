// OwnershipTest.cs: which program blocks a software import may touch
// (core/Ownership.cs, #789): the names a source declares, the refusal of
// one outside the import's group, and the check after the import.

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        static string Heads(string text)
        {
            var l = new List<string>();
            foreach (var d in Ownership.Sources(text)) l.Add(d.Kind + ":" + d.Name);
            return string.Join(",", l.ToArray());
        }

        static string HeadsOf(byte[] raw)
        {
            var l = new List<string>();
            foreach (var d in Ownership.Sources(raw)) l.Add(d.Kind + ":" + d.Name);
            return string.Join(",", l.ToArray());
        }

        // OwnershipReadsEveryHeader: quoted and bare names, any case, several
        // blocks per file and per line, any line break or white space
        // between keyword and name (the name on the next line too); a
        // keyword in a comment counts too (it can only refuse more); the
        // instance line of a DATA_BLOCK and END_ words are no headers.
        static void OwnershipReadsEveryHeader()
        {
            Eq(Heads("FUNCTION_BLOCK \"Main\"\r\nVERSION : 0.1\r\nBEGIN\r\nEND_FUNCTION_BLOCK\r\nDATA_BLOCK \"Main_DB\"\r\n\"Main\"\r\nBEGIN\r\nEND_DATA_BLOCK\r\n"),
                "FB:Main,DB:Main_DB", "a writer FB and its instance DB");
            Eq(Heads("function_block main\nEND_FUNCTION_BLOCK\n"), "FB:main", "lower case, bare name");
            Eq(Heads("\t FUNCTION \"Helper\" : Void\r\nEND_FUNCTION\r\n  TYPE \"Recipe\"\nEND_TYPE\n"), "FC:Helper,UDT:Recipe", "a function and a type");
            Eq(Heads("ORGANIZATION_BLOCK \"OB_main\"\nTITLE = \"Cyclic interrupt\"\n"), "OB:OB_main", "an OB, its title no header");
            Eq(Heads("END_FUNCTION FUNCTION_BLOCK \"Main\"\n"), "FB:Main", "a header after an END_ word on one line");
            Eq(Heads("FUNCTION_BLOCK\r\n  \"Main\"\r"), "FB:Main", "the name on the next line");
            Eq(Heads("BEGIN\rFUNCTION_BLOCK \"Main\"\rEND_FUNCTION_BLOCK"), "FB:Main", "CR-only line ends");
            Eq(Heads("{ S7_Optimized_Access := 'TRUE' }\f\u00a0FUNCTION_BLOCK\u00a0\"Main\""), "FB:Main", "a pragma, a form feed and no-break spaces");
            Eq(Heads("REGION calc (* start\nFUNCTION_BLOCK \"Main\"\n// FUNCTION_BLOCK \"InALineComment\"\n"), "FB:Main,FB:InALineComment", "headers in comments count");
            Eq(Heads("FUNCTION_BLOCKX \"No\"\nFUNCTION_BLOCKFUNCTION_BLOCK \"No\"\nFUNCTION_BLOCK\nFUNCTION_BLOCK 1x\nEND_FUNCTION_BLOCK \"No\"\nMY_FUNCTION \"No\"\n"), "", "no header");
            Eq(Heads("FUNCTION_BLOCK \"Pump // not a comment\"\n"), "FB:Pump // not a comment", "a quoted name is kept whole");
            Eq(Heads("FUNCTION_BLOCK (* c *) \"A\"\nFUNCTION_BLOCK /* c */ B\nFUNCTION_BLOCK // c\n  \"C\"\nFUNCTION_BLOCK\"D\"\nFUNCTION_BLOCK(*x*)E\n"),
                "FB:A,FB:B,FB:C,FB:D,FB:E", "comments between keyword and name; a quote right after the keyword");
            Eq(Heads("// the type of pump\nx := 'Type conflict';\n"), "", "TYPE in prose, with no END_TYPE, is no data type");
            Eq(Heads("type \"R\"\nSTRUCT\nEND_STRUCT;\nend_type\n"), "UDT:R", "a data type with its END_TYPE, any case");

            byte[] cp = new byte[] { (byte)'F', (byte)'B', (byte)' ' };
            var utf8 = System.Text.Encoding.UTF8;
            Contains(HeadsOf(utf8.GetBytes("\ufeffFUNCTION_BLOCK \"F\u00f6rderband\"\n")), "FB:F\u00f6rderband,", "UTF-8 with a BOM (the 1252 reading adds its own spelling)");
            Eq(HeadsOf(new byte[] { 0x46, 0x55, 0x4E, 0x43, 0x54, 0x49, 0x4F, 0x4E, 0x5F, 0x42, 0x4C, 0x4F, 0x43, 0x4B, 0x20, 0x22, 0x46, 0xF6, 0x72, 0x22 }),
                "FB:F\u00f6r", "Windows-1252 (invalid UTF-8) reads as 1252");
            Eq(HeadsOf(System.Text.Encoding.Unicode.GetBytes("FUNCTION_BLOCK \"Main\"")), "FB:Main", "UTF-16 without a BOM");
            Eq(HeadsOf(System.Text.Encoding.BigEndianUnicode.GetBytes("FUNCTION_BLOCK \"Main\"")), "FB:Main", "UTF-16 big-endian without a BOM");
            True(cp.Length == 3, "scratch");
        }

        static ProjectBlock PB(string name, string type, int number, string group)
        {
            return new ProjectBlock { Name = name, Type = type, Number = number, Group = group };
        }

        // OwnershipRefusesABlockOutsideTheGroup: a declared name matching a
        // block outside the group (any case, at the root or in another
        // group) conflicts; one inside the group or its user groups, or a
        // data type, does not.
        static void OwnershipRefusesABlockOutsideTheGroup()
        {
            var blocks = new List<ProjectBlock>
            {
                PB("Main", "OB", 1, ""),
                PB("Legacy", "FB", 3, "Customer"),
                PB("naut_Main", "FB", 1, "nautilus"),
                PB("Deep", "FC", 1, "nautilus/sub"),
            };
            Func<string, string> conflicts = delegate(string src)
            {
                var ds = Ownership.Sources(src);
                foreach (var d in ds) d.Source = "src/a.scl";
                return string.Join("|", Ownership.Conflicts(ds, blocks, "nautilus").ToArray());
            };
            Eq(conflicts("FUNCTION_BLOCK \"Main\"\n"), "src/a.scl declares \"Main\" [OB1] in the program blocks' root", "the root's Main, with its source");
            Eq(conflicts("function_block MAIN\n"), "src/a.scl declares \"Main\" [OB1] in the program blocks' root", "case-insensitive");
            Eq(conflicts("FUNCTION \"legacy\" : Void\n"), "src/a.scl declares \"Legacy\" [FB3] in group \"Customer\"", "another user group");
            Eq(conflicts("FUNCTION_BLOCK \"naut_Main\"\nFUNCTION \"Deep\" : Void\n"), "", "the group and its user groups are owned");
            Eq(conflicts("TYPE \"Main\"\nEND_TYPE\n"), "src/a.scl declares \"Main\" [OB1] in the program blocks' root", "a data type named like a block: one name space");
            Eq(conflicts("FUNCTION_BLOCK \"Main\"\nFUNCTION \"Legacy\" : Void\n").Split('|').Length, 2, "each conflict once");
            var two = new List<Declared> { new Declared { Kind = "FB", Name = "Main", Source = "a.scl" }, new Declared { Kind = "FB", Name = "main", Source = "b.scl" } };
            Eq(string.Join("|", Ownership.Conflicts(two, blocks, "nautilus").ToArray()), "a.scl, b.scl declares \"Main\" [OB1] in the program blocks' root", "every source that declares it");
            var types = new List<Declared> { new Declared { Kind = "UDT", Name = "Recipe", Source = "t.udt" }, new Declared { Kind = "FB", Name = "X", Source = "x.scl" } };
            True(Ownership.MixedRefused(types) == null, "data types and blocks in sources of their own");
            var mixed = new List<Declared> { types[0], types[1], new Declared { Kind = "FC", Name = "Y", Source = "t.udt" } };
            TiadException te = Ownership.MixedRefused(mixed);
            True(te != null && te.Kind == Kinds.BadRequest && te.Message.StartsWith("t.udt declares a data type and a block") && te.Message.Contains("Nothing was imported"), "a source with a data type and a block is refused, naming it");
            Eq(string.Join(",", new List<string>(Ownership.TypeSources(types)).ToArray()), "t.udt", "the data type sources");
            True(Ownership.GroupRefused("a/b") != null && Ownership.GroupRefused("a\\b") != null && Ownership.GroupRefused("nautilus") == null, "a group with a path separator is refused");
            True(Ownership.Owned("NAUTILUS/x", "nautilus"), "a user group of the group, any case");
            True(!Ownership.Owned("nautilus2", "nautilus"), "a group that only starts with the name is not owned");
            True(!Ownership.Owned("", "nautilus"), "the root is not owned");

            TiadException e = Ownership.Refusal(Ownership.Conflicts(Ownership.Sources("FUNCTION_BLOCK \"Main\"\n"), blocks, "nautilus"), "nautilus");
            Contains(e.Message, "outside group \"nautilus\"", "names the group");
            Eq(e.Status, 409, "status");
            Eq(e.Kind, Kinds.ForeignBlock, "kind");
            Contains(e.Message, "\"Main\" [OB1] in the program blocks' root", "names the block, its number and its group");
            Contains(e.Message, "Nothing was imported", "says nothing changed");
            Contains(e.Message, "Rename it in the source", "names the fix");
        }

        static ProjectBlock TY(string name, string group, string modified)
        {
            return new ProjectBlock { Name = name, Type = "PlcStruct", Group = group, DataType = true, Modified = modified };
        }

        // OwnershipRefusesADataTypeOutsideTheGroup (D1b): TIA regenerates a
        // same-named data type where it sits (TIA VM, 2026-11-24), and
        // keeps one name for blocks and data types. So a declared data type
        // or block that names a data type outside the group, in the data
        // types' root or any user group however deep, any case, conflicts;
        // and a declared data type that names a foreign block does too.
        static void OwnershipRefusesADataTypeOutsideTheGroup()
        {
            var objects = new List<ProjectBlock>
            {
                PB("Main", "OB", 1, ""),
                TY("Cust", "", "1"),
                TY("Deep", "custg/sub/subsub", "1"),
                TY("naut_Recipe", "nautilus", "1"),
                TY("naut_Sub", "nautilus/x", "1"),
            };
            Func<string, string> conflicts = delegate(string src)
            {
                var ds = Ownership.Sources(src);
                foreach (var d in ds) d.Source = "src/t.udt";
                return string.Join("|", Ownership.Conflicts(ds, objects, "nautilus").ToArray());
            };
            Eq(conflicts("TYPE \"Cust\"\nEND_TYPE\n"), "src/t.udt declares data type \"Cust\" in the PLC data types' root", "a customer type at the root");
            Eq(conflicts("TYPE \"deep\"\nEND_TYPE\n"), "src/t.udt declares data type \"Deep\" in data types group \"custg/sub/subsub\"", "a nested user group, any case");
            Eq(conflicts("FUNCTION_BLOCK \"CUST\"\n"), "src/t.udt declares data type \"Cust\" in the PLC data types' root", "a block named like a customer type");
            Eq(conflicts("TYPE \"naut_Recipe\"\nEND_TYPE\nTYPE \"naut_Sub\"\nEND_TYPE\n"), "", "the group's own data types, and its user groups'");
            Contains(Ownership.Refusal(Ownership.Conflicts(Ownership.Sources("TYPE \"Cust\"\nEND_TYPE\n"), objects, "nautilus"), "nautilus").Message, "would replace it", "the refusal");
        }

        // OwnershipPrunesOnlyPrefixedObjects: with a prefix, a prune
        // refuses when the group holds a block or data type that no source
        // declares and that lacks the prefix; with none, as before.
        static void OwnershipPrunesOnlyPrefixedObjects()
        {
            var objects = new List<ProjectBlock>
            {
                PB("Main", "OB", 1, ""),
                PB("naut_Old", "FB", 1, "nautilus"),
                PB("Theirs", "FC", 2, "nautilus/sub"),
                TY("Shared", "nautilus", "1"),
                TY("Cust", "", "1"),
            };
            var declared = new List<Declared> { new Declared { Kind = "FB", Name = "naut_Main" } };
            TiadException e = Ownership.PruneRefused(objects, declared, "nautilus", "naut_");
            True(e != null && e.Status == 409, "a prune over objects nautilus did not write is refused");
            Eq(e.Message.Substring(0, e.Message.IndexOf(':')), "\"Theirs\" [FC2] in group \"nautilus/sub\"; data type \"Shared\" in data types group \"nautilus\"", "each named, block and data type, outside objects left out");
            Contains(e.Message, "Nothing was imported", "says nothing changed");
            True(Ownership.PruneRefused(objects, declared, "nautilus", "") == null, "no prefix: the prune as before");
            declared.Add(new Declared { Kind = "FC", Name = "theirs" });
            declared.Add(new Declared { Kind = "UDT", Name = "Shared" });
            True(Ownership.PruneRefused(objects, declared, "nautilus", "NAUT_") == null, "declared ones are kept, not pruned; the prefix in any case");
        }

        // OwnershipSeesAChangeOutsideTheGroup: a block outside the group
        // that is gone, or there with another kind, number or group, or
        // new, is a change; anything inside the group is not.
        static void OwnershipSeesAChangeOutsideTheGroup()
        {
            var before = new List<ProjectBlock> { PB("Main", "OB", 1, ""), PB("Legacy", "FB", 3, "Customer"), PB("naut_A", "FB", 1, "nautilus") };
            Eq(Ownership.Changed(before, new List<ProjectBlock> { PB("Main", "OB", 1, ""), PB("Legacy", "FB", 3, "Customer"), PB("naut_B", "FC", 1, "nautilus") }, "nautilus").Count, 0,
                "a change inside the group");
            Eq(string.Join("; ", Ownership.Changed(before, new List<ProjectBlock> { PB("Main", "FB", 1, ""), PB("Legacy", "FB", 3, "Customer") }, "nautilus").ToArray()),
                "\"Main\" [OB1] in the program blocks' root is now \"Main\" [FB1] in the program blocks' root", "the bench's replacement: OB1 became an FB");
            Eq(string.Join("; ", Ownership.Changed(before, new List<ProjectBlock> { PB("Main", "OB", 1, "") }, "nautilus").ToArray()),
                "\"Legacy\" [FB3] in group \"Customer\" is gone", "a block gone");
            Eq(string.Join("; ", Ownership.Changed(before, new List<ProjectBlock> { PB("Main", "OB", 1, ""), PB("Legacy", "FB", 4, "Customer") }, "nautilus").ToArray()),
                "\"Legacy\" [FB3] in group \"Customer\" is now \"Legacy\" [FB4] in group \"Customer\"", "another number");
            Eq(string.Join("; ", Ownership.Changed(before, new List<ProjectBlock> { PB("Main", "OB", 1, ""), PB("Legacy", "FB", 3, "Customer"), PB("Stray", "FC", 1, "") }, "nautilus").ToArray()),
                "\"Stray\" [FC1] in the program blocks' root is new", "a new block outside the group");
            var m1 = new List<ProjectBlock> { PB("Main", "FB", 1, "") };
            m1[0].Modified = "1";
            var m2 = new List<ProjectBlock> { PB("Main", "FB", 1, "") };
            m2[0].Modified = "2";
            Eq(string.Join("; ", Ownership.Changed(m1, m2, "nautilus").ToArray()), "\"Main\" [FB1] in the program blocks' root was modified",
                "a body-only replacement: same kind, number and group, a new modified date");
            // Data types (D1b): a customer type's date moving is a change,
            // the group's own are not; a block and a type are one name.
            var t1 = new List<ProjectBlock> { PB("Main", "OB", 1, ""), TY("Cust", "custg", "1"), TY("naut_R", "nautilus", "1") };
            Eq(Ownership.Changed(t1, new List<ProjectBlock> { PB("Main", "OB", 1, ""), TY("Cust", "custg", "1"), TY("naut_R", "nautilus", "2") }, "nautilus").Count, 0,
                "a regenerated type of the group's: its date moves");
            Eq(string.Join("; ", Ownership.Changed(t1, new List<ProjectBlock> { PB("Main", "OB", 1, ""), TY("Cust", "custg", "2"), TY("naut_R", "nautilus", "1") }, "nautilus").ToArray()),
                "data type \"Cust\" in data types group \"custg\" was modified", "a customer type's date moved");
            Eq(string.Join("; ", Ownership.Changed(t1, new List<ProjectBlock> { PB("Main", "OB", 1, ""), TY("Cust", "", "1"), TY("naut_R", "nautilus", "1") }, "nautilus").ToArray()),
                "data type \"Cust\" in data types group \"custg\" is now data type \"Cust\" in the PLC data types' root", "a customer type moved");
            Eq(string.Join("; ", Ownership.Changed(t1, new List<ProjectBlock> { PB("Main", "OB", 1, ""), PB("Cust", "FB", 1, "custg"), TY("naut_R", "nautilus", "1") }, "nautilus").ToArray()),
                "data type \"Cust\" in data types group \"custg\" is now \"Cust\" [FB1] in group \"custg\"", "a type that became a block");
            Eq(string.Join("; ", Ownership.Changed(t1, new List<ProjectBlock> { PB("Main", "OB", 1, ""), TY("naut_R", "nautilus", "1") }, "nautilus").ToArray()),
                "data type \"Cust\" in data types group \"custg\" is gone", "a customer type gone");
            Eq(string.Join("; ", Ownership.Changed(t1, new List<ProjectBlock> { PB("Main", "OB", 1, ""), TY("Cust", "custg", "1"), TY("naut_R", "nautilus", "1"), TY("New", "", "1") }, "nautilus").ToArray()),
                "data type \"New\" in the PLC data types' root is new", "a new type outside the group");
            var f = Ownership.Foreign(new List<ProjectBlock> { TY("Cust", "", "1"), PB("Main", "OB", 1, ""), TY("naut_R", "nautilus", "1") }, "nautilus");
            Eq(f.Count == 2 && !f[0].DataType && f[1].DataType && f[1].Name == "Cust", true, "foreign: blocks, then data types; the group's own left out");
            TiadException ce = Ownership.CompileChangedError(new List<string> { "x" }, "nautilus");
            Eq(ce.Kind, Kinds.UncleanSession, "a compile's change: kind");
            Contains(ce.Message, "close it without saving", "the compile's change names what to do with the session");
            Contains(ce.Message, "needs the project changed outside nautilus", "and says the deploy needs the project changed");
            TiadException oe = Ownership.OutsideGroupError("src/t.udt: TIA generated data type \"Cust\" outside group \"nautilus\"", "nautilus");
            Eq(oe.Kind, Kinds.OperationFailed, "outside group: kind");
            Contains(oe.Message, "open or adopt the project again", "outside group: the recovery route");

            TiadException e = Ownership.ForeignChangedError(new List<string> { "x" }, "nautilus");
            Eq(e.Kind, Kinds.OperationFailed, "the failure's kind");
            Contains(e.Message, "Close it without saving", "the failure names what is left");
            Eq(Ownership.Unusable("saving it").Kind, Kinds.UncleanSession, "a later save's kind");
        }
    }
}
