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
            Eq(conflicts("TYPE \"Main\"\nEND_TYPE\n"), "", "a data type is refused on its own (TypesRefused), not here");
            Eq(conflicts("FUNCTION_BLOCK \"Main\"\nFUNCTION \"Legacy\" : Void\n").Split('|').Length, 2, "each conflict once");
            var two = new List<Declared> { new Declared { Kind = "FB", Name = "Main", Source = "a.scl" }, new Declared { Kind = "FB", Name = "main", Source = "b.scl" } };
            Eq(string.Join("|", Ownership.Conflicts(two, blocks, "nautilus").ToArray()), "a.scl, b.scl declares \"Main\" [OB1] in the program blocks' root", "every source that declares it");
            var types = new List<Declared> { new Declared { Kind = "UDT", Name = "Recipe", Source = "t.udt" }, new Declared { Kind = "FB", Name = "X", Source = "x.scl" } };
            TiadException te = Ownership.TypesRefused(types);
            True(te != null && te.Kind == Kinds.BadRequest && te.Message.Contains("t.udt declares data type \"Recipe\"") && te.Message.Contains("Nothing was imported"), "a data type is refused, naming its source");
            True(Ownership.TypesRefused(new List<Declared> { types[1] }) == null, "no data type, no refusal");
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
            Contains(e.Message, "Rename the block", "names the fix");
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
            TiadException e = Ownership.ForeignChangedError(new List<string> { "x" }, "nautilus");
            Eq(e.Kind, Kinds.OperationFailed, "the failure's kind");
            Contains(e.Message, "Close it without saving", "the failure names what is left");
            Eq(Ownership.Unusable("saving it").Kind, Kinds.UncleanSession, "a later save's kind");
        }
    }
}
