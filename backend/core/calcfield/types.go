// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import "fmt"

// Type is the data type of a formula input or result. The zero value is invalid, so a
// definition that forgot its type is refused rather than read as one of the three.
type Type uint8

const (
	// Double is a 64-bit float metric (device-management DataType "DOUBLE").
	Double Type = iota + 1
	// Integer is a 64-bit signed integer metric (DataType "INT").
	Integer
	// Boolean is a boolean metric (DataType "BOOLEAN"), stored as "1"/"0".
	Boolean
)

// String returns the device-management DataType spelling of t.
func (t Type) String() string {
	switch t {
	case Double:
		return "DOUBLE"
	case Integer:
		return "INT"
	case Boolean:
		return "BOOLEAN"
	}
	return fmt.Sprintf("Type(%d)", uint8(t))
}

// valid reports whether t is one of the three types a formula may read or produce.
func (t Type) valid() bool { return t == Double || t == Integer || t == Boolean }

// ParseType maps a device-management metric DataType ("DOUBLE", "INT", "BOOLEAN") to a
// Type. Any other value — including "STRING", which is device state and never a formula
// input or result — reports false.
func ParseType(dataType string) (Type, bool) {
	switch dataType {
	case "DOUBLE":
		return Double, true
	case "INT":
		return Integer, true
	case "BOOLEAN":
		return Boolean, true
	}
	return 0, false
}

// Input is one declared metric a formula may read: its key and its type. A caller passes the
// MEASURED metrics to NewEnv or NewPlan; the calculated metrics a formula may read are
// declared by NewPlan from their definitions (chaining, see Plan).
type Input struct {
	Key  string
	Type Type
}

// Definition is a calculated metric as the compiler sees it.
type Definition struct {
	// Key is the calculated metric's own key. It must not be an input key.
	Key string
	// Expression is the CEL source.
	Expression string
	// Type is the declared result type; the expression must type-check to exactly this.
	Type Type
	// Min and Max are the definition's optional inclusive bounds. A result outside them is
	// skipped as SkipRange — the same bounds a device-sent value of the metric must meet.
	Min, Max *float64
}

// SkipReason is why a formula produced no value for a sample. It is a closed, bounded set,
// safe as a metric label. The empty value means the formula produced a value.
type SkipReason string

const (
	// SkipNone means a value was produced.
	SkipNone SkipReason = ""
	// SkipMissingInput: an input the formula read without a presence guard is absent.
	SkipMissingInput SkipReason = "missing_input"
	// SkipInvalidInput: an input the formula read is present but did not parse as its
	// declared type. Validation normally rejects such a value before resolution, so this is
	// a backstop, kept distinct from a missing input so it is not mistaken for one.
	SkipInvalidInput SkipReason = "invalid_input"
	// SkipNonFinite: a double the formula read or computed is NaN or ±Inf: a non-finite input
	// value, a double division by zero, a double overflow, the square root of a negative.
	SkipNonFinite SkipReason = "non_finite"
	// SkipDivZero: an integer division or modulus by zero.
	SkipDivZero SkipReason = "div_zero"
	// SkipOverflow: an integer result or conversion overflowed 64 bits.
	SkipOverflow SkipReason = "overflow"
	// SkipRange: the result is outside the definition's Min/Max.
	SkipRange SkipReason = "range"
	// SkipUnstorable: a DOUBLE or INT result whose magnitude is StoredMagnitude or more, which
	// the measurement store cannot hold whatever the definition's Min/Max allow. Storage
	// would refuse the row, and with it the whole reading the value was computed from.
	SkipUnstorable SkipReason = "unstorable"
	// SkipEvalError: any other evaluation failure (for example clamp with its bounds
	// reversed). Counted, never a value.
	SkipEvalError SkipReason = "eval_error"
)

// SkipReasons is every non-empty SkipReason, in a fixed order, for registering counters.
var SkipReasons = []SkipReason{
	SkipMissingInput, SkipInvalidInput, SkipNonFinite, SkipDivZero, SkipOverflow,
	SkipRange, SkipUnstorable, SkipEvalError,
}

// Result is the outcome of evaluating one formula against one sample. Exactly one of Value
// (when Skip is SkipNone) or Skip is meaningful. Err carries the underlying evaluation error
// for a log line when there was one; it is never needed to decide what to do.
type Result struct {
	Value string
	Skip  SkipReason
	Err   error
}

// OK reports whether the formula produced a value.
func (r Result) OK() bool { return r.Skip == SkipNone }
