package writer

// The rule table: every reason the Logix target refuses a construct, with
// a stable identifier. `naut check --target logix` and `naut logix write`
// run the same lowering, so a rule fires in the editor on the keystroke
// that introduces the construct and never for the first time at import or
// build (logix-authoring.md §5.1).
//
// A rule's message always names the construct and the alternative. When a
// failure first shows up at SDK import or build instead of here, the fix is
// a new row in this table, not a workaround (§7a, "errors surface late").
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
	rulePreset        = "logix/preset"         // PT / PV that is not a literal or a variable
	ruleReset         = "logix/reset"          // CTU R that is not a plain reference
	ruleTOFPosition   = "logix/tof-position"   // TOF inside a branch
	ruleFn            = "logix/fn"             // a function contact that is not a compare
	ruleOperand       = "logix/operand"        // a compare operand that is an expression
	ruleCoilEdge      = "logix/coil-edge"      // ( P X ) / ( N X )
	ruleMember        = "logix/member"         // an accessor into a type with no Logix shape
)

// Rules lists every rule with a one-line description, for documentation
// and for the check command's --list-rules.
var Rules = []struct{ ID, Description string }{
	{ruleFunctionBlock, "user FUNCTION_BLOCKs are not in the v1 subset (AOIs later)"},
	{ruleVarSection, "only VAR and VAR_EXTERNAL map to Logix tags"},
	{ruleType, "the v1 types are BOOL, SINT, INT, DINT, REAL, LREAL, TON, TOF, CTU"},
	{ruleTime, "TIME is carried as DINT milliseconds only where it feeds a preset"},
	{ruleArrayShape, "arrays start at 0, are one-dimensional, and BOOL arrays are a multiple of 32"},
	{ruleArrayInit, "array initializers are not in the v1 subset"},
	{ruleInit, "an initial value must be a literal the tag can carry"},
	{ruleName, "tag names: at most 40 characters, no consecutive or trailing underscores"},
	{ruleFB, "TP, CTD, CTUD, R_TRIG/F_TRIG instances and user blocks are not in the v1 subset"},
	{ruleFBPin, "only PT (timers), PV and R (counters) may be bound; outputs are read as contacts"},
	{rulePreset, "a preset is a literal or a declared variable"},
	{ruleReset, "a counter reset is a plain BOOL reference"},
	{ruleTOFPosition, "a TOF sits on the rung itself, not inside a branch"},
	{ruleFn, "function contacts are the compares GT GE LT LE EQ NE"},
	{ruleOperand, "compare operands are tag references or numeric literals"},
	{ruleCoilEdge, "( P X ) and ( N X ) are not in the v1 subset; use an edge contact"},
	{ruleMember, "member access is only into timer and counter instances"},
}
