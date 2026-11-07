// Package mobilecore is the application core the iOS (and later Android)
// front end talks to. It owns sessions, hosts, files, forwards and the
// assistant; it knows nothing about Swift, UIKit or any presentation.
//
// The front end reaches it through the C ABI in package main under mobile/:
// control calls return JSON, terminal output is drained from a buffer, and
// events are polled from a queue. Everything here is safe to call from any
// goroutine.
package mobilecore

import (
	"encoding/json"
	"fmt"
)

// RPCError is the error shape every failed Call returns.
type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// Error codes the front end acts on.
const (
	CodeBadRequest  = "bad_request"
	CodeNotFound    = "not_found"
	CodeConflict    = "conflict"
	CodeInternal    = "internal"
	CodeUnavailable = "unavailable"
)

func errorf(code, format string, args ...any) *RPCError {
	return &RPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Envelope is what MottyCall returns: one JSON object, either a result or an
// error, never both.
type Envelope struct {
	OK     bool            `json:"ok"`
	Result any             `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// decodeParams unmarshals params into dst. Empty params leave dst untouched,
// so a method with only optional fields can be called with "{}" or "".
func decodeParams(params json.RawMessage, dst any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return errorf(CodeBadRequest, "invalid parameters: %v", err)
	}
	return nil
}
