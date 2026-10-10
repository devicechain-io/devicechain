// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package aiwire is the error contract between ai-inference and the services that call
// it: the GraphQL extensions.code values ai-inference sets on the refusals a caller has
// to tell apart, and the error type that carries them. It lives in core, beside neither
// side, so the code the server sets and the code the client matches are one constant —
// the shape governance.UnknownTenantError already uses.
//
// A caller classifies on svcclient.GraphQLError.Codes, never on message text: the text
// is free to be reworded, the code is not.
package aiwire

const (
	// CodeTimedOut — the provider was reached but did not answer inside ai-inference's
	// configured inference timeout. Retryable; nothing is misconfigured.
	CodeTimedOut = "INFERENCE_TIMED_OUT"
	// CodeRateLimited — the tenant is over its inference rate ceiling. Retryable by
	// waiting.
	CodeRateLimited = "INFERENCE_RATE_LIMITED"
)

// Error is an ai-inference refusal served with an extensions.code. Declared as a
// pointer-identity sentinel (var ErrX = aiwire.New(...)), so errors.Is matches it
// through any wrapping on the server side.
type Error struct {
	msg  string
	code string
}

// New builds a coded refusal.
func New(code, msg string) *Error { return &Error{msg: msg, code: code} }

func (e *Error) Error() string { return e.msg }

// Code is the extensions.code this refusal is served with.
func (e *Error) Code() string { return e.code }

// Extensions is read by the GraphQL server and served as the error's extensions.
func (e *Error) Extensions() map[string]any { return map[string]any{"code": e.code} }
