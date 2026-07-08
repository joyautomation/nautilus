package st

import (
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// DatatypeToIRType maps a PLC variable's loosely-typed `datatype` string
// (as stored on PlcVariableConfigKV — "number", "boolean", "string", or
// an IEC 61131-3 scalar name) to the corresponding *ir.Type. Returns nil
// for UDTs and any unrecognized value so the caller can skip the entry
// instead of binding it to the wrong shape.
func DatatypeToIRType(datatype string) *ir.Type {
	switch strings.ToLower(strings.TrimSpace(datatype)) {
	case "boolean", "bool":
		return ir.BoolT
	case "number", "int", "int16", "int32", "int64", "uint16", "uint32", "uint64",
		"byte", "sint", "usint", "uint", "word", "dint", "udint", "dword", "lint", "ulint", "lword":
		return ir.IntT
	case "real", "lreal", "float", "float32", "float64", "double":
		return ir.RealT
	case "time", "ltime":
		return ir.TimeT
	case "string", "wstring", "char", "wchar":
		return ir.StringT
	}
	return nil
}
