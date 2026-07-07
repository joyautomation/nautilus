
// Package st implements an IEC 61131-3 Structured Text parser.
// Phase 1 of the IR pipeline produced ST → Starlark text (see codegen.go).
// Phase 3 replaces that with ST → internal/plc/ir (typed tree-walk evaluator).
package st

// TokenType identifies a token kind.
type TokenType int

const (
	// Literals and identifiers
	TokenEOF TokenType = iota
	TokenIdent
	TokenNumber      // decimal int/real literal
	TokenBasedNumber // 16#FF, 2#1010, 8#777 (stored with base prefix intact in Literal)
	TokenString
	TokenTimeLiteral  // T#5s, T#1h30m, etc.
	TokenTypedLiteral // INT#42, REAL#3.14, BOOL#TRUE, STRING#'x', TIME#5s ...

	// Keywords — control flow
	TokenProgram
	TokenEndProgram
	TokenFunction
	TokenEndFunction
	TokenFunctionBlock
	TokenEndFunctionBlock
	TokenVar
	TokenVarInput
	TokenVarOutput
	TokenVarInOut
	TokenVarTemp
	TokenVarGlobal
	TokenVarExternal
	TokenEndVar
	TokenIf
	TokenThen
	TokenElsif
	TokenElse
	TokenEndIf
	TokenFor
	TokenTo
	TokenBy
	TokenDo
	TokenEndFor
	TokenWhile
	TokenEndWhile
	TokenRepeat
	TokenUntil
	TokenEndRepeat
	TokenCase
	TokenOf
	TokenEndCase
	TokenReturn
	TokenExit
	TokenContinue
	TokenTrue
	TokenFalse
	TokenAnd
	TokenOr
	TokenNot
	TokenXor
	TokenMod

	// Type system keywords
	TokenTypeKw // "TYPE" keyword
	TokenEndType
	TokenStruct
	TokenEndStruct
	TokenArray
	TokenRetain
	TokenConstant

	// Scalar type names (kept as distinct tokens only where the parser benefits;
	// others flow through as TokenIdent and the lowering pass resolves them)
	TokenInt
	TokenReal
	TokenBool
	TokenStringType
	TokenDint
	TokenLreal

	// Operators & punctuation
	TokenAssign       // :=
	TokenOutputAssign // =>  (IEC 61131-3 FB output binding)
	TokenEqual        // =
	TokenNotEqual  // <>
	TokenLess      // <
	TokenLessEq    // <=
	TokenGreater   // >
	TokenGreaterEq // >=
	TokenPlus      // +
	TokenMinus     // -
	TokenStar      // *
	TokenSlash     // /
	TokenLParen    // (
	TokenRParen    // )
	TokenLBracket  // [
	TokenRBracket  // ]
	TokenSemicolon // ;
	TokenColon     // :
	TokenComma     // ,
	TokenDot       // .
	TokenDotDot    // ..
	TokenHash      // #
)

// Token represents a single lexical token.
type Token struct {
	Type    TokenType
	Literal string
	Line    int
	Col     int
}

// keywords maps IEC 61131-3 keywords to token types.
var keywords = map[string]TokenType{
	"PROGRAM":            TokenProgram,
	"END_PROGRAM":        TokenEndProgram,
	"FUNCTION":           TokenFunction,
	"END_FUNCTION":       TokenEndFunction,
	"FUNCTION_BLOCK":     TokenFunctionBlock,
	"END_FUNCTION_BLOCK": TokenEndFunctionBlock,
	"VAR":                TokenVar,
	"VAR_INPUT":          TokenVarInput,
	"VAR_OUTPUT":         TokenVarOutput,
	"VAR_IN_OUT":         TokenVarInOut,
	"VAR_TEMP":           TokenVarTemp,
	"VAR_GLOBAL":         TokenVarGlobal,
	"VAR_EXTERNAL":       TokenVarExternal,
	"END_VAR":            TokenEndVar,
	"IF":                 TokenIf,
	"THEN":               TokenThen,
	"ELSIF":              TokenElsif,
	"ELSE":               TokenElse,
	"END_IF":             TokenEndIf,
	"FOR":                TokenFor,
	"TO":                 TokenTo,
	"BY":                 TokenBy,
	"DO":                 TokenDo,
	"END_FOR":            TokenEndFor,
	"WHILE":              TokenWhile,
	"END_WHILE":          TokenEndWhile,
	"REPEAT":             TokenRepeat,
	"UNTIL":              TokenUntil,
	"END_REPEAT":         TokenEndRepeat,
	"CASE":               TokenCase,
	"OF":                 TokenOf,
	"END_CASE":           TokenEndCase,
	"RETURN":             TokenReturn,
	"EXIT":               TokenExit,
	"CONTINUE":           TokenContinue,
	"TRUE":               TokenTrue,
	"FALSE":              TokenFalse,
	"AND":                TokenAnd,
	"OR":                 TokenOr,
	"NOT":                TokenNot,
	"XOR":                TokenXor,
	"MOD":                TokenMod,
	"TYPE":               TokenTypeKw,
	"END_TYPE":           TokenEndType,
	"STRUCT":             TokenStruct,
	"END_STRUCT":         TokenEndStruct,
	"ARRAY":              TokenArray,
	"RETAIN":             TokenRetain,
	"CONSTANT":           TokenConstant,
	"INT":                TokenInt,
	"REAL":               TokenReal,
	"BOOL":               TokenBool,
	"STRING":             TokenStringType,
	"DINT":               TokenDint,
	"LREAL":              TokenLreal,
}

// scalarTypeNames is the full set of IEC 61131-3 elementary type names.
// The lexer emits these as TokenIdent if not explicitly tokenized above; the
// parser and type resolver both consult this table so "USINT", "BYTE" etc.
// don't need a dedicated token kind.
var scalarTypeNames = map[string]struct{}{
	"BOOL":    {},
	"BYTE":    {},
	"WORD":    {},
	"DWORD":   {},
	"LWORD":   {},
	"SINT":    {},
	"INT":     {},
	"DINT":    {},
	"LINT":    {},
	"USINT":   {},
	"UINT":    {},
	"UDINT":   {},
	"ULINT":   {},
	"REAL":    {},
	"LREAL":   {},
	"TIME":    {},
	"LTIME":   {},
	"DATE":    {},
	"TOD":     {},
	"TIME_OF_DAY": {},
	"DT":      {},
	"DATE_AND_TIME": {},
	"STRING":  {},
	"WSTRING": {},
	"CHAR":    {},
	"WCHAR":   {},
}

// IsScalarTypeName reports whether name is a known IEC elementary type.
func IsScalarTypeName(name string) bool {
	_, ok := scalarTypeNames[name]
	return ok
}
