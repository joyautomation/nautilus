package writer

// Mnemonics are what the writer can emit. The importer accepts any name
// and only the build rejects one it does not know (logix-target.md §21:
// a hand-authored GEQ cost a month), so every name here is checked
// against what real exports contain (mnemonics_test.go) — never against
// the editor's captions.
var Mnemonics = []string{
	"XIC", "XIO", "OTE", "OTL", "OTU",
	"ONS", "OSR", "OSF",
	"GT", "GE", "LT", "LE", "EQ", "NE",
	"TON", "TOF", "CTU", "RES",
	"MOVE", "ADD", "SUB", "MUL", "DIV", "ABS", "CPT", // assignments
	"CMP", // a compare with an expression operand
}

// CorpusVocabulary is every instruction mnemonic found across the 52 real
// exports surveyed on 2026-09-21 (logix-target.md §21). The committed
// fixtures in lang/l5x/testdata cover a subset of it; the full corpus is
// client work that stays out of the repo, and TestMnemonicsInCorpus
// re-derives this list from it when NAUTILUS_L5X_CORPUS is set.
var CorpusVocabulary = []string{
	"ABS", "ADD", "AVE", "BTD", "CLR", "CMP", "CONCAT", "COP", "CPT", "CTD",
	"CTU", "DIV", "DTOS", "EQ", "GE", "GSV", "GT", "JMP", "JSR", "LBL", "LE",
	"LIMIT", "LT", "MOVE", "MSG", "MUL", "NE", "NOP", "ONS", "OSF", "OSR",
	"OTE", "OTL", "OTU", "PID", "RES", "RTO", "SIZE", "SSV", "STOD", "STOR",
	"SUB", "TND", "TOF", "TON", "XIC", "XIO",
}
