// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"context"
	"errors"
	"sync/atomic"
)

// A request budget is how many credential checks one inbound request may make.
//
// 🔴 WHY A BUDGET AS WELL AS THE ROOT-FIELD LIMIT. One GraphQL request can reach
// Check once per aliased `login`, and each of those is a bcrypt compare. The GraphQL
// root-field limit refuses such a document before it runs — but only as long as its
// reading of the document agrees with graphql-go's, which is a property of two parsers,
// held by a fuzz test. The budget does not read the document at all: it is a counter in
// the request's context, and every check spends one unit of it BEFORE doing anything
// else. However a document is written, however it reaches the resolvers, one request
// cannot buy more compares than its budget. The root-field limit is the first line; this
// is the backstop that holds whatever the document looks like.
//
// It is installed by construction, not by remembering: core/graphql's Schema — the only
// way a service can execute GraphQL — wraps every Exec and Subscribe context with one.
//
// A context WITHOUT a budget is not limited. The only callers that reach Check outside
// GraphQL are HTTP handlers that make exactly one check per request by their own shape
// (an OAuth client authenticating at the token endpoint, a person submitting the
// authorize login form), so there is nothing for a budget to bound there, and GraphQL
// execution cannot reach Check without one. Refusing an unbudgeted check instead would
// turn every such handler into a place that has to remember to install a budget of 1,
// which is the kind of rule this type exists to take away.
//
// 🔴 A BUDGET CAN BE TIGHTENED BUT NEVER WIDENED, AND THAT HOLDS FOR THE AGGREGATE, NOT
// JUST ONE CHAIN. A budget installed inside another is NESTED in it: every check spends
// one unit from the innermost budget AND from every budget enclosing it, and is refused
// if any of them is spent. So however many children one request's context is wrapped
// into, and whatever each is given, the checks made under all of them together never
// exceed the outermost budget — a child can only ever narrow what its parent allows.
type requestBudget struct {
	remaining atomic.Int64
	// parent is the budget this one was installed inside, or nil.
	parent *requestBudget
}

type budgetKey struct{}

// WithRequestBudget returns a context in which at most n credential checks may run —
// fewer if ctx already carries a budget, because the new one is nested inside it and
// every check spends from both. An n below 1 allows no checks at all.
func WithRequestBudget(ctx context.Context, n int) context.Context {
	b := &requestBudget{}
	if parent, ok := ctx.Value(budgetKey{}).(*requestBudget); ok {
		b.parent = parent
	}
	if n > 0 {
		b.remaining.Store(int64(n))
	}
	return context.WithValue(ctx, budgetKey{}, b)
}

// spend takes one unit from the context's budget and from every budget it is nested in,
// reporting false — and taking nothing from any of them — when any is spent. A context
// with no budget always has one to spend.
//
// Each level is decremented atomically, innermost first, and a refusal gives back the
// units it took on the way up. So resolvers running concurrently in one request —
// graphql-go runs a query's root fields in parallel — cannot both take the last unit of
// any level, and a refused check leaks nothing. A check racing a refusal can see a
// level transiently short by the unit being given back, and be refused when a moment
// later it would not have been: that errs toward refusing, never toward allowing.
func spend(ctx context.Context) bool {
	b, ok := ctx.Value(budgetKey{}).(*requestBudget)
	if !ok {
		return true
	}
	for level := b; level != nil; level = level.parent {
		if level.remaining.Add(-1) < 0 {
			for undo := b; undo != level.parent; undo = undo.parent {
				undo.remaining.Add(1)
			}
			return false
		}
	}
	return true
}

// ErrRequestBudgetExhausted is returned, as a *RequestBudgetError, when a request has
// already made as many credential checks as its budget allows.
var ErrRequestBudgetExhausted = errors.New("credential: the request's credential-check budget is spent")

// CodeTooManyCredentialChecks is the GraphQL extension code a budget refusal carries.
const CodeTooManyCredentialChecks = "TOO_MANY_CREDENTIAL_CHECKS"

// RequestBudgetError is how Check reports ErrRequestBudgetExhausted (errors.Is matches
// it). The attempt was NOT evaluated — no read, no charge, no lookup, no compare — and
// the refusal is taken before the principal is looked at, so it is identical for an
// account that exists and one that does not. It carries an extension code so a client
// can tell it from a wrong password: the password was never checked.
type RequestBudgetError struct{}

func (*RequestBudgetError) Error() string {
	return "this request has already made its credential checks; send one sign-in per request"
}
func (*RequestBudgetError) Unwrap() error { return ErrRequestBudgetExhausted }
func (*RequestBudgetError) Extensions() map[string]any {
	return map[string]any{"code": CodeTooManyCredentialChecks}
}
