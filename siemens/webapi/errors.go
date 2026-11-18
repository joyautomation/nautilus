package webapi

import (
	"errors"
	"fmt"
)

// Error codes the CPU answers with, from the ApiErrorCode enum of Siemens'
// own .NET client (github.com/siemens/simatic-s7-webserver-api). Only the
// ones this package acts on or names are listed.
const (
	CodeInternalError        = 1
	CodePermissionDenied     = 2
	CodeSystemIsBusy         = 3
	CodeNoResources          = 4 // e.g. the CPU's session limit is reached
	CodeSystemIsReadOnly     = 5
	CodeNotAccepted          = 6
	CodeLoginFailed          = 100
	CodeAlreadyAuthenticated = 101
	CodePasswordExpired      = 102
	CodeAddressDoesNotExist  = 200
	CodeInvalidAddress       = 201
	CodeNotAStructure        = 202
	CodeInvalidArrayIndex    = 203
	CodeUnsupportedAddress   = 204
	CodeAddressIsReadOnly    = 205

	// JSON-RPC 2.0's own.
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
)

var codeText = map[int]string{
	CodeInternalError:        "internal error",
	CodePermissionDenied:     "permission denied",
	CodeSystemIsBusy:         "system is busy",
	CodeNoResources:          "no resources (session or ticket limit)",
	CodeSystemIsReadOnly:     "system is read-only",
	CodeNotAccepted:          "not accepted",
	CodeLoginFailed:          "login failed",
	CodeAlreadyAuthenticated: "already authenticated",
	CodePasswordExpired:      "password expired",
	CodeAddressDoesNotExist:  "address does not exist",
	CodeInvalidAddress:       "invalid address",
	CodeNotAStructure:        "variable is not a structure",
	CodeInvalidArrayIndex:    "invalid array index",
	CodeUnsupportedAddress:   "unsupported address",
	CodeAddressIsReadOnly:    "address is read-only",
	CodeMethodNotFound:       "method not found on this firmware",
	CodeInvalidParams:        "invalid params",
}

// Error is a JSON-RPC error from the CPU.
type Error struct {
	Code    int
	Message string
	Method  string
}

func (e *Error) Error() string {
	name := codeText[e.Code]
	if name == "" {
		name = "error"
	}
	if e.Message != "" && e.Message != name {
		return fmt.Sprintf("webapi: %s: %s (%d): %s", e.Method, name, e.Code, e.Message)
	}
	return fmt.Sprintf("webapi: %s: %s (%d)", e.Method, name, e.Code)
}

// Code returns the CPU's error code for err, or 0.
func Code(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}
