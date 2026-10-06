// Package st implements an IEC 61131-3 Structured Text parser.
// Phase 1 of the IR pipeline produced ST → Starlark text (see codegen.go).
// Phase 3 replaces that with ST → internal/plc/ir (typed tree-walk evaluator).
package st

import (
	"fmt"
	"strings"
)

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
	TokenNotEqual     // <>
	TokenLess         // <
	TokenLessEq       // <=
	TokenGreater      // >
	TokenGreaterEq    // >=
	TokenPlus         // +
	TokenMinus        // -
	TokenStar         // *
	TokenSlash        // /
	TokenLParen       // (
	TokenRParen       // )
	TokenLBracket     // [
	TokenRBracket     // ]
	TokenSemicolon    // ;
	TokenColon        // :
	TokenComma        // ,
	TokenDot          // .
	TokenDotDot       // ..
	TokenHash         // #

	// tokenKindCount is one past the last token kind: the length the
	// tokenNames table must cover (TestTokenNamesCoverEveryKind).
	tokenKindCount
)

// tokenNames is how a diagnostic names each token kind: a keyword or
// punctuation by its spelling, a literal class by what it is. A parse error
// never prints a kind's number (#178) — String falls back to a description,
// never to %d, and a test walks every kind to keep this table complete.
var tokenNames = [...]string{
	TokenEOF:          "end of file",
	TokenIdent:        "an identifier",
	TokenNumber:       "a number",
	TokenBasedNumber:  "a based number (16#FF)",
	TokenString:       "a string",
	TokenTimeLiteral:  "a TIME literal",
	TokenTypedLiteral: "a typed literal (INT#5)",

	TokenProgram:          "PROGRAM",
	TokenEndProgram:       "END_PROGRAM",
	TokenFunction:         "FUNCTION",
	TokenEndFunction:      "END_FUNCTION",
	TokenFunctionBlock:    "FUNCTION_BLOCK",
	TokenEndFunctionBlock: "END_FUNCTION_BLOCK",
	TokenVar:              "VAR",
	TokenVarInput:         "VAR_INPUT",
	TokenVarOutput:        "VAR_OUTPUT",
	TokenVarInOut:         "VAR_IN_OUT",
	TokenVarTemp:          "VAR_TEMP",
	TokenVarGlobal:        "VAR_GLOBAL",
	TokenVarExternal:      "VAR_EXTERNAL",
	TokenEndVar:           "END_VAR",
	TokenIf:               "IF",
	TokenThen:             "THEN",
	TokenElsif:            "ELSIF",
	TokenElse:             "ELSE",
	TokenEndIf:            "END_IF",
	TokenFor:              "FOR",
	TokenTo:               "TO",
	TokenBy:               "BY",
	TokenDo:               "DO",
	TokenEndFor:           "END_FOR",
	TokenWhile:            "WHILE",
	TokenEndWhile:         "END_WHILE",
	TokenRepeat:           "REPEAT",
	TokenUntil:            "UNTIL",
	TokenEndRepeat:        "END_REPEAT",
	TokenCase:             "CASE",
	TokenOf:               "OF",
	TokenEndCase:          "END_CASE",
	TokenReturn:           "RETURN",
	TokenExit:             "EXIT",
	TokenContinue:         "CONTINUE",
	TokenTrue:             "TRUE",
	TokenFalse:            "FALSE",
	TokenAnd:              "AND",
	TokenOr:               "OR",
	TokenNot:              "NOT",
	TokenXor:              "XOR",
	TokenMod:              "MOD",

	TokenTypeKw:    "TYPE",
	TokenEndType:   "END_TYPE",
	TokenStruct:    "STRUCT",
	TokenEndStruct: "END_STRUCT",
	TokenArray:     "ARRAY",
	TokenRetain:    "RETAIN",
	TokenConstant:  "CONSTANT",

	TokenInt:        "INT",
	TokenReal:       "REAL",
	TokenBool:       "BOOL",
	TokenStringType: "STRING",
	TokenDint:       "DINT",
	TokenLreal:      "LREAL",

	TokenAssign:       "':='",
	TokenOutputAssign: "'=>'",
	TokenEqual:        "'='",
	TokenNotEqual:     "'<>'",
	TokenLess:         "'<'",
	TokenLessEq:       "'<='",
	TokenGreater:      "'>'",
	TokenGreaterEq:    "'>='",
	TokenPlus:         "'+'",
	TokenMinus:        "'-'",
	TokenStar:         "'*'",
	TokenSlash:        "'/'",
	TokenLParen:       "'('",
	TokenRParen:       "')'",
	TokenLBracket:     "'['",
	TokenRBracket:     "']'",
	TokenSemicolon:    "';'",
	TokenColon:        "':'",
	TokenComma:        "','",
	TokenDot:          "'.'",
	TokenDotDot:       "'..'",
	TokenHash:         "'#'",
}

