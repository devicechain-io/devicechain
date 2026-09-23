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
type requestBudget struct {
	remaining atomic.Int64
}

type budgetKey struct{}

// WithRequestBudget returns a context in which at most n credential checks may run. A
// budget already present is REPLACED only if n is smaller — so wrapping twice can
// tighten a budget but never widen one — and an n below 1 allows no checks at all.
func WithRequestBudget(ctx context.Context, n int) context.Context {
	if existing, ok := ctx.Value(budgetKey{}).(*requestBudget); ok && existing.remaining.Load() <= int64(n) {
		return ctx
	}
	b := &requestBudget{}
	if n > 0 {
		b.remaining.Store(int64(n))
	}
	return context.WithValue(ctx, budgetKey{}, b)
}

// spend takes one unit of the context's budget, reporting false when there is none
// left. A context with no budget always has one to spend.
//
// It is atomic, so resolvers running concurrently in one request — graphql-go runs a
// query's root fields in parallel — cannot both take the last unit.
func spend(ctx context.Context) bool {
	b, ok := ctx.Value(budgetKey{}).(*requestBudget)
	if !ok {
		return true
	}
	return b.remaining.Add(-1) >= 0
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
