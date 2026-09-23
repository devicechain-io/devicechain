// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// budgetSDL has a credential check reachable from a query and a mutation;
// budgetSubSDL from a subscription.
const budgetSDL = `
	schema { query: Query mutation: Mutation }
	type Query { check: Boolean! }
	type Mutation { check: Boolean! }
`

const budgetSubSDL = `
	schema { query: Query subscription: Subscription }
	type Query { ping: Boolean! }
	type Subscription { check: Boolean! }
`

// budgetRoot runs one REAL credential check per resolver call, with whatever context
// the execution hands it, and counts how many were refused by the request budget.
type budgetRoot struct {
	checker *credential.Checker
	hash    string
	refused atomic.Int32
	ran     atomic.Int32
}

func newBudgetRoot(t *testing.T) *budgetRoot {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	require.NoError(t, err)
	unthrottled := credential.Policy{Unthrottled: true}
	c, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: unthrottled, credential.KindOAuthClient: unthrottled,
	})
	require.NoError(t, err)
	return &budgetRoot{checker: c, hash: string(hash)}
}

func (r *budgetRoot) check(ctx context.Context) (bool, error) {
	err := r.checker.Check(ctx, credential.Principal{Kind: credential.KindIdentity, ID: "x"}, "pw",
		func(context.Context) (string, error) { return r.hash, nil })
	if errors.Is(err, credential.ErrRequestBudgetExhausted) {
		r.refused.Add(1)
		return false, err
	}
	r.ran.Add(1)
	return err == nil, err
}

func (r *budgetRoot) Check(ctx context.Context) (bool, error) { return r.check(ctx) }

// budgetSubRoot makes two checks inside one subscription operation.
type budgetSubRoot struct{ *budgetRoot }

func (s *budgetSubRoot) Ping() bool { return true }

func (s *budgetSubRoot) Check(ctx context.Context) (<-chan bool, error) {
	first, _ := s.check(ctx)
	second, _ := s.check(ctx)
	ch := make(chan bool, 1)
	ch <- first && !second
	close(ch)
	return ch, nil
}

// 🔴 EVERY GraphQL EXECUTION CARRIES THE BUDGET, and this is the test that says a
// GraphQL path cannot reach the Checker without one. A context with no budget is
// unlimited (credential/budget.go), so each case below would run EVERY check if the
// Schema had not installed one: two aliased checks in a query, two in a mutation, both
// through Exec and through the HTTP handler. At the default budget of 1, exactly one
// runs and the other is refused.
func TestEveryExecutionCarriesACredentialBudget(t *testing.T) {
	require.Equal(t, 1, DefaultGraphQLMaxCredentialChecks)
	for _, doc := range []string{"query { a: check b: check }", "mutation { a: check b: check }"} {
		t.Run("Exec "+doc, func(t *testing.T) {
			root := newBudgetRoot(t)
			resp := MustParseSchema(budgetSDL, root).Exec(context.Background(), doc, "", nil)
			require.Len(t, resp.Errors, 1, "%v", resp.Errors)
			assert.Equal(t, credential.CodeTooManyCredentialChecks, resp.Errors[0].Extensions["code"])
			assert.Equal(t, int32(1), root.ran.Load())
			assert.Equal(t, int32(1), root.refused.Load())
		})
		t.Run("HTTP "+doc, func(t *testing.T) {
			root := newBudgetRoot(t)
			srv := httptest.NewServer(NewHttpHandler(MustParseSchema(budgetSDL, root), map[ContextKey]interface{}{}, nil))
			defer srv.Close()
			body, _ := json.Marshal(map[string]string{"query": doc})
			resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
			require.NoError(t, err)
			resp.Body.Close()
			assert.Equal(t, int32(1), root.ran.Load())
			assert.Equal(t, int32(1), root.refused.Load())
		})
	}

	// Each request gets its own budget: a second Exec on the same Schema is not
	// refused because the first spent its unit.
	root := newBudgetRoot(t)
	schema := MustParseSchema(budgetSDL, root)
	for i := 0; i < 3; i++ {
		resp := schema.Exec(context.Background(), "mutation { check }", "", nil)
		require.Empty(t, resp.Errors, "request %d", i)
	}
	assert.Equal(t, int32(3), root.ran.Load())
	assert.Equal(t, int32(0), root.refused.Load())
}

