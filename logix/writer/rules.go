package writer

// The rule table: every reason the Logix target refuses a construct, with
// a stable identifier. The editor (`naut lsp`, internal/lsp/logix.go),
// `naut check --target logix` and `naut logix write` run the same
// lowering, so a rule fires in the editor on the keystroke that introduces
// the construct and never for the first time at import or build
// (logix-authoring.md §5.1).
//
// A rule's message names the construct and, where Logix has one, the
// alternative (TestRefusalsNameTheAlternative). When a failure first shows
// up at SDK import or build instead of here, the fix is a new row in this
// table, not a workaround (§7a, "errors surface late").
const (
	ruleFunctionBlock = "logix/function-block" // user FUNCTION_BLOCK defined in the file
	ruleVarSection    = "logix/var-section"    // VAR_INPUT & co. in a program
	ruleType          = "logix/type"           // a type with no v1 mapping
	ruleTime          = "logix/time"           // TIME outside a timer preset
	ruleArrayShape    = "logix/array-shape"    // lower bound, dimensions, BOOL×32
	ruleArrayInit     = "logix/array-init"     // array initializer
	ruleInit          = "logix/init"           // unreadable initial value
	ruleName          = "logix/name"           // Logix tag naming limits
	ruleFB            = "logix/fb"             // a block the v1 subset lacks
	ruleFBPin         = "logix/fb-pin"         // a pin binding with no mapping
	ruleMessage       = "logix/message"        // a READ/WRITE the MSG it is written as cannot say
	rulePreset        = "logix/preset"         // PT / PV that is not a literal or a variable
	ruleReset         = "logix/reset"          // CTU R that is not a plain reference
	ruleTOFPosition   = "logix/block-position" // TOF or CTU inside a branch
	ruleFn            = "logix/fn"             // a function contact that is not a compare
	ruleOperand       = "logix/operand"        // a compare operand that is an expression
	ruleCoilEdge      = "logix/coil-edge"      // ( P X ) / ( N X )
	ruleMember        = "logix/member"         // an accessor into a type with no Logix shape
	ruleST            = "logix/st"             // an ST statement or function with no Logix form
	ruleDataOp        = "logix/data-op"        // an assignment with no Logix data instruction or expression
	ruleConversion    = "logix/conversion"     // an X_TO_Y Logix would compute differently, or not at all
	ruleIOModule      = "logix/io-module"      // an alias of a rack point whose module the hardware lacks
	ruleHardware      = "logix/hardware"       // a hardware configuration for another controller
)

// Rules lists every rule with a one-line description, for documentation
// and for the check command's --list-rules.
var Rules = []struct{ ID, Description string }{
	{ruleFunctionBlock, "user FUNCTIONs have no Logix form (inline them); a FUNCTION_BLOCK becomes an Add-On Instruction, which cannot reach a controller tag"},
	{ruleVarSection, "a program's VAR and VAR_EXTERNAL map to Logix tags; a program takes no parameters"},
	{ruleType, "the types are BOOL, SINT, INT, DINT, REAL, LREAL, TON, TOF, CTU, library STRUCTs (UDTs), enumerations (DINT, members as their values) and user blocks (AOIs); an AOI takes structures and arrays only as VAR_IN_OUT"},
	{ruleTime, "TIME is carried as DINT milliseconds only where it feeds a preset"},
	{ruleArrayShape, "arrays start at 0, are one-dimensional, and BOOL arrays are a multiple of 32"},
	{ruleArrayInit, "array initializers are not in the v1 subset"},
	{ruleInit, "an initial value must be a literal the tag can carry"},
	{ruleName, "tag names: at most 40 characters, no consecutive or trailing underscores"},
	{ruleFB, "TP, CTD, CTUD and R_TRIG/F_TRIG instances are not in the v1 subset; user blocks are, as AOIs"},
	{ruleMessage, "a READ or WRITE is an MSG on a ladder rung: ID a channel of type cip, VAR_1 a constant tag path, RD_1 / SD_1 a tag, one call per instance"},
	{ruleFBPin, "timers bind PT (TONR also Reset), counters PV and R, user blocks their declared parameters, each input of an ST call a tag, a member or element of one, or a literal (an expression fails the Logix build); the rung's power drives IN, or a user block's first unbound BOOL input"},
	{rulePreset, "a preset is a literal or a declared variable"},
	{ruleReset, "a counter reset is a plain BOOL reference"},
	{ruleTOFPosition, "a TOF or CTU sits on the rung itself, not inside a branch: its done bit outlives its rung-in"},
	{ruleFn, "function contacts are the compares GT GE LT LE EQ NE, and FIRST_SCAN() (S:FS)"},
	{ruleOperand, "a compare operand must be a reference, a literal, or an expression Logix can spell (CMP)"},
	{ruleCoilEdge, "an edge coil ( P X ) / ( N X ) must be its rung's only coil"},
	{ruleMember, "member access is only into timer, counter and user-block instances and declared STRUCT types"},
	{ruleST, "ST: RETURN, CONTINUE, STRING, MIN/MAX/LIMIT/SEL/MUX, ATAN2 and user calls have no Logix form"},
	{ruleDataOp, "an assignment is MOVE/ADD/SUB/MUL/DIV/ABS or a CPT expression; no comparisons, booleans, or functions Logix cannot spell"},
	{ruleIOModule, "an alias of a rack point (Local:1:I.Data.3, Remote1:1:I.Data.0, Drive1:I.Speed) needs that module in the hardware configuration (target.logix.hardware)"},
	{ruleHardware, "the hardware configuration's Local module is the target's processor, at the target's revision"},
	{ruleConversion, "ST conversions are written so Logix computes nautilus's result: REAL → integer only as the whole right-hand side of an assignment to a tag of that integer type (the store rounds), TRUNC_<int> only as the whole right-hand side of an integer assignment, no integer / whose quotient meets a REAL, in ST or on a rung (DIV, CPT, CMP), since Logix divides it in REAL, no unsigned, bit-string, LINT, LTIME or STRING conversions, BOOL → number and int → LREAL only as a whole right-hand side, int → REAL of a tag or literal"},
}
