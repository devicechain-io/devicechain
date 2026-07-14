// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import "fmt"

// CompileError is a publish-time selector rejection: a parse error, a type error, or a
// non-boolean result. Its message is console-surfaceable — it names the offending source
// so an author sees what to fix. cel-go's Issues carry line/column detail, preserved in Err.
type CompileError struct {
	Source string
	Err    error
}

func (e *CompileError) Error() string {
	return fmt.Sprintf("invalid selector %q: %v", e.Source, e.Err)
}

func (e *CompileError) Unwrap() error { return e.Err }

// CostError is a publish-time rejection of a selector whose worst-case evaluation cost
// exceeds the per-tenant ceiling, or that carries more facet leaves than MaxSelectorLeaves.
// It is the fail-closed guard (ADR-023) against an author publishing a selector whose
// resolved query would carry an unbounded number of EXISTS semi-joins.
type CostError struct {
	Source       string
	EstimatedMax uint64
	Ceiling      uint64
}

func (e *CostError) Error() string {
	return fmt.Sprintf("selector %q is too expensive: estimated worst-case cost %d exceeds the ceiling of %d",
		e.Source, e.EstimatedMax, e.Ceiling)
}

// NotLowerableError is a publish-time rejection of a selector that type-checks but uses a
// CEL node outside the facet-predicate subset the SQL lowering supports (SD-3). v1 accepts
// only `&&`/`||`/`!`, the comparisons `== != < <= > >=`, `k in attr`, and `attr[k]` against
// scalar literals; anything else (a function/macro, a two-index comparison, a non-literal
// key, a bare `attr[k]` boolean, a numeric operator on a non-numeric literal) fails here.
// The message names the reason so the console can surface it inline while authoring.
type NotLowerableError struct {
	Source string
	Reason string
}

func (e *NotLowerableError) Error() string {
	return fmt.Sprintf("selector %q is not expressible as a facet selector yet: %s", e.Source, e.Reason)
}
