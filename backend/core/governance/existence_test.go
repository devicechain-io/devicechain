// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unknownTenantErr() error {
	return &svcclient.GraphQLError{URL: "um", Messages: []string{"unknown tenant"}, Codes: []string{CodeUnknownTenant}}
}

// Only an authoritative "no such tenant" is ExistenceNone; every other failure is Unknown, so the
// caller that refuses on ExistenceNone alone fails open exactly as the resolver does.
func TestExistence_OnlyAnAuthoritativeNoIsNone(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want Existence
	}{
		"fetched":           {nil, ExistenceKnown},
		"unknown tenant":    {unknownTenantErr(), ExistenceNone},
		"unreachable":       {errors.New("connection refused"), ExistenceUnknown},
		"other graphql":     {&svcclient.GraphQLError{URL: "um", Messages: []string{"forbidden"}, Codes: []string{"FORBIDDEN"}}, ExistenceUnknown},
		"uncoded graphql":   {&svcclient.GraphQLError{URL: "um", Messages: []string{"boom"}}, ExistenceUnknown},
		"deadline exceeded": {context.DeadlineExceeded, ExistenceUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			r := newTenantResolver(countingFetch(&calls, 42, tc.err), 7, "test")
			// A cold tenant is Unknown until the lookup it triggers has finished.
			assert.Equal(t, ExistenceUnknown, r.existence("acme"))
			require.Eventually(t, r.idle, 10*time.Second, time.Millisecond)
			assert.Equal(t, tc.want, r.existence("acme"))
		})
	}
}

// AwaitExistence answers a never-seen tenant on the FIRST ask, which is the point: the first
// request for a typo'd tenant is refused as the second would be.
func TestAwaitExistence_AnswersAColdTenantOnTheFirstAsk(t *testing.T) {
	var calls atomic.Int64
	r := newTenantResolver(countingFetch(&calls, 42, unknownTenantErr()), 7, "test")
	assert.Equal(t, ExistenceNone, r.awaitExistence(context.Background(), "typo"))
	assert.EqualValues(t, 1, calls.Load())

	r2 := newTenantResolver(countingFetch(&calls, 42, nil), 7, "test")
	assert.Equal(t, ExistenceKnown, r2.awaitExistence(context.Background(), "acme"))
}

// The wait is bounded by ctx, and an answer arriving after it is still cached for the next ask.
func TestAwaitExistence_WaitIsBoundedByContext(t *testing.T) {
	release := make(chan struct{})
	fetch := func(ctx context.Context, tenant string) (int, error) {
		<-release
		return 1, nil
	}
	r := newTenantResolver(fetch, 7, "test")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	assert.Equal(t, ExistenceUnknown, r.awaitExistence(ctx, "acme"))
	assert.Less(t, time.Since(start), 5*time.Second)
	close(release)
	require.Eventually(t, r.idle, 10*time.Second, time.Millisecond)
	assert.Equal(t, ExistenceKnown, r.existence("acme"))
}

// When the resolver's refresh bound refuses to start a lookup, nothing is running, so the wait
// ends at once instead of burning the budget: a flood of invented tenants must not hold handlers.
func TestAwaitExistence_DoesNotWaitWhenNoRefreshStarted(t *testing.T) {
	var calls atomic.Int64
	clk := newFakeClock()
	r := newTenantResolver(countingFetch(&calls, 42, unknownTenantErr()), 7, "test")
	r.Configure(ResolverOptions{RefreshesPerSecond: 1, RefreshBurst: 1, Clock: clk.now})
	// Spend the one token on another tenant.
	r.existence("first")
	require.Eventually(t, r.idle, 10*time.Second, time.Millisecond)

	start := time.Now()
	assert.Equal(t, ExistenceUnknown, r.awaitExistence(context.Background(), "second"))
	assert.Less(t, time.Since(start), time.Second, "no refresh was running, so there was nothing to wait for")
	assert.EqualValues(t, 1, calls.Load())
}

// The exported methods on the limit resolver read the same cache the ceiling does.
func TestTenantLimitResolver_Existence(t *testing.T) {
	fetch := fetcherFunc(func(ctx context.Context, tenant string) (Limits, error) {
		if tenant == "typo" {
			return Limits{}, unknownTenantErr()
		}
		return Limits{RatePerSecond: 5, Burst: 5}, nil
	})
	r := NewTenantLimitResolver(fetch, Limits{RatePerSecond: 1, Burst: 1}, "ingest")
	assert.Equal(t, ExistenceNone, r.AwaitExistence(context.Background(), "typo"))
	assert.Equal(t, ExistenceNone, r.Existence("typo"))
	assert.Equal(t, ExistenceKnown, r.AwaitExistence(context.Background(), "acme"))
	assert.Equal(t, ExistenceKnown, r.Existence("acme"))
}

type fetcherFunc func(ctx context.Context, tenant string) (Limits, error)

func (f fetcherFunc) Fetch(ctx context.Context, tenant string) (Limits, error) { return f(ctx, tenant) }
