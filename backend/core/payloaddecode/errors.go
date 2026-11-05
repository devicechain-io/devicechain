// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"errors"
	"fmt"
	"strings"
)

// Reason is why a decode failed. The values are the sub-reasons a record carries in its error
// text (`decode_failed: no_section: ...`), so they are wire strings: only ever appended.
type Reason string

const (
	// ReasonEvalError is an evaluation error anywhere: an offset out of range, a missing JSON
	// key read without has(), a non-finite double, a failed conversion.
	ReasonEvalError Reason = "eval_error"
	// ReasonCostExceeded is a program that spent more than its static estimate at run time.
	ReasonCostExceeded Reason = "cost_exceeded"
	// ReasonInvalidOutput is an output the target metric's validation refused (type, storable
	// magnitude, min/max, enum), or one that cannot be rendered as the metric's type.
	ReasonInvalidOutput Reason = "invalid_output"
	// ReasonNoSection is an uplink no section's `when` matched.
	ReasonNoSection Reason = "no_section"
	// ReasonNoOutputs is a matched, non-ignore section that produced no reading.
	ReasonNoOutputs Reason = "no_outputs"
	// ReasonMissingInput is an uplink without the input kind the decoder declared.
	ReasonMissingInput Reason = "missing_input"
	// ReasonMapOverflow is a map output that yielded more keys than its maxKeys.
	ReasonMapOverflow Reason = "map_overflow"
	// ReasonInputTooLarge is an input over a cap (frame, JSON), refused before any work.
	ReasonInputTooLarge Reason = "input_too_large"
	// ReasonInputMalformed is a JSON input that does not parse or nests past MaxJSONDepth.
	ReasonInputMalformed Reason = "input_malformed"
	// ReasonEnvUnsupported is a document naming a function-library version this build does
	// not know (a downgrade).
	ReasonEnvUnsupported Reason = "env_unsupported"
	// ReasonCompileFailed is a document that does not compile.
	ReasonCompileFailed Reason = "compile_failed"
)

// DecodeError is a failed decode. Its text is built only from the reason, the expression path,
// the function name and the offset, never from platform internals, so it is safe to show a
// tenant.
type DecodeError struct {
	Reason Reason
	// Path locates the failing expression, as `sections[1].outputs[2].expr`; empty when the
	// failure is not tied to one.
	Path string
	// Message is the human-readable detail.
	Message string
	// Offset is the byte offset a native refused, or -1 when the failure has none.
	Offset int
	// Cost is what the decode had spent when it failed.
	Cost uint64
}

func (e *DecodeError) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Reason))
	if e.Path != "" {
		b.WriteString(": ")
		b.WriteString(e.Path)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// ErrUnsupportedDecoderKind is a decoder kind that is a valid vocabulary member but is not
// executable in this build (WASM). It is refused loudly, never skipped.
var ErrUnsupportedDecoderKind = errors.New("payloaddecode: unsupported decoder kind")

// ErrEnvUnsupported is a document whose envVersion this build does not know.
var ErrEnvUnsupported = errors.New("payloaddecode: unsupported decoder environment version")

// Issue is one thing wrong with a document, located by Path.
type Issue struct {
	Path    string
	Message string
}

func (i Issue) String() string {
	if i.Path == "" {
		return i.Message
	}
	return i.Path + ": " + i.Message
}

// CompileError is a document refused at compile. It lists every issue found, each located by
// the path of the expression, so the author fixes them in one pass.
type CompileError struct {
	Issues []Issue
	// Costs are the issues that are a cost ceiling exceeded, for errors.As.
	Costs []*CostError
}

// Unwrap exposes the cost causes.
func (e *CompileError) Unwrap() []error {
	out := make([]error, len(e.Costs))
	for i, c := range e.Costs {
		out[i] = c
	}
	return out
}

func (e *CompileError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, is := range e.Issues {
		parts[i] = is.String()
	}
	return "payloaddecode: " + strings.Join(parts, "; ")
}

// CostError is an expression, or a whole decoder, whose static worst-case cost is above its
// ceiling. It is a CompileError issue's cause and is also returned as an Issue message; the
// type exists so callers can test errors.As for it.
type CostError struct {
	Path      string // empty for the decoder-wide check
	Estimated uint64
	Ceiling   uint64
}

func (e *CostError) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("the decoder estimates to cost %d, above the ceiling of %d", e.Estimated, e.Ceiling)
	}
	return fmt.Sprintf("%s: estimated cost %d is above the ceiling of %d", e.Path, e.Estimated, e.Ceiling)
}