// String names the token kind for a diagnostic ("':='", "END_VAR", "an
// identifier"). An out-of-table kind names itself as unknown rather than
// printing its number.
func (t TokenType) String() string {
	if t >= 0 && int(t) < len(tokenNames) && tokenNames[t] != "" {
		return tokenNames[t]
	}
	return "an unknown token"
}

// describe renders a token as a diagnostic shows what it found: its text,
// quoted, or the kind's name when it has no text (end of file).
func (t Token) describe() string {
	if t.Type == TokenEOF {
		return "end of file"
	}
	if t.Literal == "" {
		return t.Type.String()
	}
	return fmt.Sprintf("%q", t.Literal)
}

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
	"BOOL":          {},
	"BYTE":          {},
	"WORD":          {},
	"DWORD":         {},
	"LWORD":         {},
	"SINT":          {},
	"INT":           {},
	"DINT":          {},
	"LINT":          {},
	"USINT":         {},
	"UINT":          {},
	"UDINT":         {},
	"ULINT":         {},
	"REAL":          {},
	"LREAL":         {},
	"TIME":          {},
	"LTIME":         {},
	"DATE":          {},
	"TOD":           {},
	"TIME_OF_DAY":   {},
	"DT":            {},
	"DATE_AND_TIME": {},
	"STRING":        {},
	"WSTRING":       {},
	"CHAR":          {},
	"WCHAR":         {},
}

// IsScalarTypeName reports whether name is a known IEC elementary type.
func IsScalarTypeName(name string) bool {
	_, ok := scalarTypeNames[name]
	return ok
}

// KeywordNames returns every IEC 61131-3 keyword spelling the lexer
// recognizes, in unspecified order. It includes the few elementary types
// that carry dedicated token kinds (INT, REAL, BOOL, STRING, DINT, LREAL);
// callers that want the type names on their own should use ScalarTypeNames.
// Exposed so tooling (the LSP completion set) derives keywords from the one
// authoritative table instead of hand-maintaining a copy.
func KeywordNames() []string {
	out := make([]string, 0, len(keywords))
	for k := range keywords {
		out = append(out, k)
	}
	return out
}

// ScalarTypeNames returns every IEC 61131-3 elementary type name, in
// unspecified order — the full set the parser accepts (26 names), so
// tooling advertises exactly what the compiler understands.
func ScalarTypeNames() []string {
	out := make([]string, 0, len(scalarTypeNames))
	for k := range scalarTypeNames {
		out = append(out, k)
	}
	return out
}

// IsKeyword reports whether name collides (case-insensitively) with an IEC
// 61131-3 keyword the lexer reserves.
func IsKeyword(name string) bool {
	_, ok := keywords[strings.ToUpper(name)]
	return ok
}

// EscapeKeyword returns name unchanged, unless it collides
// (case-insensitively) with an IEC 61131-3 keyword, in which case it
// appends a trailing underscore. A source (Logix UDT member, tag, etc.)
// may be named "retain" or "of" — both valid there, both parse errors as
// an ST identifier — so an importer that mirrors that source's field names
// one-for-one needs a deterministic escape. `naut logix import` and
// `naut eip import` both call this so a name renames the same way no
// matter which importer produced it.
func EscapeKeyword(name string) string {
	if IsKeyword(name) {
		return name + "_"
	}
	return name
}