// Subscribe carries the budget too: two checks inside one subscription operation
// run one and refuse the other.
func TestSubscribeCarriesACredentialBudget(t *testing.T) {
	root := newBudgetRoot(t)
	schema := MustParseSchema(budgetSubSDL, &budgetSubRoot{root})
	ch, err := schema.Subscribe(context.Background(), "subscription { check }", "", nil)
	require.NoError(t, err)
	var got []any
	for v := range ch {
		got = append(got, v)
	}
	require.NotEmpty(t, got)
	resp, ok := got[0].(*graphql.Response)
	require.True(t, ok, "%T", got[0])
	require.Empty(t, resp.Errors, "%v", resp.Errors)
	assert.JSONEq(t, `{"check":true}`, string(resp.Data), "the first check ran and the second was refused")
	assert.Equal(t, int32(1), root.ran.Load())
	assert.Equal(t, int32(1), root.refused.Load())
}

// The budget is env-tunable like the other ceilings, and never switched off.
func TestCredentialBudgetEnvOverride(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want int
	}{{"", 1}, {"3", 3}, {"0", 1}, {"-1", 1}, {"x", 1}} {
		t.Setenv(EnvGraphQLMaxCredentialChecks, tc.val)
		assert.Equal(t, tc.want, maxCredentialChecks(), "%q", tc.val)
	}
	t.Setenv(EnvGraphQLMaxCredentialChecks, "2")
	root := newBudgetRoot(t)
	resp := MustParseSchema(budgetSDL, root).Exec(context.Background(), "mutation { a: check b: check c: check }", "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, int32(2), root.ran.Load())
	assert.Equal(t, int32(1), root.refused.Load())
}

// A Schema whose budget was left at zero — any construction path other than
// MustParseSchema — refuses every check rather than allowing unlimited ones.
func TestZeroBudgetSchemaRefusesEveryCheck(t *testing.T) {
	root := newBudgetRoot(t)
	schema := &Schema{
		inner:            graphql.MustParseSchema(budgetSDL, root),
		maxQueryLength:   DefaultGraphQLMaxQueryLength,
		maxQueryRoots:    DefaultGraphQLMaxQueryRootFields,
		maxMutationRoots: DefaultGraphQLMaxMutationRootFields,
	}
	resp := schema.Exec(context.Background(), "mutation { check }", "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, credential.CodeTooManyCredentialChecks, resp.Errors[0].Extensions["code"])
	assert.Equal(t, int32(0), root.ran.Load())
	assert.Equal(t, int32(1), root.refused.Load())
}

// budgetMixedSDL has a subscription root, so graphql-go's Subscribe accepts a document,
// and a mutation, which Subscribe runs to completion inside the call.
const budgetMixedSDL = `
	schema { query: Query mutation: Mutation subscription: Subscription }
	type Query { ping: Boolean! }
	type Mutation { check: Boolean! }
	type Subscription { tick: Boolean! }
`

type budgetMixedRoot struct{ *budgetRoot }

func (r *budgetMixedRoot) Ping() bool { return true }
func (r *budgetMixedRoot) Tick(context.Context) <-chan bool {
	ch := make(chan bool)
	close(ch)
	return ch
}

// Subscribe carries the budget for whatever it runs, not only for subscription
// resolvers: a mutation with two aliased checks handed to Subscribe runs one and
// refuses the other.
func TestSubscribeBudgetsAMutationItRuns(t *testing.T) {
	root := newBudgetRoot(t)
	schema := MustParseSchema(budgetMixedSDL, &budgetMixedRoot{root})
	ch, err := schema.Subscribe(context.Background(), "mutation { a: check b: check }", "", nil)
	require.NoError(t, err)
	for range ch {
	}
	assert.Equal(t, int32(1), root.ran.Load())
	assert.Equal(t, int32(1), root.refused.Load())
}
