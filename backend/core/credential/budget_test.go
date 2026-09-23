// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A request's budget bounds its checks: the first n are evaluated, and every one after
// is refused with the distinct error — having done NOTHING: no read, no charge, no
// lookup, no compare. The policy is generous enough that the backoff never fires here,
// so every refusal below is the budget's.
func TestRequestBudgetBoundsChecks(t *testing.T) {
	store := credentialtest.NewStore()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total"}, []string{"kind", "outcome"})
	c, err := credential.NewChecker(store, policies(credential.Policy{Free: 100, Base: 1, Cap: 2}),
		credential.WithClock(newClock().Now), credential.WithCounter(counter))
	require.NoError(t, err)
	var compares atomic.Int32
	credential.ObserveCompares(c, func([]byte) { compares.Add(1) })
	acct := newAccount(t)

	ctx := credential.WithRequestBudget(context.Background(), 2)
	require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	require.NoError(t, c.Check(ctx, alice, secret, acct.lookup))
	opsBefore := len(store.Ops())

	for i := 0; i < 3; i++ {
		err := c.Check(ctx, alice, secret, acct.lookup)
		require.ErrorIs(t, err, credential.ErrRequestBudgetExhausted, "check %d past the budget", i)
		var be *credential.RequestBudgetError
		require.True(t, errors.As(err, &be))
		assert.Equal(t, map[string]any{"code": credential.CodeTooManyCredentialChecks}, be.Extensions())
	}
	assert.Equal(t, int32(2), acct.calls.Load(), "a refused check looks nothing up")
	assert.Equal(t, int32(2), compares.Load(), "a refused check runs no compare")
	assert.Len(t, store.Ops(), opsBefore, "a refused check neither reads nor charges the attempt store")
	assert.Equal(t, 3.0, testutil.ToFloat64(counter.WithLabelValues("identity", credential.OutcomeRequestBudget)))

	// The budget is the context's, not the checker's: a fresh request starts full.
	require.NoError(t, c.Check(credential.WithRequestBudget(context.Background(), 2), alice, secret, acct.lookup))
}

// 🔴 UNDER EXHAUSTION A KNOWN AND AN UNKNOWN PRINCIPAL ARE INDISTINGUISHABLE: the
// refusal is taken before the principal is keyed, so both return the same error, with
// no lookup, no compare and no store operation for either.
func TestRequestBudgetRefusalIsTheSameForEveryPrincipal(t *testing.T) {
	run := func(acct *account, id string) (error, []string, int32) {
		store := credentialtest.NewStore()
		c := newChecker(t, store, newClock())
		var compares atomic.Int32
		credential.ObserveCompares(c, func([]byte) { compares.Add(1) })
		ctx := credential.WithRequestBudget(context.Background(), 0)
		err := c.Check(ctx, credential.Principal{Kind: credential.KindIdentity, ID: id}, secret, acct.lookup)
		return err, store.Ops(), compares.Load() + acct.calls.Load()
	}
	knownErr, knownOps, knownWork := run(newAccount(t), "alice@example.com")
	unknownErr, unknownOps, unknownWork := run(unknownAccount(), "nobody@example.com")
	require.ErrorIs(t, knownErr, credential.ErrRequestBudgetExhausted)
	assert.Equal(t, knownErr, unknownErr)
	assert.Empty(t, knownOps)
	assert.Empty(t, unknownOps)
	assert.Zero(t, knownWork)
	assert.Zero(t, unknownWork)
}

// The budget binds an Unthrottled kind too: it bounds compares, and those still cost.
func TestRequestBudgetBindsUnthrottledKinds(t *testing.T) {
	c, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: testPolicy, credential.KindOAuthClient: {Unthrottled: true},
	})
	require.NoError(t, err)
	client := credential.Principal{Kind: credential.KindOAuthClient, ID: "grafana"}
	acct := newAccount(t)
	ctx := credential.WithRequestBudget(context.Background(), 1)
	require.NoError(t, c.Check(ctx, client, secret, acct.lookup))
	require.ErrorIs(t, c.Check(ctx, client, secret, acct.lookup), credential.ErrRequestBudgetExhausted)
}

// A context with no budget is not limited — the rule for the OAuth HTTP handlers,
// which make one check per request by their shape (budget.go says why).
func TestNoBudgetIsUnlimited(t *testing.T) {
	c := newChecker(t, credentialtest.NewStore(), newClock())
	client := credential.Principal{Kind: credential.KindIdentity, ID: "carol@example.com"}
	acct := newAccount(t)
	for i := 0; i < 10; i++ {
		require.NoError(t, c.Check(context.Background(), client, secret, acct.lookup))
	}
}

// Wrapping a context that already has a budget can tighten it, never widen it, along one
// chain — so no layer between the Schema and the Checker can hand a request more checks.
// TestSiblingBudgetsNeverExceedTheirParent is the same claim for the aggregate.
func TestRequestBudgetCanOnlyTighten(t *testing.T) {
	c := newChecker(t, credentialtest.NewStore(), newClock())
	acct := newAccount(t)
	id := func(n int) credential.Principal {
		return credential.Principal{Kind: credential.KindIdentity, ID: string(rune('a'+n)) + "@example.com"}
	}

	widened := credential.WithRequestBudget(credential.WithRequestBudget(context.Background(), 1), 5)
	require.NoError(t, c.Check(widened, id(0), secret, acct.lookup))
	require.ErrorIs(t, c.Check(widened, id(1), secret, acct.lookup), credential.ErrRequestBudgetExhausted)

	tightened := credential.WithRequestBudget(credential.WithRequestBudget(context.Background(), 5), 1)
	require.NoError(t, c.Check(tightened, id(2), secret, acct.lookup))
	require.ErrorIs(t, c.Check(tightened, id(3), secret, acct.lookup), credential.ErrRequestBudgetExhausted)
}

