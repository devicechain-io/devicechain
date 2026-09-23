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

// Wrapping a context that already has a budget can tighten it, never widen it — so no
// layer between the Schema and the Checker can hand a request more checks.
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
