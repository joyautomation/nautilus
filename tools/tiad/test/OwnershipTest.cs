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

        // OwnershipReadsEveryHeader: quoted and bare names, any case, several
        // blocks per file, CRLF, comments before a header; the instance
        // line of a DATA_BLOCK and END_ lines are no headers.
        static void OwnershipReadsEveryHeader()
        {
            Eq(Heads("FUNCTION_BLOCK \"Main\"\r\nVERSION : 0.1\r\nBEGIN\r\nEND_FUNCTION_BLOCK\r\nDATA_BLOCK \"Main_DB\"\r\n\"Main\"\r\nBEGIN\r\nEND_DATA_BLOCK\r\n"),
                "FB:Main,DB:Main_DB", "a writer FB and its instance DB");
            Eq(Heads("function_block main\nEND_FUNCTION_BLOCK\n"), "FB:main", "lower case, bare name");
            Eq(Heads("\t FUNCTION \"Helper\" : Void\r\nEND_FUNCTION\r\n  TYPE \"Recipe\"\nEND_TYPE\n"), "FC:Helper,UDT:Recipe", "a function and a type");
            Eq(Heads("ORGANIZATION_BLOCK \"OB_main\"\nTITLE = \"Cyclic interrupt\"\n"), "OB:OB_main", "an OB, its title no header");
            Eq(Heads("(* a header comment *) FUNCTION_BLOCK \"A\"\n// FUNCTION_BLOCK \"InALineComment\"\n/* FUNCTION_BLOCK \"InABlock\"\n*/ DATA_BLOCK \"B\"\n"),
                "FB:A,DB:B", "comments before a header, headers inside comments");
            Eq(Heads("FUNCTION_BLOCKX \"No\"\nFUNCTION_BLOCK\nFUNCTION_BLOCK 1x\nEND_FUNCTION_BLOCK \"No\"\n"), "", "no header");
            Eq(Heads("FUNCTION_BLOCK \"Pump // not a comment\"\n"), "FB:Pump // not a comment", "a quoted name keeps what looks like a comment");
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
                var l = new List<string>();
                foreach (var b in Ownership.Conflicts(Ownership.Sources(src), blocks, "nautilus")) l.Add(b.Name);
                return string.Join(",", l.ToArray());
            };
            Eq(conflicts("FUNCTION_BLOCK \"Main\"\n"), "Main", "the root's Main");
            Eq(conflicts("function_block MAIN\n"), "Main", "case-insensitive");
            Eq(conflicts("FUNCTION \"legacy\" : Void\n"), "Legacy", "another user group");
            Eq(conflicts("FUNCTION_BLOCK \"naut_Main\"\nFUNCTION \"Deep\" : Void\n"), "", "the group and its user groups are owned");
            Eq(conflicts("TYPE \"Main\"\nEND_TYPE\n"), "", "a declared data type is not checked yet, even one named like a block");
            Eq(conflicts("FUNCTION_BLOCK \"Main\"\nFUNCTION \"Legacy\" : Void\n"), "Main,Legacy", "each conflict once, in project order");
            True(Ownership.Owned("NAUTILUS/x", "nautilus"), "a user group of the group, any case");
            True(!Ownership.Owned("nautilus2", "nautilus"), "a group that only starts with the name is not owned");
            True(!Ownership.Owned("", "nautilus"), "the root is not owned");

            TiadException e = Ownership.Refusal(Ownership.Conflicts(Ownership.Sources("FUNCTION_BLOCK \"Main\"\n"), blocks, "nautilus"), "nautilus");
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
            TiadException e = Ownership.ForeignChangedError(new List<string> { "x" }, "nautilus");
            Eq(e.Kind, Kinds.OperationFailed, "the failure's kind");
            Contains(e.Message, "Close it without saving", "the failure names what is left");
            Eq(Ownership.Unusable("saving it").Kind, Kinds.UncleanSession, "a later save's kind");
        }
    }
}