// 🔴 THE AGGREGATE BOUND: children installed inside one budget — however many, and
// whatever each is given — together never make more checks than the parent allows,
// because every check spends from its own budget AND from each one enclosing it. Five
// siblings of 1 under a parent of 2 get two checks between them, not five; and a
// sibling that could widen (10 under a parent of 2) gets no more.
func TestSiblingBudgetsNeverExceedTheirParent(t *testing.T) {
	c := newChecker(t, credentialtest.NewStore(), newClock())
	acct := newAccount(t)
	client := credential.Principal{Kind: credential.KindIdentity, ID: "carol@example.com"}

	parent := credential.WithRequestBudget(context.Background(), 2)
	evaluated := 0
	for i := 0; i < 5; i++ {
		child := credential.WithRequestBudget(parent, 1)
		if c.Check(child, client, secret, acct.lookup) == nil {
			evaluated++
		}
	}
	assert.Equal(t, 2, evaluated, "five children of 1 under a parent of 2 share the parent's 2")

	parent = credential.WithRequestBudget(context.Background(), 2)
	wide := credential.WithRequestBudget(parent, 10)
	evaluated = 0
	for i := 0; i < 10; i++ {
		if c.Check(wide, client, secret, acct.lookup) == nil {
			evaluated++
		}
	}
	assert.Equal(t, 2, evaluated, "a child given more than its parent gets no more than the parent")
	require.ErrorIs(t, c.Check(parent, client, secret, acct.lookup), credential.ErrRequestBudgetExhausted,
		"and what the child spent, the parent no longer has")
}

// A refusal at an inner level takes nothing from the levels above it: a child that has
// spent its own unit is refused, and the parent's remaining unit is still there for the
// next check.
func TestRefusedCheckLeaksNoParentUnits(t *testing.T) {
	c := newChecker(t, credentialtest.NewStore(), newClock())
	acct := newAccount(t)
	client := credential.Principal{Kind: credential.KindIdentity, ID: "dave@example.com"}

	parent := credential.WithRequestBudget(context.Background(), 2)
	child := credential.WithRequestBudget(parent, 1)
	require.NoError(t, c.Check(child, client, secret, acct.lookup))
	for i := 0; i < 5; i++ {
		require.ErrorIs(t, c.Check(child, client, secret, acct.lookup), credential.ErrRequestBudgetExhausted)
	}
	require.NoError(t, c.Check(parent, client, secret, acct.lookup), "the parent's second unit survived five refusals")
	require.ErrorIs(t, c.Check(parent, client, secret, acct.lookup), credential.ErrRequestBudgetExhausted)
}

// The aggregate bound under concurrency: 40 goroutines across 8 sibling children (3
// each, 24 between them) under a parent of 5 — exactly 5 checks run.
func TestSiblingBudgetsHoldUnderConcurrency(t *testing.T) {
	c, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: testPolicy, credential.KindOAuthClient: {Unthrottled: true},
	})
	require.NoError(t, err)
	acct := newAccount(t)
	parent := credential.WithRequestBudget(context.Background(), 5)
	children := make([]context.Context, 8)
	for i := range children {
		children[i] = credential.WithRequestBudget(parent, 3)
	}

	var wg sync.WaitGroup
	var evaluated atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			<-start
			err := c.Check(ctx, credential.Principal{Kind: credential.KindOAuthClient, ID: "c"}, secret, acct.lookup)
			switch {
			case err == nil:
				evaluated.Add(1)
			case errors.Is(err, credential.ErrRequestBudgetExhausted):
			default:
				t.Errorf("unexpected error %v", err)
			}
		}(children[i%len(children)])
	}
	close(start)
	wg.Wait()
	assert.LessOrEqual(t, evaluated.Load(), int32(5), "never more than the parent allows")
	assert.Equal(t, int32(5), evaluated.Load(), "and, with demand far above it, all of it is used")
}

// Concurrent checks in one request cannot overspend: graphql-go runs a query's root
// fields in parallel, so the last unit must go to exactly one of them.
func TestRequestBudgetIsSafeUnderConcurrency(t *testing.T) {
	c, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: testPolicy, credential.KindOAuthClient: {Unthrottled: true},
	})
	require.NoError(t, err)
	acct := newAccount(t)
	ctx := credential.WithRequestBudget(context.Background(), 3)

	var wg sync.WaitGroup
	var evaluated, refused atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := c.Check(ctx, credential.Principal{Kind: credential.KindOAuthClient, ID: "c"}, secret, acct.lookup)
			switch {
			case err == nil:
				evaluated.Add(1)
			case errors.Is(err, credential.ErrRequestBudgetExhausted):
				refused.Add(1)
			default:
				t.Errorf("unexpected error %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int32(3), evaluated.Load())
	assert.Equal(t, int32(29), refused.Load())
}

// WithCompareObserver sees every compare, the dummy included, and changes no result.
func TestCompareObserverSeesEveryCompare(t *testing.T) {
	var seen atomic.Int32
	c, err := credential.NewChecker(credentialtest.NewStore(), policies(testPolicy),
		credential.WithClock(newClock().Now), credential.WithCompareObserver(func([]byte) { seen.Add(1) }))
	require.NoError(t, err)
	require.NoError(t, c.Check(context.Background(), alice, secret, newAccount(t).lookup))
	require.ErrorIs(t, c.Check(context.Background(), alice, "x", unknownAccount().lookup), credential.ErrMismatch)
	assert.Equal(t, int32(2), seen.Load())
}
