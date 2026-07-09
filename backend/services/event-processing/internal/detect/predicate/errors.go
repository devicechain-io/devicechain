// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import "fmt"

// CompileError is a publish-time predicate rejection: a parse error, a type error, or a
// non-boolean result. Its message is console-surfaceable — it names the offending source
// so an author sees what to fix. cel-go's Issues already carry line/column detail, which
// is preserved in the wrapped Err.
type CompileError struct {
	Source string
	Err    error
}

func (e *CompileError) Error() string {
	return fmt.Sprintf("invalid predicate %q: %v", e.Source, e.Err)
}

func (e *CompileError) Unwrap() error { return e.Err }

// CostError is a publish-time rejection of a predicate whose worst-case evaluation cost
// exceeds the per-tenant ceiling. It is the fail-closed guard against an author (or a
// generated canvas graph) publishing an expression that would load the singleton — the
// analog of a query the planner would refuse.
type CostError struct {
	Source       string
	EstimatedMax uint64
	Ceiling      uint64
}

func (e *CostError) Error() string {
	return fmt.Sprintf("predicate %q is too expensive: estimated worst-case cost %d exceeds the ceiling of %d",
		e.Source, e.EstimatedMax, e.Ceiling)
}
