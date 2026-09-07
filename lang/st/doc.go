package st

// Comment forms recognized by the lexer (see Lexer.skipBlockComment /
// Lexer.skipWhitespaceAndComments in lexer.go):
//
//   - "(* ... *)" block comments, which NEST per IEC 61131-3 3rd edition —
//     "(* outer (* inner *) still outer *)" is one comment, not "outer"
//     followed by stray code. An unterminated block comment (nesting depth
//     never returns to zero before EOF) is reported through LexErrors as a
//     LexError positioned at the outermost "(*", with message
//     "unterminated block comment opened at line N:col".
//   - "//" line comments, terminated by newline or EOF. These do not nest
//     (there's nothing to nest) and are unaffected by the change above.
//
// There is no "/* ... */" C-style form; IEC 61131-3 doesn't define one and
// the lexer has never accepted it.
//
// Lex(input) keeps returning only []Token for backward compatibility with
// its existing callers (lang/st.Parse, lang/sfc, lang/fbd,
// internal/project) — none of them were touched to add error plumbing.
// Callers that want to observe lexical errors (currently just the
// unterminated-comment case above) should call LexErrors(input) instead,
// which returns the same token stream plus a []LexError.
//
// Known gap this doesn't cover: internal/lsp/rename.go has its own
// hand-rolled comment/string scanner (identOccurrences) used for
// rename-symbol occurrence search — it is entirely separate from this
// lexer and does not nest block comments. Likewise
// tools/vscode-iec/syntaxes/iec-st.tmLanguage.json defines "(* ... *)" as a
// plain non-nesting TextMate begin/end rule. Neither was changed here (out
// of this file's scope); both would need their own fix to match nesting
// behavior in the editor.
