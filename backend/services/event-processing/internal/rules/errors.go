// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import "fmt"

// ValidationError is a publish-time rejection of a structurally invalid rule: a missing
// required field, a field set that is not allowed for the rule's type, or an out-of-range
// value. Its message is console-surfaceable so an author sees exactly what to fix. It
// names the field so the form builder can anchor the error to an input.
type ValidationError struct {
	RuleID string
	Field  string
	Msg    string
	// Err is the wrapped cause (e.g. a predicate.CompileError or predicate.CostError from
	// the leaf), preserved so a caller can errors.As through to the specific kind while the
	// outer error still carries the rule id + field anchor for the console.
	Err error
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("invalid rule %q: %s: %s", e.RuleID, e.Field, e.Msg)
	}
	return fmt.Sprintf("invalid rule %q: %s", e.RuleID, e.Msg)
}

func (e *ValidationError) Unwrap() error { return e.Err }

// invalid is a small constructor for a field validation error.
func invalid(ruleID, field, format string, args ...any) *ValidationError {
	return &ValidationError{RuleID: ruleID, Field: field, Msg: fmt.Sprintf(format, args...)}
}
